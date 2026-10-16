package crd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The needle: a plain marker no scrubber pattern recognises (S0b §7).
const ndl = "PAYLOADmarker4X"

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func objOf(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Every registry row (S0b §1, §1.1): the needle is absent from the
// sanitised copy, its references and keys stay readable, the object is not
// edit-eligible, and Describe is withheld.
func TestSanitizeEmbedded_Registry(t *testing.T) {
	cases := []struct {
		name, group, kind, doc string
		keep                   []string // strings that must survive
	}{
		{"ResourceSet embedded Secret, template, inputs, steps", "fluxcd.controlplane.io", "ResourceSet", `{
			"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSet","metadata":{"name":"rs","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"spec\":\"` + ndl + `\"}"}},
			"spec":{"inputs":[{"db_password":"` + ndl + `","region":"west","n":1234567,"on":true,"list":["` + ndl + `"]}],
			"resources":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db"},"stringData":{"password":"` + ndl + `"},"data":{"k":"` + ndl + `"}},{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cm"},"data":{"mode":"fast"}}],
			"resourcesTemplate":"apiVersion: v1\nkind: Secret\nstringData:\n  t: ` + ndl + `\n",
			"steps":[{"name":"db","resources":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"s"},"stringData":{"p":"` + ndl + `"}}],"resourcesTemplate":"` + ndl + `"}]}}`,
			[]string{"db_password", "region", "password", "ConfigMap", "fast", `"db"`}},
		{"ResourceSetInputProvider exported id is free-form", "fluxcd.controlplane.io", "ResourceSetInputProvider", `{
			"spec":{"type":"ExternalService","defaultValues":{"token":"` + ndl + `"},"url":"https://bob:` + ndl + `@svc.example.com/inputs"},
			"status":{"exportedInputs":[{"id":"` + ndl + `","sha":"abc"}]}}`,
			[]string{"ExternalService", "svc.example.com", `"id"`, `"sha"`}},
		{"FluxInstance patches and sync URL", "fluxcd.controlplane.io", "FluxInstance", `{
			"spec":{"kustomize":{"patches":[{"target":{"kind":"Deployment"},"patch":"- op: add\n  value: ` + ndl + `"}]},
			"sync":{"kind":"GitRepository","url":"https://x-access-token:` + ndl + `@github.com/org/fleet","ref":"refs/heads/main","path":"clusters/a"}}}`,
			[]string{"Deployment", "github.com/org/fleet", "clusters/a"}},
		{"Kustomization patches, deprecated spellings, substitute", "kustomize.toolkit.fluxcd.io", "Kustomization", `{
			"spec":{"patches":[{"target":{"kind":"Secret"},"patch":"` + ndl + `"}],"patchesStrategicMerge":[{"stringData":{"a":"` + ndl + `"}}],
			"patchesJson6902":[{"target":{"name":"x"},"patch":[{"op":"add","value":"` + ndl + `"}]}],
			"postBuild":{"substitute":{"TOKEN":"` + ndl + `"},"substituteFrom":[{"kind":"Secret","name":"vars"}]}}}`,
			[]string{"TOKEN", "substituteFrom", "vars"}},
		{"HelmRelease values and post-renderer patches", "helm.toolkit.fluxcd.io", "HelmRelease", `{
			"spec":{"values":{"auth":{"apiKey":"` + ndl + `"},"replicas":3},"valuesFrom":[{"kind":"Secret","name":"chart-values"}],
			"postRenderers":[{"kustomize":{"patches":[{"patch":"` + ndl + `"}]}}]}}`,
			[]string{"apiKey", "replicas", "chart-values"}},
		{"Application: clean spec, historical source, pending operation, manifests, jsonnet, plugin", "argoproj.io", "Application", `{
			"spec":{"source":{"repoURL":"https://github.com/org/app","path":"deploy","helm":{"valueFiles":["values-prod.yaml"]}}},
			"operation":{"sync":{"source":{"repoURL":"https://github.com/org/app","helm":{"values":"` + ndl + `"}},"manifests":["` + ndl + `"]}},
			"status":{"history":[{"source":{"repoURL":"https://github.com/org/app","helm":{"valuesObject":{"pw":"` + ndl + `"},"parameters":[{"name":"p","value":"` + ndl + `"}],"fileParameters":[{"name":"f","path":"files/f.txt"}]}}}],
			"sync":{"comparedTo":{"sources":[{"repoURL":"https://github.com/org/app","directory":{"jsonnet":{"extVars":[{"name":"e","value":"` + ndl + `"}],"tlas":[{"name":"t","value":"` + ndl + `"}],"libs":["vendor"]}}}]}},
			"operationState":{"operation":{"sync":{"manifests":["` + ndl + `"]}},"syncResult":{"source":{"repoURL":"https://github.com/org/app","plugin":{"env":[{"name":"E","value":"` + ndl + `"}],"parameters":[{"name":"x","string":"` + ndl + `","array":["` + ndl + `"],"map":{"k":"` + ndl + `"}}]},"kustomize":{"patches":[{"patch":"` + ndl + `"}]}}}}}}`,
			[]string{"values-prod.yaml", "files/f.txt", "vendor", "github.com/org/app", `"pw"`}},
		{"ApplicationSet template, templatePatch, generators", "argoproj.io", "ApplicationSet", `{
			"spec":{"templatePatch":"` + ndl + `",
			"template":{"spec":{"source":{"repoURL":"https://github.com/org/app","helm":{"values":"` + ndl + `"}}}},
			"generators":[{"list":{"elements":[{"cluster":"a","secret":"` + ndl + `"}],"template":{"spec":{"sources":[{"helm":{"parameters":[{"name":"p","value":"` + ndl + `"}]}}]}}}},
			{"matrix":{"generators":[{"git":{"values":{"tok":"` + ndl + `"}}},{"plugin":{"input":{"parameters":{"k":"` + ndl + `"}}}}]}},
			{"clusters":{"values":{"v":"` + ndl + `"}}}]}}`,
			[]string{"cluster", `"secret"`, "tok", "github.com/org/app"}},
	}
	for _, c := range cases {
		in := objOf(t, c.doc)
		before := mustJSON(t, in)
		s := SanitizeEmbedded(c.group, c.kind, in)
		out := mustJSON(t, s.Object)
		if strings.Contains(out, ndl) {
			t.Errorf("%s: a payload survived:\n%s", c.name, out)
		}
		for _, k := range c.keep {
			if !strings.Contains(out, k) {
				t.Errorf("%s: %q should stay readable:\n%s", c.name, k, out)
			}
		}
		if s.EditEligible {
			t.Errorf("%s: an object with a payload must not be edit-eligible", c.name)
		}
		if s.DescribeMode != DescribeWithheld || !s.Payload {
			t.Errorf("%s: a payload, Describe withheld; got %q payload=%v", c.name, s.DescribeMode, s.Payload)
		}
		if mustJSON(t, in) != before {
			t.Errorf("%s: the input was mutated", c.name)
		}
		if again := SanitizeEmbedded(c.group, c.kind, s.Object); !reflect.DeepEqual(again.Object, s.Object) {
			t.Errorf("%s: not idempotent:\n%s\n%s", c.name, out, mustJSON(t, again.Object))
		}
	}
}

// URL-only kinds (S0b revision 2, finding 1): a GitRepository with a
// credential URL and no registered payload is sanitised, not edit-eligible,
// and Describe is url-scrubbed rather than withheld.
func TestSanitizeEmbedded_URLOnlyKind(t *testing.T) {
	in := objOf(t, `{"apiVersion":"source.toolkit.fluxcd.io/v1","kind":"GitRepository","spec":{"interval":"1m","url":"https://bob:`+ndl+`@github.com/org/repo"},
		"status":{"conditions":[{"type":"Ready","message":"unable to clone 'https://bob:`+ndl+`@github.com/org/repo'"}]}}`)
	s := SanitizeEmbedded("source.toolkit.fluxcd.io", "GitRepository", in)
	out := mustJSON(t, s.Object)
	if strings.Contains(out, ndl) || !strings.Contains(out, "https://github.com/org/repo") {
		t.Fatalf("url: %s", out)
	}
	if s.EditEligible || s.DescribeMode != DescribeURLScrubbed || s.Payload {
		t.Fatalf("a URL is not a payload: %+v", s)
	}
	clean := SanitizeEmbedded("source.toolkit.fluxcd.io", "GitRepository", objOf(t, `{"spec":{"url":"https://github.com/org/repo","interval":"1m"}}`))
	if !clean.EditEligible || len(clean.Paths) != 0 {
		t.Fatalf("a clean GitRepository stays editable: %+v", clean)
	}
}

// A registered kind with nothing in its surfaces stays editable; its
// last-applied snapshot is removed but that does not block an edit.
func TestSanitizeEmbedded_RegisteredKindWithoutPayload(t *testing.T) {
	s := SanitizeEmbedded("kustomize.toolkit.fluxcd.io", "Kustomization", objOf(t, `{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},"spec":{"path":"./apps","interval":"10m"}}`))
	if !s.EditEligible || s.Payload || s.DescribeMode != DescribeURLScrubbed {
		t.Fatalf("no payload: editable and describable, got %+v", s)
	}
	if strings.Contains(mustJSON(t, s.Object), "last-applied-configuration") {
		t.Fatal("the snapshot is stripped for a registered kind")
	}
}

func TestSanitizeEmbedded_SecretStillMasked(t *testing.T) {
	s := SanitizeEmbedded("", "Secret", objOf(t, `{"apiVersion":"v1","kind":"Secret","data":{"k":"`+ndl+`"}}`))
	if strings.Contains(mustJSON(t, s.Object), ndl) || s.EditEligible {
		t.Fatalf("secret: %+v", s)
	}
}

// Beyond the depth bound the sanitiser withholds; it never returns raw.
func TestSanitizeEmbedded_DepthWithholds(t *testing.T) {
	deep := map[string]any{"v": ndl}
	for i := 0; i < sanitizeMaxDepth+5; i++ {
		deep = map[string]any{"n": deep}
	}
	s := SanitizeEmbedded("fluxcd.controlplane.io", "ResourceSet", map[string]any{"spec": map[string]any{"inputs": []any{deep}}})
	if strings.Contains(mustJSON(t, s.Object), ndl) {
		t.Fatal("a too-deep payload leaked")
	}
}

func TestHasPlaceholder(t *testing.T) {
	for _, doc := range []string{"a: " + OmittedText, "b: " + OmittedValue, "c: <masked>", "u: https://<redacted>/x"} {
		if !HasPlaceholder(doc) {
			t.Errorf("%q carries a placeholder", doc)
		}
	}
	if HasPlaceholder("spec:\n  path: ./apps\n") {
		t.Error("a clean document has no placeholder")
	}
}

func TestValueAt(t *testing.T) {
	obj := objOf(t, `{"metadata":{"annotations":{"a.b/c":"v1"}},"spec":{"list":[{"x":"y"}],"k[1]":"q"}}`)
	for path, want := range map[string]any{
		`metadata.annotations["a.b/c"]`: "v1",
		`spec.list[0].x`:                "y",
		`spec["k[1]"]`:                  "q",
	} {
		if got, ok := ValueAt(obj, path); !ok || got != want {
			t.Errorf("%s: got %v %v", path, got, ok)
		}
	}
	if _, ok := ValueAt(obj, "spec.missing"); ok {
		t.Error("a missing path reads not found")
	}
	// every path the sanitiser reports reads back
	s := SanitizeEmbedded("fluxcd.controlplane.io", "ResourceSet", objOf(t, `{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},"spec":{"inputs":[{"a.b":"x"}],"resourcesTemplate":"t"}}`))
	raw := objOf(t, `{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},"spec":{"inputs":[{"a.b":"x"}],"resourcesTemplate":"t"}}`)
	for _, p := range s.Paths {
		if _, ok := ValueAt(raw, p); !ok {
			t.Errorf("path %q does not read back", p)
		}
	}
}
