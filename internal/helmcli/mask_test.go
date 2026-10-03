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

// Review 2026-10-03 #3: sensitivity must be inherited down a secret subtree.
// The map branch used to reset it to the leaf key alone, so
// credentials: {value: x} came back unredacted.
func TestMaskValues_InheritsSensitivityDownTheSubtree(t *testing.T) {
	cases := []struct {
		name, raw, secret string
		wantPaths         []string
	}{
		{"nested map (Codex repro)", "credentials:\n  username: demo\n  value: synthetic-private-value\n", "synthetic-private-value",
			[]string{"credentials.username", "credentials.value"}},
		{"deep nesting", "auth:\n  secret:\n    nested:\n      deep: deep-private\n", "deep-private",
			[]string{"auth.secret.nested.deep"}},
		{"list of maps under a sensitive key", "tokens:\n  - id: a\n    value: list-private\n", "list-private",
			[]string{"tokens[0].id", "tokens[0].value"}},
		{"alias of a secret subtree", "base: &b\n  password: alias-private\nother:\n  credentials: *b\n", "alias-private",
			[]string{"base.password", "other.credentials.password"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, paths, err := MaskValues(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret leaked through masking:\n%s", got)
			}
			if !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Errorf("paths: got %v, want %v", paths, tc.wantPaths)
			}
		})
	}
}

// yaml.v3 decodes a mapping with any non-string key into
// map[interface{}]interface{}; that subtree used to fall through to the scalar
// branch and be returned raw, nested secrets included.
func TestMaskValues_NonStringKeyedMapsAreWalked(t *testing.T) {
	raw := "ports:\n  8080:\n    password: port-private\n    protocol: TCP\n"
	got, paths, err := MaskValues(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "port-private") {
		t.Fatalf("secret under an integer-keyed map leaked:\n%s", got)
	}
	if !strings.Contains(got, "protocol: TCP") {
		t.Errorf("non-secret siblings must survive:\n%s", got)
	}
	if !reflect.DeepEqual(paths, []string{"ports.8080.password"}) {
		t.Errorf("paths: got %v", paths)
	}
	// Reveal parity: the masked path resolves through ValueAtPath.
	if v, ok, err := ValueAtPath(raw, "ports.8080.password"); err != nil || !ok || v != "port-private" {
		t.Fatalf("reveal of a non-string-keyed path: got (%q,%v,%v)", v, ok, err)
	}
}

func TestMaskValues_PlainValuesUntouched(t *testing.T) {
	raw := "replicaCount: 3\nimage:\n  repository: nginx\n  tag: v1\nexistingSecret: \"\"\n"
	got, paths, err := MaskValues(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("nothing secret here, got masked paths %v", paths)
	}
	for _, want := range []string{"replicaCount: 3", "repository: nginx", "tag: v1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// MaskManifest keeps secret REFERENCES readable at their KNOWN API locations
// (even under a secret-looking parent like volumes[].secret) - "secret X not
// found" is what a fix most often needs - while still masking credential-like
// values, and a reference-shaped value anywhere else.
func TestMaskManifest_KeepsSecretReferencesReadable(t *testing.T) {
	raw := `env:
- name: API_TOKEN
  valueFrom:
    secretKeyRef: {name: api-creds, key: token}
- name: DB_PASSWORD
  value: hunter2
volumes:
- name: tls
  secret: {secretName: web-tls}
imagePullSecrets: [{name: regcred}]
credentials:
  secretRef: {name: nested}
`
	got, paths, err := MaskManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"api-creds", "token", "web-tls", "regcred"} {
		if !strings.Contains(got, keep) {
			t.Errorf("reference %q must stay readable:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "hunter2") {
		t.Fatalf("credential values must be masked:\n%s", got)
	}
	// credentials.secretRef is not a known API location: masked like any value.
	if strings.Contains(got, "nested") {
		t.Fatalf("a reference shape outside a known API location must be masked:\n%s", got)
	}
	if len(paths) == 0 {
		t.Fatal("masked paths must be reported")
	}
}

// Codex batch-2 review R1: a reference KEY is not a reference unless it has the
// reference SHAPE. Chart values or a CRD can reuse the key for anything - e.g.
// values.credentials.secretRef.password - and that must still be masked.
func TestMaskManifest_ReferenceKeyWithoutReferenceShapeIsMasked(t *testing.T) {
	raw := `spec:
  values:
    credentials:
      secretRef:
        password: b2-review-private-password
    other:
      secretKeyRef: "literal-token"
      imagePullSecrets: [{name: ok, token: also-private}]
`
	got, _, err := MaskManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"b2-review-private-password", "literal-token", "also-private"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaked through a reference-key exemption:\n%s", leak, got)
		}
	}
}

// Codex batch-2 re-review R1: shape alone does not prove a reference. Inside
// free-form data (ConfigMap data, chart values) nothing is exempt; under spec,
// a reference is only recognised for the API group that defines it.
func TestMaskManifest_ReferenceNeedsItsAPILocation(t *testing.T) {
	cases := map[string]string{
		"ConfigMap data key":    "apiVersion: v1\nkind: ConfigMap\ndata:\n  secretName: leak-1\n",
		"values identifier":     "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nspec:\n  values:\n    credentials:\n      secretRef: {name: chart-option, key: leak-2}\n",
		"values scalar":         "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nspec:\n  values:\n    credentials:\n      existingSecret: leak-3\n",
		"spec ref, wrong group": "apiVersion: example.com/v1\nkind: Widget\nspec:\n  secretRef: {name: w, key: leak-4}\n",
	}
	for name, raw := range cases {
		got, _, err := MaskManifest(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, leak := range []string{"leak-1", "leak-2", "leak-3", "leak-4"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q leaked:\n%s", name, leak, got)
			}
		}
	}
	// Known locations stay readable, including group-scoped spec references.
	keep := map[string]string{
		"Flux source secretRef":    "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nspec:\n  secretRef: {name: git-auth}\n",
		"ESO secretStoreRef":       "apiVersion: external-secrets.io/v1beta1\nkind: ExternalSecret\nspec:\n  secretStoreRef: {name: vault, kind: ClusterSecretStore}\n",
		"Certificate secretName":   "apiVersion: cert-manager.io/v1\nkind: Certificate\nspec:\n  secretName: web-tls\n",
		"Ingress tls secretName":   "apiVersion: networking.k8s.io/v1\nkind: Ingress\nspec:\n  tls:\n  - secretName: ing-tls\n",
		"ServiceAccount pull refs": "apiVersion: v1\nkind: ServiceAccount\nimagePullSecrets: [{name: regcred}]\n",
	}
	for name, raw := range keep {
		got, _, err := MaskManifest(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(got, "<redacted>") {
			t.Errorf("%s: a genuine reference was masked:\n%s", name, got)
		}
	}
}
