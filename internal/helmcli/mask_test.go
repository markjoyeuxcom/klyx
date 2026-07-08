package helmcli

import (
	"reflect"
	"strings"
	"testing"
)

func TestMaskValues_RedactsSensitiveLeavesKeepsPlainOnes(t *testing.T) {
	raw := `
replicaCount: 3
image:
  repository: nginx
  tag: "1.27"
postgresql:
  auth:
    username: app
    password: hunter2
    existingSecret: ""
env:
  - name: API_TOKEN
    value: t0psecret
apiKey: abc123
publicKey: not-really-secret-but-ends-in-key
`
	masked, paths, err := MaskValues(raw)
	if err != nil {
		t.Fatalf("MaskValues: %v", err)
	}

	// The actual secret bytes must not survive anywhere in the masked document.
	for _, leak := range []string{"hunter2", "t0psecret", "abc123"} {
		if strings.Contains(masked, leak) {
			t.Fatalf("masked output leaked %q:\n%s", leak, masked)
		}
	}
	// Non-sensitive values are preserved.
	for _, keep := range []string{"nginx", "app", "replicaCount"} {
		if !strings.Contains(masked, keep) {
			t.Fatalf("masked output dropped non-sensitive %q:\n%s", keep, masked)
		}
	}
	// Paths cover the sensitive leaves (nested map, top-level, *Key suffix).
	got := map[string]bool{}
	for _, p := range paths {
		got[p] = true
	}
	if !got["postgresql.auth.password"] {
		t.Fatalf("expected postgresql.auth.password masked; paths=%v", paths)
	}
	if !got["apiKey"] {
		t.Fatalf("expected apiKey masked; paths=%v", paths)
	}
	if !got["publicKey"] {
		t.Fatalf("expected publicKey (*Key suffix) masked; paths=%v", paths)
	}
	// The k8s env {name: API_TOKEN, value: ...} secret is masked via the sibling name.
	if !got["env[0].value"] {
		t.Fatalf("expected env[0].value masked via sibling name; paths=%v", paths)
	}
	// An empty value under a sensitive key is NOT masked (would falsely imply a secret).
	if got["postgresql.auth.existingSecret"] {
		t.Fatalf("empty existingSecret must not be masked; paths=%v", paths)
	}
}

func TestMaskValues_ArrayUnderSensitiveKeyMasksEachElement(t *testing.T) {
	raw := "tokens:\n  - one\n  - two\n"
	masked, paths, err := MaskValues(raw)
	if err != nil {
		t.Fatalf("MaskValues: %v", err)
	}
	if strings.Contains(masked, "one") || strings.Contains(masked, "two") {
		t.Fatalf("array under sensitive key not masked:\n%s", masked)
	}
	if !reflect.DeepEqual(paths, []string{"tokens[0]", "tokens[1]"}) {
		t.Fatalf("array paths wrong: %v", paths)
	}
}

func TestMaskValues_EmptyAndInvalid(t *testing.T) {
	if m, p, err := MaskValues("   "); err != nil || m != "" || p != nil {
		t.Fatalf("empty input: got (%q,%v,%v)", m, p, err)
	}
	// Invalid YAML must error so the caller fails closed (never returns raw).
	if _, _, err := MaskValues("a: b:\n  - ]["); err == nil {
		t.Fatal("invalid YAML must return an error, not fall back to raw")
	}
}

func TestValueAtPath(t *testing.T) {
	raw := "postgresql:\n  auth:\n    password: hunter2\nport: 5432\n"
	if v, ok, err := ValueAtPath(raw, "postgresql.auth.password"); err != nil || !ok || v != "hunter2" {
		t.Fatalf("nested reveal: got (%q,%v,%v)", v, ok, err)
	}
	if v, ok, _ := ValueAtPath(raw, "port"); !ok || v != "5432" {
		t.Fatalf("scalar reveal: got (%q,%v)", v, ok)
	}
	if _, ok, _ := ValueAtPath(raw, "nope.missing"); ok {
		t.Fatal("missing path must report found=false")
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{"password", "Password", "dbPassword", "secret", "clientSecret", "token", "apiKey", "api_key", "privateKey", "credentials", "tlsKey", "signing_key", "key"}
	for _, k := range sensitive {
		if !isSensitiveKey(k) {
			t.Errorf("%q should be sensitive", k)
		}
	}
	plain := []string{"username", "replicaCount", "repository", "keyspace", "monkey", "image", "port"}
	for _, k := range plain {
		if isSensitiveKey(k) {
			t.Errorf("%q should NOT be sensitive", k)
		}
	}
}
