package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

const s3aNeedle = "S3aLABneedle"

func s3aObj(kind string, body map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind,
		"metadata": map[string]any{"name": "p", "uid": "11111111-2222-3333-4444-555555555555", "generation": int64(2)}}
	for k, v := range body {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

// The literal-needle regression at the serialised boundary (S3a §8): an
// expression and a type-checking warning carrying a credential-shaped
// literal with no URL, a selector value, a caBundle and a URL path never
// reach the rows DTO, the instance facts, the AI Explain evidence or the
// chat projection.
func TestAdmissionNeedleNeverCrosses(t *testing.T) {
	pol := s3aObj("ValidatingAdmissionPolicy", map[string]any{
		"spec": map[string]any{"failurePolicy": "Fail", "validations": []any{map[string]any{"expression": "object.spec.token != 'sk_live_" + s3aNeedle + "'"}},
			"variables": []any{map[string]any{"name": "t", "expression": "'" + s3aNeedle + "'"}}},
		"status": map[string]any{"observedGeneration": int64(2), "typeChecking": map[string]any{"expressionWarnings": []any{
			map[string]any{"fieldRef": "spec.validations[0].expression", "warning": "ERROR: <input>:1:13: undefined field 'sk_live_" + s3aNeedle + "'"}}}}})
	bnd := s3aObj("ValidatingAdmissionPolicyBinding", map[string]any{"spec": map[string]any{"policyName": "p", "validationActions": []any{"Deny"},
		"paramRef": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"team": s3aNeedle}}, "parameterNotFoundAction": "Deny"}}})
	cfg := s3aObj("ValidatingWebhookConfiguration", map[string]any{"webhooks": []any{map[string]any{"name": "a.example.com",
		"clientConfig":   map[string]any{"url": "https://hooks.example.com/" + s3aNeedle, "caBundle": s3aNeedle},
		"objectSelector": map[string]any{"matchLabels": map[string]any{"k": s3aNeedle}}}}})

	var surfaces []any
	// the rows DTOs
	p := admission.ProjectPolicy(pol, admission.Validating)
	b := admission.ProjectBinding(bnd, admission.Validating)
	c := admission.ProjectConfig(cfg, admission.Validating)
	eval := health.NativeEval{Role: health.Degraded, Label: "Type-checking warnings", Included: true}
	surfaces = append(surfaces,
		admissionPolicyDTO(healthsrc.Row[healthsrc.AdmissionPolicyRow]{Ref: healthsrc.RowRef{UID: "u"}, Eval: eval, Data: healthsrc.AdmissionPolicyRow{Policy: p, Bindings: []*admission.Binding{b}}}),
		admissionPolicyDTO(healthsrc.Row[healthsrc.AdmissionPolicyRow]{Ref: healthsrc.RowRef{UID: "b"}, Data: healthsrc.AdmissionPolicyRow{Binding: b, PolicyState: "found"}}),
		admissionConfigDTO(healthsrc.Row[healthsrc.AdmissionConfigRow]{Ref: healthsrc.RowRef{UID: "c"}, Data: healthsrc.AdmissionConfigRow{Config: c,
			Backends: []admission.Backend{admission.BackendOf(&c.Webhooks[0], admission.Backends{})}}}))
	// the instance facts, and the AI Explain evidence built from them
	for _, o := range []struct {
		res string
		u   *unstructured.Unstructured
	}{{"validatingadmissionpolicies", pol}, {"validatingadmissionpolicybindings", bnd}, {"validatingwebhookconfigurations", cfg}} {
		facts := crd.BuildFacts("admissionregistration.k8s.io", o.res, o.u)
		surfaces = append(surfaces, facts)
		sc := &scrubber{}
		surfaces = append(surfaces, sc.detailObject(crd.InstanceDetail{Kind: o.u.GetKind(), Name: o.u.GetName(), Facts: facts}, ""))
		// the chat projection
		surfaces = append(surfaces, chatProject(schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: o.res}, o.u))
	}
	for i, v := range surfaces {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), s3aNeedle) {
			t.Errorf("surface %d disclosed: %s", i, raw)
		}
	}
}

type fakeAdmissionConn struct {
	rows   observe.Rows
	params fleet.AdmissionParams
	err    error
	got    schema.GroupVersionResource
}

func (f *fakeAdmissionConn) HealthRowsNow(context.Context, string, string) (observe.Rows, bool) {
	return f.rows, true
}
func (f *fakeAdmissionConn) CheckAdmissionParams(_ context.Context, _, _ string, gvr schema.GroupVersionResource, _ bool) (fleet.AdmissionParams, error) {
	f.got = gvr
	return f.params, f.err
}

// The param check's wording (S3a §6): a missing param under Deny denies
// only when the policy is fail-closed; Allow skips the binding.
func TestAdmissionParamCheck(t *testing.T) {
	pol := &admission.Policy{Meta: admission.Meta{Name: "p", UID: "pu"}, Kind: admission.Validating, ParamKind: &admission.ParamKind{APIVersion: "example.com/v1", Kind: "Limits"}}
	row := func(p *admission.Policy, st *admission.ParamKindState) observe.Rows {
		return observe.Rows{Rows: healthsrc.RowSet[healthsrc.AdmissionPolicyRow]{Rows: []healthsrc.Row[healthsrc.AdmissionPolicyRow]{
			{Ref: healthsrc.RowRef{UID: "pu"}, Data: healthsrc.AdmissionPolicyRow{Policy: p, ParamKind: st}}}}}
	}
	served := &admission.ParamKindState{State: admission.ParamServed, Namespaced: true, Resource: "limits", At: time.Now()}
	checks := []fleet.AdmissionParamCheck{
		{Binding: "deny", State: fleet.ParamCheckNotFound, NotFoundAction: "Deny"},
		{Binding: "allow", State: fleet.ParamCheckNotFound, NotFoundAction: "Allow"},
		{Binding: "ns", State: fleet.ParamCheckRequestNamespace},
		{Binding: "forbidden", State: fleet.ParamCheckUnreadable, Class: "forbidden"},
		{Binding: "ok", State: fleet.ParamCheckFound},
	}
	conn := &fakeAdmissionConn{rows: row(pol, served), params: fleet.AdmissionParams{Checks: checks, ReadAt: time.Now()}}
	svc := NewAdmissionService(func(string) (AdmissionConn, bool) { return conn, true })
	out := svc.CheckParams("c1", "pu")
	if out.State != "read" || out.Cluster != "c1" || out.PolicyUID != "pu" || conn.got != (schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "limits"}) {
		t.Fatalf("%+v %v", out, conn.got)
	}
	by := map[string]string{}
	for _, c := range out.Checks {
		by[c.Binding] = c.Text
	}
	if !strings.Contains(by["deny"], "is denied") || !strings.Contains(by["allow"], "skipped") || !strings.Contains(by["ns"], "request's namespace") ||
		!strings.Contains(by["forbidden"], "not readable") || by["ok"] != "found" {
		t.Fatalf("fail-closed texts: %+v", by)
	}
	// Ignore: the configuration error is ignored, so the binding is skipped
	ign := *pol
	ign.FailurePolicy = "Ignore"
	conn.rows = row(&ign, served)
	for _, c := range svc.CheckParams("c1", "pu").Checks {
		if c.Binding == "deny" && !strings.Contains(c.Text, "ignores configuration errors") {
			t.Fatalf("Deny under Ignore: %s", c.Text)
		}
	}
	// not applicable without a served paramKind; an error is said
	conn.rows = row(pol, &admission.ParamKindState{State: admission.ParamUnknown})
	if out := svc.CheckParams("c1", "pu"); out.State != "not-applicable" {
		t.Fatalf("%+v", out)
	}
	conn.rows, conn.err = row(pol, served), errors.New("boom")
	if out := svc.CheckParams("c1", "pu"); out.State != "error" {
		t.Fatalf("%+v", out)
	}
	// a binding's fact row is never Klyx's verdict
	set := healthsrc.RowSet[healthsrc.AdmissionPolicyRow]{Rows: []healthsrc.Row[healthsrc.AdmissionPolicyRow]{
		{Ref: healthsrc.RowRef{UID: "b"}, Data: healthsrc.AdmissionPolicyRow{Binding: &admission.Binding{Meta: admission.Meta{Name: "b"}}}}}}
	r, _ := findRow(set, "b")
	if _, ok := verdictOf(observe.Rows{Part: observe.Part{Classifier: "native"}, Rows: set}, r, new(int)); ok {
		t.Fatal("a binding's row was read as a verdict")
	}
}
