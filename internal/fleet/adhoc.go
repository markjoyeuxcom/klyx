package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/moomora/klyx/internal/clock"
)

// Ad-hoc create: the deliberately-non-GitOps path for scratch/diagnostic
// objects (a debug pod, a temporary job) that must NEVER blur into authoring
// desired state. The bifurcation from managed objects is hard and visible:
//   - every object is stamped with the klyx.dev/adhoc label + created-by/at
//     annotations, so a Scratch surface can list and clean them up;
//   - plain CREATE under a distinct field manager (klyx-adhoc): a name
//     collision with ANY existing object errors outright, so an ad-hoc create
//     can never silently touch controller-owned state;
//   - Secrets are refused outright (they belong in ESO/SOPS in Git - ad-hoc
//     plaintext secrets are exactly the sprawl Klyx exists to prevent);
//   - namespaced objects must name their namespace explicitly (no silent
//     "default").
const (
	// AdhocLabel marks every Klyx-created ad-hoc object.
	AdhocLabel         = "klyx.dev/adhoc"
	adhocCreatedByAnno = "klyx.dev/created-by"
	adhocCreatedAtAnno = "klyx.dev/created-at"
	adhocFieldManager  = "klyx-adhoc"
)

// AdhocResult is the per-document outcome of an ad-hoc create. On a dry-run the
// NamespaceOwner* fields carry the GitOps namespace context for the target
// (GitOpsOwnership: reach is context, NamespaceManaged the conditional
// namespace-deletion risk; empty = no managing unit DETECTED, never "safe").
// ArgoTracking*: whether an Argo Application reaching the namespace would treat
// the object as one of its own resources (argoTrackingEvidence) - the one way
// a new object becomes subject to Argo pruning.
type AdhocResult struct {
	Kind      string
	Namespace string
	Name      string
	OK        bool
	Error     string

	NamespaceOwnerKind      string
	NamespaceOwnerName      string
	NamespaceOwnerNamespace string
	NamespacePrunes         bool
	NamespaceManaged        bool

	ArgoTracking       string // "tracked" | "possible" | "" (no recognized evidence)
	ArgoTrackingApp    string // "<namespace>/<name>" of the app the evidence names
	ArgoTrackingPrunes bool   // that app prunes automatically
	ArgoPruneOption    string // the object's own sync option: "false" | "confirm" | ""
	// ArgoConfigKnown: the controller's tracking method was read (argocd-cm),
	// for the app the evidence names - or, with no evidence, for the Argo app
	// managing the namespace. Unknown settings make every Argo verdict
	// conditional.
	ArgoConfigKnown bool
}

// yamlDocSep splits a multi-document YAML stream on standalone `---` lines.
var yamlDocSep = regexp.MustCompile(`(?m)^---\s*$`)

// CreateResourceYAML creates each document in yamlDocs as a NEW ad-hoc object.
// Documents are processed independently with honest per-doc results - one bad
// doc never hides the others' outcomes. dryRun validates against the server
// without persisting and enriches results with the namespace prune-reach verdict.
func (c *ClusterConn) CreateResourceYAML(ctx context.Context, yamlDocs string, dryRun bool) []AdhocResult {
	docs := yamlDocSep.Split(yamlDocs, -1)
	out := make([]AdhocResult, 0, len(docs))
	applied := false
	for _, doc := range docs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		r := c.applyAdhocDoc(ctx, doc, dryRun)
		if r.OK && !dryRun {
			applied = true
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		out = append(out, AdhocResult{Error: "no YAML documents found"})
	}
	if applied {
		c.signalRefresh()
	}
	return out
}

func (c *ClusterConn) applyAdhocDoc(ctx context.Context, doc string, dryRun bool) AdhocResult {
	jsonDoc, err := yaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return AdhocResult{Error: "parse yaml: " + err.Error()}
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(jsonDoc, &obj); err != nil {
		return AdhocResult{Error: "parse yaml: " + err.Error()}
	}
	u := &unstructured.Unstructured{Object: obj}
	kind := u.GetKind()
	name := u.GetName()
	ns := u.GetNamespace()
	r := AdhocResult{Kind: kind, Namespace: ns, Name: name}

	if u.GetAPIVersion() == "" || kind == "" {
		r.Error = "document needs apiVersion and kind"
		return r
	}
	if name == "" {
		r.Error = "document needs metadata.name"
		return r
	}
	// Deliberate stance: no ad-hoc Secrets. They bypass ESO/SOPS and invite
	// plaintext sprawl - the opposite of what the scratch path is for.
	if kind == "Secret" {
		r.Error = "ad-hoc Secret creation is not supported - author Secrets through ESO/SOPS in Git so the value never lives only on this machine"
		return r
	}

	gvr, res, err := c.resolveGVK(u.GetAPIVersion(), kind)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	// Every ad-hoc object must be findable and removable from Scratch, so the
	// composer creates only kinds the inventory can list and delete (Codex
	// review 2026-10-13: a create the inventory cannot see is an orphan).
	if !hasVerbs(res.Verbs, "list", "delete") {
		r.Error = fmt.Sprintf("%s cannot be listed and deleted on this cluster, so Scratch could not clean it up - the composer does not create it", kind)
		return r
	}
	namespaced := res.Namespaced
	if namespaced && ns == "" {
		r.Error = "set metadata.namespace explicitly - ad-hoc objects must land where you aimed, never an implicit default"
		return r
	}
	if !namespaced && ns != "" {
		r.Error = fmt.Sprintf("cluster-scoped %s must not set metadata.namespace", kind)
		return r
	}

	// Stamp the ad-hoc identity (label + provenance) so the object is findable
	// and cleanable, and never mistaken for Git-managed state.
	labels := u.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[AdhocLabel] = "true"
	u.SetLabels(labels)
	annos := u.GetAnnotations()
	if annos == nil {
		annos = map[string]string{}
	}
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	annos[adhocCreatedByAnno] = "klyx"
	annos[adhocCreatedAtAnno] = clk.Now().UTC().Format(time.RFC3339)
	u.SetAnnotations(annos)

	if c.dyn == nil {
		r.Error = "dynamic client unavailable"
		return r
	}
	// CREATE, deliberately not server-side apply: this surface only makes NEW
	// objects. Colliding with an existing name errors outright ("already
	// exists") - stronger than SSA Force:false, which would silently update an
	// existing object whose fields carry no other manager.
	opts := metav1.CreateOptions{FieldManager: adhocFieldManager}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	resource := c.dyn.Resource(gvr)
	if namespaced {
		_, err = resource.Namespace(ns).Create(ctx, u, opts)
	} else {
		_, err = resource.Create(ctx, u, opts)
	}
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			r.Error = fmt.Sprintf("%s %q already exists - the composer only creates new objects; edit the existing one in Resources", kind, name)
			return r
		}
		r.Error = err.Error()
		return r
	}
	r.OK = true

	// Dry-run enriches with the GitOps namespace context so the composer can
	// warn BEFORE the commit step - stating only what is known: a reaching unit
	// prunes its own objects, not this one, unless the object carries Argo
	// tracking metadata or the unit manages (and could delete) the namespace.
	if dryRun && namespaced {
		o := c.GitOpsOwnership(ctx, gvr.Group, kind, ns, name)
		r.NamespaceOwnerKind = o.NamespaceOwnerKind
		r.NamespaceOwnerName = o.NamespaceOwnerName
		r.NamespaceOwnerNamespace = o.NamespaceOwnerNamespace
		r.NamespacePrunes = o.NamespacePrunes
		r.NamespaceManaged = o.NamespaceManaged
		c.noteArgoTracking(ctx, &r, u, gvr.Group, kind, ns, name, o)
		r.ArgoPruneOption = argoPruneOption(u)
	}
	return r
}

// argoTrackingCfg is the controller's resource-tracking configuration from
// argocd-cm (https://argo-cd.readthedocs.io/en/stable/user-guide/resource_tracking/).
type argoTrackingCfg struct {
	known          bool   // application.resourceTrackingMethod was read
	method         string // "label" | "annotation" | "annotation+label"
	labelKey       string // application.instanceLabelKey
	installationID string // installationID: annotation tracking also requires a matching installation-id annotation
}

var argoCMGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// argoTrackingConfig reads argocd-cm in the namespace of an Application, on
// the assumption that it is the controller's namespace. That holds for apps in
// the control-plane namespace; with apps-in-any-namespace it does not, and the
// lookup there normally finds no argocd-cm, leaving the configuration unknown
// (so every verdict stays conditional). An unreadable or absent ConfigMap, or
// one that does not set the method (whose default changed between Argo CD 2.x
// and 3.x), also leaves it unknown.
func (c *ClusterConn) argoTrackingConfig(ctx context.Context, ns string) argoTrackingCfg {
	cfg := argoTrackingCfg{labelKey: "app.kubernetes.io/instance"}
	if c.dyn == nil {
		return cfg
	}
	cm, err := c.dyn.Resource(argoCMGVR).Namespace(ns).Get(ctx, "argocd-cm", metav1.GetOptions{})
	if err != nil {
		return cfg
	}
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	if k := data["application.instanceLabelKey"]; k != "" {
		cfg.labelKey = k
	}
	cfg.installationID = data["installationID"]
	switch m := data["application.resourceTrackingMethod"]; m {
	case "label", "annotation", "annotation+label":
		cfg.known, cfg.method = true, m
	}
	return cfg
}

// argoTrackingEvidence classifies whether Argo app appNS/app would treat u
// (identified by group/kind/namespace/name) as one of its resources:
//   - "tracked": the controller's method is known and the matching metadata is
//     present - the instance label under label tracking, or a SELF-referencing
//     tracking-id annotation under annotation tracking (a copied, non-self-
//     referencing tracking-id is excluded by Argo; under annotation+label the
//     label is informational only) whose installation-id annotation matches a
//     configured installationID, as Argo's getAppInstanceValue requires;
//   - "possible": the method is unknown and either kind of metadata matches;
//   - "": no recognized evidence.
//
// App references accept "<app>" and the apps-in-any-namespace form
// "<namespace>_<app>" (batch-4 review R2: the label or any tracking-id alone
// asserted certain pruning).
func argoTrackingEvidence(u *unstructured.Unstructured, group, kind, ns, name, appNS, app string, cfg argoTrackingCfg) string {
	isApp := func(ref string) bool { return ref != "" && (ref == app || ref == appNS+"_"+app) }
	labelMatch := isApp(u.GetLabels()[cfg.labelKey])
	selfRef := false
	if parts := strings.SplitN(u.GetAnnotations()["argocd.argoproj.io/tracking-id"], ":", 3); len(parts) == 3 && isApp(parts[0]) {
		gk := strings.SplitN(parts[1], "/", 2)
		nn := strings.SplitN(parts[2], "/", 2)
		selfRef = len(gk) == 2 && len(nn) == 2 &&
			gk[0] == group && gk[1] == kind && nn[0] == ns && nn[1] == name
	}
	if !cfg.known {
		if selfRef || labelMatch {
			return "possible"
		}
		return ""
	}
	if cfg.method == "label" {
		if labelMatch {
			return "tracked"
		}
		return ""
	}
	if selfRef && (cfg.installationID == "" || u.GetAnnotations()["argocd.argoproj.io/installation-id"] == cfg.installationID) {
		return "tracked"
	}
	return ""
}

// argoPruneOption reads the object's own Argo sync options: "false" (Argo
// skips pruning it), "confirm" (pruning waits for approval) or "".
// https://argo-cd.readthedocs.io/en/stable/user-guide/sync-options/#no-prune-resources
func argoPruneOption(u *unstructured.Unstructured) string {
	for _, opt := range strings.Split(u.GetAnnotations()["argocd.argoproj.io/sync-options"], ",") {
		switch strings.TrimSpace(opt) {
		case "Prune=false":
			return "false"
		case "Prune=confirm":
			return "confirm"
		}
	}
	return ""
}

// noteArgoTracking checks EVERY Argo app reaching the namespace for tracking
// evidence on u - not only the unit managing the namespace, which can be a
// Flux Kustomization while an Argo app deploys there too - and records the
// strongest finding.
func (c *ClusterConn) noteArgoTracking(ctx context.Context, r *AdhocResult, u *unstructured.Unstructured, group, kind, ns, name string, o GitOpsOwnership) {
	cfgs := map[string]argoTrackingCfg{}
	cfgFor := func(appNS string) argoTrackingCfg {
		if cfg, ok := cfgs[appNS]; ok {
			return cfg
		}
		cfg := c.argoTrackingConfig(ctx, appNS)
		cfgs[appNS] = cfg
		return cfg
	}
	for _, a := range o.ArgoApps {
		cfg := cfgFor(a.Namespace)
		ev := argoTrackingEvidence(u, group, kind, ns, name, a.Namespace, a.Name, cfg)
		if ev == "" || (r.ArgoTracking == "tracked") || (r.ArgoTracking == "possible" && ev == "possible") {
			continue
		}
		r.ArgoTracking = ev
		r.ArgoTrackingApp = a.Namespace + "/" + a.Name
		r.ArgoTrackingPrunes = a.Prunes
		r.ArgoConfigKnown = cfg.known
	}
	if r.ArgoTracking == "" && o.NamespaceBasis == "argo" {
		r.ArgoConfigKnown = cfgFor(o.NamespaceOwnerNamespace).known
	}
}

// ScratchNamespace is where the diagnostic-pod launcher deploys. A dedicated
// unmanaged namespace designs the prune risk out: no GitOps unit targets it, so
// nothing there is ever reverted or pruned by a reconcile.
const ScratchNamespace = "klyx-scratch"

// LaunchDiagnosticPod creates a scratch debugging pod in the klyx-scratch
// namespace (created on first use, adhoc-stamped like everything else). The pod
// is TTL-bounded by activeDeadlineSeconds - the CLUSTER kills it when time is
// up, so cleanup never depends on Klyx still running - with restartPolicy Never
// so it dies once instead of crash-looping forever.
func (c *ClusterConn) LaunchDiagnosticPod(ctx context.Context, name, image string, ttlSeconds int64) AdhocResult {
	r := AdhocResult{Kind: "Pod", Namespace: ScratchNamespace, Name: name}
	if c.typed == nil {
		r.Error = "cluster connection unavailable"
		return r
	}
	if name == "" || image == "" {
		r.Error = "name and image are required"
		return r
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	stamp := map[string]string{AdhocLabel: "true"}
	annos := map[string]string{adhocCreatedByAnno: "klyx", adhocCreatedAtAnno: clk.Now().UTC().Format(time.RFC3339)}

	// Ensure the scratch namespace exists; racing an existing one is fine.
	nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ScratchNamespace, Labels: stamp, Annotations: annos}}
	if _, err := c.typed.CoreV1().Namespaces().Create(ctx, nsObj, metav1.CreateOptions{FieldManager: adhocFieldManager}); err != nil && !apierrors.IsAlreadyExists(err) {
		r.Error = "create " + ScratchNamespace + " namespace: " + err.Error()
		return r
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ScratchNamespace, Labels: stamp, Annotations: annos},
		Spec: corev1.PodSpec{
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &ttlSeconds,
			Containers: []corev1.Container{{
				Name:    "shell",
				Image:   image,
				Command: []string{"sleep", fmt.Sprintf("%d", ttlSeconds)},
			}},
		},
	}
	if _, err := c.typed.CoreV1().Pods(ScratchNamespace).Create(ctx, pod, metav1.CreateOptions{FieldManager: adhocFieldManager}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			r.Error = fmt.Sprintf("pod %q already exists in %s - delete it from the scratch list or pick another name", name, ScratchNamespace)
			return r
		}
		r.Error = err.Error()
		return r
	}
	r.OK = true
	c.signalRefresh()
	return r
}

// AdhocObject is one Klyx-created scratch object (labeled klyx.dev/adhoc).
type AdhocObject struct {
	Kind       string
	Group      string
	Version    string
	Plural     string
	Scope      string
	Namespace  string
	Name       string
	UID        string
	AgeSeconds int64
}

// AdhocInventory is the Scratch sweep: every object labeled klyx.dev/adhoc,
// and the kinds that could not be read. A non-empty Unread means the
// inventory is INCOMPLETE - never "nothing else exists".
type AdhocInventory struct {
	Objects []AdhocObject
	Unread  []string
}

const (
	adhocSweepWorkers = 8
	adhocListPage     = 500
)

type adhocKind struct {
	gvr   schema.GroupVersionResource
	kind  string
	scope string
}

func hasVerbs(verbs []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, v := range verbs {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func resourceLabel(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Group
}

// adhocSweepKinds is every served kind the composer can create and Scratch
// can clean up: top-level resources that support list and delete, each group's
// preferred version first, plus any resource served only in another version.
// It matches what applyAdhocDoc admits, so nothing Klyx created is left out.
// Secrets are skipped - the composer refuses them, so none can be Klyx's.
//
// Discovery is read with ctx (the context-aware cache the health probes
// share), so the caller's deadline or cancellation ends it at the transport;
// once ctx ends the sweep stops and says the inventory was not read.
func (c *ClusterConn) adhocSweepKinds(ctx context.Context) ([]adhocKind, []string, error) {
	if c.typed == nil {
		return nil, nil, errors.New("discovery unavailable: no cluster connection")
	}
	d := c.probeDiscovery()
	groups, err := d.ServerGroupsCtx(ctx)
	if err != nil || groups == nil {
		return nil, nil, fmt.Errorf("discovery failed: %v", err)
	}
	var kinds []adhocKind
	var unread []string
	for _, g := range groups.Groups {
		versions := make([]string, 0, len(g.Versions)+1)
		if g.PreferredVersion.GroupVersion != "" {
			versions = append(versions, g.PreferredVersion.GroupVersion)
		}
		for _, v := range g.Versions {
			if v.GroupVersion != g.PreferredVersion.GroupVersion {
				versions = append(versions, v.GroupVersion)
			}
		}
		seen := map[string]bool{}
		for _, gvStr := range versions {
			if err := ctx.Err(); err != nil {
				return nil, nil, fmt.Errorf("discovery stopped: %v", err)
			}
			gv, err := schema.ParseGroupVersion(gvStr)
			if err != nil {
				continue
			}
			rl, err := d.ServerResourcesForGroupVersionCtx(ctx, gvStr)
			if err := ctx.Err(); err != nil {
				return nil, nil, fmt.Errorf("discovery stopped: %v", err)
			}
			if err != nil || rl == nil {
				unread = append(unread, fmt.Sprintf("%s: discovery failed: %v", gvStr, err))
				continue
			}
			for _, r := range rl.APIResources {
				if strings.Contains(r.Name, "/") || seen[r.Name] || !hasVerbs(r.Verbs, "list", "delete") {
					continue
				}
				if gv.Group == "" && r.Name == "secrets" {
					continue
				}
				seen[r.Name] = true
				scope := "Cluster"
				if r.Namespaced {
					scope = "Namespaced"
				}
				kinds = append(kinds, adhocKind{gvr: gv.WithResource(r.Name), kind: r.Kind, scope: scope})
			}
		}
	}
	return kinds, unread, nil
}

// ListAdhocObjects sweeps every kind the composer can create for objects
// labeled klyx.dev/adhoc - the cleanup backstop for TTL'd scratch objects.
// Each kind is read page by page; a kind whose list fails (RBAC, an
// unavailable aggregated API, a timeout) is named in Unread rather than
// silently dropped. Objects served under two groups (events) appear once.
func (c *ClusterConn) ListAdhocObjects(ctx context.Context) (AdhocInventory, error) {
	if c.dyn == nil {
		return AdhocInventory{}, errors.New("dynamic client unavailable")
	}
	kinds, unread, err := c.adhocSweepKinds(ctx)
	if err != nil {
		return AdhocInventory{}, err
	}
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	type result struct {
		objs []AdhocObject
		err  string
	}
	results := make([]result, len(kinds))
	sem := make(chan struct{}, adhocSweepWorkers)
	var wg sync.WaitGroup
	for i, k := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = result{err: fmt.Sprintf("%s: not read: %v", resourceLabel(k.gvr), ctx.Err())}
				return
			}
			defer func() { <-sem }()
			objs, err := c.sweepAdhocKind(ctx, k, now)
			results[i] = result{objs: objs}
			if err != nil {
				results[i].err = fmt.Sprintf("%s: %v", resourceLabel(k.gvr), err)
			}
		}()
	}
	wg.Wait()

	inv := AdhocInventory{Unread: unread}
	seen := map[types.UID]bool{}
	for _, r := range results {
		if r.err != "" {
			inv.Unread = append(inv.Unread, r.err)
		}
		for _, o := range r.objs {
			if o.UID != "" {
				if seen[types.UID(o.UID)] {
					continue
				}
				seen[types.UID(o.UID)] = true
			}
			inv.Objects = append(inv.Objects, o)
		}
	}
	sort.SliceStable(inv.Objects, func(i, j int) bool {
		a, b := inv.Objects[i], inv.Objects[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	sort.Strings(inv.Unread)
	return inv, nil
}

// sweepAdhocKind reads one kind's labeled objects, following continue tokens.
// On a mid-way failure it returns what it read and the error.
func (c *ClusterConn) sweepAdhocKind(ctx context.Context, k adhocKind, now time.Time) ([]AdhocObject, error) {
	var out []AdhocObject
	opts := metav1.ListOptions{LabelSelector: AdhocLabel + "=true", Limit: adhocListPage}
	for {
		list, err := c.dyn.Resource(k.gvr).List(ctx, opts)
		if err != nil {
			return out, err
		}
		if list == nil {
			return out, nil
		}
		for i := range list.Items {
			u := &list.Items[i]
			age := int64(0)
			if ct := u.GetCreationTimestamp(); !ct.IsZero() {
				age = int64(now.Sub(ct.Time).Seconds())
				if age < 0 {
					age = 0
				}
			}
			out = append(out, AdhocObject{
				Kind: k.kind, Group: k.gvr.Group, Version: k.gvr.Version, Plural: k.gvr.Resource,
				Scope: k.scope, Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()), AgeSeconds: age,
			})
		}
		cont := list.GetContinue()
		if cont == "" {
			return out, nil
		}
		opts.Continue = cont
	}
}

// DeleteAdhocObject deletes one Klyx-created scratch object. Two guards: the
// object is fetched first and MUST carry klyx.dev/adhoc, so this verb can
// never be aimed at anything Klyx did not create; and the delete is pinned to
// the UID the inventory showed, with a server-side UID precondition, so an
// object replaced under the same name - before the check or between the check
// and the delete - is never removed.
func (c *ClusterConn) DeleteAdhocObject(ctx context.Context, group, version, plural, ns, name, uid string) error {
	if c.dyn == nil {
		return errors.New("dynamic client unavailable")
	}
	if uid == "" {
		return errors.New("no object UID given - refresh the scratch list and try again")
	}
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
	resource := c.dyn.Resource(gvr)
	var u *unstructured.Unstructured
	var err error
	if ns != "" {
		u, err = resource.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	} else {
		u, err = resource.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return err
	}
	if u.GetLabels()[AdhocLabel] != "true" {
		return fmt.Errorf("%s %q is not a Klyx ad-hoc object (missing %s label) - refusing to delete", u.GetKind(), name, AdhocLabel)
	}
	if string(u.GetUID()) != uid {
		return fmt.Errorf("%s %q was replaced since the scratch list was read - refresh and check it before deleting", u.GetKind(), name)
	}
	pinned := types.UID(uid)
	opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pinned}}
	if ns != "" {
		err = resource.Namespace(ns).Delete(ctx, name, opts)
	} else {
		err = resource.Delete(ctx, name, opts)
	}
	if err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("%s %q changed between the check and the delete, so nothing was deleted - refresh and try again", u.GetKind(), name)
		}
		return err
	}
	c.signalRefresh()
	return nil
}

// resolveGVK maps the document's own apiVersion+kind to the served GVR + scope
// via the TTL-cached discovery. The doc declares its version, so this is exact -
// no preferred-version guessing. A miss names the likely causes (typo, CRD not
// installed, or discovery cache staleness for a just-installed CRD).
func (c *ClusterConn) resolveGVK(apiVersion, kind string) (schema.GroupVersionResource, metav1.APIResource, error) {
	if c.typed == nil {
		return schema.GroupVersionResource{}, metav1.APIResource{}, fmt.Errorf("discovery unavailable: no cluster connection")
	}
	rl, err := c.discovery().ServerResourcesForGroupVersion(apiVersion)
	if err != nil || rl == nil {
		return schema.GroupVersionResource{}, metav1.APIResource{}, fmt.Errorf("apiVersion %q is not served (typo, or the CRD is not installed)", apiVersion)
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, metav1.APIResource{}, fmt.Errorf("parse apiVersion %q: %v", apiVersion, err)
	}
	for _, res := range rl.APIResources {
		if strings.Contains(res.Name, "/") { // subresources (pods/log, ...)
			continue
		}
		if res.Kind == kind {
			return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: res.Name}, res, nil
		}
	}
	return schema.GroupVersionResource{}, metav1.APIResource{}, fmt.Errorf("kind %q is not served under %q (typo, or a just-installed CRD - discovery refreshes within a minute)", kind, apiVersion)
}
