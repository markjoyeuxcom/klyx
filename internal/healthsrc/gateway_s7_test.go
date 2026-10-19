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
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

var s7t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

// gwAllowing is gw with allowedListeners from the given namespaces value.
func gwAllowing(name, from string) *unstructured.Unstructured {
	u := gw(name, "True")
	if from != "" {
		u.Object["spec"].(map[string]any)["allowedListeners"] = map[string]any{"namespaces": map[string]any{"from": from}}
	}
	return u
}

func listenerSet(ns, name, parent string, created time.Time, accepted, reason string, statusListeners ...any) *unstructured.Unstructured {
	u := cr("gateway.networking.k8s.io/v1", "ListenerSet", ns, name, map[string]any{
		"parentRef": map[string]any{"name": parent, "namespace": "infra"},
		"listeners": []any{map[string]any{"name": "extra", "protocol": "HTTPS", "port": int64(8443), "hostname": name + ".example.com"}}},
		[]any{cond("Accepted", accepted, reason), cond("Programmed", accepted, reason)}, map[string]any{"listeners": statusListeners})
	u.Object["metadata"].(map[string]any)["uid"] = "uid-" + ns + "-" + name
	u.SetCreationTimestamp(metav1.NewTime(created))
	return u
}

func namespaceMeta(name string, labels map[string]string) *metav1.PartialObjectMetadata {
	m := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("ns-" + name), Labels: labels}}
	p, _ := ProjectNamespaceMeta(m)
	return p.(*metav1.PartialObjectMetadata)
}

func listenerSetRows(t *testing.T, res observe.PartResult) map[string]Row[ListenerSetRow] {
	t.Helper()
	out := map[string]Row[ListenerSetRow]{}
	for _, r := range res.Rows.(RowSet[ListenerSetRow]).Rows {
		out[r.Ref.Namespace+"/"+r.Ref.Name] = r
	}
	return out
}

// Coverage S7 §4: each ListenerSet is judged by its own conditions, its
// parent is "not found" only from a complete gateways read, the merge order
// is creation time then namespace/name, and Klyx's allowance reading is
// evidence beside the controller's verdict.
func TestListenerSetsPart(t *testing.T) {
	conflicted := map[string]any{"name": "extra", "attachedRoutes": int64(0), "conditions": []any{cond("Accepted", "True", "Accepted"), cond("Conflicted", "True", "HostnameConflict")}}
	objs := []any{
		projected(t, KindListenerSets, listenerSet("infra", "first", "eg", s7t0, "True", "Accepted")),
		projected(t, KindListenerSets, listenerSet("infra", "third", "eg", s7t0.Add(2*time.Second), "True", "ListenersNotValid", conflicted)),
		projected(t, KindListenerSets, listenerSet("team-b", "second", "eg", s7t0.Add(time.Second), "False", "NotAllowed")),
		projected(t, KindListenerSets, listenerSet("infra", "orphan", "gone", s7t0, "True", "Accepted")),
	}
	capture := func(gatewaysComplete, namespacesComplete bool) observe.Capture {
		return observe.Capture{Rows: true, Objects: map[string][]any{
			KindListenerSets: objs,
			KindGateways:     {projected(t, KindGateways, gwAllowing("eg", "Same"))},
			KindNamespaces:   {namespaceMeta("infra", nil), namespaceMeta("team-b", map[string]string{"team": "b"})},
		}, Complete: map[string]bool{KindGateways: gatewaysComplete, KindNamespaces: namespacesComplete, KindListenerSets: true},
			Current: map[string]bool{KindGateways: true, KindNamespaces: true, KindListenerSets: true}}
	}
	res := evalListenerSets("c1")(capture(true, true))
	if res.Native.Total != 4 || res.Native.Included != 3 || res.Native.Roles.Fault != 2 || res.Native.Roles.Degraded != 1 {
		t.Fatalf("counts: %+v", res.Native)
	}
	rows := listenerSetRows(t, res)
	for key, want := range map[string]struct {
		role             health.Kind
		label            string
		parent           gwapi.ParentState
		precedence, sibs int
		allowed          string
	}{
		"infra/first":   {health.OK, "programmed", gwapi.ParentFound, 1, 3, gwapi.AllowedYes},
		"team-b/second": {health.Fault, "not accepted: NotAllowed", gwapi.ParentFound, 2, 3, gwapi.AllowedNo},
		"infra/third":   {health.Degraded, "listener extra: HostnameConflict", gwapi.ParentFound, 3, 3, gwapi.AllowedYes},
		"infra/orphan":  {health.Fault, "parent Gateway not found", gwapi.ParentNotFound, 1, 1, ""},
	} {
		r, ok := rows[key]
		switch {
		case !ok:
			t.Errorf("%s: no row", key)
		case r.Eval.Role != want.role || r.Eval.Label != want.label || r.Data.ParentState != want.parent:
			t.Errorf("%s: %v %q parent %s", key, r.Eval.Role, r.Eval.Label, r.Data.ParentState)
		case r.Data.Precedence != want.precedence || r.Data.Siblings != want.sibs || !r.Data.PrecedenceComplete:
			t.Errorf("%s: precedence %d of %d (complete %v)", key, r.Data.Precedence, r.Data.Siblings, r.Data.PrecedenceComplete)
		case r.Data.Allowance.Allowed != want.allowed || r.Data.Allowance.Disagrees:
			t.Errorf("%s: allowance %+v", key, r.Data.Allowance)
		case r.Ref.GVR != "gateway.networking.k8s.io/v1/listenersets" || r.Ref.UID != "uid-"+strings.ReplaceAll(key, "/", "-"):
			t.Errorf("%s: ref %+v", key, r.Ref)
		}
	}
	if got := res.Top[0]; got.Role != "fault" {
		t.Errorf("top: %+v", res.Top)
	}

	// an incomplete gateways read never proves the parent absent; the
	// controller's own failure is still judged
	rows = listenerSetRows(t, evalListenerSets("c1")(capture(false, true)))
	if r := rows["infra/orphan"]; r.Eval.Role != health.Unknown || r.Eval.Label != "parent Gateway not established" || r.Data.ParentState != gwapi.ParentNotEstablished {
		t.Errorf("orphan, incomplete: %v %q %s", r.Eval.Role, r.Eval.Label, r.Data.ParentState)
	}
	if r := rows["team-b/second"]; r.Eval.Role != health.Fault {
		t.Errorf("a controller-reported failure is judged whatever the parent read: %+v", r.Eval)
	}
}

// Coverage S7 §4, §8: a Selector allowance reads namespace labels only from
// a complete namespaces read.
func TestListenerSetSelectorNeedsCompleteNamespaces(t *testing.T) {
	g := gw("eg", "True")
	g.Object["spec"].(map[string]any)["allowedListeners"] = map[string]any{"namespaces": map[string]any{"from": "Selector",
		"selector": map[string]any{"matchLabels": map[string]any{"team": "b"}}}}
	capture := func(nsComplete bool, ns ...any) observe.Capture {
		return observe.Capture{Rows: true, Objects: map[string][]any{
			KindListenerSets: {projected(t, KindListenerSets, listenerSet("team-b", "x", "eg", s7t0, "True", "Accepted"))},
			KindGateways:     {projected(t, KindGateways, g)},
			KindNamespaces:   ns,
		}, Complete: map[string]bool{KindGateways: true, KindNamespaces: nsComplete}, Current: map[string]bool{KindGateways: true, KindNamespaces: true}}
	}
	labelled := namespaceMeta("team-b", map[string]string{"team": "b"})
	for _, tc := range []struct {
		name    string
		c       observe.Capture
		allowed string
		text    string
	}{
		{"labels match", capture(true, labelled), gwapi.AllowedYes, "matches the Gateway's selector (team=b)"},
		{"labels do not match", capture(true, namespaceMeta("team-b", map[string]string{"team": "a"})), gwapi.AllowedNo, "does not match"},
		{"a truncated namespaces read", capture(false, labelled), "", "are not established"},
		{"the namespace absent", capture(true), "", "are not established"},
	} {
		a := listenerSetRows(t, evalListenerSets("c1")(tc.c))["team-b/x"].Data.Allowance
		if a.From != "Selector" || a.Allowed != tc.allowed || !strings.Contains(a.Text, tc.text) {
			t.Errorf("%s: %+v", tc.name, a)
		}
	}
	// the disagreement is said, the verdict stays the controller's
	rows := listenerSetRows(t, evalListenerSets("c1")(capture(true, namespaceMeta("team-b", map[string]string{"team": "a"}))))
	if r := rows["team-b/x"]; !r.Data.Allowance.Disagrees || r.Eval.Role != health.OK || !strings.HasPrefix(r.Data.Allowance.Text, "the controller accepted it although") {
		t.Errorf("disagreement: %+v %+v", r.Eval, r.Data.Allowance)
	}
}

// Coverage S7 §3: the Gateways part judges listener status.
func TestGatewaysPartJudgesListeners(t *testing.T) {
	g := gw("eg", "True")
	g.Object["status"].(map[string]any)["listeners"] = []any{map[string]any{"name": "https", "attachedRoutes": int64(0),
		"conditions": []any{cond("Accepted", "True", "Accepted"), cond("ResolvedRefs", "False", "InvalidCertificateRef")}}}
	res := evalGateways("c1")(observe.Capture{Rows: true, Objects: map[string][]any{KindGateways: {projected(t, KindGateways, g)}}})
	if res.Native.Included != 1 || res.Top[0].Label != "listener https: InvalidCertificateRef" || res.Top[0].Role != "degraded" {
		t.Fatalf("gateways part: %+v %+v", res.Native, res.Top)
	}
	rows := res.Rows.(RowSet[gwapi.GatewayRef]).Rows
	if len(rows) != 1 || rows[0].Data.Listeners[0].Status == nil || rows[0].Eval.Role != health.Degraded {
		t.Fatalf("rows: %+v", rows)
	}
}

// The Observer runs the ListenerSets part end to end: watched at v1,
// projected within its budget, joined to the Gateways and namespaces.
func TestListenerSetsPartOnTheObserver(t *testing.T) {
	dynObjs := []runtime.Object{
		gwAllowing("eg", "All"),
		listenerSet("infra", "ok", "eg", s7t0, "True", "Accepted"),
		listenerSet("team-b", "rejected", "eg", s7t0.Add(time.Second), "False", "NotAllowed"),
	}
	s, o := startGateway(t, true, dynObjs, fake.NewClientset())
	o.SubscribeRows(TaskGateway, "listenersets", observe.Owner{Kind: "lens", ID: "t"})
	waitFor(t, "listenersets: the rejected one is the fault", func() bool {
		p, ok := nativePart(s, TaskGateway, "listenersets")
		return ok && p.Coverage == observe.Live && p.Native.Total == 2 && p.Native.Included == 1 && p.Top[0].Name == "rejected" &&
			p.Top[0].Label == "not accepted: NotAllowed"
	})
	waitFor(t, "the disagreement is in the rows", func() bool {
		r, ok := s.rowsOf(TaskGateway + "/listenersets")
		if !ok {
			return false
		}
		for _, row := range r.Rows.(RowSet[ListenerSetRow]).Rows {
			if row.Ref.Name == "rejected" {
				return row.Data.Allowance.Disagrees && row.Data.Allowance.From == "All" && row.Data.ParentState == gwapi.ParentFound
			}
		}
		return false
	})
	p, _ := nativePart(s, TaskGateway, "listenersets")
	var deps []string
	for _, k := range p.Kinds {
		deps = append(deps, k.Kind)
	}
	if strings.Join(deps, ",") != "listenersets,gateways,gatewayclasses,namespaces" {
		t.Errorf("the part's kinds: %v", deps)
	}
}

// Coverage S7 §2: each kind's probe takes its own candidates, and a served
// version outside them is a named gap.
func TestGatewayKindsProbeTheirCandidates(t *testing.T) {
	var asked []string
	nd := NativeDeps{Cluster: "c1", ServedAmong: func(_ context.Context, group, resource string, candidates []string) (observe.Probe, error) {
		asked = append(asked, resource+"="+strings.Join(candidates, ","))
		return observe.Probe{Served: true, Version: candidates[0]}, nil
	}}
	kinds, _ := Gateway(nd)
	for _, k := range kinds {
		if _, err := k.Probe(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(asked, " "); got != "gatewayclasses=v1,v1beta1 gateways=v1,v1beta1 listenersets=v1 httproutes=v1,v1beta1 grpcroutes=v1 tlsroutes=v1,v1alpha3,v1alpha2 tcproutes=v1,v1alpha2 udproutes=v1,v1alpha2 referencegrants=v1,v1beta1" {
		t.Errorf("probes: %s", got)
	}
	budgets := map[string]int{}
	for _, k := range kinds {
		budgets[k.Name] = k.Budget
	}
	if budgets[KindGateways] != 1<<20 || budgets[KindListenerSets] != 512<<10 || budgets[KindGatewayClasses] != 64<<10 || budgets[KindHTTPRoutes] != 1<<20 ||
		budgets[KindGRPCRoutes] != 256<<10 || budgets[KindTLSRoutes] != 256<<10 || budgets[KindTCPRoutes] != 128<<10 || budgets[KindUDPRoutes] != 128<<10 || budgets[KindReferenceGrants] != 128<<10 {
		t.Errorf("budgets: %v", budgets)
	}
	// without ServedAmong, Served's answer is checked against the candidates
	old := NativeDeps{Cluster: "c1", Served: func(context.Context, string, string, string) (observe.Probe, error) {
		return observe.Probe{Served: true, Version: "v2"}, nil
	}}
	kinds, _ = Gateway(old)
	if _, err := kinds[1].Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "served at v2; Klyx reads v1 or v1beta1") {
		t.Errorf("a version outside the candidates: %v", err)
	}
}

// Codex's S7a implementation review, finding 3: a parent present only in a
// stale read is found, but its allowedListeners establishes no current
// allowance and no disagreement; a stale namespaces read establishes no
// labels.
func TestAStaleParentEstablishesNoAllowance(t *testing.T) {
	c := observe.Capture{Rows: true, Objects: map[string][]any{
		KindGateways:     {projected(t, KindGateways, gwAllowing("eg", "None"))},
		KindListenerSets: {projected(t, KindListenerSets, listenerSet("infra", "x", "eg", s7t0, "True", "Accepted"))},
		KindNamespaces:   {namespaceMeta("infra", nil)},
	}, Complete: map[string]bool{KindListenerSets: true}, Current: map[string]bool{KindListenerSets: true}}
	r := listenerSetRows(t, evalListenerSets("c1")(c))["infra/x"]
	if r.Data.ParentState != gwapi.ParentFound || r.Data.ParentCurrent {
		t.Errorf("parent: %s current %v", r.Data.ParentState, r.Data.ParentCurrent)
	}
	if r.Data.Allowance.Allowed != "" || r.Data.Allowance.Disagrees || !strings.Contains(r.Data.Allowance.Text, "not current") {
		t.Errorf("a stale parent manufactured an allowance claim: %+v", r.Data.Allowance)
	}
	if r.Eval.Role != health.OK {
		t.Errorf("the controller's verdict stands: %+v", r.Eval)
	}
}

// Codex's S7a implementation review, finding 3, through the real Observer:
// with ListenerSets served and the Gateways API confirmed absent, the parent
// is not established, never "not found".
func TestAParentKindNotServedIsNotMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gatewayListKinds())
	gvr := schema.GroupVersionResource{Group: gwapi.Group, Version: "v1", Resource: "listenersets"}
	if _, err := dyn.Resource(gvr).Namespace("infra").Create(context.Background(), listenerSet("infra", "x", "eg", s7t0, "True", "Accepted"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	probe := func(_ context.Context, group, res, _ string) (observe.Probe, error) {
		return observe.Probe{Served: group == gwapi.Group && res == "listenersets", Version: "v1"}, nil
	}
	nd := NativeDeps{Cluster: "c1", Dyn: dyn, Typed: fake.NewClientset(), Meta: metafake.NewSimpleMetadataClient(scheme), Served: probe}
	kinds, domains := Gateway(nd)
	kinds = append(append(SharedNetworkKinds(nd), namespacesKind(nd.Meta)), kinds...)
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	defer func() { cancel(); <-o.Done() }()
	o.SubscribeRows(TaskGateway, "listenersets", observe.Owner{Kind: "lens", ID: "t"})
	waitFor(t, "the ListenerSet read beside an absent Gateways API", func() bool {
		p, ok := nativePart(s, TaskGateway, "listenersets")
		if !ok || p.Native == nil || p.Native.Total != 1 {
			return false
		}
		for _, k := range p.Kinds {
			if k.Kind == KindGateways && k.Coverage == observe.Absent {
				return true
			}
		}
		return false
	})
	r, ok := o.RowsNow(TaskGateway, "listenersets")
	if !ok {
		t.Fatal("no rows")
	}
	row := r.Rows.(RowSet[ListenerSetRow]).Rows[0]
	if row.Eval.Role != health.Unknown || row.Data.ParentState != gwapi.ParentNotEstablished || !strings.Contains(row.Data.Allowance.Text, "not served") {
		t.Errorf("not-served parent kind: %v %q parent %s, allowance %q", row.Eval.Role, row.Eval.Label, row.Data.ParentState, row.Data.Allowance.Text)
	}
}
