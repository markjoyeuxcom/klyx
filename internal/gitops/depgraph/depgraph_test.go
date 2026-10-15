package depgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// The Line slice 8b: the dependency graph over a fake reader - typed edges,
// reported blockers only where the controller reports them, incomplete
// evidence as stubs, supply facts without causality, change proximity.

var t0 = time.Date(2026, 10, 15, 20, 21, 0, 0, time.UTC)

const sha = "a1b9e3c0d4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9"

type fakeReader struct {
	objs  map[string]*unstructured.Unstructured
	metas map[string]*metav1.PartialObjectMetadata
	fail  map[string]error
	gets  []string
}

func (f *fakeReader) Get(_ context.Context, ref Ref) (*unstructured.Unstructured, error) {
	f.gets = append(f.gets, ref.ID())
	if err := f.fail[ref.ID()]; err != nil {
		return nil, err
	}
	if o, ok := f.objs[ref.ID()]; ok {
		return o, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: ref.Kind}, ref.Name)
}

func (f *fakeReader) GetMeta(_ context.Context, ref Ref) (*metav1.PartialObjectMetadata, error) {
	f.gets = append(f.gets, ref.ID())
	if err := f.fail[ref.ID()]; err != nil {
		return nil, err
	}
	if m, ok := f.metas[ref.ID()]; ok {
		return m, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, ref.Name)
}

func ready(status, reason, msg string, at time.Time) map[string]any {
	return map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": at.Format(time.RFC3339)}
}

func obj(kind, ns, name, uid string, spec map[string]any, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": kind, "metadata": map[string]any{"namespace": ns, "name": name}, "spec": spec, "status": status}}
	u.SetUID(types.UID(uid))
	return u
}

func ks(name string, spec, status map[string]any) *unstructured.Unstructured {
	return obj("Kustomization", "flux-system", name, "uid-"+name, spec, status)
}

func managed(at time.Time, manager, op, sub string) []metav1.ManagedFieldsEntry {
	return []metav1.ManagedFieldsEntry{{Manager: manager, Operation: metav1.ManagedFieldsOperationType(op), Subresource: sub, Time: &metav1.Time{Time: at}}}
}

func world() *fakeReader {
	apps := ks("apps", map[string]any{
		"sourceRef":  map[string]any{"kind": "GitRepository", "name": "platform"},
		"dependsOn":  []any{map[string]any{"name": "infra"}},
		"decryption": map[string]any{"secretRef": map[string]any{"name": "sops-age"}},
		"path":       "./apps",
	}, map[string]any{
		"conditions":          []any{ready("False", "DependencyNotReady", "dependency 'flux-system/infra' is not ready", t0.Add(2*time.Minute))},
		"lastAppliedRevision": "main@sha1:" + sha,
		"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_web_apps_Deployment", "v": "v1"},
			map[string]any{"id": "shop_db_apps_StatefulSet", "v": "v1"},
			map[string]any{"id": "shop_web__Service", "v": "v1"},
		}},
	})
	infra := ks("infra", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "platform"}},
		map[string]any{"conditions": []any{ready("False", "ArtifactFailed", "Source artifact not found, retrying in 30s", t0)}})
	platform := obj("GitRepository", "flux-system", "platform", "uid-platform",
		map[string]any{"url": "ssh://git@git.example.com/platform/fleet.git", "ref": map[string]any{"branch": "main"}, "secretRef": map[string]any{"name": "github-deploy-key"}},
		map[string]any{"conditions": []any{ready("False", "GitOperationFailed", "auth failed", t0.Add(-time.Minute))}})
	es := obj("ExternalSecret", "flux-system", "github-deploy-key", "es-1", map[string]any{}, map[string]any{"conditions": []any{ready("True", "SecretSynced", "", t0.Add(-time.Hour))}})
	es.SetManagedFields(managed(t0.Add(-4*time.Minute), "kubectl", "Update", ""))
	tr := true
	key := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "flux-system", Name: "github-deploy-key", UID: "sec-1",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "external-secrets.io/v1", Kind: "ExternalSecret", Name: "github-deploy-key", UID: "es-1", Controller: &tr}},
		ManagedFields:   managed(t0.Add(-5*time.Minute), "external-secrets", "Update", "")}}
	age := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "flux-system", Name: "sops-age", UID: "sec-2",
		ManagedFields: managed(t0.Add(-time.Hour), "kubectl", "Apply", "")}}
	return &fakeReader{
		objs: map[string]*unstructured.Unstructured{
			"Kustomization/flux-system/apps": apps, "Kustomization/flux-system/infra": infra,
			"GitRepository/flux-system/platform": platform, "ExternalSecret/flux-system/github-deploy-key": es,
		},
		metas: map[string]*metav1.PartialObjectMetadata{"Secret/flux-system/github-deploy-key": key, "Secret/flux-system/sops-age": age},
		fail:  map[string]error{},
	}
}

var appsRef = Ref{GroupKustomize, "Kustomization", "flux-system", "apps"}

func build(t *testing.T, r Reader, uid string) Graph {
	t.Helper()
	return Build(context.Background(), r, appsRef, uid, func() time.Time { return t0.Add(10 * time.Minute) })
}

func node(t *testing.T, g Graph, id string) Node {
	t.Helper()
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %s in %+v", id, g.Nodes)
	return Node{}
}

func edge(g Graph, from, to string) (Edge, bool) {
	for _, e := range g.Edges {
		if e.From == from && e.To == to {
			return e, true
		}
	}
	return Edge{}, false
}

func TestGraphEdgesAndLabels(t *testing.T) {
	r := world()
	g := build(t, r, "uid-apps")
	if g.Start != "Kustomization/flux-system/apps" || len(g.Incomplete) != 0 {
		t.Fatalf("start %s incomplete %v", g.Start, g.Incomplete)
	}
	// The diamond: the GitRepository is read once, reached twice.
	if len(g.Nodes) != 3 || g.Reads != 6 {
		t.Fatalf("nodes %d reads %d (%v)", len(g.Nodes), g.Reads, r.gets)
	}
	srcApps, _ := edge(g, "Kustomization/flux-system/apps", "GitRepository/flux-system/platform")
	depApps, _ := edge(g, "Kustomization/flux-system/apps", "Kustomization/flux-system/infra")
	srcInfra, _ := edge(g, "Kustomization/flux-system/infra", "GitRepository/flux-system/platform")
	if srcApps.Type != EdgeSource || srcApps.Reported {
		t.Fatalf("apps' source edge: %+v (apps reports its dependency, not its source)", srcApps)
	}
	if depApps.Type != EdgeDependsOn || !depApps.Reported {
		t.Fatalf("apps' dependsOn edge: %+v", depApps)
	}
	if !srcInfra.Reported {
		t.Fatalf("infra's source edge: %+v", srcInfra)
	}
	apps, infra, platform := node(t, g, "Kustomization/flux-system/apps"), node(t, g, "Kustomization/flux-system/infra"), node(t, g, "GitRepository/flux-system/platform")
	if apps.UpstreamFailure || !infra.UpstreamFailure || !platform.UpstreamFailure {
		t.Fatalf("upstream failure: apps %v infra %v platform %v", apps.UpstreamFailure, infra.UpstreamFailure, platform.UpstreamFailure)
	}
	if apps.ChainEnd || infra.ChainEnd || !platform.ChainEnd {
		t.Fatalf("chain end: apps %v infra %v platform %v", apps.ChainEnd, infra.ChainEnd, platform.ChainEnd)
	}
	if apps.Inventory == nil || apps.Inventory.Entries != 3 || apps.Inventory.Workloads != 2 || apps.Inventory.LastApplied != "main@sha1:"+sha {
		t.Fatalf("inventory: %+v", apps.Inventory)
	}
	if apps.Git == nil || apps.Git.URL != "ssh://git@git.example.com/platform/fleet.git" || apps.Git.RefKind != "branch" || apps.Git.RefName != "main" || apps.Git.Applied != sha || apps.Git.Path != "./apps" {
		t.Fatalf("git facts: %+v", apps.Git)
	}
	if apps.ReadAt.IsZero() || apps.UID != "uid-apps" || apps.Ready != "False" || !apps.ConditionTime.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("apps: %+v", apps)
	}
}

func TestGraphSupply(t *testing.T) {
	g := build(t, world(), "")
	platform := node(t, g, "GitRepository/flux-system/platform")
	if len(platform.Supply) != 1 {
		t.Fatalf("supply: %+v", platform.Supply)
	}
	s := platform.Supply[0]
	if s.Field != "spec.secretRef" || s.SecretUID != "sec-1" || s.Supplier == nil || s.Supplier.UID != "es-1" || s.Supplier.Ready != "True" {
		t.Fatalf("supplied: %+v", s)
	}
	apps := node(t, g, "Kustomization/flux-system/apps")
	if len(apps.Supply) != 1 || apps.Supply[0].Field != "spec.decryption.secretRef" || apps.Supply[0].Supplier != nil || apps.Supply[0].SupplierNote != "" {
		t.Fatalf("no supplier recorded: %+v", apps.Supply)
	}
	// An owner reference whose UID the read does not return names nobody.
	r := world()
	r.objs["ExternalSecret/flux-system/github-deploy-key"].SetUID("es-2")
	s = node(t, build(t, r, ""), "GitRepository/flux-system/platform").Supply[0]
	if s.Supplier != nil || !strings.Contains(s.SupplierNote, "UID es-1, but that name now has UID es-2") {
		t.Fatalf("mismatched owner: %+v", s)
	}
}

func TestGraphProximity(t *testing.T) {
	g := build(t, world(), "")
	got := map[string]bool{}
	for _, p := range g.Proximity {
		got[p.Failing+" ← "+p.Object] = true
		if p.Write.Subresource == "status" {
			t.Fatalf("a status update was paired: %+v", p)
		}
	}
	// infra's condition is at t0; the deploy key's update (t0-5m) and the
	// ExternalSecret's (t0-4m) fall within 10 minutes before it.
	for _, want := range []string{
		"Kustomization/flux-system/infra ← Secret/flux-system/github-deploy-key",
		"Kustomization/flux-system/infra ← ExternalSecret/flux-system/github-deploy-key",
		"GitRepository/flux-system/platform ← Secret/flux-system/github-deploy-key", // its condition is t0-1m
		"Kustomization/flux-system/apps ← Secret/flux-system/github-deploy-key",
	} {
		if !got[want] {
			t.Errorf("missing pair %s in %v", want, got)
		}
	}
	if got["Kustomization/flux-system/apps ← Secret/flux-system/sops-age"] {
		t.Fatal("an update an hour before was paired")
	}
	// A status-only update is never paired, however close.
	r := world()
	r.metas["Secret/flux-system/github-deploy-key"].ManagedFields = managed(t0.Add(-time.Minute), "external-secrets", "Update", "status")
	for _, p := range build(t, r, "").Proximity {
		if p.Object == "Secret/flux-system/github-deploy-key" {
			t.Fatalf("a status update was paired: %+v", p)
		}
	}
}

func TestGraphReportedNeedsTheControllersWords(t *testing.T) {
	r := world()
	// DependencyNotReady naming another dependency reports no edge to infra.
	r.objs["Kustomization/flux-system/apps"].Object["status"].(map[string]any)["conditions"] = []any{ready("False", "DependencyNotReady", "dependency 'flux-system/other' is not ready", t0)}
	if e, _ := edge(build(t, r, ""), "Kustomization/flux-system/apps", "Kustomization/flux-system/infra"); e.Reported {
		t.Fatal("reported for a dependency the message does not name")
	}
	// ArtifactFailed with a Ready source is not the source's (a missing path).
	r = world()
	r.objs["GitRepository/flux-system/platform"].Object["status"] = map[string]any{"conditions": []any{ready("True", "Succeeded", "", t0)},
		"artifact": map[string]any{"revision": "main@sha1:" + sha}}
	r.objs["Kustomization/flux-system/infra"].Object["status"] = map[string]any{"conditions": []any{ready("False", "ArtifactFailed", "kustomization path not found", t0)}}
	if e, _ := edge(build(t, r, ""), "Kustomization/flux-system/infra", "GitRepository/flux-system/platform"); e.Reported {
		t.Fatal("a Ready source reported as the blocker")
	}
	// A missing dependency the message names is a reported edge to a
	// not-found node, which is a fact, not incomplete evidence.
	r = world()
	r.objs["Kustomization/flux-system/apps"].Object["status"].(map[string]any)["conditions"] = []any{ready("False", "DependencyNotReady", "dependency 'flux-system/infra' not found: kustomizations.kustomize.toolkit.fluxcd.io \"infra\" not found", t0)}
	delete(r.objs, "Kustomization/flux-system/infra")
	g := build(t, r, "")
	var nf Node
	for _, n := range g.Nodes {
		if n.Stub == StubNotFound {
			nf = n
		}
	}
	if nf.ID == "" || len(g.Incomplete) != 0 {
		t.Fatalf("not found: %+v, incomplete %v", g.Nodes, g.Incomplete)
	}
	if e, _ := edge(g, "Kustomization/flux-system/apps", nf.ID); !e.Reported || !nf.ChainEnd {
		t.Fatalf("reported to a missing dependency: %+v %+v", e, nf)
	}
	// Suspended: no reported edges, no failing.
	r = world()
	r.objs["Kustomization/flux-system/apps"].Object["spec"].(map[string]any)["suspend"] = true
	g = build(t, r, "")
	if e, _ := edge(g, "Kustomization/flux-system/apps", "Kustomization/flux-system/infra"); e.Reported || node(t, g, g.Start).Failing {
		t.Fatal("a suspended node reports")
	}
}

func chain(n int) *fakeReader {
	r := &fakeReader{objs: map[string]*unstructured.Unstructured{}, metas: map[string]*metav1.PartialObjectMetadata{}, fail: map[string]error{}}
	for i := 0; i < n; i++ {
		spec := map[string]any{}
		if i+1 < n {
			spec["dependsOn"] = []any{map[string]any{"name": fmt.Sprintf("k%d", i+1)}}
		}
		r.objs[fmt.Sprintf("Kustomization/flux-system/k%d", i)] = ks(fmt.Sprintf("k%d", i), spec, map[string]any{})
	}
	return r
}

func TestGraphBounds(t *testing.T) {
	// Depth: k0 at depth 0, k6 at depth 6 is read but not followed.
	g := Build(context.Background(), chain(9), Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	if len(g.Nodes) != 8 || g.Reads != 7 || !containsPrefix(g.Incomplete, "depth: not followed past depth 6") {
		t.Fatalf("depth: %d nodes, %d reads, %v", len(g.Nodes), g.Reads, g.Incomplete)
	}
	// Nodes: 30 dependencies of one node.
	r := chain(1)
	var deps []any
	for i := 1; i <= 30; i++ {
		deps = append(deps, map[string]any{"name": fmt.Sprintf("d%d", i)})
		r.objs[fmt.Sprintf("Kustomization/flux-system/d%d", i)] = ks(fmt.Sprintf("d%d", i), map[string]any{}, map[string]any{})
	}
	r.objs["Kustomization/flux-system/k0"].Object["spec"] = map[string]any{"dependsOn": deps}
	g = Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	read := 0
	for _, n := range g.Nodes {
		if n.Stub == "" {
			read++
		}
	}
	if read != MaxNodes || g.Reads != MaxReads || !containsPrefix(g.Incomplete, "node limit") {
		t.Fatalf("nodes: %d read, %d reads, %v", read, g.Reads, g.Incomplete)
	}
	// Reads: every node also reads its decryption Secret, so the reads run out first.
	for k, o := range r.objs {
		if k != "Kustomization/flux-system/k0" {
			o.Object["spec"] = map[string]any{"decryption": map[string]any{"secretRef": map[string]any{"name": "age"}}}
		}
	}
	g = Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	if g.Reads != MaxReads || !containsPrefix(g.Incomplete, "read limit") {
		t.Fatalf("reads: %d reads, %v", g.Reads, g.Incomplete)
	}
}

func TestGraphIncompleteEvidence(t *testing.T) {
	// A cycle is cut and named.
	r := chain(2)
	r.objs["Kustomization/flux-system/k1"].Object["spec"] = map[string]any{"dependsOn": []any{map[string]any{"name": "k0"}}}
	g := Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	if !containsPrefix(g.Incomplete, "cycle: Kustomization/flux-system/k1 → Kustomization/flux-system/k0") {
		t.Fatalf("cycle: %v", g.Incomplete)
	}
	// Unreadable.
	r = world()
	r.fail["Kustomization/flux-system/infra"] = errors.New("forbidden: no list")
	g = build(t, r, "")
	if !containsPrefix(g.Incomplete, "unreadable: Kustomization/flux-system/infra: forbidden") {
		t.Fatalf("unreadable: %v", g.Incomplete)
	}
	// Replaced: the selection's UID is not the one read.
	g = build(t, world(), "uid-older")
	if node(t, g, g.Start).Stub != StubReplaced || !containsPrefix(g.Incomplete, "replaced: Kustomization/flux-system/apps now has UID uid-apps") {
		t.Fatalf("replaced: %+v %v", g.Nodes, g.Incomplete)
	}
	// Stale: both generations named.
	r = world()
	r.objs["Kustomization/flux-system/infra"].SetGeneration(5)
	r.objs["Kustomization/flux-system/infra"].Object["status"].(map[string]any)["observedGeneration"] = int64(4)
	g = build(t, r, "")
	if !node(t, g, "Kustomization/flux-system/infra").Stale || !containsPrefix(g.Incomplete, "stale: Kustomization/flux-system/infra status describes generation 4 of 5") {
		t.Fatalf("stale: %v", g.Incomplete)
	}
	// Cancelled before the walk.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g = Build(ctx, world(), appsRef, "", time.Now)
	if node(t, g, g.Start).Stub != StubCancelled || !containsPrefix(g.Incomplete, "cancelled") {
		t.Fatalf("cancelled: %+v %v", g.Nodes, g.Incomplete)
	}
}

// A HelmRelease's source is the HelmChart its status records, and the
// chart's own source follows.
func TestGraphHelmRelease(t *testing.T) {
	hr := obj("HelmRelease", "apps", "podinfo", "uid-hr", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "podinfo", "namespace": "flux-system"}}}},
		map[string]any{"helmChart": "flux-system/apps-podinfo", "conditions": []any{ready("False", "SourceNotReady", "HelmChart 'flux-system/apps-podinfo' is not ready: does not have an artifact", t0)}})
	chart := obj("HelmChart", "flux-system", "apps-podinfo", "uid-hc", map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "podinfo"}},
		map[string]any{"conditions": []any{ready("False", "IndexationFailed", "", t0)}})
	repo := obj("HelmRepository", "flux-system", "podinfo", "uid-repo", map[string]any{}, map[string]any{"conditions": []any{ready("False", "IndexationFailed", "", t0)}})
	r := &fakeReader{objs: map[string]*unstructured.Unstructured{"HelmRelease/apps/podinfo": hr, "HelmChart/flux-system/apps-podinfo": chart, "HelmRepository/flux-system/podinfo": repo}, fail: map[string]error{}}
	g := Build(context.Background(), r, Ref{GroupHelm, "HelmRelease", "apps", "podinfo"}, "", time.Now)
	e1, ok1 := edge(g, "HelmRelease/apps/podinfo", "HelmChart/flux-system/apps-podinfo")
	_, ok2 := edge(g, "HelmChart/flux-system/apps-podinfo", "HelmRepository/flux-system/podinfo")
	if !ok1 || !ok2 || !e1.Reported {
		t.Fatalf("helm edges: %+v", g.Edges)
	}
}

func containsPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// Design review of 06f7f47: the graph stays bounded with its stubs, every
// object is read once (failures and supply included), a condition reports
// only for the generation it describes, helm-controller's SourceNotReady is
// its own report, and a recorded zero observedGeneration is lag.
func TestGraphReviewOf06f7f47(t *testing.T) {
	// 1,000 dependencies: 24 nodes read in all, the rest one summary stub.
	r := chain(1)
	var deps []any
	for i := 0; i < 1000; i++ {
		deps = append(deps, map[string]any{"name": fmt.Sprintf("d%d", i)})
	}
	r.objs["Kustomization/flux-system/k0"].Object["spec"] = map[string]any{"dependsOn": deps}
	g := Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	if len(g.Nodes) != MaxNodes+1 || len(g.Edges) != MaxNodes || g.Reads != MaxNodes {
		t.Fatalf("1,000 dependencies: %d nodes, %d edges, %d reads", len(g.Nodes), len(g.Edges), g.Reads)
	}
	last := g.Nodes[len(g.Nodes)-1]
	if last.Stub != StubNodes || last.Note != "977 not read: the 24-node limit" {
		t.Fatalf("summary: %+v", last)
	}
	// Depth: one summary per node, whatever its fan-out.
	r = chain(7)
	r.objs["Kustomization/flux-system/k6"].Object["spec"] = map[string]any{"dependsOn": []any{map[string]any{"name": "x"}, map[string]any{"name": "y"}, map[string]any{"name": "z"}}}
	g = Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	depth := 0
	for _, n := range g.Nodes {
		if n.Stub == StubDepth {
			depth++
			if n.Note != "3 not followed past depth 6" {
				t.Fatalf("depth summary: %+v", n)
			}
		}
	}
	if depth != 1 {
		t.Fatalf("%d depth stubs", depth)
	}

	// A missing source reached twice is read once, one node.
	r = world()
	delete(r.objs, "GitRepository/flux-system/platform")
	g = build(t, r, "")
	reads := 0
	for _, id := range r.gets {
		if id == "GitRepository/flux-system/platform" {
			reads++
		}
	}
	if reads != 1 || node(t, g, "GitRepository/flux-system/platform").Stub != StubNotFound {
		t.Fatalf("missing diamond: %d reads", reads)
	}
	// A Secret (and its ExternalSecret) shared by two nodes is read once.
	r = world()
	r.objs["Kustomization/flux-system/infra"].Object["spec"].(map[string]any)["decryption"] = map[string]any{"secretRef": map[string]any{"name": "sops-age"}}
	g = build(t, r, "")
	reads = 0
	for _, id := range r.gets {
		if id == "Secret/flux-system/sops-age" {
			reads++
		}
	}
	if reads != 1 || node(t, g, "Kustomization/flux-system/infra").Supply[0].SecretUID != "sec-2" {
		t.Fatalf("shared Secret read %d times", reads)
	}

	// A condition from generation 1 does not report generation 2's edge.
	r = world()
	u := r.objs["Kustomization/flux-system/apps"]
	u.SetGeneration(2)
	u.Object["spec"] = map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "new"}}
	c := ready("False", "ArtifactFailed", "Source artifact not found, retrying in 30s", t0)
	c["observedGeneration"] = int64(1)
	u.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{c}}
	r.objs["GitRepository/flux-system/new"] = obj("GitRepository", "flux-system", "new", "new", map[string]any{}, map[string]any{"conditions": []any{ready("False", "GitOperationFailed", "auth failed", t0)}})
	g = build(t, r, "")
	if e, _ := edge(g, "Kustomization/flux-system/apps", "GitRepository/flux-system/new"); e.Reported {
		t.Fatal("an old generation's condition reported today's edge")
	}
	// The same, current: reported.
	c["observedGeneration"] = int64(2)
	u.Object["status"] = map[string]any{"observedGeneration": int64(2), "conditions": []any{c}}
	if e, _ := edge(build(t, r, ""), "Kustomization/flux-system/apps", "GitRepository/flux-system/new"); !e.Reported {
		t.Fatal("a current condition did not report")
	}

	// A recorded observedGeneration of 0 behind generation 1 is lag.
	r = world()
	u = r.objs["Kustomization/flux-system/apps"]
	u.SetGeneration(1)
	u.Object["status"].(map[string]any)["observedGeneration"] = int64(0)
	if !node(t, build(t, r, ""), "Kustomization/flux-system/apps").Stale {
		t.Fatal("observedGeneration 0 behind generation 1 is not stale")
	}
}

// helm-controller's SourceNotReady: reported when the message names this
// source and the source is still not ready by the controller's own rules.
func TestGraphHelmSourceNotReady(t *testing.T) {
	hr := func(msg string) *unstructured.Unstructured {
		return obj("HelmRelease", "apps", "web", "hr", map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "web"}},
			map[string]any{"conditions": []any{ready("False", "SourceNotReady", msg, t0)}})
	}
	chart := func(status map[string]any) *unstructured.Unstructured {
		return obj("HelmChart", "apps", "web", "hc", map[string]any{}, status)
	}
	reported := func(h, c *unstructured.Unstructured) bool {
		r := &fakeReader{objs: map[string]*unstructured.Unstructured{"HelmRelease/apps/web": h, "HelmChart/apps/web": c}, fail: map[string]error{}}
		e, _ := edge(Build(context.Background(), r, Ref{GroupHelm, "HelmRelease", "apps", "web"}, "", time.Now), "HelmRelease/apps/web", "HelmChart/apps/web")
		return e.Reported
	}
	noArtifact := map[string]any{"conditions": []any{ready("False", "FetchFailed", "chart fetch failed", t0)}}
	if !reported(hr("HelmChart 'apps/web' is not ready: does not have an artifact"), chart(noArtifact)) {
		t.Fatal("the controller's SourceNotReady naming this chart")
	}
	if reported(hr("HelmChart 'apps/other' is not ready: does not have an artifact"), chart(noArtifact)) {
		t.Fatal("a message naming another source")
	}
	recovered := map[string]any{"observedGeneration": int64(0), "conditions": []any{ready("True", "Succeeded", "", t0)}, "artifact": map[string]any{"revision": "1.0.0"}}
	if reported(hr("HelmChart 'apps/web' is not ready: does not have an artifact"), chart(recovered)) {
		t.Fatal("a source that has recovered since")
	}
	stalled := map[string]any{"observedGeneration": int64(0), "conditions": []any{ready("True", "Succeeded", "", t0), map[string]any{"type": "Stalled", "status": "True"}}, "artifact": map[string]any{"revision": "1.0.0"}}
	if !reported(hr("HelmChart 'apps/web' is not ready: invalid chart"), chart(stalled)) {
		t.Fatal("a Stalled source is not ready by the controller's rules")
	}
}

// Design review of d0ca1d2: one global bound, cycles included, and each
// controller's own source readiness.
func TestGraphReviewOfd0ca1d2(t *testing.T) {
	// A six-deep chain whose nodes each name every ancestor (cycles) and a
	// hundred more: at most 24 nodes read and the one summary.
	r := chain(6)
	for i := 0; i < 6; i++ {
		var deps []any
		if i < 5 {
			deps = append(deps, map[string]any{"name": fmt.Sprintf("k%d", i+1)})
		}
		for j := 0; j < i; j++ {
			deps = append(deps, map[string]any{"name": fmt.Sprintf("k%d", j)})
		}
		for j := 0; j < 100; j++ {
			deps = append(deps, map[string]any{"name": fmt.Sprintf("extra-%d-%d", i, j)})
		}
		r.objs[fmt.Sprintf("Kustomization/flux-system/k%d", i)].Object["spec"] = map[string]any{"dependsOn": deps}
	}
	g := Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	summaries, cycles := 0, 0
	for _, n := range g.Nodes {
		if strings.HasPrefix(n.ID, "stub:") {
			summaries++
		}
	}
	for _, e := range g.Edges {
		if e.Cycle {
			cycles++
			if n := node(t, g, e.To); n.Stub != "" || n.UID == "" {
				t.Fatalf("a cycle edge leads to %+v, not to the node it closes on", n)
			}
		}
	}
	if len(g.Nodes) > MaxNodes+1 || summaries != 1 || cycles == 0 || !containsPrefix(g.Incomplete, "cycle: ") {
		t.Fatalf("nested bounds: %d nodes, %d summaries, %d cycles, %v", len(g.Nodes), summaries, cycles, g.Incomplete)
	}

	// kustomize-controller uses a source's artifact whatever its generation:
	// a missing path with a stale but artifact-holding source is not the source's.
	r = world()
	u := r.objs["Kustomization/flux-system/apps"]
	u.SetGeneration(1)
	c := ready("False", "ArtifactFailed", "kustomization path not found: stat /tmp/build/missing: no such file or directory", t0)
	c["observedGeneration"] = int64(1)
	u.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{c}}
	src := r.objs["GitRepository/flux-system/platform"]
	src.SetGeneration(2)
	src.Object["status"] = map[string]any{"observedGeneration": int64(1), "artifact": map[string]any{"revision": "main@sha1:" + sha}, "conditions": []any{ready("True", "Succeeded", "stored artifact", t0)}}
	if e, _ := edge(build(t, r, "uid-apps"), "Kustomization/flux-system/apps", "GitRepository/flux-system/platform"); e.Reported {
		t.Fatal("a Kustomization's missing path attributed to a source with an artifact")
	}
	// The same Kustomization with the source's artifact gone: reported.
	delete(src.Object["status"].(map[string]any), "artifact")
	c["message"] = "Source artifact not found, retrying in 30s"
	if e, _ := edge(build(t, r, "uid-apps"), "Kustomization/flux-system/apps", "GitRepository/flux-system/platform"); !e.Reported {
		t.Fatal("a source with no artifact")
	}
	// helm-controller's ArtifactFailed names a source only when it is missing.
	hr := obj("HelmRelease", "apps", "web", "hr", map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "web"}},
		map[string]any{"conditions": []any{ready("False", "ArtifactFailed", "Could not load chart: bad tgz", t0)}})
	hc := obj("HelmChart", "apps", "web", "hc", map[string]any{}, map[string]any{"conditions": []any{ready("False", "FetchFailed", "", t0)}})
	hrr := &fakeReader{objs: map[string]*unstructured.Unstructured{"HelmRelease/apps/web": hr, "HelmChart/apps/web": hc}, fail: map[string]error{}}
	if e, _ := edge(Build(context.Background(), hrr, Ref{GroupHelm, "HelmRelease", "apps", "web"}, "", time.Now), "HelmRelease/apps/web", "HelmChart/apps/web"); e.Reported {
		t.Fatal("a HelmRelease's chart load failure attributed to its chart")
	}
	delete(hrr.objs, "HelmChart/apps/web")
	if e, _ := edge(Build(context.Background(), hrr, Ref{GroupHelm, "HelmRelease", "apps", "web"}, "", time.Now), "HelmRelease/apps/web", "HelmChart/apps/web"); !e.Reported {
		t.Fatal("a missing chart a HelmRelease reports")
	}
}

// Design review of eb6a10e: a cycle edge keeps the controller's report (a
// reported cycle ends no chain), and a walk cancelled during its first read
// still names its start.
type cancellingReader struct {
	fakeReader
	cancel context.CancelFunc
}

func (c *cancellingReader) Get(ctx context.Context, ref Ref) (*unstructured.Unstructured, error) {
	c.cancel()
	return nil, ctx.Err()
}

func TestGraphReviewOfeb6a10e(t *testing.T) {
	r := chain(2)
	r.objs["Kustomization/flux-system/k0"].Object["status"] = map[string]any{"conditions": []any{ready("False", "DependencyNotReady", "dependency 'flux-system/k1' is not ready", t0)}}
	r.objs["Kustomization/flux-system/k1"].Object["spec"] = map[string]any{"dependsOn": []any{map[string]any{"name": "k0"}}}
	r.objs["Kustomization/flux-system/k1"].Object["status"] = map[string]any{"conditions": []any{ready("False", "DependencyNotReady", "dependency 'flux-system/k0' is not ready", t0)}}
	g := Build(context.Background(), r, Ref{GroupKustomize, "Kustomization", "flux-system", "k0"}, "", time.Now)
	back, ok := edge(g, "Kustomization/flux-system/k1", "Kustomization/flux-system/k0")
	if !ok || !back.Cycle || !back.Reported {
		t.Fatalf("the reported cycle edge: %+v", back)
	}
	if node(t, g, "Kustomization/flux-system/k1").ChainEnd || node(t, g, "Kustomization/flux-system/k0").ChainEnd {
		t.Fatal("a reported cycle ended a chain")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g = Build(ctx, &cancellingReader{cancel: cancel}, appsRef, "", time.Now)
	if g.Start == "" || node(t, g, g.Start).Stub != StubCancelled || node(t, g, g.Start).Ref != appsRef || !containsPrefix(g.Incomplete, "cancelled") {
		t.Fatalf("cancelled during the first read: %+v", g)
	}
}
