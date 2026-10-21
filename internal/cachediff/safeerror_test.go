package cachediff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

// dryRunRefusing is an API server that serves one namespaced kind and
// refuses every PATCH with status, echoing the submitted value in its
// message, as a validation or admission refusal can.
func dryRunRefusing(t *testing.T, group, version, plural, kind string, obj *unstructured.Unstructured, status string) *httptest.Server {
	gv := group + "/" + version
	base := "/apis/" + gv
	if group == "" {
		gv, base = version, "/api/"+version
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
		case "/api/v1":
			if group == "" {
				w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"v1","resources":[{"name":"` + plural + `","kind":"` + kind + `","namespaced":true,"verbs":["get","list","patch"]}]}`))
				return
			}
			w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"v1","resources":[]}`))
		case "/apis":
			if group == "" {
				w.Write([]byte(`{"kind":"APIGroupList","groups":[]}`))
				return
			}
			w.Write([]byte(`{"kind":"APIGroupList","groups":[{"name":"` + group + `","versions":[{"groupVersion":"` + gv + `","version":"` + version + `"}],"preferredVersion":{"groupVersion":"` + gv + `","version":"` + version + `"}}]}`))
		case base:
			w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"` + gv + `","resources":[{"name":"` + plural + `","kind":"` + kind + `","namespaced":true,"verbs":["get","list","patch"]}]}`))
		case base + "/namespaces/shop/" + plural + "/x":
			if r.Method == http.MethodPatch {
				w.WriteHeader(422)
				w.Write([]byte(status))
				return
			}
			json.NewEncoder(w).Encode(obj.Object)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Codex's S0b implementation review, finding 3: a refused dry-run of a
// payload kind or a Secret keeps only structured facts, never the API's text,
// which can echo the submitted value. Other kinds keep the message.
func TestDryRunErrorKeepsOnlyStructuredFacts(t *testing.T) {
	const needle = "DRYRUNneedle4P"
	status := `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Invalid","code":422,` +
		`"message":"x is invalid: spec.field: Invalid value: ` + needle + `; admission webhook \"policy.example.io\" denied the request: ` + needle + `",` +
		`"details":{"causes":[{"reason":"FieldValueInvalid","message":"Invalid value: ` + needle + `","field":"spec.field"}]}}`
	// a real webhook denial: the outer envelope (coverage S3b §2.1)
	denied := `{"kind":"Status","apiVersion":"v1","status":"Failure","code":400,` +
		`"message":"admission webhook \"policy.example.io\" denied the request: ` + needle + `"}`
	cases := []struct {
		name, group, version, plural, kind string
		protected                          bool
	}{
		{"ResourceSet", "fluxcd.controlplane.io", "v1", "resourcesets", "ResourceSet", true},
		{"HelmRelease", "helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmRelease", true},
		{"Secret", "", "v1", "secrets", "Secret", true},
		{"ConfigMap", "", "v1", "configmaps", "ConfigMap", false},
	}
	for _, c := range append(cases, struct {
		name, group, version, plural, kind string
		protected                          bool
	}{"HelmRelease denied", "helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmRelease", true}) {
		st := status
		if c.name == "HelmRelease denied" {
			st = denied
		}
		apiVersion := c.version
		if c.group != "" {
			apiVersion = c.group + "/" + c.version
		}
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": c.kind,
			"metadata": map[string]any{"namespace": "shop", "name": "x", "uid": "u", "resourceVersion": "7"},
			"spec":     map[string]any{"field": needle}}}
		srv := dryRunRefusing(t, c.group, c.version, c.plural, c.kind, obj, st)
		got, err := ServerSideDiff(context.Background(), &rest.Config{Host: srv.URL}, []*unstructured.Unstructured{obj}, nil, false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != 1 || got[0].Action != ActionFailed {
			t.Fatalf("%s: fixture did not fail the dry-run: %+v", c.name, got)
		}
		e := got[0].Error
		if c.protected {
			if strings.Contains(e, needle) {
				t.Errorf("%s: the value crossed in the error: %s", c.name, e)
			}
			wants := []string{"Invalid (422)", "FieldValueInvalid at spec.field", "withheld"}
			if c.name == "HelmRelease denied" {
				wants = []string{"(400)", `denied by admission webhook "policy.example.io"`, "withheld"}
			} else if strings.Contains(e, "policy.example.io") {
				// S3b §2.1: a denial quoted inside an Invalid's detail is not
				// an outer envelope, so it attributes nothing
				t.Errorf("%s: a quoted denial was attributed: %s", c.name, e)
			}
			for _, want := range wants {
				if !strings.Contains(e, want) {
					t.Errorf("%s: %q missing from %s", c.name, want, e)
				}
			}
		} else if !strings.Contains(e, "Invalid value") {
			t.Errorf("%s: an ordinary kind keeps its message: %s", c.name, e)
		}
	}
}
