package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
)

// Review 2026-10-03 #2: a client-side-applied Secret carries its full base64
// data in kubectl.kubernetes.io/last-applied-configuration. The YAML was masked
// but the DTO's annotations were read from the raw object, so the value reached
// the inspector without the explicit reveal flow. Check the WHOLE serialised
// detail, not just its YAML field.
func TestGetInstanceDetail_SecretDetailCarriesNoSecretBytes(t *testing.T) {
	const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"
	const encoded = "c3ludGhldGljLXNlY3JldA==" // base64("synthetic-secret")
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]interface{}{
			"name": "db", "namespace": "shop",
			"labels": map[string]interface{}{"app": "shop"},
			"annotations": map[string]interface{}{
				lastApplied:          `{"apiVersion":"v1","data":{"password":"` + encoded + `"},"kind":"Secret"}`,
				"team.example/owner": "payments",
			},
		},
		"data": map[string]interface{}{"password": encoded},
	}}
	c := &ClusterConn{dyn: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), u), typed: typedfake.NewSimpleClientset()}

	d, err := c.GetInstanceDetail(context.Background(), "", "v1", "secrets", "shop", "db")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(whole), encoded) {
		t.Fatalf("serialised Secret detail still carries the encoded value: %s", whole)
	}
	if _, ok := d.Annotations[lastApplied]; ok {
		t.Error("last-applied-configuration must not reach the DTO for a Secret")
	}
	// Masking is surgical: ordinary metadata still renders.
	if d.Annotations["team.example/owner"] != "payments" || d.Labels["app"] != "shop" {
		t.Errorf("non-sensitive metadata must survive: labels=%v annotations=%v", d.Labels, d.Annotations)
	}
	if len(d.SecretKeys) != 1 || d.SecretKeys[0].Key != "password" {
		t.Errorf("key info must still travel separately for the reveal flow: %+v", d.SecretKeys)
	}
}

// Non-Secret kinds are untouched: their last-applied annotation is ordinary
// metadata and keeps rendering.
func TestGetInstanceDetail_NonSecretKeepsLastApplied(t *testing.T) {
	const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "cfg", "namespace": "shop",
			"annotations": map[string]interface{}{lastApplied: `{"kind":"ConfigMap"}`},
		},
	}}
	c := &ClusterConn{dyn: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), u), typed: typedfake.NewSimpleClientset()}
	d, err := c.GetInstanceDetail(context.Background(), "", "v1", "configmaps", "shop", "cfg")
	if err != nil {
		t.Fatal(err)
	}
	if d.Annotations[lastApplied] == "" {
		t.Error("a ConfigMap's last-applied annotation is ordinary metadata and must still render")
	}
}
