package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_ValidatingWebhook(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ValidatingWebhookConfiguration",
		"webhooks": []interface{}{
			map[string]interface{}{
				"name":           "cert-manager-webhook",
				"failurePolicy":  "Fail",
				"timeoutSeconds": int64(10),
				"clientConfig": map[string]interface{}{
					"service": map[string]interface{}{
						"namespace": "cert-manager",
						"name":      "cert-manager-webhook",
						"port":      int64(443),
					},
				},
			},
			map[string]interface{}{
				"name":           "ext.example.com",
				"failurePolicy":  "Ignore",
				"timeoutSeconds": int64(5),
				"clientConfig": map[string]interface{}{
					"url": "https://hooks.example.com/validate",
				},
			},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingwebhookconfigurations", u)
	if f, ok := factByLabel(facts, "webhooks"); !ok || f.Value != "2" {
		t.Fatalf("webhooks fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "fail-closed"); !ok || f.Value != "1" || f.Tone != "warning" {
		t.Fatalf("fail-closed fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "external endpoint"); !ok || f.Value != "yes" || f.Tone != "info" {
		t.Fatalf("external endpoint fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "cert-manager-webhook"); !ok || f.Value != "cert-manager/cert-manager-webhook:443 (Fail, 10s)" {
		t.Fatalf("per-webhook row wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "ext.example.com"); !ok || f.Value != "https://hooks.example.com/validate (Ignore, 5s)" {
		t.Fatalf("url webhook row wrong: %+v", f)
	}
}

func TestBuildFacts_ValidatingWebhook_NoFailClosed(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ValidatingWebhookConfiguration",
		"webhooks": []interface{}{
			map[string]interface{}{
				"name":          "safe",
				"failurePolicy": "Ignore",
				"clientConfig": map[string]interface{}{
					"service": map[string]interface{}{"namespace": "kube-system", "name": "svc"},
				},
			},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "validatingwebhookconfigurations", u)
	if f, ok := factByLabel(facts, "fail-closed"); !ok || f.Value != "0" || f.Tone != "" {
		t.Fatalf("fail-closed must be 0 with no tone: %+v", f)
	}
	if _, ok := factByLabel(facts, "external endpoint"); ok {
		t.Fatalf("external endpoint must be absent when no url set")
	}
}

func TestBuildFacts_MutatingWebhook_Reinvocation(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "MutatingWebhookConfiguration",
		"webhooks": []interface{}{
			map[string]interface{}{
				"name":               "sidecar-injector",
				"failurePolicy":      "Ignore",
				"reinvocationPolicy": "IfNeeded",
				"clientConfig": map[string]interface{}{
					"service": map[string]interface{}{"namespace": "istio-system", "name": "istiod", "port": int64(443), "path": "/inject"},
				},
			},
		},
	}}
	facts := BuildFacts("admissionregistration.k8s.io", "mutatingwebhookconfigurations", u)
	if f, ok := factByLabel(facts, "webhooks"); !ok || f.Value != "1" {
		t.Fatalf("webhooks fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "sidecar-injector"); !ok || f.Value != "istio-system/istiod:443/inject (Ignore)" {
		t.Fatalf("per-webhook row wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "reinvocation"); !ok || f.Value != "IfNeeded" {
		t.Fatalf("reinvocation fact wrong: %+v", f)
	}
}
