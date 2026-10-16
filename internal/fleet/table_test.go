package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// The Line slice 15a: a kind without a hand-written list is read as the
// server's table; a server that cannot render one falls back to metadata.
const widgetTable = `{"kind":"Table","apiVersion":"meta.k8s.io/v1","metadata":{"continue":"next-page"},
"columnDefinitions":[{"name":"Name","type":"string","format":"name"},
 {"name":"Phase","type":"string","description":"Custom resource definition column (in JSONPath format): .status.phase"},
 {"name":"Replicas","type":"integer","description":"Custom resource definition column (in JSONPath format): .spec.replicas","priority":1}],
"rows":[{"cells":["w1","Running",3],"object":{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"name":"w1","namespace":"team-a","creationTimestamp":"2026-10-10T10:00:00Z"}}}]}`

func tableServer(t *testing.T, seen *[]*http.Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r)
		switch {
		case strings.HasPrefix(r.URL.Path, "/apis/example.com/v1/widgets"):
			w.Header().Set("Content-Type", "application/json;as=Table;v=v1;g=meta.k8s.io")
			_, _ = w.Write([]byte(widgetTable))
		case strings.HasPrefix(r.URL.Path, "/apis/example.com/v1/gadgets"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotAcceptable","code":406}`))
		case strings.HasPrefix(r.URL.Path, "/apis/example.com/v1/plains"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"kind":"PlainList","items":[]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"widgets are forbidden","code":403}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tableConn(t *testing.T, host string, objs ...*metav1.PartialObjectMetadata) *ClusterConn {
	t.Helper()
	scheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(scheme)
	items := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		items = append(items, o)
	}
	mc := metadatafake.NewSimpleMetadataClient(scheme, items...)
	return NewClusterConn("x", kubefake.NewSimpleClientset(), mc, nil, nil, clock.Real{}, config.MetricsConfig{}).WithRESTConfig(&rest.Config{Host: host})
}

func TestListInstances_ReadsTheServersTable(t *testing.T) {
	var seen []*http.Request
	srv := tableServer(t, &seen)
	c := tableConn(t, srv.URL)
	page, err := c.ListInstances(context.Background(), "example.com", "v1", "widgets", 50, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Columns) != 2 || page.Columns[0].Name != "Phase" || page.Columns[1].Priority != 1 || !page.Columns[1].Numeric {
		t.Fatalf("columns %+v", page.Columns)
	}
	if page.Continue != "next-page" || len(page.Items) != 1 || page.Items[0].Namespace != "team-a" || strings.Join(page.Items[0].Cells, ",") != "Running,3" {
		t.Fatalf("page %+v", page)
	}
	r := seen[0]
	if !strings.Contains(r.Header.Get("Accept"), "as=Table;v=v1;g=meta.k8s.io") {
		t.Errorf("accept %q", r.Header.Get("Accept"))
	}
	q := r.URL.Query()
	if q.Get("includeObject") != "Metadata" || q.Get("limit") != "50" || q.Get("continue") != "tok" {
		t.Errorf("query %v", q)
	}
}

func TestListInstances_FallsBackWithoutATable(t *testing.T) {
	var seen []*http.Request
	srv := tableServer(t, &seen)
	for _, plural := range []string{"gadgets", "plains"} {
		c := tableConn(t, srv.URL, partialMeta("example.com", "v1", "Gadget", "team-a", "g1"))
		page, err := c.ListInstances(context.Background(), "example.com", "v1", plural, 50, "")
		if err != nil {
			t.Fatalf("%s: %v", plural, err)
		}
		if page.Columns != nil {
			t.Errorf("%s: a fallback page has no columns, got %+v", plural, page.Columns)
		}
	}
}

func TestListInstances_ATableFailureFallsBackToThePlainRead(t *testing.T) {
	var seen []*http.Request
	srv := tableServer(t, &seen)
	c := tableConn(t, srv.URL, partialMeta("example.com", "v1", "Secretthing", "team-a", "s1"))
	page, err := c.ListInstances(context.Background(), "example.com", "v1", "secretthings", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Columns != nil || len(page.Items) != 1 {
		t.Fatalf("a failed table read gives the plain read's answer, got %+v", page)
	}
}

// The Line slice 15b: one response carries the object and its table row.
const gadgetObjectTable = `{"kind":"Table","apiVersion":"meta.k8s.io/v1","metadata":{"resourceVersion":"77"},
"columnDefinitions":[{"name":"Name","type":"string","format":"name"},
 {"name":"Ready","type":"string","description":"Custom resource definition column (in JSONPath format): .status.conditions[?(@.type==\"Ready\")].status"},
 {"name":"Mode","type":"string","priority":1,"description":"Custom resource definition column (in JSONPath format): .spec.mode"}],
"rows":[{"cells":["g1","False","fast"],"object":{"apiVersion":"example.com/v1","kind":"Gadget","metadata":{"name":"g1","namespace":"team-a","uid":"u-1","resourceVersion":"77","generation":4,
 "finalizers":["example.com/cleanup"],"deletionTimestamp":"2026-10-16T09:00:00Z",
 "managedFields":[{"manager":"kubectl","operation":"Update","time":"2026-10-15T09:00:00Z","fieldsV1":{"f:spec":{}}}]},
 "spec":{"mode":"fast"},"status":{"observedGeneration":3,"conditions":[{"type":"Ready","status":"False","reason":"Broken","lastTransitionTime":"2026-10-15T08:00:00Z","observedGeneration":3}]}}}]}`

func TestGetInstanceDetail_OneSnapshotWithItsRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/example.com/v1/namespaces/team-a/gadgets/g1" || r.URL.Query().Get("includeObject") != "Object" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json;as=Table;v=v1;g=meta.k8s.io")
		_, _ = w.Write([]byte(gadgetObjectTable))
	}))
	t.Cleanup(srv.Close)
	c := tableConn(t, srv.URL)
	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "gadgets", "team-a", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if d.UID != "u-1" || d.Generation != 4 || d.ObservedGeneration != 3 || d.Deleting.IsZero() || len(d.Finalizers) != 1 {
		t.Fatalf("object facts %+v", d)
	}
	if len(d.Summary) != 2 || d.Summary[0].Value != "False" || d.Summary[0].ConditionType != "Ready" || d.Summary[1].Priority != 1 || d.Summary[1].Value != "fast" {
		t.Fatalf("summary %+v", d.Summary)
	}
	if len(d.Managers) != 1 || d.Managers[0].Manager != "kubectl" {
		t.Fatalf("managers %+v", d.Managers)
	}
	if !strings.Contains(d.FieldsJSON, `"mode":"fast"`) || strings.Contains(d.FieldsJSON, "managedFields") {
		t.Fatalf("fields %q", d.FieldsJSON)
	}
	if len(d.Conditions) != 1 || d.Conditions[0].ObservedGeneration != 3 {
		t.Fatalf("conditions %+v", d.Conditions)
	}
}
