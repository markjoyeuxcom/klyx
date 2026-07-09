package crd

import (
	"encoding/base64"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseConditions(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "reason": "OK", "message": "all good"},
				map[string]interface{}{"type": "Synced", "status": "False", "reason": "Err", "message": "boom"},
			},
		},
	}}
	cs := ParseConditions(u.Object)
	if len(cs) != 2 {
		t.Fatalf("want 2 conditions, got %d", len(cs))
	}
	if cs[0].Type != "Ready" || cs[0].Status != "True" || cs[0].Message != "all good" {
		t.Fatalf("cond[0]: %+v", cs[0])
	}
	if cs[1].Status != "False" || cs[1].Reason != "Err" {
		t.Fatalf("cond[1]: %+v", cs[1])
	}
}

func TestParseConditionsNoneWhenAbsent(t *testing.T) {
	if cs := ParseConditions(map[string]interface{}{}); len(cs) != 0 {
		t.Fatalf("want 0, got %d", len(cs))
	}
}

func TestToYAML(t *testing.T) {
	obj := map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": "web-tls", "namespace": "default"},
	}
	y, err := ToYAML(obj)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if !strings.Contains(y, "kind: Certificate") || !strings.Contains(y, "name: web-tls") {
		t.Fatalf("yaml missing fields:\n%s", y)
	}
}

func TestMaskSecretDataMasksValues(t *testing.T) {
	// base64 for "hunter2" is "aHVudGVyMg=="
	b64 := base64.StdEncoding.EncodeToString([]byte("hunter2"))
	obj := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "my-secret", "namespace": "default"},
		"data": map[string]interface{}{
			"password": b64,
			"token":    base64.StdEncoding.EncodeToString([]byte("abc")),
		},
	}

	masked, keys := MaskSecretData(obj)

	// YAML must contain key names but not the base64 value.
	y, err := ToYAML(masked)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if !strings.Contains(y, "password") || !strings.Contains(y, "token") {
		t.Fatalf("key names must be in YAML:\n%s", y)
	}
	if strings.Contains(y, b64) {
		t.Fatalf("base64 value must NOT appear in YAML:\n%s", y)
	}
	if !strings.Contains(y, "<masked>") {
		t.Fatalf("placeholder <masked> must appear in YAML:\n%s", y)
	}

	// SecretKeys must list both keys with correct byte lengths.
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %d: %+v", len(keys), keys)
	}
	byKey := map[string]SecretKeyInfo{}
	for _, k := range keys {
		byKey[k.Key] = k
	}
	if byKey["password"].Bytes != 7 { // len("hunter2") == 7
		t.Fatalf("password bytes: %d", byKey["password"].Bytes)
	}
	if byKey["token"].Bytes != 3 { // len("abc") == 3
		t.Fatalf("token bytes: %d", byKey["token"].Bytes)
	}

	// Input must not be mutated.
	origData := obj["data"].(map[string]interface{})
	if origData["password"] != b64 {
		t.Fatal("MaskSecretData mutated the input map")
	}
}

func TestMaskSecretDataStripsLastAppliedConfig(t *testing.T) {
	// A kubectl-applied Secret carries a full JSON snapshot (incl. base64 data) in
	// the last-applied-configuration annotation. Masking spec data is not enough -
	// that annotation must be stripped, or the plaintext leaks via the YAML.
	b64 := base64.StdEncoding.EncodeToString([]byte("supersecret"))
	lac := `{"apiVersion":"v1","kind":"Secret","data":{"password":"` + b64 + `"}}`
	obj := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      "creds",
			"namespace": "app",
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/last-applied-configuration": lac,
				"reloader.stakater.com/match":                      "true",
			},
		},
		"data": map[string]interface{}{"password": b64},
	}

	masked, _ := MaskSecretData(obj)
	y, err := ToYAML(masked)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if strings.Contains(y, b64) {
		t.Fatalf("base64 secret value leaked via last-applied-configuration:\n%s", y)
	}
	if strings.Contains(y, "last-applied-configuration") {
		t.Fatalf("last-applied-configuration annotation must be stripped:\n%s", y)
	}
	// A benign annotation must survive.
	if !strings.Contains(y, "reloader.stakater.com/match") {
		t.Fatalf("benign annotations must be preserved:\n%s", y)
	}
	// The input must not be mutated (shared informer object).
	origAnn := obj["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
	if _, has := origAnn["kubectl.kubernetes.io/last-applied-configuration"]; !has {
		t.Fatal("MaskSecretData mutated the input annotations")
	}
}

func TestMaskSecretDataStringData(t *testing.T) {
	obj := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"stringData": map[string]interface{}{
			"api-key": "supersecret",
		},
	}
	masked, keys := MaskSecretData(obj)

	y, _ := ToYAML(masked)
	if strings.Contains(y, "supersecret") {
		t.Fatalf("stringData value must be masked:\n%s", y)
	}
	if len(keys) != 1 || keys[0].Key != "api-key" || keys[0].Bytes != 11 {
		t.Fatalf("keys: %+v", keys)
	}
}

func TestMaskSecretDataSorted(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"zebra": b64, "alpha": b64, "middle": b64,
		},
	}
	_, keys := MaskSecretData(obj)
	if len(keys) != 3 || keys[0].Key != "alpha" || keys[1].Key != "middle" || keys[2].Key != "zebra" {
		t.Fatalf("keys not sorted: %+v", keys)
	}
}

func TestMaskSecretDataEmptyObject(t *testing.T) {
	_, keys := MaskSecretData(map[string]interface{}{})
	if len(keys) != 0 {
		t.Fatalf("want 0 keys for empty object, got %d", len(keys))
	}
}

func TestToYAMLStripsManagedFields(t *testing.T) {
	obj := map[string]interface{}{
		"kind": "Certificate",
		"metadata": map[string]interface{}{
			"name":          "web-tls",
			"labels":        map[string]interface{}{"app": "web"},
			"managedFields": []interface{}{map[string]interface{}{"manager": "kustomize-controller"}},
		},
	}
	y, err := ToYAML(obj)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if strings.Contains(y, "managedFields") || strings.Contains(y, "kustomize-controller") {
		t.Fatalf("managedFields should be stripped:\n%s", y)
	}
	if !strings.Contains(y, "name: web-tls") || !strings.Contains(y, "app: web") {
		t.Fatalf("real fields must survive:\n%s", y)
	}
	// Input must not be mutated.
	md := obj["metadata"].(map[string]interface{})
	if _, has := md["managedFields"]; !has {
		t.Fatal("ToYAML mutated the input map (removed managedFields)")
	}
}
