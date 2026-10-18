package gwapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

func cond(t, status, reason string, og int64) map[string]any {
	return map[string]any{"type": t, "status": status, "reason": reason, "message": reason + " message", "observedGeneration": og,
		"lastTransitionTime": "2026-10-18T09:00:00Z"}
}

func listenerStatus(name string, attached int64, conds ...map[string]any) map[string]any {
	cs := make([]any, len(conds))
	for i, c := range conds {
		cs[i] = c
	}
	return map[string]any{"name": name, "attachedRoutes": attached, "conditions": cs,
		"supportedKinds": []any{map[string]any{"group": Group, "kind": "HTTPRoute"}}}
}

func healthyListener(name string, og int64) map[string]any {
	return listenerStatus(name, 1, cond("Accepted", "True", "Accepted", og), cond("Conflicted", "False", "NoConflicts", og),
		cond("ResolvedRefs", "True", "ResolvedRefs", og), cond("Programmed", "True", "Programmed", og))
}

func gatewayWithListeners(gen int64, spec map[string]any, statusListeners ...map[string]any) *unstructured.Unstructured {
	sl := make([]any, len(statusListeners))
	for i, l := range statusListeners {
		sl[i] = l
	}
	if spec == nil {
		spec = map[string]any{}
	}
	if _, ok := spec["listeners"]; !ok {
		spec["listeners"] = []any{
			map[string]any{"name": "https", "protocol": "HTTPS", "port": int64(443), "hostname": "*.example.com",
				"tls": map[string]any{"mode": "Terminate", "certificateRefs": []any{map[string]any{"name": "wildcard"}}}},
			map[string]any{"name": "http", "protocol": "HTTP", "port": int64(80),
				"allowedRoutes": map[string]any{"namespaces": map[string]any{"from": "Selector", "selector": map[string]any{"matchLabels": map[string]any{"team": "shop"}}},
					"kinds": []any{map[string]any{"kind": "HTTPRoute"}}}},
		}
	}
	spec["gatewayClassName"] = "eg"
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
		"metadata": map[string]any{"name": "eg", "namespace": "infra", "generation": gen, "uid": "gw-uid", "labels": map[string]any{"app": "edge"}},
		"spec":     spec,
		"status": map[string]any{"conditions": []any{cond("Accepted", "True", "Accepted", gen), cond("Programmed", "True", "Programmed", gen)},
			"listeners": sl, "addresses": []any{map[string]any{"type": "IPAddress", "value": "192.0.2.1"}}},
	}}
}

// Coverage S7 §3: a listener carries its status and allowedRoutes, by name.
func TestParseListenerStatusAndAllowance(t *testing.T) {
	u := gatewayWithListeners(4, map[string]any{"allowedListeners": map[string]any{"namespaces": map[string]any{"from": "Same"}}},
		healthyListener("https", 4), listenerStatus("http", 0, cond("Accepted", "True", "Accepted", 4)), healthyListener("https", 9))
	unstructured.SetNestedField(u.Object, int64(2), "status", "attachedListenerSets")
	g := ParseGatewayRef(u)
	if len(g.Listeners) != 2 {
		t.Fatalf("listeners: %+v", g.Listeners)
	}
	https, http := g.Listeners[0], g.Listeners[1]
	if https.TLSMode != "Terminate" || https.Status == nil || https.Status.AttachedRoutes != 1 || len(https.Status.Conditions) != 4 ||
		https.Status.Of("Programmed").ObservedGeneration != 4 {
		t.Errorf("https: %+v (a repeated name keeps its first entry)", https.Status)
	}
	if !reflect.DeepEqual(https.Status.SupportedKinds, []RouteKind{{Group: Group, Kind: "HTTPRoute"}}) || https.AllowedRoutes != nil {
		t.Errorf("https kinds/allowedRoutes: %+v %+v", https.Status.SupportedKinds, https.AllowedRoutes)
	}
	ar := http.AllowedRoutes
	if ar == nil || !ar.Namespaces.Set || ar.Namespaces.From != "Selector" || ar.Namespaces.Selector == nil ||
		ar.Namespaces.Selector.MatchLabels["team"] != "shop" || !reflect.DeepEqual(ar.Kinds, []RouteKind{{Kind: "HTTPRoute"}}) {
		t.Errorf("http allowedRoutes: %+v", ar)
	}
	if !g.AllowedListeners.Set || g.AllowedListeners.From != "Same" || g.AttachedListenerSets == nil || *g.AttachedListenerSets != 2 {
		t.Errorf("allowedListeners %+v attached %v", g.AllowedListeners, g.AttachedListenerSets)
	}
	// unset: no allowance written, no count reported
	g = ParseGatewayRef(gatewayWithListeners(1, nil))
	if g.AllowedListeners.Set || g.AttachedListenerSets != nil || g.Listeners[0].Status != nil {
		t.Errorf("unset: %+v %v %+v", g.AllowedListeners, g.AttachedListenerSets, g.Listeners[0].Status)
	}
	// allowedListeners written without namespaces: the API defaults it (None)
	g = ParseGatewayRef(gatewayWithListeners(1, map[string]any{"allowedListeners": map[string]any{}}))
	if !g.AllowedListeners.Set || g.AllowedListeners.From != "" {
		t.Errorf("defaulted: %+v", g.AllowedListeners)
	}
	// a selector Klyx cannot read is said, never guessed
	g = ParseGatewayRef(gatewayWithListeners(1, map[string]any{"allowedListeners": map[string]any{"namespaces": map[string]any{
		"from": "Selector", "selector": map[string]any{"matchLabels": "not-a-map"}}}}))
	if !g.AllowedListeners.SelectorInvalid || g.AllowedListeners.Selector != nil {
		t.Errorf("invalid selector: %+v", g.AllowedListeners)
	}
}

// Coverage S7 §3: with the Gateway healthy, a failing listener reads
// Degraded, named; conditions of an older generation are shown, never
// judged.
func TestGatewayVerdictListenerEvidence(t *testing.T) {
	ok := func(gen int64) (Cond, Cond) {
		return Cond{Status: "True", ObservedGeneration: gen}, Cond{Status: "True", ObservedGeneration: gen}
	}
	parse := func(gen int64, ls ...map[string]any) []Listener {
		return ParseGatewayRef(gatewayWithListeners(gen, nil, ls...)).Listeners
	}
	for _, tc := range []struct {
		name      string
		listeners []Listener
		role      health.Kind
		label     string
	}{
		{"every listener healthy", parse(3, healthyListener("https", 3), healthyListener("http", 3)), health.OK, "programmed"},
		{"no listener status reported", parse(3), health.OK, "programmed"},
		{"an unresolved certificate", parse(3, listenerStatus("https", 0, cond("Accepted", "True", "Accepted", 3), cond("ResolvedRefs", "False", "InvalidCertificateRef", 3),
			cond("Programmed", "False", "Invalid", 3)), healthyListener("http", 3)), health.Degraded, "listener https: InvalidCertificateRef"},
		{"a conflicted listener lost precedence", parse(3, healthyListener("https", 3), listenerStatus("http", 0, cond("Accepted", "True", "Accepted", 3),
			cond("Conflicted", "True", "HostnameConflict", 3), cond("Programmed", "False", "Invalid", 3))), health.Degraded, "listener http: HostnameConflict"},
		{"not accepted explains first", parse(3, listenerStatus("https", 0, cond("Accepted", "False", "UnsupportedProtocol", 3), cond("Programmed", "False", "Invalid", 3))),
			health.Degraded, "listener https: UnsupportedProtocol"},
		{"two failing listeners", parse(3, listenerStatus("https", 0, cond("Programmed", "False", "PortUnavailable", 3)),
			listenerStatus("http", 0, cond("Programmed", "False", "", 3))), health.Degraded, "listener https: PortUnavailable (+1 more)"},
		{"a failure observed for an older generation is not judged", parse(3, listenerStatus("https", 0, cond("Programmed", "False", "PortUnavailable", 2))), health.OK, "programmed"},
	} {
		a, p := ok(3)
		e := GatewayVerdict(a, p, 3, tc.listeners)
		if e.Role != tc.role || e.Label != tc.label {
			t.Errorf("%s: %v %q, want %v %q", tc.name, e.Role, e.Label, tc.role, tc.label)
		}
		if tc.role == health.Degraded && (!e.Included || e.Reason == "" || e.Since != nil || e.Cond == nil) {
			t.Errorf("%s: a listener-degraded Gateway is included, reasoned, undated: %+v", tc.name, e)
		}
	}
	// the Gateway's own verdict comes first: a not-accepted Gateway is not
	// re-labelled by its listeners
	bad := parse(3, listenerStatus("https", 0, cond("Programmed", "False", "PortUnavailable", 3)))
	if e := GatewayVerdict(Cond{Status: "False", Reason: "Invalid", ObservedGeneration: 3}, Cond{}, 3, bad); e.Role != health.Fault || e.Label != "not accepted" {
		t.Errorf("own verdict first: %+v", e)
	}
	if e := GatewayVerdict(Cond{Status: "True", ObservedGeneration: 2}, Cond{Status: "True", ObservedGeneration: 2}, 3, bad); e.Role != health.Progressing {
		t.Errorf("a syncing Gateway judges no listener: %+v", e)
	}
	// a missing reason names the condition
	if e := GatewayVerdict(Cond{Status: "True"}, Cond{Status: "True"}, 0, parse(0, listenerStatus("http", 0, cond("Conflicted", "True", "", 0)))); e.Label != "listener http: conflicted" {
		t.Errorf("reasonless conflict: %q", e.Label)
	}
}

func listenerSetObj(ns, name string, created time.Time, gen int64, parent map[string]any, conds []any, statusListeners ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "ListenerSet",
		"metadata": map[string]any{"name": name, "namespace": ns, "generation": gen, "uid": ns + "-" + name,
			"creationTimestamp": created.UTC().Format(time.RFC3339)},
		"spec": map[string]any{"parentRef": parent, "listeners": []any{
			map[string]any{"name": "extra", "protocol": "HTTPS", "port": int64(8443), "hostname": "a.example.com",
				"tls": map[string]any{"certificateRefs": []any{map[string]any{"name": "a-cert"}}}}}},
		"status": map[string]any{"conditions": conds, "listeners": statusListeners},
	}}
}

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func lsConds(acc, accReason, prog, progReason string, og int64) []any {
	return []any{cond("Accepted", acc, accReason, og), cond("Programmed", prog, progReason, og)}
}

func TestParseListenerSetDefaultsTheParent(t *testing.T) {
	ls := ParseListenerSet(listenerSetObj("team-a", "a", t0, 2, map[string]any{"name": "eg", "namespace": "infra"}, lsConds("True", "Accepted", "True", "Programmed", 2)))
	if ls.Parent != (ParentRef{Group: Group, Kind: "Gateway", Namespace: "infra", Name: "eg"}) || !ls.Parent.IsGateway() || ls.Parent.Key() != "infra/eg" {
		t.Errorf("parent: %+v", ls.Parent)
	}
	if ls.UID != "team-a-a" || !ls.Created.Equal(t0) || ls.Generation != 2 || len(ls.Listeners) != 1 || ls.Listeners[0].CertificateRefs[0].Namespace != "team-a" {
		t.Errorf("listener set: %+v", ls)
	}
	ls = ParseListenerSet(listenerSetObj("infra", "b", t0, 1, map[string]any{"name": "eg"}, nil))
	if ls.Parent.Namespace != "infra" || ls.Parent.Kind != "Gateway" || ls.Parent.Group != Group {
		t.Errorf("namespace defaults to the ListenerSet's: %+v", ls.Parent)
	}
	// an explicit empty group is the core group: not a Gateway
	ls = ParseListenerSet(listenerSetObj("infra", "c", t0, 1, map[string]any{"group": "", "kind": "Gateway", "name": "eg"}, nil))
	if ls.Parent.IsGateway() {
		t.Errorf("an explicit empty group is not the Gateway API's: %+v", ls.Parent)
	}
}

// Coverage S7 §4: the ListenerSet's verdict.
func TestListenerSetRule(t *testing.T) {
	gw := ParseGatewayRef(gatewayWithListeners(1, nil))
	found := ParentLookup{State: ParentFound, Gateway: &gw, Current: true}
	healthy := listenerSetObj("infra", "a", t0, 2, map[string]any{"name": "eg"}, lsConds("True", "Accepted", "True", "Programmed", 2),
		healthyListener("extra", 2))
	for _, tc := range []struct {
		name   string
		u      *unstructured.Unstructured
		parent ParentLookup
		role   health.Kind
		label  string
	}{
		{"healthy", healthy, found, health.OK, "programmed"},
		{"not allowed", listenerSetObj("team-b", "b", t0, 1, map[string]any{"name": "eg", "namespace": "infra"}, lsConds("False", "NotAllowed", "False", "Invalid", 1)),
			found, health.Fault, "not accepted: NotAllowed"},
		{"its own failure is judged whatever the parent's read", listenerSetObj("team-b", "b", t0, 1, map[string]any{"name": "eg", "namespace": "infra"},
			lsConds("False", "ParentNotAccepted", "False", "Invalid", 1)), ParentLookup{State: ParentNotEstablished}, health.Fault, "not accepted: ParentNotAccepted"},
		{"not programmed is a fault", listenerSetObj("infra", "a", t0, 1, map[string]any{"name": "eg"}, lsConds("True", "Accepted", "False", "PortUnavailable", 1)),
			found, health.Fault, "not programmed: PortUnavailable"},
		{"pending", listenerSetObj("infra", "a", t0, 1, map[string]any{"name": "eg"}, lsConds("Unknown", "Pending", "Unknown", "Pending", 1)),
			found, health.Progressing, "acceptance pending"},
		{"no status", listenerSetObj("infra", "a", t0, 1, map[string]any{"name": "eg"}, nil), found, health.Unknown, "no status reported"},
		{"syncing", listenerSetObj("infra", "a", t0, 3, map[string]any{"name": "eg"}, lsConds("True", "Accepted", "True", "Programmed", 2)),
			found, health.Progressing, "syncing"},
		{"a last-observed negative", listenerSetObj("infra", "a", t0, 3, map[string]any{"name": "eg"}, lsConds("False", "NotAllowed", "False", "Invalid", 2)),
			ParentLookup{State: ParentNotEstablished}, health.Degraded, "not accepted (last observed): NotAllowed"},
		{"parent absent from a complete read", healthy, ParentLookup{State: ParentNotFound}, health.Fault, "parent Gateway not found"},
		{"parent absent from an incomplete read", healthy, ParentLookup{State: ParentNotEstablished}, health.Unknown, "parent Gateway not established"},
		{"a parent of another kind", healthy, ParentLookup{State: ParentUnsupported}, health.Unknown, "parent is a Gateway, not a Gateway"},
		{"a conflicted listener", listenerSetObj("infra", "a", t0, 2, map[string]any{"name": "eg"}, lsConds("True", "ListenersNotValid", "True", "Programmed", 2),
			listenerStatus("extra", 0, cond("Accepted", "True", "Accepted", 2), cond("Conflicted", "True", "HostnameConflict", 2))), found, health.Degraded, "listener extra: HostnameConflict"},
	} {
		e := ListenerSetRule(ParseListenerSet(tc.u), tc.parent)
		if e.Role != tc.role || e.Label != tc.label {
			t.Errorf("%s: %v %q, want %v %q", tc.name, e.Role, e.Label, tc.role, tc.label)
		}
		if e.Included != (tc.role == health.Fault || tc.role == health.Degraded) {
			t.Errorf("%s: included %v", tc.name, e.Included)
		}
	}
	// the parent of another kind names it
	other := ParseListenerSet(listenerSetObj("infra", "a", t0, 2, map[string]any{"kind": "XGateway", "name": "eg"}, lsConds("True", "Accepted", "True", "Programmed", 2)))
	if lk := LookupParent(other, []GatewayRef{gw}, ParentRead{Current: true, Complete: true}); lk.State != ParentUnsupported {
		t.Errorf("lookup of another kind: %+v", lk)
	}
	if e := ListenerSetRule(other, ParentLookup{State: ParentUnsupported}); e.Label != "parent is a XGateway, not a Gateway" {
		t.Errorf("unsupported parent: %q", e.Label)
	}
}

func TestLookupParentProvesAbsenceOnlyFromACompleteRead(t *testing.T) {
	gw := ParseGatewayRef(gatewayWithListeners(1, nil))
	ls := ParseListenerSet(listenerSetObj("team-a", "a", t0, 1, map[string]any{"name": "eg", "namespace": "infra"}, nil))
	current, truncated, stale, absent := ParentRead{Current: true, Complete: true}, ParentRead{Current: true}, ParentRead{}, ParentRead{Absent: true, Complete: true}
	if lk := LookupParent(ls, []GatewayRef{gw}, truncated); lk.State != ParentFound || lk.Gateway.Name != "eg" || !lk.Current {
		t.Errorf("found in a truncated, current read: %+v", lk)
	}
	if lk := LookupParent(ls, []GatewayRef{gw}, stale); lk.State != ParentFound || lk.Current {
		t.Errorf("found only in a stale read is found, never current: %+v", lk)
	}
	if lk := LookupParent(ls, nil, current); lk.State != ParentNotFound {
		t.Errorf("complete, current absence: %+v", lk)
	}
	for name, read := range map[string]ParentRead{"truncated": truncated, "stale": stale} {
		if lk := LookupParent(ls, nil, read); lk.State != ParentNotEstablished || lk.NotServed {
			t.Errorf("%s absence: %+v", name, lk)
		}
	}
	// Codex's S7a implementation review, finding 3: a confirmed-absent
	// Gateways API is complete, but proves no parent missing
	if lk := LookupParent(ls, nil, absent); lk.State != ParentNotEstablished || !lk.NotServed {
		t.Errorf("Gateways not served: %+v", lk)
	}
	if e := ListenerSetRule(ls, LookupParent(ls, nil, absent)); e.Role != health.Unknown {
		t.Errorf("a parent kind not served reads %v %q", e.Role, e.Label)
	}
}

// Coverage S7 §4: Klyx's own allowedListeners check is evidence beside the
// controller's Accepted condition, and says when they disagree.
func TestListenerSetAllowance(t *testing.T) {
	gwWith := func(al map[string]any) *GatewayRef {
		spec := map[string]any{}
		if al != nil {
			spec["allowedListeners"] = al
		}
		g := ParseGatewayRef(gatewayWithListeners(1, spec))
		return &g
	}
	from := func(f string, sel map[string]any) map[string]any {
		ns := map[string]any{"from": f}
		if sel != nil {
			ns["selector"] = sel
		}
		return map[string]any{"namespaces": ns}
	}
	accepted := lsConds("True", "Accepted", "True", "Programmed", 1)
	notAllowed := lsConds("False", "NotAllowed", "False", "Invalid", 1)
	team := map[string]any{"matchLabels": map[string]any{"team": "shop"}}
	known := NamespaceLabels{Known: true, Labels: map[string]string{"team": "shop"}}
	other := NamespaceLabels{Known: true, Labels: map[string]string{"team": "ops"}}
	for _, tc := range []struct {
		name      string
		gw        *GatewayRef
		ns        string
		conds     []any
		labels    NamespaceLabels
		from      string
		allowed   string
		disagrees bool
		text      string
	}{
		{"unset allows none, agreeing with NotAllowed", gwWith(nil), "infra", notAllowed, known, "None", AllowedNo, false, "sets no allowedListeners"},
		{"None, yet accepted", gwWith(from("None", nil)), "infra", accepted, known, "None", AllowedNo, true, "the controller accepted it although the Gateway allows no ListenerSets (allowedListeners None)"},
		{"Same, same namespace", gwWith(from("Same", nil)), "infra", accepted, known, "Same", AllowedYes, false, "own namespace (Same)"},
		{"Same, another namespace", gwWith(from("Same", nil)), "team-b", notAllowed, known, "Same", AllowedNo, false, "this one is in team-b"},
		{"All", gwWith(from("All", nil)), "team-b", accepted, known, "All", AllowedYes, false, "every namespace"},
		{"All, yet NotAllowed", gwWith(from("All", nil)), "team-b", notAllowed, known, "All", AllowedYes, true, "the controller reported NotAllowed although"},
		{"Selector matches", gwWith(from("Selector", team)), "team-b", accepted, known, "Selector", AllowedYes, false, "matches the Gateway's selector (team=shop)"},
		{"Selector does not match", gwWith(from("Selector", team)), "team-b", notAllowed, other, "Selector", AllowedNo, false, "does not match"},
		{"Selector with labels not established", gwWith(from("Selector", team)), "team-b", notAllowed, NamespaceLabels{}, "Selector", "", false, "are not established"},
		{"Selector with none written", gwWith(from("Selector", nil)), "team-b", accepted, known, "Selector", "", false, "cannot be read"},
		{"a value Klyx does not read", gwWith(from("Mesh", nil)), "team-b", accepted, known, "Mesh", "", false, "not a value Klyx reads"},
	} {
		ls := ParseListenerSet(listenerSetObj(tc.ns, "x", t0, 1, map[string]any{"name": "eg", "namespace": "infra"}, tc.conds))
		a := ListenerSetAllowance(ls, ParentLookup{State: ParentFound, Gateway: tc.gw, Current: true}, tc.labels)
		if a.From != tc.from || a.Allowed != tc.allowed || a.Disagrees != tc.disagrees || !strings.Contains(a.Text, tc.text) {
			t.Errorf("%s: %+v", tc.name, a)
		}
	}
	// a parent from a stale read establishes no current allowance, and so
	// no disagreement (Codex's S7a implementation review, finding 3)
	none := ParseListenerSet(listenerSetObj("team-b", "y", t0, 1, map[string]any{"name": "eg", "namespace": "infra"}, accepted))
	if a := ListenerSetAllowance(none, ParentLookup{State: ParentFound, Gateway: gwWith(from("None", nil))}, known); a.Allowed != "" || a.Disagrees || !strings.Contains(a.Text, "not current") {
		t.Errorf("stale parent: %+v", a)
	}
	if a := ListenerSetAllowance(none, ParentLookup{State: ParentNotEstablished, NotServed: true}, known); a.Allowed != "" || !strings.Contains(a.Text, "Gateways are not served") {
		t.Errorf("not served: %+v", a)
	}
	// a stale Accepted condition is never compared
	ls := ParseListenerSet(listenerSetObj("team-b", "x", t0, 2, map[string]any{"name": "eg", "namespace": "infra"}, accepted))
	if a := ListenerSetAllowance(ls, ParentLookup{State: ParentFound, Gateway: gwWith(from("None", nil)), Current: true}, known); a.Disagrees {
		t.Errorf("stale: %+v", a)
	}
	for _, st := range []ParentState{ParentNotFound, ParentNotEstablished} {
		if a := ListenerSetAllowance(ls, ParentLookup{State: st}, known); a.Allowed != "" || a.Text != "the parent Gateway is "+string(st) {
			t.Errorf("%s: %+v", st, a)
		}
	}
}

func TestSortByPrecedence(t *testing.T) {
	sets := []ListenerSet{
		{Namespace: "b", Name: "x", Created: t0},
		{Namespace: "a", Name: "y", Created: t0.Add(time.Second)},
		{Namespace: "a", Name: "z", Created: t0},
		{Namespace: "a", Name: "a", Created: t0.Add(-time.Hour)},
	}
	SortByPrecedence(sets)
	var got []string
	for _, s := range sets {
		got = append(got, s.Namespace+"/"+s.Name)
	}
	if want := []string{"a/a", "a/z", "b/x", "a/y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("precedence %v, want %v (creation time, then namespace/name)", got, want)
	}
}

// Coverage S7 §8: every field the rules read is the same through the
// projection, and the projection keeps what the index reads.
func TestProjectionKeepsWhatTheRulesRead(t *testing.T) {
	gwU := gatewayWithListeners(4, map[string]any{"allowedListeners": map[string]any{"namespaces": map[string]any{"from": "Selector",
		"selector": map[string]any{"matchExpressions": []any{map[string]any{"key": "team", "operator": "In", "values": []any{"a", "b"}}}}}},
		"infrastructure": map[string]any{"annotations": map[string]any{"big": strings.Repeat("x", 4000)}}},
		healthyListener("https", 4), listenerStatus("http", 0, cond("Programmed", "False", "PortUnavailable", 4)))
	unstructured.SetNestedField(gwU.Object, int64(3), "status", "attachedListenerSets")
	gwU.SetCreationTimestamp(metav1.NewTime(t0))
	gwU.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": strings.Repeat("y", 4000)})
	p, err := Project("gateways")(gwU)
	if err != nil {
		t.Fatal(err)
	}
	po := p.(*Object)
	raw, proj := ParseGatewayRef(gwU), *po.Gateway
	if !reflect.DeepEqual(raw, proj) {
		t.Errorf("gateway differs through the projection:\nraw  %+v\nproj %+v", raw, proj)
	}
	if !reflect.DeepEqual(GatewayVerdict(raw.AcceptedCond, raw.ProgrammedCond, raw.Generation, raw.Listeners), GatewayVerdict(proj.AcceptedCond, proj.ProgrammedCond, proj.Generation, proj.Listeners)) {
		t.Error("the verdict differs through the projection")
	}
	if po.GetLabels()["app"] != "edge" || !po.GetCreationTimestamp().Time.Equal(t0) || po.GetUID() != "gw-uid" || len(po.GetAnnotations()) != 0 ||
		po.GetGeneration() != 4 || po.APIVersion != "gateway.networking.k8s.io/v1" || po.ListenerSet != nil || po.Class != nil {
		t.Errorf("metadata: labels %v created %v uid %v annotations %v", po.GetLabels(), po.GetCreationTimestamp(), po.GetUID(), po.GetAnnotations())
	}
	shaped, _ := build("gateways", gwU, &clip{limit: messageClip, listenerLimit: messageClip})
	if _, has, _ := unstructured.NestedMap(shaped.Object, "spec", "infrastructure"); has {
		t.Error("a field no rule reads was kept")
	}
	if encodedSize(shaped) != po.Size {
		t.Errorf("Size %d is not the shaped projection's %d", po.Size, encodedSize(shaped))
	}
	if po.Size <= 0 || po.Size > MaxGateway || SizeOf(po) != po.Size || len(po.Clipped) != 0 {
		t.Errorf("size %d clipped %v", po.Size, po.Clipped)
	}

	lsU := listenerSetObj("team-a", "a", t0, 2, map[string]any{"name": "eg", "namespace": "infra"}, lsConds("True", "Accepted", "True", "Programmed", 2),
		listenerStatus("extra", 0, cond("Conflicted", "True", "HostnameConflict", 2)))
	lp, _ := Project("listenersets")(lsU)
	if raw, proj := ParseListenerSet(lsU), *lp.(*Object).ListenerSet; !reflect.DeepEqual(raw, proj) {
		t.Errorf("listener set differs:\nraw  %+v\nproj %+v", raw, proj)
	}
	// an explicit empty parent group stays explicit
	lsU = listenerSetObj("team-a", "a", t0, 2, map[string]any{"group": "", "name": "eg"}, nil)
	lp, _ = Project("listenersets")(lsU)
	if lp.(*Object).ListenerSet.Parent.IsGateway() {
		t.Error("the projection must keep an explicit empty group")
	}

	gc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "GatewayClass",
		"metadata": map[string]any{"name": "eg", "uid": "gc-uid"},
		"spec":     map[string]any{"controllerName": "gateway.envoyproxy.io/gatewayclass-controller", "parametersRef": map[string]any{"name": "p"}},
		"status": map[string]any{"conditions": []any{cond("Accepted", "True", "Accepted", 1)},
			"supportedFeatures": []any{map[string]any{"name": "Gateway"}, "HTTPRoute"}}}}
	gp, _ := Project("gatewayclasses")(gc)
	got := gp.(*Object).Class
	if got == nil || got.ControllerName != "gateway.envoyproxy.io/gatewayclass-controller" || got.AcceptedCond.Status != "True" || got.Name != "eg" {
		t.Errorf("class: %+v", got)
	}
	if !reflect.DeepEqual(got.SupportedFeatures, []string{"Gateway", "HTTPRoute"}) || !got.FeaturesReported {
		t.Errorf("supported features: %v", got.SupportedFeatures)
	}
	if !reflect.DeepEqual(*got, ParseGatewayClass(gc)) {
		t.Errorf("class differs through the projection: %+v", got)
	}
	shapedGC, _ := build("gatewayclasses", gc, &clip{limit: messageClip, listenerLimit: messageClip})
	if _, has, _ := unstructured.NestedMap(shapedGC.Object, "spec", "parametersRef"); has {
		t.Error("parametersRef is read on demand, not kept")
	}
}

// Messages are scrubbed before they are clipped, and an object over its
// limit is rebuilt tight.
func TestProjectionScrubsAndClipsMessages(t *testing.T) {
	long := "token=" + strings.Repeat("s", 2000)
	u := gatewayWithListeners(1, nil, listenerStatus("https", 0, map[string]any{"type": "Programmed", "status": "False", "reason": "Invalid", "message": long, "observedGeneration": int64(1)}))
	p, _ := Project("gateways")(u)
	po := p.(*Object)
	msg := po.Gateway.Listeners[0].Status.Of("Programmed").Message
	if strings.Contains(msg, "sss") || !strings.Contains(msg, "[redacted]") {
		t.Errorf("message not scrubbed: %q", msg)
	}
	plain := strings.Repeat("é", 1500)
	u = gatewayWithListeners(1, nil, listenerStatus("https", 0, map[string]any{"type": "Programmed", "status": "False", "reason": "Invalid", "message": plain, "observedGeneration": int64(1)}))
	p, _ = Project("gateways")(u)
	po = p.(*Object)
	msg = po.Gateway.Listeners[0].Status.Of("Programmed").Message
	if len(msg) > messageClip+len("…") || !strings.HasSuffix(msg, "…") || len(po.Clipped) != 1 || !strings.Contains(po.Clipped[0], "status.listeners[0].conditions[0].message") {
		t.Errorf("clip: %d bytes, clipped %v", len(msg), po.Clipped)
	}
	// 64 listeners with long messages exceed the limit: rebuilt tight
	var ls, sts []any
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("l%02d", i)
		ls = append(ls, map[string]any{"name": name, "protocol": "HTTPS", "port": int64(443), "hostname": name + ".example.com"})
		sts = append(sts, map[string]any{"name": name, "attachedRoutes": int64(1), "conditions": []any{
			map[string]any{"type": "Programmed", "status": "False", "reason": "Invalid", "message": strings.Repeat("m", 900), "observedGeneration": int64(1)}}})
	}
	u = gatewayWithListeners(1, map[string]any{"listeners": ls})
	unstructured.SetNestedSlice(u.Object, sts, "status", "listeners")
	p, _ = Project("gateways")(u)
	po = p.(*Object)
	if po.Size > MaxGateway {
		t.Errorf("tight rebuild still %d bytes", po.Size)
	}
	if got := po.Gateway.Listeners[63].Status.Of("Programmed"); got.Reason != "Invalid" || got.Message != "" || got.ObservedGeneration != 1 {
		t.Errorf("tight drops listener messages, keeps what the rules read: %+v", got)
	}
	if len(po.Clipped) != 64 {
		t.Errorf("clipped: %d entries", len(po.Clipped))
	}
}

func TestProjectRejectsAnotherType(t *testing.T) {
	if _, err := Project("gateways")("not an object"); err == nil {
		t.Error("want an error")
	}
	if SizeOf("x") <= MaxGateway {
		t.Error("an unknown value never fits")
	}
}

// fullListeners is n listeners with the status a controller writes: four
// conditions with messages, two supported kinds, a certificate and an
// allowedRoutes (S7 §8's sizing case).
func fullListeners(n int) (spec, status []any) {
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("https-tenant-%02d", i)
		spec = append(spec, map[string]any{"name": name, "protocol": "HTTPS", "port": int64(443), "hostname": fmt.Sprintf("tenant-%02d.apps.example.com", i),
			"tls":           map[string]any{"mode": "Terminate", "certificateRefs": []any{map[string]any{"name": fmt.Sprintf("tenant-%02d-tls", i)}}},
			"allowedRoutes": map[string]any{"namespaces": map[string]any{"from": "All"}}})
		var cs []any
		for _, c := range [][2]string{{"Accepted", "Accepted"}, {"Conflicted", "NoConflicts"}, {"ResolvedRefs", "ResolvedRefs"}, {"Programmed", "Programmed"}} {
			cs = append(cs, map[string]any{"type": c[0], "status": "True", "reason": c[1], "message": "Listener has been successfully translated",
				"observedGeneration": int64(12), "lastTransitionTime": "2026-10-18T09:00:00Z"})
		}
		status = append(status, map[string]any{"name": name, "attachedRoutes": int64(3), "conditions": cs,
			"supportedKinds": []any{map[string]any{"group": Group, "kind": "HTTPRoute"}, map[string]any{"group": Group, "kind": "GRPCRoute"}}})
	}
	return spec, status
}

// Coverage S7 §8's sizing test: the API's largest listener lists, with
// their status, fit their MaxObject once tight, and a ten-listener Gateway
// fits without clipping.
func TestProjectionSizing(t *testing.T) {
	for _, n := range []int{1, 10, 64} {
		spec, status := fullListeners(n)
		gw := gatewayWithListeners(12, map[string]any{"listeners": spec})
		unstructured.SetNestedSlice(gw.Object, status, "status", "listeners")
		ls := listenerSetObj("team-a", "a", t0, 12, map[string]any{"name": "eg", "namespace": "infra"}, lsConds("True", "Accepted", "True", "Programmed", 12), status...)
		unstructured.SetNestedSlice(ls.Object, spec, "spec", "listeners")
		for _, tc := range []struct {
			resource string
			u        *unstructured.Unstructured
		}{{"gateways", gw}, {"listenersets", ls}} {
			p, _ := Project(tc.resource)(tc.u)
			o := p.(*Object)
			shaped, _ := build(tc.resource, tc.u, &clip{limit: messageClip, listenerLimit: messageClip})
			t.Logf("%s with %d listeners: %d bytes whole, %d projected (MaxObject %d), %d fields clipped", tc.resource, n, encodedSize(shaped), o.Size, MaxObject(tc.resource), len(o.Clipped))
			if o.Size > MaxObject(tc.resource) {
				t.Errorf("%s with %d listeners: %d bytes over MaxObject %d", tc.resource, n, o.Size, MaxObject(tc.resource))
			}
			if n <= 10 && len(o.Clipped) > 0 {
				t.Errorf("%s with %d listeners was clipped: %v", tc.resource, n, o.Clipped)
			}
		}
	}
}

// Codex's S7a implementation review, finding 1: every Gateway API condition
// message takes the accepted URL rule - user info removed, encoded
// separators included - on the read path and through the projection, and a
// credential crossing the clip is removed whole.
func TestConditionMessagesTakeTheURLRule(t *testing.T) {
	const needle = "S7needle"
	for _, msg := range []string{
		"cannot fetch https://alice:" + needle + "@example.test/cert",
		"cannot fetch https://alice:" + needle + "%40example.test/cert",
		strings.Repeat("x", 1000) + " https://alice:" + needle + "@example.test/" + strings.Repeat("p", 40),
	} {
		if got := SafeMessage(msg); strings.Contains(got, needle) {
			t.Errorf("SafeMessage kept the credential: %q", got)
		}
		st := listenerStatus("https", 0, map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "InvalidCertificateRef", "message": msg, "observedGeneration": int64(1)})
		u := gatewayWithListeners(1, nil, st)
		u.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Accepted", "status": "False", "reason": "Invalid", "message": msg, "observedGeneration": int64(1)}}
		raw := ParseGatewayRef(u)
		p, _ := Project("gateways")(u)
		proj := *p.(*Object).Gateway
		for name, g := range map[string]GatewayRef{"read": raw, "projected": proj} {
			if strings.Contains(g.AcceptedCond.Message, needle) || strings.Contains(g.Listeners[0].Status.Of("ResolvedRefs").Message, needle) {
				t.Errorf("%s path kept the credential: %q", name, msg[:40])
			}
		}
		lsU := listenerSetObj("infra", "a", t0, 1, map[string]any{"name": "eg"}, []any{map[string]any{"type": "Accepted", "status": "False", "reason": "Invalid", "message": msg, "observedGeneration": int64(1)}})
		lp, _ := Project("listenersets")(lsU)
		if strings.Contains(lp.(*Object).ListenerSet.AcceptedCond.Message, needle) || strings.Contains(ParseListenerSet(lsU).AcceptedCond.Message, needle) {
			t.Errorf("a ListenerSet's own condition kept the credential")
		}
		if e := GatewayVerdict(raw.AcceptedCond, raw.ProgrammedCond, raw.Generation, raw.Listeners); strings.Contains(e.Reason, needle) {
			t.Errorf("the verdict's reason kept the credential")
		}
	}
	// the clip falls inside the password: scrubbing after it would leave a
	// readable prefix with no '@' for the URL rule to find
	secret := "S7SECRETPASSWORDxyz"
	msg := strings.Repeat("x", messageClip-24) + " https://alice:" + secret + "@example.test/cert"
	if i := strings.Index(msg, secret); i >= messageClip || i+len(secret) <= messageClip {
		t.Fatalf("the fixture must cross the clip: the secret spans %d-%d", i, i+len(secret))
	}
	st := listenerStatus("https", 0, map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "InvalidCertificateRef", "message": msg, "observedGeneration": int64(1)})
	p, _ := Project("gateways")(gatewayWithListeners(1, nil, st))
	if got := p.(*Object).Gateway.Listeners[0].Status.Of("ResolvedRefs").Message; strings.Contains(got, secret[:6]) {
		t.Errorf("a credential crossing the clip left a prefix: %q", got[len(got)-40:])
	}
	// the clip itself, whose output the budget measures, scrubs first
	if got := (&clip{limit: messageClip}).message("m", msg, messageClip); strings.Contains(got, secret[:6]) {
		t.Errorf("the clip cut through the credential: %q", got[len(got)-40:])
	}
}
