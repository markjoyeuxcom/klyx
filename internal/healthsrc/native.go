package healthsrc

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/observe"
)

// Native-domain kinds and tasks (1c addendum §1). Each CRD kind resolves its
// own served version (KindSpec.Probe); a task is absent when its CRDs are.
const (
	KindCertificates        = "certificates"
	KindIssuers             = "issuers"
	KindClusterIssuers      = "clusterissuers"
	KindExternalSecrets     = "externalsecrets"
	KindSecretStores        = "secretstores"
	KindClusterSecretStores = "clustersecretstores"
	KindNodePools           = "nodepools"
	KindNodeClaims          = "nodeclaims"

	TaskCerts     = "certs"
	TaskESO       = "eso"
	TaskKarpenter = "karpenter"
)

// NativeDeps are the clients the native tasks need.
type NativeDeps struct {
	Cluster string
	Dyn     dynamic.Interface
	// Served resolves one CRD resource's served version from discovery
	// (absent, a version, or an error that confirms nothing).
	Served func(ctx context.Context, group, resource, fallback string) (observe.Probe, error)
	// ServedAmong resolves the first of a resource's candidate versions
	// discovery serves (coverage S7 §2). Nil: Served with the first
	// candidate as its fallback, its answer checked against the list.
	ServedAmong func(ctx context.Context, group, resource string, candidates []string) (observe.Probe, error)
	// Typed and Meta serve the core kinds the Gateway routes part reads as
	// evidence (EndpointSlices, Service existence).
	Typed kubernetes.Interface
	Meta  metadata.Interface
}

// servedAmong resolves a resource's version among candidates: a version
// outside them is a named gap, never a list Klyx would misread.
func (d NativeDeps) servedAmong(ctx context.Context, group, resource string, candidates []string) (observe.Probe, error) {
	if d.ServedAmong != nil {
		return d.ServedAmong(ctx, group, resource, candidates)
	}
	p, err := d.Served(ctx, group, resource, candidates[0])
	if err == nil && p.Served && !slices.Contains(candidates, p.Version) {
		return observe.Probe{}, fmt.Errorf("%s/%s is served at %s; Klyx reads %s", group, resource, p.Version, strings.Join(candidates, " or "))
	}
	return p, err
}

// Native returns the cert-manager, External Secrets and Karpenter kinds and
// tasks. Their rules are the relocated lens rules (internal/*/rules.go).
func Native(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	crd := func(name, group, resource, fallback, scope string) observe.KindSpec {
		return crdKind(d, name, group, resource, fallback, scope)
	}
	kinds := []observe.KindSpec{
		crd(KindCertificates, "cert-manager.io", "certificates", "v1", "all namespaces"),
		crd(KindIssuers, "cert-manager.io", "issuers", "v1", "all namespaces"),
		crd(KindClusterIssuers, "cert-manager.io", "clusterissuers", "v1", "cluster-scoped"),
		crd(KindExternalSecrets, "external-secrets.io", "externalsecrets", "v1beta1", "all namespaces"),
		crd(KindSecretStores, "external-secrets.io", "secretstores", "v1beta1", "all namespaces"),
		crd(KindClusterSecretStores, "external-secrets.io", "clustersecretstores", "v1beta1", "cluster-scoped"),
		crd(KindNodePools, "karpenter.sh", "nodepools", "v1", "cluster-scoped"),
		crd(KindNodeClaims, "karpenter.sh", "nodeclaims", "v1", "cluster-scoped"),
	}
	c := d.Cluster
	domains := []observe.DomainSpec{
		{ID: TaskCerts, Parts: []observe.PartSpec{
			{ID: "certificates", Entity: "Certificates", Scope: "all namespaces", Classifier: "native",
				Counted: []string{KindCertificates}, Evaluate: evalCertificates(c), Ledger: true},
			{ID: "issuers", Entity: "Issuers and ClusterIssuers", Scope: "all namespaces + cluster-scoped", Classifier: "native",
				Counted: []string{KindIssuers, KindClusterIssuers}, Evaluate: evalIssuers(c), Ledger: true},
		}},
		{ID: TaskESO, Parts: []observe.PartSpec{
			{ID: "externalsecrets", Entity: "ExternalSecrets", Scope: "all namespaces", Classifier: "native",
				Counted: []string{KindExternalSecrets}, Evaluate: evalExternalSecrets(c), Ledger: true},
			{ID: "stores", Entity: "SecretStores and ClusterSecretStores", Scope: "all namespaces + cluster-scoped", Classifier: "native",
				Counted: []string{KindSecretStores, KindClusterSecretStores}, Evaluate: evalStores(c), Ledger: true},
		}},
		{ID: TaskKarpenter, Parts: []observe.PartSpec{
			{ID: "nodeclaims", Entity: "NodeClaims", Scope: "cluster-scoped", Classifier: "native",
				Counted: []string{KindNodeClaims}, Evaluate: evalNodeClaims(c), Ledger: true},
			{ID: "nodepools", Entity: "NodePools", Scope: "cluster-scoped", Classifier: "native",
				Counted: []string{KindNodePools}, Deps: []string{KindNodeClaims}, Evaluate: evalNodePools(c), Ledger: true},
		}},
	}
	return kinds, domains
}

// ProjectUnstructured drops what no parser reads: managedFields and the
// kubectl last-applied annotation (often the largest field). It copies.
func ProjectUnstructured(obj any) (any, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, errWant("*Unstructured", obj)
	}
	out := u.DeepCopy()
	out.SetManagedFields(nil)
	if ann := out.GetAnnotations(); ann != nil {
		if _, big := ann["kubectl.kubernetes.io/last-applied-configuration"]; big {
			delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
			if len(ann) == 0 {
				ann = nil
			}
			out.SetAnnotations(ann)
		}
	}
	return out, nil
}

func unstructuredOf(objs []any) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.(*unstructured.Unstructured))
	}
	return out
}

type nativeItem struct {
	eval health.NativeEval
	ref  refInput
}

// nativeResult partitions relocated-rule verdicts by role, counts the lens's
// inclusion, and picks Top refs among included objects (role order, then
// namespace/name) - spec §2.
func nativeResult(roleCap int, cluster, route, word string, items []nativeItem) observe.PartResult {
	nc := observe.NativeCounts{Word: word}
	var next *time.Time
	var incl []nativeItem
	roles := newRoles(roleCap, len(items))
	capped := false
	for _, it := range items {
		roles = appendRole(roles, &capped, roleCap, observe.RoleEntry{Source: it.ref.source, GVR: it.ref.gvr, Key: it.ref.key(),
			NS: it.ref.ns, Name: it.ref.name, Role: it.eval.Role.String(), Label: it.eval.Label, Since: it.eval.Since,
			Boundary: it.eval.Boundary, Condition: conditionOf(it.eval.Cond), Reason: it.eval.Reason})
		nc.Total++
		switch it.eval.Role {
		case health.Fault:
			nc.Roles.Fault++
		case health.Degraded:
			nc.Roles.Degraded++
		case health.Progressing:
			nc.Roles.Progressing++
		case health.Paused:
			nc.Roles.Paused++
		case health.OK:
			nc.Roles.OK++
		default:
			nc.Roles.Unknown++
		}
		nc.Roles.Total++
		if it.eval.Included {
			nc.Included++
			incl = append(incl, it)
		}
		if n := it.eval.Next; n != nil && (next == nil || n.Before(*next)) {
			t := *n
			next = &t
		}
	}
	sort.SliceStable(incl, func(a, b int) bool {
		if incl[a].eval.Role != incl[b].eval.Role {
			return incl[a].eval.Role < incl[b].eval.Role
		}
		if incl[a].ref.ns != incl[b].ref.ns {
			return incl[a].ref.ns < incl[b].ref.ns
		}
		return incl[a].ref.name < incl[b].ref.name
	})
	var top []observe.Ref
	for _, it := range incl {
		if len(top) == maxTop {
			break
		}
		top = append(top, observe.Ref{Cluster: cluster, GVR: it.ref.gvr, Namespace: it.ref.ns, Name: it.ref.name, UID: it.ref.uid,
			Role: it.eval.Role.String(), Label: it.eval.Label, Generation: it.ref.generation, Route: route,
			Since: it.eval.Since, Condition: conditionOf(it.eval.Cond), Reason: it.eval.Reason})
	}
	return observe.PartResult{Native: &nc, Top: top, Next: next, Roles: roles, RolesCapped: capped}
}

// conditionOf carries a rule's condition evidence onto a ref.
func conditionOf(c *health.CondSince) *observe.Condition {
	if c == nil {
		return nil
	}
	return &observe.Condition{Type: c.Type, Status: c.Status, Since: c.At}
}

// refOf builds a Top ref whose GVR is the version the object was served at
// (its own apiVersion, from the kind's current generation) - never a
// documented fallback - plus the resource: "cert-manager.io/v1/certificates",
// the same group/version/resource form as core refs.
func refOf(u *unstructured.Unstructured, resource string) refInput {
	return refInput{gvr: u.GetAPIVersion() + "/" + resource, ns: u.GetNamespace(), name: u.GetName(), uid: string(u.GetUID()), generation: u.GetGeneration(),
		source: resource} // kind names are their resources
}

func evalCertificates(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[certmanager.Certificate]
		for _, u := range unstructuredOf(c.Objects[KindCertificates]) {
			cert := certmanager.ParseCertificate(u)
			it := nativeItem{certmanager.CertificateRule(cert, c.Now), refOf(u, "certificates")}
			items = append(items, it)
			rows = append(rows, nativeRow(it, cert))
		}
		r := nativeResult(c.RoleCap, cluster, TaskCerts, "not ready", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

func evalIssuers(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[certmanager.Issuer]
		for _, kind := range []string{KindIssuers, KindClusterIssuers} {
			for _, u := range unstructuredOf(c.Objects[kind]) {
				is := certmanager.ParseIssuer(u)
				it := nativeItem{certmanager.IssuerRule(is), refOf(u, kind)}
				items = append(items, it)
				rows = append(rows, nativeRow(it, is))
			}
		}
		r := nativeResult(c.RoleCap, cluster, TaskCerts, "not ready", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

func evalExternalSecrets(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[externalsecrets.ExternalSecret]
		for _, u := range unstructuredOf(c.Objects[KindExternalSecrets]) {
			e := externalsecrets.ParseExternalSecret(u, c.Now)
			it := nativeItem{externalsecrets.ExternalSecretRule(e), refOf(u, "externalsecrets")}
			items = append(items, it)
			rows = append(rows, nativeRow(it, e))
		}
		r := nativeResult(c.RoleCap, cluster, TaskESO, "not synced or refresh overdue", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

func evalStores(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[externalsecrets.Store]
		for _, kind := range []string{KindSecretStores, KindClusterSecretStores} {
			for _, u := range unstructuredOf(c.Objects[kind]) {
				s := externalsecrets.ParseStore(u)
				it := nativeItem{externalsecrets.StoreRule(s), refOf(u, kind)}
				items = append(items, it)
				rows = append(rows, nativeRow(it, s))
			}
		}
		r := nativeResult(c.RoleCap, cluster, TaskESO, "not ready", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

func evalNodeClaims(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[karpenter.NodeClaim]
		for _, u := range unstructuredOf(c.Objects[KindNodeClaims]) {
			cl := karpenter.ParseNodeClaim(u, c.Now)
			it := nativeItem{karpenter.ClaimRule(cl), refOf(u, "nodeclaims")}
			items = append(items, it)
			rows = append(rows, nativeRow(it, cl))
		}
		r := nativeResult(c.RoleCap, cluster, TaskKarpenter, "not ready or appear stuck", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

func evalNodePools(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		perPool := map[string]int{}
		for _, u := range unstructuredOf(c.Objects[KindNodeClaims]) {
			perPool[u.GetLabels()[karpenter.LabelNodePool]]++
		}
		var items []nativeItem
		var rows []Row[karpenter.NodePool]
		for _, u := range unstructuredOf(c.Objects[KindNodePools]) {
			p := karpenter.ParseNodePool(u)
			p.Nodes = perPool[p.Meta.Name]
			// Stage 1 of S10: the class link is wired in stage 3; until then
			// it is unresolved and the pool is judged on its own conditions.
			it := nativeItem{karpenter.PoolRule(p, karpenter.PoolInputs{}).Eval, refOf(u, "nodepools")}
			items = append(items, it)
			rows = append(rows, nativeRow(it, p))
		}
		r := nativeResult(c.RoleCap, cluster, TaskKarpenter, "not ready", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

// crdKind is one CRD-backed kind: discovery picks its served version (the
// fallback is only tried when discovery cannot rule it out), the projection
// drops what no parser reads, and the client's watch-list support is
// forwarded.
func crdKind(d NativeDeps, name, group, resource, fallback, scope string) observe.KindSpec {
	return observe.KindSpec{Name: name, GVR: group + "/" + resource, Scope: scope, ExpectedType: &unstructured.Unstructured{},
		Project: ProjectUnstructured,
		Probe: func(ctx context.Context) (observe.Probe, error) {
			return d.Served(ctx, group, resource, fallback)
		},
		NewListWatch: func(version string) cache.ListerWatcherWithContext {
			ri := d.Dyn.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: resource})
			return cache.ToListWatcherWithWatchListSemantics(&cache.ListWatch{
				ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
					return ri.List(ctx, o)
				},
				WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
					return ri.Watch(ctx, o)
				},
			}, d.Dyn).(cache.ListerWatcherWithContext)
		}}
}
