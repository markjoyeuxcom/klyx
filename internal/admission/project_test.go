package admission

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const needle = "S3aLABneedle"

func obj(kind string, spec map[string]any, extra map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind,
		"metadata": map[string]any{"name": "x", "uid": "11111111-2222-3333-4444-555555555555", "generation": int64(3)}}
	for k, v := range spec {
		o[k] = v
	}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

// Nothing protected crosses: a URL's user info, query and path, a
// caBundle, selector values, expressions and warning text (S3a §8).
func TestProjectionsWithholdNeedles(t *testing.T) {
	cfg := obj("ValidatingWebhookConfiguration", map[string]any{"webhooks": []any{
		map[string]any{"name": "a.example.com", "failurePolicy": "Fail", "clientConfig": map[string]any{
			"url": "https://u:" + needle + "@hooks.example.com/" + needle + "?t=" + needle, "caBundle": needle},
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{"team": needle}},
			"objectSelector":    map[string]any{"matchExpressions": []any{map[string]any{"key": "k", "operator": "In", "values": []any{needle}}}},
			"matchConditions":   []any{map[string]any{"name": "c", "expression": "object.x == '" + needle + "'"}},
			"rules":             []any{map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{"lab.klyx.dev"}, "apiVersions": []any{"v1"}, "resources": []any{"widgets"}}}},
		map[string]any{"name": "b.example.com", "clientConfig": map[string]any{"service": map[string]any{"namespace": "sys", "name": "hook", "path": "/" + needle}}},
	}}, nil)
	c := ProjectConfig(cfg, Validating)
	pol := obj("ValidatingAdmissionPolicy", map[string]any{"spec": map[string]any{
		"validations": []any{map[string]any{"expression": "object.spec.token != '" + needle + "'", "message": needle}},
		"variables":   []any{map[string]any{"name": "v", "expression": "'" + needle + "'"}},
		"paramKind":   map[string]any{"apiVersion": "example.com/v1", "kind": "Limits"}}},
		map[string]any{"status": map[string]any{"observedGeneration": int64(3), "typeChecking": map[string]any{"expressionWarnings": []any{
			map[string]any{"fieldRef": "spec.validations[0].expression", "warning": "undefined field '" + needle + "'"},
			map[string]any{"fieldRef": "spec." + needle, "warning": needle}}}}})
	p := ProjectPolicy(pol, Validating)
	b := ProjectBinding(obj("ValidatingAdmissionPolicyBinding", map[string]any{"spec": map[string]any{"policyName": "x",
		"paramRef":          map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"k": needle}}, "parameterNotFoundAction": "Deny"},
		"validationActions": []any{"Deny"}}}, nil), Validating)
	for name, v := range map[string]any{"config": c, "policy": p, "binding": b} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), needle) {
			t.Errorf("%s disclosed: %s", name, raw)
		}
	}
	w := c.Webhooks[0]
	if !w.URL || w.URLOrigin != "https://hooks.example.com" || !w.URLPathSet || !w.CABundle || !w.NamespaceSelector || w.ObjectExprs != 1 || w.MatchConditions != 1 || w.RuleCount != 1 {
		t.Fatalf("webhook: %+v", w)
	}
	if s := c.Webhooks[1].Service; s == nil || s.Port != 443 || !s.PathSet || c.Webhooks[1].FailClosed() != true {
		t.Fatalf("service ref, default port and failure policy: %+v", c.Webhooks[1])
	}
	if p.WarningCount != 2 || p.WarningFields[0] != "spec.validations[0].expression" || p.WarningFields[1] != "(unrecognised field)" || p.Validations != 1 || p.Variables != 1 {
		t.Fatalf("policy: %+v", p)
	}
	if g, v, k, ok := p.ParamKind.GVK(); !ok || g != "example.com" || v != "v1" || k != "Limits" {
		t.Fatalf("param kind: %s %s %s", g, v, k)
	}
	if b.ParamRef == nil || !b.ParamRef.Selector || b.ParamRef.NotFoundAction != "Deny" {
		t.Fatalf("binding: %+v", b)
	}
}

// A configuration is cut to fit with its count whole (S3a §7).
func TestConfigCut(t *testing.T) {
	var hooks []any
	for i := 0; i < 100; i++ {
		var rules []any
		for j := 0; j < 10; j++ {
			rules = append(rules, map[string]any{"operations": []any{"CREATE", "UPDATE", "DELETE", "CONNECT"},
				"apiGroups": []any{strings.Repeat("g", 60)}, "apiVersions": []any{"v1"}, "resources": []any{strings.Repeat("r", 60)}})
		}
		hooks = append(hooks, map[string]any{"name": fmt.Sprintf("%s-%d.example.com", strings.Repeat("h", 200), i), "rules": rules,
			"clientConfig": map[string]any{"service": map[string]any{"namespace": strings.Repeat("n", 63), "name": strings.Repeat("s", 63)}}})
	}
	c := ProjectConfig(obj("MutatingWebhookConfiguration", map[string]any{"webhooks": hooks}, nil), Mutating)
	if c.Size > MaxConfig || size(c) != c.Size || !c.Cut || c.WebhookCount != 100 || len(c.Webhooks) >= 64 || c.Kind != Mutating {
		t.Fatalf("size %d cut %v count %d kept %d", c.Size, c.Cut, c.WebhookCount, len(c.Webhooks))
	}
	small := ProjectConfig(obj("ValidatingWebhookConfiguration", map[string]any{"webhooks": hooks[:1]}, nil), Validating)
	if small.Cut || len(small.Webhooks) != 1 {
		t.Fatal("a small configuration is never cut")
	}
}

// An oversized paramKind is not kept and classifies unknown, never as a
// truncated name (Codex's S3a implementation review, finding 4).
func TestParamKindOversizedIsUnknown(t *testing.T) {
	p := ProjectPolicy(obj("ValidatingAdmissionPolicy", map[string]any{"spec": map[string]any{"paramKind": map[string]any{"apiVersion": strings.Repeat("g", 500) + "/v1", "kind": "K"}}}, nil), Validating)
	if _, _, _, ok := p.ParamKind.GVK(); ok || !p.ParamKind.Oversized {
		t.Fatalf("%+v", p.ParamKind)
	}
	ev := PolicyVerdict(PolicyInput{P: p, Bindings: []*Binding{{PolicyName: "x"}}, BindingsRead: ReadComplete, ParamKinds: ParamKinds{}})
	if ev.Label != "paramKind unverified" {
		t.Fatalf("%+v", ev)
	}
}
