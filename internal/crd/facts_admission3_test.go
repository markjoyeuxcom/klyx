package crd

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_ValidatingAdmissionPolicy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "admissionregistration.k8s.io/v1",
		"kind":       "ValidatingAdmissionPolicy",
		"spec": map[string]interface{}{
			"failurePolicy": "Fail",
			"paramKind": map[string]interface{}{
				"apiVersion": "rules.example.com/v1",
				"kind":       "ReplicaLimit",
			},
			"validations": []interface{}{
				map[string]interface{}{"expression": "object.spec.replicas <= 5"},
				map[string]interface{}{"expression": "object.spec.replicas >= 1"},
			},
			"matchConstraints": map[string]interface{}{
				"resourceRules": []interface{}{
					map[string]interface{}{
						"apiGroups":   []interface{}{"apps"},
						"resources":   []interface{}{"deployments"},
						"operations":  []interface{}{"CREATE", "UPDATE"},
						"apiVersions": []interface{}{"v1"},
					},
				},
			},
		},
		"status": map[string]interface{}{
			"typeChecking": map[string]interface{}{
				"expressionWarnings": []interface{}{
					map[string]interface{}{
						"fieldRef": "spec.validations[0].expression",
						"warning":  "no matching overload for '_<=_' applied to '(int, string)'",
					},
				},
			},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingadmissionpolicies", u)

	if f, ok := factByLabel(facts, "type warnings"); !ok || f.Value != "1" || f.Tone != "danger" {
		t.Fatalf("type warnings fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "validations"); !ok || f.Value != "2" {
		t.Fatalf("validations fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "failure policy"); !ok || f.Value != "Fail" {
		t.Fatalf("failure policy fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "params"); !ok || f.Value != "rules.example.com/v1 ReplicaLimit" {
		t.Fatalf("params fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "apps/deployments"); !ok || f.Value != "CREATE,UPDATE" || f.Group != "Matches" {
		t.Fatalf("resource rule fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "warning 1"); !ok || f.Group != "Type Warnings" || f.Tone != "danger" ||
		f.Value != "type-checking warning on spec.validations[0].expression" {
		t.Fatalf("per-warning fact wrong: %+v", f)
	}
}

func TestBuildFacts_ValidatingAdmissionPolicy_Healthy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ValidatingAdmissionPolicy",
		"spec": map[string]interface{}{
			"validations": []interface{}{map[string]interface{}{"expression": "true"}},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingadmissionpolicies", u)
	// A healthy policy shows a zero type-warnings headline with no danger tone.
	if f, ok := factByLabel(facts, "type warnings"); !ok || f.Value != "0" || f.Tone != "" {
		t.Fatalf("healthy type warnings fact wrong: %+v", f)
	}
	// No paramKind => no params row.
	if _, ok := factByLabel(facts, "params"); ok {
		t.Fatalf("params row must be absent when paramKind unset")
	}
}

func TestBuildFacts_ValidatingAdmissionPolicy_WarningTextWithheld(t *testing.T) {
	long := strings.Repeat("x", 500)
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ValidatingAdmissionPolicy",
		"status": map[string]interface{}{
			"typeChecking": map[string]interface{}{
				"expressionWarnings": []interface{}{
					map[string]interface{}{"fieldRef": "spec", "warning": long},
				},
			},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingadmissionpolicies", u)
	f, ok := factByLabel(facts, "warning 1")
	if !ok {
		t.Fatalf("expected a warning row")
	}
	// coverage S3a §8: the warning's text quotes expression source, so it
	// never crosses - only its validated field
	if f.Value != "type-checking warning on (unrecognised field)" || strings.Contains(f.Value, long[:20]) {
		t.Fatalf("the warning text crossed: %q", f.Value)
	}
}

func TestBuildFacts_ValidatingAdmissionPolicyBinding(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ValidatingAdmissionPolicyBinding",
		"spec": map[string]interface{}{
			"policyName":        "replica-limit",
			"validationActions": []interface{}{"Deny", "Audit"},
			"paramRef": map[string]interface{}{
				"name":                    "replica-limit-prod",
				"parameterNotFoundAction": "Deny",
			},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingadmissionpolicybindings", u)

	if f, ok := factByLabel(facts, "binds policy"); !ok || f.Value != "replica-limit" {
		t.Fatalf("binds policy fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "enforcement"); !ok || f.Value != "Deny, Audit" {
		t.Fatalf("enforcement fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "params"); !ok || f.Value != "replica-limit-prod (Deny)" {
		t.Fatalf("params fact wrong: %+v", f)
	}
}

func TestBuildFacts_ValidatingAdmissionPolicyBinding_Minimal(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ValidatingAdmissionPolicyBinding",
		"spec": map[string]interface{}{
			"policyName":        "replica-limit",
			"validationActions": []interface{}{"Warn"},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingadmissionpolicybindings", u)
	if f, ok := factByLabel(facts, "enforcement"); !ok || f.Value != "Warn" {
		t.Fatalf("enforcement fact wrong: %+v", f)
	}
	// No paramRef => no params row.
	if _, ok := factByLabel(facts, "params"); ok {
		t.Fatalf("params row must be absent when paramRef unset")
	}
}
