package healthsrc

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthnotify"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S10 §2-§5 through healthsrc.

var s10Now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func kpool(name, class string, conds ...any) *unstructured.Unstructured {
	u := cr("karpenter.sh/v1", "NodePool", "", name, map[string]any{"template": map[string]any{"spec": map[string]any{
		"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": class}}}}, conds, nil)
	u.SetUID(types.UID("uid-" + name))
	u.SetGeneration(1)
	return u
}

func kclass(name string, conds ...any) *unstructured.Unstructured {
	u := cr("karpenter.k8s.aws/v1", "EC2NodeClass", "", name, map[string]any{}, conds, nil)
	u.SetUID(types.UID("uid-class-" + name))
	u.SetGeneration(1)
	return u
}

func kc(t, status, reason, message string) map[string]any {
	return map[string]any{"type": t, "status": status, "reason": reason, "message": message, "observedGeneration": int64(1)}
}

// awsReady is a detector reading: one AWS controller, ready.
func awsReady() KarpenterFacts {
	k := capability.KarpenterCapability{Present: true, Inventory: capability.InventoryComplete,
		Controllers: []capability.KarpenterController{{Namespace: "karpenter", Name: "karpenter", Provider: capability.ProviderAWS, Ready: true}}}
	return KarpenterFactsOf(k)
}

// §9 parity: a failing class lights its pools once each and the class not
// at all; the class part is evidence; no class transition reaches a
// notification.
func TestKarpenterThroughTheObserver(t *testing.T) {
	failing := []any{kc("SubnetsReady", "False", "SubnetsNotFound", "no subnets for subnet-0a1b2c3d4e5f60718"), kc("Ready", "False", "UnhealthyDependents", "")}
	copied := []any{kc("NodeClassReady", "False", "SubnetsNotFound", "no subnets for subnet-0a1b2c3d4e5f60718"), kc("ValidationSucceeded", "True", "", ""),
		kc("Ready", "False", "UnhealthyDependents", "")}
	objs := []runtime.Object{
		kclass("broken", failing...), kclass("ok", kc("Ready", "True", "", "")),
		kpool("a", "broken", copied...), kpool("b", "broken", copied...),
		kpool("c", "ok", kc("NodeClassReady", "True", "", ""), kc("Ready", "True", "", "")),
		kpool("d", "ghost", kc("NodeClassReady", "False", "NodeClassNotFound", ""), kc("Ready", "False", "UnhealthyDependents", "")),
	}
	lists := map[schema.GroupVersionResource]string{
		{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}:           "NodePoolList",
		{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}:          "NodeClaimList",
		{Group: "karpenter.k8s.aws", Version: "v1", Resource: "ec2nodeclasses"}: "EC2NodeClassList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		switch group {
		case "karpenter.sh", "karpenter.k8s.aws":
			return observe.Probe{Served: true, Version: "v1"}, nil
		}
		return observe.Probe{Served: false}, nil
	}
	kinds, domains := Native(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &recordingSink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: 20 * time.Millisecond})
	o.SetFact(FactKarpenter, awsReady())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	waitFor(t, "the pools: the class's failure once per pool, the missing class a fault", func() bool {
		p, ok := nativePart(&s.sink, TaskKarpenter, "nodepools")
		if !ok || p.Coverage != observe.Live || p.Native == nil || p.Native.Total != 4 || p.Native.Included != 3 || len(p.Top) != 3 {
			return false
		}
		labels := map[string]string{}
		for _, r := range p.Top {
			labels[r.Name] = r.Label
		}
		return labels["a"] == "NodeClass broken not ready" && labels["b"] == "NodeClass broken not ready" && labels["d"] == "NodeClass EC2NodeClass/ghost not found"
	})
	waitFor(t, "the classes: evidence, never counted", func() bool {
		p, ok := nativePart(&s.sink, TaskKarpenter, "nodeclasses")
		return ok && p.Coverage == observe.Live && p.Evidence != nil && *p.Evidence == 2 && p.Native == nil && len(p.Top) == 0
	})
	p, _ := nativePart(&s.sink, TaskKarpenter, "nodepools")
	for _, r := range p.Top {
		if strings.Contains(r.Reason, "subnet-0a1b2c3d4e5f60718") {
			t.Errorf("the summary's Top carries provider text: %+v", r)
		}
	}
	var sent []healthnotify.Notification
	eng := healthnotify.New(time.Now, healthnotify.Hooks{Send: func(n healthnotify.Notification) error { sent = append(sent, n); return nil }}, 100)
	eng.SetSettings(healthnotify.Settings{Enabled: true, Scope: healthnotify.ScopeAll})
	s.mu.Lock()
	for _, b := range s.batches {
		if b.Task == TaskKarpenter && b.Part == "nodeclasses" {
			t.Errorf("the class part produced a transition batch: %+v", b)
		}
		for _, it := range b.Items {
			if strings.Contains(it.Reason, "subnet-0a1b2c3d4e5f60718") {
				t.Errorf("a transition carries provider text: %+v", it)
			}
		}
		eng.Apply(b)
	}
	s.mu.Unlock()
}

func s10Capture(objs map[string][]any, current, complete map[string]bool, asOf map[string]time.Time, f KarpenterFacts) observe.Capture {
	if asOf == nil {
		asOf = map[string]time.Time{}
	}
	return observe.Capture{Now: s10Now, Objects: objs, Current: current, Complete: complete, AsOf: asOf, Rows: true, Facts: map[string]any{FactKarpenter: f}}
}

func kproj(resource string, u *unstructured.Unstructured) any {
	v, err := karpenter.Project(resource)(u)
	if err != nil {
		panic(err)
	}
	return v
}

func poolRows(t *testing.T, r observe.PartResult) map[string]Row[KarpenterPoolRow] {
	t.Helper()
	set, ok := r.Rows.(RowSet[KarpenterPoolRow])
	if !ok {
		t.Fatalf("rows: %T", r.Rows)
	}
	out := map[string]Row[KarpenterPoolRow]{}
	for _, row := range set.Rows {
		out[row.Data.Pool.Meta.Name] = row
	}
	return out
}

// The capture's coverage maps onto the link states (§3).
func TestKarpenterLinkStatesFromTheCapture(t *testing.T) {
	pool := kproj(KindNodePools, kpool("a", "default", kc("NodeClassReady", "True", "", ""), kc("Ready", "True", "", "")))
	class := kproj(KindEC2NodeClasses, kclass("default", kc("Ready", "True", "", "")))
	other := kproj(KindEC2NodeClasses, kclass("other", kc("Ready", "True", "", "")))
	asOf := s10Now.Add(-time.Hour)
	cases := []struct {
		name              string
		classes           []any
		current, complete bool
		asOf              time.Time
		otherVersion      bool
		want              karpenter.LinkState
	}{
		{"found", []any{class}, true, true, time.Time{}, false, karpenter.LinkFound},
		{"found in a capped kind", []any{class}, true, false, time.Time{}, false, karpenter.LinkFound},
		{"not found", []any{other}, true, true, time.Time{}, false, karpenter.LinkNotFound},
		{"omitted by the kind budget", []any{other}, true, false, time.Time{}, false, karpenter.LinkCut},
		{"kind not served", nil, false, true, time.Time{}, false, karpenter.LinkKindNotServed},
		{"stale", []any{class}, false, false, asOf, false, karpenter.LinkStale},
		{"unreadable", nil, false, false, time.Time{}, false, karpenter.LinkUnreadable},
		{"served at another version", nil, false, false, time.Time{}, true, karpenter.LinkVersionUnsupported},
	}
	for _, tc := range cases {
		f := awsReady()
		kf := f.Kinds[karpenter.AdapterEC2]
		kf.OtherVersion = tc.otherVersion
		f.Kinds[karpenter.AdapterEC2] = kf
		at := map[string]time.Time{}
		if !tc.asOf.IsZero() {
			at[KindEC2NodeClasses] = tc.asOf
		}
		c := s10Capture(map[string][]any{KindNodePools: {pool}, KindEC2NodeClasses: tc.classes},
			map[string]bool{KindNodePools: true, KindEC2NodeClasses: tc.current}, map[string]bool{KindNodePools: true, KindEC2NodeClasses: tc.complete}, at, f)
		row := poolRows(t, evalNodePools("c1")(c))["a"]
		if row.Data.Link.State != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, row.Data.Link.State, tc.want)
		}
		if tc.want == karpenter.LinkStale && (row.Data.Class == nil || row.Data.Class.State != karpenter.ClassReady) {
			t.Errorf("stale keeps its last good reading: %+v", row.Data.Class)
		}
		if tc.want == karpenter.LinkFound && row.Data.Handler != "the karpenter controller karpenter/karpenter is ready" {
			t.Errorf("the handler's reading: %q", row.Data.Handler)
		}
	}
}

// The pool's node count, the claims aged at evaluation, and an unsupported
// provider with a complete inventory (§3 rule 1).
func TestKarpenterPoolsClaimsAndUnsupported(t *testing.T) {
	claim := cr("karpenter.sh/v1", "NodeClaim", "", "c1", map[string]any{}, []any{kc("Ready", "Unknown", "", "")}, nil)
	claim.SetLabels(map[string]string{karpenter.LabelNodePool: "gcp"})
	claim.SetCreationTimestamp(metav1.NewTime(s10Now.Add(-20 * time.Minute)))
	gcp := cr("karpenter.sh/v1", "NodePool", "", "gcp", map[string]any{"template": map[string]any{"spec": map[string]any{
		"nodeClassRef": map[string]any{"group": "karpenter.k8s.gcp", "kind": "GCENodeClass", "name": "x"}}}}, nil, nil)
	c := s10Capture(map[string][]any{KindNodePools: {kproj(KindNodePools, gcp)}, KindNodeClaims: {kproj(KindNodeClaims, claim)}},
		map[string]bool{KindNodePools: true, KindNodeClaims: true}, map[string]bool{KindNodePools: true, KindNodeClaims: true}, nil, awsReady())
	row := poolRows(t, evalNodePools("c1")(c))["gcp"]
	if row.Data.Pool.Nodes != 1 || row.Data.Link.State != karpenter.LinkUnsupported || row.Eval.Label != "not reported" ||
		!strings.Contains(row.Data.Findings[0].Text, "no running Karpenter supports karpenter.k8s.gcp/GCENodeClass") {
		t.Fatalf("unsupported, unreported, with a complete inventory: %+v", row)
	}
	claims := evalNodeClaims("c1")(c)
	set := claims.Rows.(RowSet[karpenter.NodeClaim])
	if len(set.Rows) != 1 || set.Rows[0].Data.AgeSeconds != 1200 || set.Rows[0].Eval.Role != health.Degraded || set.Rows[0].Ref.GVR != "karpenter.sh/v1/nodeclaims" {
		t.Fatalf("the claim is aged at evaluation: %+v", set.Rows)
	}
}

// The class rows: users, their completeness, and no verdict (§4).
func TestKarpenterClassRows(t *testing.T) {
	pools := []any{
		kproj(KindNodePools, kpool("a", "default", kc("Ready", "True", "", ""))),
		kproj(KindNodePools, kpool("b", "default", kc("Ready", "True", "", ""))),
	}
	class := kproj(KindEC2NodeClasses, kclass("default", kc("SubnetsReady", "False", "SubnetsNotFound", "x"), kc("Ready", "False", "UnhealthyDependents", "")))
	for _, complete := range []bool{true, false} {
		c := s10Capture(map[string][]any{KindNodePools: pools, KindEC2NodeClasses: {class}},
			map[string]bool{KindNodePools: true, KindEC2NodeClasses: true}, map[string]bool{KindNodePools: complete, KindEC2NodeClasses: true}, nil, awsReady())
		r := evalNodeClasses(c)
		set := r.Rows.(RowSet[KarpenterClassRow])
		row := set.Rows[0]
		if *r.Evidence != 1 || r.Native != nil || row.Eval != (health.NativeEval{}) || !row.Data.FactOnly() ||
			strings.Join(row.Data.Users, ",") != "a,b" || row.Data.UsersComplete != complete || row.Data.Reading.State != karpenter.ClassNotReady ||
			row.Ref.GVR != "karpenter.k8s.aws/v1/ec2nodeclasses" || row.Ref.UID != "uid-class-default" {
			t.Errorf("complete=%v: %+v", complete, row)
		}
	}
}

// §5: managed is its own controller state, a note.
func TestKarpenterControllerIssueStates(t *testing.T) {
	auto := []capability.KarpenterKind{{Kind: karpenter.ClassKinds[2], Served: true, InUse: true}}
	managed := capability.KarpenterCapability{Present: true, Inventory: capability.InventoryComplete, Kinds: auto, AutoMode: true}
	unread := managed
	unread.Inventory, unread.InventoryWhy = capability.InventoryUnreadable, "forbidden"
	failing := managed
	failing.Controllers = []capability.KarpenterController{{Namespace: "k", Name: "karpenter", Provider: capability.ProviderAWS}}
	healthy := capability.KarpenterCapability{Present: true, Inventory: capability.InventoryComplete,
		Controllers: []capability.KarpenterController{{Namespace: "k", Name: "karpenter", Provider: capability.ProviderAWS, Ready: true}}}
	for name, tc := range map[string]struct {
		caps capability.KarpenterCapability
		want string
	}{
		"managed": {managed, ControllerManaged}, "an unread inventory beside Auto Mode": {unread, ControllerUnknown},
		"a failing controller beside Auto Mode": {failing, ControllerNotReady}, "healthy": {healthy, ""},
	} {
		got := ControllerIssues(capability.Set{Karpenter: tc.caps})[TaskKarpenter]
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s: %+v", name, got)
		case tc.want != "" && (got == nil || got.State != tc.want):
			t.Errorf("%s: %+v, want %s", name, got, tc.want)
		case tc.want == ControllerManaged && !strings.Contains(got.Reason, "managed by EKS Auto Mode"):
			t.Errorf("%s: %+v", name, got)
		}
	}
}
