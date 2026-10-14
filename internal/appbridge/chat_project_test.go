package appbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
)

// Sentinels (spec 2026-10-14 §10). Arbitrary ones carry no credential shape:
// only the structural rules can keep them out. Shaped ones would also be
// caught by the scrubber, so they prove the heuristic layer too.
const (
	sentArbitrary = "ZEBRA-QUOKKA-7731"
	sentShaped    = "password=hunter2-ZEBRA"
)

func mustNotLeak(t *testing.T, what string, v any, sentinels ...string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sentinels {
		if strings.Contains(string(b), s) {
			t.Fatalf("%s leaked %q: %s", what, s, b)
		}
	}
}

func uobj(m map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: m} }

func meta(name string, extra map[string]any) map[string]any {
	m := map[string]any{"name": name, "namespace": "shop", "uid": "uid-" + name,
		"annotations": map[string]any{
			"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"pw":"` + sentArbitrary + `"}}`,
			"example.com/notes":                 sentArbitrary,
			"deployment.kubernetes.io/revision": "7",
		}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestChatProjectSecretSendsKeyNamesOnly(t *testing.T) {
	o := chatProject(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, uobj(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": meta("db", nil),
		"data":       map[string]any{"password": "WkVCUkE=", "user": sentArbitrary},
		"stringData": map[string]any{"token": sentArbitrary},
	}))
	mustNotLeak(t, "Secret", o, sentArbitrary, "WkVCUkE=")
	spec, _ := o.Spec.(map[string]any)
	keys, _ := spec["keys"].([]string)
	if strings.Join(keys, ",") != "password,token,user" || spec["type"] != "Opaque" {
		t.Fatalf("a Secret sends its type and key names: %+v", o.Spec)
	}
	if o.Annotations["deployment.kubernetes.io/revision"] != "7" || o.AnnotationsDropped != 2 {
		t.Fatalf("only allowlisted annotations cross, the rest are counted: %+v %d", o.Annotations, o.AnnotationsDropped)
	}
}

func TestChatProjectWorkloadDropsEnvValuesCommandAndArgs(t *testing.T) {
	podSpec := map[string]any{
		"serviceAccountName": "web",
		"containers": []any{map[string]any{
			"name": "app", "image": "ghcr.io/shop/web:1.2",
			"command": []any{"/bin/sh", "-c", "curl -u admin:" + sentArbitrary},
			"args":    []any{"--token", sentArbitrary},
			"env": []any{
				map[string]any{"name": "DB_PASSWORD", "value": sentArbitrary},
				map[string]any{"name": "API_URL", "value": "https://user:" + sentArbitrary + "@api"},
				map[string]any{"name": "DB_HOST", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "db", "key": "host"}}},
			},
			"readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": int64(8080),
				"httpHeaders": []any{map[string]any{"name": "Authorization", "value": "Bearer " + sentArbitrary}}}},
		}},
		"volumes": []any{map[string]any{"name": "creds", "secret": map[string]any{"secretName": "db", "items": []any{map[string]any{"key": "k", "path": sentArbitrary}}}}},
	}
	o := chatProject(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, uobj(map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("web", nil),
		"spec": map[string]any{"replicas": int64(3), "template": map[string]any{"spec": podSpec}},
	}))
	mustNotLeak(t, "Deployment", o, sentArbitrary)
	b, _ := json.Marshal(o)
	for _, want := range []string{`"DB_PASSWORD"`, `"secretKeyRef"`, `"ghcr.io/shop/web:1.2"`, `"/healthz"`, `"replicas":3`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("the projection keeps %s: %s", want, b)
		}
	}
}

func TestChatProjectHelmValuesNeverCross(t *testing.T) {
	hr := chatProject(schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, uobj(map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": meta("web", nil),
		"spec": map[string]any{
			"interval":      "5m",
			"chart":         map[string]any{"spec": map[string]any{"chart": "web", "version": "1.2.3"}},
			"values":        map[string]any{"db": map[string]any{"pw": sentArbitrary}},
			"valuesFrom":    []any{map[string]any{"kind": "Secret", "name": "web-values", "valuesKey": sentArbitrary}},
			"postRenderers": []any{map[string]any{"kustomize": map[string]any{"patches": []any{sentArbitrary}}}},
		},
	}))
	mustNotLeak(t, "HelmRelease", hr, sentArbitrary)

	app := chatProject(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, uobj(map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": meta("web", nil),
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL": "https://git:" + sentArbitrary + "@git.example.com/shop.git", "path": "charts/web", "targetRevision": "main",
				"helm": map[string]any{
					"values": "db:\n  pw: " + sentArbitrary, "valuesObject": map[string]any{"pw": sentArbitrary},
					"parameters": []any{map[string]any{"name": "image.tag", "value": sentArbitrary}},
				},
				"plugin": map[string]any{"name": "cmp", "env": []any{map[string]any{"name": "X", "value": sentArbitrary}}},
			},
		},
	}))
	mustNotLeak(t, "Application", app, sentArbitrary)
	b, _ := json.Marshal(app)
	if !strings.Contains(string(b), `"image.tag"`) || !strings.Contains(string(b), "git.example.com/shop.git") {
		t.Fatalf("parameter names and the repo (without userinfo) cross: %s", b)
	}
}

func TestChatProjectConfigMapSendsKeyNamesAndSizesOnly(t *testing.T) {
	// A HelmRelease can take values from this ConfigMap under a custom
	// valuesKey (Flux valuesFrom), so no value may cross (Codex re-review).
	o := chatProject(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, uobj(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("web-values", nil),
		"data":       map[string]any{"app.conf": "db:\n  password: " + sentArbitrary, "log_level": "debug", "conn": sentShaped},
		"binaryData": map[string]any{"blob": sentArbitrary},
	}))
	mustNotLeak(t, "ConfigMap", o, sentArbitrary, "hunter2", "debug")
	spec, _ := o.Spec.(map[string]any)
	sizes, _ := spec["dataKeyBytes"].(map[string]any)
	if len(sizes) != 3 || sizes["log_level"] != 5 {
		t.Fatalf("key names and sizes cross, never values: %+v", spec)
	}
}

// Facts come from a sanitized copy: an ownership annotation outside the
// allowlist never reaches a fact value (Codex re-review of f012dc1).
func TestChatProjectFactsNeverCarryDroppedAnnotations(t *testing.T) {
	for _, gvr := range []schema.GroupVersionResource{
		{Version: "v1", Resource: "persistentvolumeclaims"},
		{Version: "v1", Resource: "namespaces"},
		{Group: "batch", Version: "v1", Resource: "cronjobs"},
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
	} {
		o := chatProject(gvr, uobj(map[string]any{
			"apiVersion": "v1", "kind": "Thing", "metadata": meta("x", map[string]any{"annotations": map[string]any{
				"meta.helm.sh/release-name":         sentArbitrary,
				"kustomize.toolkit.fluxcd.io/name":  sentArbitrary,
				"cert-manager.io/certificate-name":  sentArbitrary,
				"deployment.kubernetes.io/revision": "3",
			}}),
			"spec": map[string]any{"schedule": "*/5 * * * *"},
		}))
		mustNotLeak(t, gvr.Resource+" facts", o, sentArbitrary)
	}
}

func TestChatProjectUnknownKindDropsSensitiveAndValueSubtrees(t *testing.T) {
	o := chatProject(schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}, uobj(map[string]any{
		"apiVersion": "example.io/v1", "kind": "Widget", "metadata": meta("w", nil),
		"spec": map[string]any{
			"replicas":    int64(2),
			"secretName":  "w-tls",
			"apiKey":      sentArbitrary,
			"credentials": map[string]any{"user": sentArbitrary},
			"values":      map[string]any{"anything": sentArbitrary},
			"config":      map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": sentArbitrary}}}},
			"description": strings.Repeat("x", 400),
		},
	}))
	mustNotLeak(t, "Widget", o, sentArbitrary)
	spec, _ := o.Spec.(map[string]any)
	if spec["replicas"] != int64(2) || spec["secretName"] != "w-tls" {
		t.Fatalf("scalars and reference names cross: %+v", spec)
	}
	if d, _ := spec["description"].(string); len([]rune(d)) > chatCRDStringMax+20 {
		t.Fatalf("an unknown kind's strings are capped at %d: %d", chatCRDStringMax, len(d))
	}
}

// The sanitized copy is what stops it: the ConfigMap fact builder reads the
// Helm ownership annotation, so the raw object leaks and the copy does not.
func TestChatFactsInputStripsWhatBuildersCopy(t *testing.T) {
	u := uobj(map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "c", "namespace": "shop", "annotations": map[string]any{"meta.helm.sh/release-name": sentArbitrary}},
		"data":     map[string]any{"k": "v"}})
	raw, _ := json.Marshal(crd.BuildFacts("", "configmaps", u))
	if !strings.Contains(string(raw), sentArbitrary) {
		t.Fatal("precondition: the builder copies the annotation from a raw object")
	}
	mustNotLeak(t, "facts from the sanitized copy", crd.BuildFacts("", "configmaps", factsInput(u)), sentArbitrary)
}
