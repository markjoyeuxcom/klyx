package gwapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Coverage S7b §8: a route's projection keeps every field the rules read;
// display lists are capped, but counts and fingerprints are the whole
// object's.
func TestRouteProjectionKeepsWhatTheRulesRead(t *testing.T) {
	var hosts []any
	for i := 0; i < 20; i++ {
		hosts = append(hosts, fmt.Sprintf("h%02d.example.com", i))
	}
	var matches []any
	for i := 0; i < 10; i++ {
		matches = append(matches, map[string]any{"path": map[string]any{"type": "PathPrefix", "value": fmt.Sprintf("/p%d", i)}})
	}
	u := routeObj(KindHTTPRoute, "shop", "web", 3, map[string]any{
		"parentRefs": []any{refTo("eg", "sectionName", "https"), refTo("extra", "kind", "ListenerSet", "port", int64(8443))},
		"hostnames":  hosts,
		"rules": []any{map[string]any{"name": "main", "matches": matches, "filters": []any{map[string]any{"type": "RequestHeaderModifier"}},
			"backendRefs": []any{map[string]any{"name": "web", "port": int64(80), "weight": int64(3)}, map[string]any{"name": "api", "namespace": "backends", "port": int64(80)}}}},
	}, parentEntry(refTo("eg", "sectionName", "https"), egCtl, "True", "True", 3))
	raw := ParseRoute(KindHTTPRoute, u)
	p, err := Project("httproutes")(u)
	if err != nil {
		t.Fatal(err)
	}
	o := p.(*Object)
	if !reflect.DeepEqual(raw, *o.Route) {
		t.Errorf("the route differs through the projection:\nraw  %+v\nproj %+v", raw, *o.Route)
	}
	if o.Route.HostnameCount != 20 || len(o.Route.Hostnames) != 16 || o.Route.Rules[0].MatchCount != 10 || len(o.Route.Rules[0].Matches) != 8 {
		t.Errorf("caps and counts: %d/%d, %d/%d", len(o.Route.Hostnames), o.Route.HostnameCount, len(o.Route.Rules[0].Matches), o.Route.Rules[0].MatchCount)
	}
	if !reflect.DeepEqual(o.Route.Fingerprints, RuleFingerprints(u)) {
		t.Error("fingerprints are taken over the whole rule")
	}
	shaped, _ := build("httproutes", u, &clip{limit: routeMessageClip})
	if _, has := shaped.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["filters"]; has {
		t.Error("a field no rule reads was kept")
	}
	if o.Size != encodedSize(shaped) || o.Size > MaxRoute || o.GetUID() != "HTTPRoute-web" {
		t.Errorf("size %d", o.Size)
	}
	// a ReferenceGrant
	g := routeObj("ReferenceGrant", "backends", "from-shop", 1, map[string]any{"from": []any{map[string]any{"group": Group, "kind": "HTTPRoute", "namespace": "shop"}}, "to": []any{map[string]any{"group": "", "kind": "Service"}}})
	gp, _ := Project("referencegrants")(g)
	if gr := gp.(*Object).Grant; gr == nil || !reflect.DeepEqual(*gr, ParseReferenceGrant(g)) || len(gr.From) != 1 || gr.To[0].Kind != "Service" {
		t.Errorf("grant: %+v", gr)
	}
}

// S7b §8's sizing test: a large route of each shape fits its MaxObject.
func TestRouteProjectionSizing(t *testing.T) {
	cond4 := func(og int64) []any {
		return []any{cond("Accepted", "True", "Accepted", og), cond("ResolvedRefs", "True", "ResolvedRefs", og)}
	}
	var rules []any
	for r := 0; r < 16; r++ {
		var bs, ms []any
		for b := 0; b < 4; b++ {
			bs = append(bs, map[string]any{"name": fmt.Sprintf("svc-%02d-%d", r, b), "port": int64(8080), "weight": int64(25)})
		}
		for m := 0; m < 8; m++ {
			ms = append(ms, map[string]any{"path": map[string]any{"type": "PathPrefix", "value": fmt.Sprintf("/api/v1/tenant-%02d/%d", r, m)}, "method": "GET"})
		}
		rules = append(rules, map[string]any{"matches": ms, "backendRefs": bs})
	}
	var parents, status []any
	for i := 0; i < 4; i++ {
		ref := refTo(fmt.Sprintf("gw-%d", i), "sectionName", "https")
		parents = append(parents, ref)
		status = append(status, map[string]any{"parentRef": ref, "controllerName": egCtl, "conditions": cond4(1)})
	}
	http := routeObj(KindHTTPRoute, "shop", "big", 1, map[string]any{"parentRefs": parents, "hostnames": []any{"shop.example.com"}, "rules": rules}, status...)
	tcp := routeObj(KindTCPRoute, "shop", "tcp", 1, map[string]any{"parentRefs": parents[:2], "rules": []any{map[string]any{"backendRefs": rules[0].(map[string]any)["backendRefs"]}}}, status[:2]...)
	for _, tc := range []struct {
		resource string
		u        any
	}{{"httproutes", http}, {"tcproutes", tcp}} {
		p, _ := Project(tc.resource)(tc.u)
		o := p.(*Object)
		t.Logf("%s: %d bytes projected (MaxObject %d), %d fields clipped", tc.resource, o.Size, MaxObject(tc.resource), len(o.Clipped))
		if o.Size > MaxObject(tc.resource) {
			t.Errorf("%s over its limit: %d", tc.resource, o.Size)
		}
	}
	// a long message is made safe and clipped at 512 bytes
	long := routeObj(KindHTTPRoute, "shop", "long", 1, map[string]any{"parentRefs": []any{refTo("eg")}},
		map[string]any{"parentRef": refTo("eg"), "controllerName": egCtl, "conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "R",
			"message": "see https://u:secret@x.test " + strings.Repeat("m", 900), "observedGeneration": int64(1)}}})
	p, _ := Project("httproutes")(long)
	msg := p.(*Object).Route.Parents[0].Accepted.Message
	if strings.Contains(msg, "secret") || len(msg) > routeMessageClip+len("…") {
		t.Errorf("message: %d bytes %q", len(msg), msg[:40])
	}
}
