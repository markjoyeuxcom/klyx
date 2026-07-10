package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
// NamespaceOwner* fields carry the GitOps prune-reach verdict for the target
// namespace (empty = no managing unit DETECTED, never "safe").
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

	gvr, namespaced, err := c.resolveGVK(u.GetAPIVersion(), kind)
	if err != nil {
		r.Error = err.Error()
		return r
	}
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

	// Dry-run enriches with the prune-reach verdict so the composer can warn
	// BEFORE the commit step ("this namespace belongs to Kustomization X, which
	// prunes - your object will be DELETED on the next reconcile").
	if dryRun && namespaced {
		o := c.GitOpsOwnership(ctx, gvr.Group, kind, ns, name)
		r.NamespaceOwnerKind = o.NamespaceOwnerKind
		r.NamespaceOwnerName = o.NamespaceOwnerName
		r.NamespaceOwnerNamespace = o.NamespaceOwnerNamespace
		r.NamespacePrunes = o.NamespacePrunes
	}
	return r
}

// resolveGVK maps the document's own apiVersion+kind to the served GVR + scope
// via the TTL-cached discovery. The doc declares its version, so this is exact -
// no preferred-version guessing. A miss names the likely causes (typo, CRD not
// installed, or discovery cache staleness for a just-installed CRD).
func (c *ClusterConn) resolveGVK(apiVersion, kind string) (schema.GroupVersionResource, bool, error) {
	if c.typed == nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("discovery unavailable: no cluster connection")
	}
	rl, err := c.discovery().ServerResourcesForGroupVersion(apiVersion)
	if err != nil || rl == nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("apiVersion %q is not served (typo, or the CRD is not installed)", apiVersion)
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("parse apiVersion %q: %v", apiVersion, err)
	}
	for _, res := range rl.APIResources {
		if strings.Contains(res.Name, "/") { // subresources (pods/log, ...)
			continue
		}
		if res.Kind == kind {
			return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: res.Name}, res.Namespaced, nil
		}
	}
	return schema.GroupVersionResource{}, false, fmt.Errorf("kind %q is not served under %q (typo, or a just-installed CRD - discovery refreshes within a minute)", kind, apiVersion)
}
