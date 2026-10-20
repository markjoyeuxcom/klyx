package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/karpenter"
)

// Karpenter detection (coverage S10 §5). The karpenter.sh CRDs are the
// capability. The controllers are Deployments labelled as Karpenter's (plus
// the conventional karpenter Deployment), each attributed to a provider by
// its running pods' images; platform management (EKS Auto Mode, AKS node
// auto-provisioning) rests on positive evidence only, and never clears an
// unread inventory.
const (
	karpenterGroup         = "karpenter.sh"
	karpenterNameSelector  = "app.kubernetes.io/name=karpenter"
	karpenterMaxCandidates = 4
	karpenterMaxPods       = 8
	karpenterMaxPools      = 500

	// The controller images (§1.5): a custom or mirrored image proves no
	// provider.
	karpenterImageAWS   = "public.ecr.aws/karpenter/controller"
	karpenterImageAzure = "mcr.microsoft.com/aks/karpenter/controller"

	// EKS Auto Mode's node label (S5 and the EKS user guide).
	autoModeLabel = "eks.amazonaws.com/compute-type=auto"

	// AKS NAP: the Helm release that installs its CRDs (Microsoft Learn,
	// migration from self-hosted Karpenter), and its Events' source.
	napRelease     = "aks-managed-karpenter-overlay-base"
	napEventSource = "karpenter-events"
	napPageSize    = 100
	napMaxPages    = 3
	// NAPWindow is the API server's default Event TTL: activity older than
	// it is no evidence (§5).
	NAPWindow = time.Hour
)

// KarpenterProvider is the provider a controller is attributed to.
type KarpenterProvider string

const (
	ProviderAWS   KarpenterProvider = "aws"
	ProviderAzure KarpenterProvider = "azure"
)

// supports is the class adapter each provider's controller reconciles
// (GetSupportedNodeClasses: AWS supports EC2NodeClass only).
func (p KarpenterProvider) supports(a karpenter.Adapter) bool {
	return (p == ProviderAWS && a == karpenter.AdapterEC2) || (p == ProviderAzure && a == karpenter.AdapterAKS)
}

func (p KarpenterProvider) label() string {
	switch p {
	case ProviderAWS:
		return "AWS"
	case ProviderAzure:
		return "Azure"
	}
	return "unattributed"
}

// KarpenterController is one candidate controller Deployment.
type KarpenterController struct {
	Namespace, Name, UID string
	Ready                bool
	// Provider comes from the running pods' images, fenced by owner UID;
	// "" when unattributed, and Why says why.
	Provider KarpenterProvider
	Why      string
}

// Inventory is the controller inventory's acquisition outcome.
type Inventory string

const (
	// InventoryComplete: every list finished, at most four candidates,
	// every candidate's pods read and attributed.
	InventoryComplete Inventory = "complete"
	// InventoryIncomplete: a continued list, more candidates or pods than
	// are read, or an unattributed candidate.
	InventoryIncomplete Inventory = "incomplete"
	// InventoryUnreadable: the Deployments could not be listed.
	InventoryUnreadable Inventory = "unreadable"
)

// NAP activity states (§5).
const (
	NAPActive         = "active"
	NAPNone           = "none"
	NAPNotEstablished = "not established"
)

// NAPActivity is AKS NAP's Event evidence: management only, never a
// handler and never health.
type NAPActivity struct {
	State string
	// Newest is the newest Event time read, for an active reading.
	Newest time.Time
	Why    string
}

// KarpenterKind is one class kind's standing on the cluster.
type KarpenterKind struct {
	Kind karpenter.ClassKind
	// Served at the version S10 reads. OtherVersion: served only at
	// another version, a stated limit (§2).
	Served, OtherVersion bool
	// InUse: served, and with classes or referenced by a NodePool (or a
	// read that could not rule either out).
	InUse    bool
	InUseWhy string
}

// KarpenterCapability is Karpenter's detection. Healthy, Verified and Reason
// follow the controller task (Task).
type KarpenterCapability struct {
	Base
	Present      bool
	Healthy      bool // the task is healthy or managed (a note, never attention)
	Verified     bool // the task is not unknown
	Inventory    Inventory
	InventoryWhy string
	Controllers  []KarpenterController
	Kinds        []KarpenterKind
	// AutoMode: the Auto Mode NodeClass is served and a node is labelled
	// compute-type auto. AutoModeWhy names a failed node read.
	AutoMode    bool
	AutoModeWhy string
	// NAPInstalled: the Karpenter CRDs carry NAP's Helm release: history,
	// never activity on its own.
	NAPInstalled bool
	NAP          NAPActivity
	// AzureNodes: a node's providerID is azure:// - an AKS cluster, for
	// §6's networking note (read with the AKSNodeClass kind served).
	AzureNodes bool
}

// Served: the adapter's class kind is served at the version Klyx reads.
func (k KarpenterCapability) Served(a karpenter.Adapter) bool {
	for _, kk := range k.Kinds {
		if kk.Kind.Adapter == a {
			return kk.Served
		}
	}
	return false
}

// Karpenter task states (§5), worst first.
const (
	KarpenterNotReady = "not ready"
	KarpenterUnknown  = "unknown"
	KarpenterManaged  = "managed"
	KarpenterHealthy  = "healthy"
)

func (k KarpenterCapability) attributed(a karpenter.Adapter) []KarpenterController {
	var out []KarpenterController
	for _, c := range k.Controllers {
		if c.Provider.supports(a) {
			out = append(out, c)
		}
	}
	return out
}

// Reading is §5's reading for one class kind: the handler the NodePool rule
// reads, and the sentence that says it.
func (k KarpenterCapability) Reading(kind karpenter.ClassKind) (karpenter.Handler, string) {
	if cs := k.attributed(kind.Adapter); len(cs) > 0 {
		for _, c := range cs {
			if !c.Ready {
				return karpenter.Handler{State: karpenter.HandlerNotReady}, "the karpenter controller " + c.Namespace + "/" + c.Name + " is not ready"
			}
		}
		return karpenter.Handler{State: karpenter.HandlerReady}, "the karpenter controller " + cs[0].Namespace + "/" + cs[0].Name + " is ready"
	}
	complete := k.Inventory == InventoryComplete
	switch kind.Adapter {
	case karpenter.AdapterEKSAuto:
		if k.AutoMode {
			return karpenter.Handler{State: karpenter.HandlerManaged}, "managed by EKS Auto Mode; controller health not directly observed"
		}
	case karpenter.AdapterAKS:
		switch {
		case k.NAP.State == NAPActive && complete:
			return karpenter.Handler{State: karpenter.HandlerManaged},
				"managed by AKS node auto-provisioning (activity as of " + k.NAP.Newest.UTC().Format(time.RFC3339) + "); controller health not directly observed"
		case k.NAP.State == NAPActive:
			// platform evidence never clears an unread inventory
			return karpenter.Handler{}, "AKS node auto-provisioning activity as of " + k.NAP.Newest.UTC().Format(time.RFC3339) + ", but the Karpenter controllers could not all be read"
		case k.NAPInstalled:
			return karpenter.Handler{}, "CRDs installed by AKS NAP; current activity unverified"
		}
	}
	if complete {
		return karpenter.Handler{NoneSupports: true}, "no controller for " + kind.Kind + " found"
	}
	return karpenter.Handler{}, "the Karpenter controllers could not all be read"
}

// NoneSupports: the inventory is complete (so every candidate is
// attributed), no candidate's provider supports the reference, and no
// platform manages it (§3 rule 1).
func (k KarpenterCapability) NoneSupports(group, kind string) bool {
	if k.Inventory != InventoryComplete {
		return false
	}
	ck, ok := karpenter.KindOf(group, kind)
	if !ok {
		return true
	}
	h, _ := k.Reading(ck)
	return h.NoneSupports
}

// Task is the controller task's state and reason (§5): not ready, unknown,
// managed, healthy, in that order. Platform evidence never clears an
// unread inventory, and an observed failure is never hidden by a managed
// kind beside it: the management is said as a note.
func (k KarpenterCapability) Task() (string, string) {
	var failures, gaps, notes []string
	add := func(list *[]string, s string) {
		for _, x := range *list {
			if x == s {
				return
			}
		}
		*list = append(*list, s)
	}
	for _, c := range k.Controllers {
		if c.Provider != "" && !c.Ready {
			add(&failures, "the karpenter controller "+c.Namespace+"/"+c.Name+" ("+c.Provider.label()+") is not ready")
		}
	}
	if k.Inventory != InventoryComplete {
		why := "the Karpenter controllers could not all be read"
		if k.InventoryWhy != "" {
			why += " (" + k.InventoryWhy + ")"
		}
		add(&gaps, why)
	}
	inUse := 0
	for _, kk := range k.Kinds {
		if !kk.InUse {
			continue
		}
		inUse++
		h, text := k.Reading(kk.Kind)
		switch h.State {
		case karpenter.HandlerManaged:
			add(&notes, kk.Kind.Kind+" "+text)
		case karpenter.HandlerUnproven:
			// the inventory's own gap already says an unread inventory
			if !strings.HasPrefix(text, "the Karpenter controllers could not all be read") {
				add(&gaps, kk.Kind.Kind+": "+text)
			}
		}
	}
	if inUse == 0 && len(k.Controllers) == 0 && k.Inventory == InventoryComplete {
		add(&gaps, "no Karpenter controller found (no Deployment labelled "+karpenterNameSelector+
			", none named karpenter in kube-system or karpenter) and no NodeClass kind in use")
	}
	join := func(parts ...[]string) string {
		var all []string
		for _, p := range parts {
			all = append(all, p...)
		}
		return strings.Join(all, "; ")
	}
	switch {
	case len(failures) > 0:
		return KarpenterNotReady, join(failures, gaps, notes)
	case len(gaps) > 0:
		return KarpenterUnknown, join(gaps, notes)
	case len(notes) > 0:
		return KarpenterManaged, join(notes)
	}
	return KarpenterHealthy, ""
}

// settle derives Healthy, Verified, the reason and the tier from the task.
func (k *KarpenterCapability) settle() {
	state, reason := k.Task()
	k.Healthy = state == KarpenterHealthy || state == KarpenterManaged
	k.Verified = state != KarpenterUnknown
	k.Base = Base{Tier: Classify(true, k.Healthy), Reason: reason}
}

// Refreshed is a re-probe's capability given the last one (§5): a NAP read
// that failed or was cut keeps the last activity only until its newest
// time leaves the window, then expires to "not established".
func (k KarpenterCapability) Refreshed(prev KarpenterCapability, now time.Time) KarpenterCapability {
	if !k.Present {
		return k
	}
	if k.NAP.State == NAPNotEstablished && prev.NAP.State == NAPActive && now.Sub(prev.NAP.Newest) <= NAPWindow {
		why := k.NAP.Why
		k.NAP = prev.NAP
		k.NAP.Why = "kept from the last read until it leaves the window: " + why
		k.settle()
	}
	return k
}

func (d *Detector) clockNow() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *Detector) detectKarpenter(ctx context.Context, served map[string]bool) KarpenterCapability {
	out := KarpenterCapability{Present: served[karpenterGroup]}
	if !out.Present {
		out.Base = Base{Tier: Absent, Reason: "no Karpenter CRDs installed"}
		return out
	}
	out.Controllers, out.Inventory, out.InventoryWhy = d.karpenterInventory(ctx)
	refs, refsRead := d.karpenterPoolRefs(ctx)
	for _, ck := range karpenter.ClassKinds {
		kk := KarpenterKind{Kind: ck}
		if res, _ := d.groupResources(served, ck.Group, ck.Version); res[ck.Resource] {
			kk.Served = true
			kk.InUse, kk.InUseWhy = d.karpenterKindInUse(ctx, ck, refs, refsRead)
		} else {
			kk.OtherVersion = d.servedElsewhere(served, ck)
		}
		out.Kinds = append(out.Kinds, kk)
		switch {
		case !kk.Served:
		case ck.Adapter == karpenter.AdapterEKSAuto:
			out.AutoMode, out.AutoModeWhy = d.autoModeNode(ctx)
		case ck.Adapter == karpenter.AdapterAKS:
			out.NAPInstalled = d.napInstalled(ctx)
			out.NAP = d.napActivity(ctx, d.clockNow())
			out.AzureNodes = d.azureNodes(ctx)
		}
	}
	out.settle()
	return out
}

// servedElsewhere: the kind's resource is served at a version other than
// the pinned one.
func (d *Detector) servedElsewhere(served map[string]bool, ck karpenter.ClassKind) bool {
	var others []string
	for gv := range served {
		if v, ok := strings.CutPrefix(gv, ck.Group+"/"); ok && v != ck.Version {
			others = append(others, v)
		}
	}
	if len(others) == 0 {
		return false
	}
	res, _ := d.groupResources(served, ck.Group, others...)
	return res[ck.Resource]
}

// karpenterInventory reads the candidate controllers (§5): Deployments
// labelled as Karpenter's in any namespace, plus the conventional
// karpenter Deployment in kube-system and karpenter.
func (d *Detector) karpenterInventory(ctx context.Context) ([]KarpenterController, Inventory, string) {
	var deps []appsv1.Deployment
	var gaps []string
	outcome := InventoryComplete
	list, err := d.cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: karpenterNameSelector, Limit: karpenterMaxCandidates + 1})
	switch {
	case err != nil:
		outcome = InventoryUnreadable
		gaps = append(gaps, "the Deployments labelled "+karpenterNameSelector+" could not be listed: "+err.Error())
	case list.Continue != "":
		outcome = InventoryIncomplete
		gaps = append(gaps, "more Deployments are labelled "+karpenterNameSelector+" than are read")
		deps = list.Items
	default:
		deps = list.Items
	}
	seen := map[string]bool{}
	for i := range deps {
		seen[string(deps[i].UID)] = true
	}
	for _, ns := range []string{"kube-system", "karpenter"} {
		dep, err := d.cs.AppsV1().Deployments(ns).Get(ctx, "karpenter", metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			if outcome == InventoryComplete {
				outcome = InventoryIncomplete
			}
			gaps = append(gaps, "Deployment "+ns+"/karpenter unreadable: "+err.Error())
		case !seen[string(dep.UID)]:
			seen[string(dep.UID)] = true
			deps = append(deps, *dep)
		}
	}
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].Namespace != deps[j].Namespace {
			return deps[i].Namespace < deps[j].Namespace
		}
		return deps[i].Name < deps[j].Name
	})
	if len(deps) > karpenterMaxCandidates {
		deps = deps[:karpenterMaxCandidates]
		if outcome == InventoryComplete {
			outcome = InventoryIncomplete
		}
		gaps = append(gaps, fmt.Sprintf("more than %d candidate controllers", karpenterMaxCandidates))
	}
	out := make([]KarpenterController, 0, len(deps))
	for i := range deps {
		c := KarpenterController{Namespace: deps[i].Namespace, Name: deps[i].Name, UID: string(deps[i].UID), Ready: DeploymentReady(&deps[i])}
		c.Provider, c.Why = d.karpenterProvider(ctx, &deps[i])
		if c.Provider == "" {
			if outcome == InventoryComplete {
				outcome = InventoryIncomplete
			}
			gaps = append(gaps, c.Namespace+"/"+c.Name+" is unattributed: "+c.Why)
		}
		out = append(out, c)
	}
	return out, outcome, strings.Join(gaps, "; ")
}

// imageRepo is an image's repository: the reference without its tag or
// digest.
func imageRepo(image string) string {
	if i := strings.IndexByte(image, '@'); i >= 0 {
		image = image[:i]
	}
	if c := strings.LastIndexByte(image, ':'); c > strings.LastIndexByte(image, '/') {
		image = image[:c]
	}
	return image
}

func providerOfImage(image string) KarpenterProvider {
	switch imageRepo(image) {
	case karpenterImageAWS:
		return ProviderAWS
	case karpenterImageAzure:
		return ProviderAzure
	}
	return ""
}

// karpenterProvider attributes a candidate by its running pods' images,
// fenced by owner UID (its ReplicaSets, then their pods): every pod must
// run one provider's controller image.
func (d *Detector) karpenterProvider(ctx context.Context, dep *appsv1.Deployment) (KarpenterProvider, string) {
	if dep.Spec.Selector == nil {
		return "", "the Deployment has no selector"
	}
	sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return "", "its selector cannot be read"
	}
	rss, err := d.cs.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String(), Limit: 50})
	if err != nil || rss.Continue != "" {
		return "", "its ReplicaSets could not all be read"
	}
	owned := map[string]bool{}
	for i := range rss.Items {
		if controlledBy(rss.Items[i].OwnerReferences, string(dep.UID)) {
			owned[string(rss.Items[i].UID)] = true
		}
	}
	pods, err := d.cs.CoreV1().Pods(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String(), Limit: 50})
	if err != nil || pods.Continue != "" {
		return "", "its pods could not all be read"
	}
	var provider KarpenterProvider
	n := 0
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || (p.Status.Phase != corev1.PodRunning && p.Status.Phase != corev1.PodPending) || !ownedByAny(p.OwnerReferences, owned) {
			continue
		}
		if n++; n > karpenterMaxPods {
			return "", fmt.Sprintf("more than %d running pods", karpenterMaxPods)
		}
		var pp KarpenterProvider
		for _, c := range p.Spec.Containers {
			if cp := providerOfImage(c.Image); cp != "" {
				if pp != "" && pp != cp {
					return "", "a pod runs both providers' images"
				}
				pp = cp
			}
		}
		switch {
		case pp == "":
			return "", "a pod runs no known Karpenter controller image (a custom or mirrored image proves no provider)"
		case provider != "" && pp != provider:
			return "", "its pods run different providers' images"
		}
		provider = pp
	}
	if n == 0 {
		return "", "no running pods"
	}
	return provider, ""
}

// karpenterPoolRefs reads the class kinds the NodePools reference; false
// when the list failed or was continued (then no kind is ruled out).
func (d *Detector) karpenterPoolRefs(ctx context.Context) (map[string]bool, bool) {
	raw, err := d.list(ctx, "/apis/karpenter.sh/v1/nodepools", map[string]string{"limit": fmt.Sprint(karpenterMaxPools)})
	if err != nil {
		return nil, false
	}
	var l struct {
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
		Items []struct {
			Spec struct {
				Template struct {
					Spec struct {
						NodeClassRef *struct {
							Group string `json:"group"`
							Kind  string `json:"kind"`
						} `json:"nodeClassRef"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &l) != nil || l.Metadata.Continue != "" {
		return nil, false
	}
	refs := map[string]bool{}
	for _, p := range l.Items {
		if r := p.Spec.Template.Spec.NodeClassRef; r != nil {
			refs[r.Group+"/"+r.Kind] = true
		}
	}
	return refs, true
}

// karpenterKindInUse: a served kind is in use with classes or a NodePool
// referencing it; a read that cannot rule either out counts it in use.
func (d *Detector) karpenterKindInUse(ctx context.Context, ck karpenter.ClassKind, refs map[string]bool, refsRead bool) (bool, string) {
	if refsRead && refs[ck.Group+"/"+ck.Kind] {
		return true, "referenced by a NodePool"
	}
	raw, err := d.list(ctx, "/apis/"+ck.Group+"/"+ck.Version+"/"+ck.Resource, map[string]string{"limit": "1"})
	var l struct {
		Items []json.RawMessage `json:"items"`
	}
	switch {
	case err != nil || json.Unmarshal(raw, &l) != nil:
		return true, "its classes could not be read"
	case len(l.Items) > 0:
		return true, "classes exist"
	case !refsRead:
		return true, "the NodePools could not all be read"
	}
	return false, ""
}

// autoModeNode is Auto Mode's evidence beside its served API: one node
// labelled compute-type auto.
func (d *Detector) autoModeNode(ctx context.Context) (bool, string) {
	nodes, err := d.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: autoModeLabel, Limit: 1})
	if err != nil {
		return false, "the nodes labelled " + autoModeLabel + " could not be read: " + err.Error()
	}
	return len(nodes.Items) > 0, ""
}

// azureNodes: one node read, its providerID azure:// (§6: an AKS cluster).
// A failed read proves nothing either way: false.
func (d *Detector) azureNodes(ctx context.Context) bool {
	nodes, err := d.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	return err == nil && len(nodes.Items) > 0 && strings.HasPrefix(nodes.Items[0].Spec.ProviderID, "azure://")
}

// napInstalled: the NodePool CRD carries NAP's Helm release annotation.
func (d *Detector) napInstalled(ctx context.Context) bool {
	raw, err := d.get(ctx, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/nodepools.karpenter.sh")
	if err != nil {
		return false
	}
	var crd struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &crd) != nil {
		return false
	}
	a := crd.Metadata.Annotations
	return a["meta.helm.sh/release-name"] == napRelease && a["meta.helm.sh/release-namespace"] == "kube-system"
}

// eventTime is an Event's latest observation (§5): its series' last
// observation, else lastTimestamp, else eventTime, else firstTimestamp.
func eventTime(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

// napActivity reads NAP's Events (§5): field-selected, at most three pages
// of 100, the newest time across every page read.
func (d *Detector) napActivity(ctx context.Context, now time.Time) NAPActivity {
	var newest time.Time
	cut, failed := false, ""
	cont := ""
	for page := 0; ; page++ {
		if page == napMaxPages {
			cut = true
			break
		}
		l, err := d.cs.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "source=" + napEventSource, Limit: napPageSize, Continue: cont})
		if err != nil {
			failed = err.Error()
			break
		}
		for i := range l.Items {
			if t := eventTime(&l.Items[i]); t.After(newest) {
				newest = t
			}
		}
		if cont = l.Continue; cont == "" {
			break
		}
	}
	if !newest.IsZero() && now.Sub(newest) <= NAPWindow {
		return NAPActivity{State: NAPActive, Newest: newest}
	}
	switch {
	case failed != "":
		return NAPActivity{State: NAPNotEstablished, Why: "its Events could not be read: " + failed}
	case cut:
		return NAPActivity{State: NAPNotEstablished, Why: "its Events past three pages were not read"}
	}
	return NAPActivity{State: NAPNone}
}

// get and list read one API path through the discovery REST client, or the
// injected seams.
func (d *Detector) get(ctx context.Context, path string) ([]byte, error) {
	return d.list(ctx, path, nil)
}

func (d *Detector) list(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	if d.rawList != nil {
		return d.rawList(ctx, path, params)
	}
	if len(params) == 0 && d.rawGet != nil {
		return d.rawGet(ctx, path)
	}
	rc := d.cs.Discovery().RESTClient()
	if rc == nil {
		return nil, fmt.Errorf("no REST client for %s", path)
	}
	r := rc.Get().AbsPath(path)
	for k, v := range params {
		r = r.Param(k, v)
	}
	return r.DoRaw(ctx)
}
