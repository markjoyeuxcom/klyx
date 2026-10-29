package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

func TestDirectTransport(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"scalar","result":[1,"1"]}}`))
	}))
	defer srv.Close()

	tr := NewDirectTransport(srv.URL, "tok", srv.Client())
	status, body, err := tr.q.InstantQuery(context.Background(), "vector(1)")
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if !strings.HasPrefix(gotPath, "/api/v1/query?query=vector") {
		t.Fatalf("bad path: %s", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("bad auth: %s", gotAuth)
	}
	if !strings.Contains(string(body), "success") {
		t.Fatalf("bad body: %s", body)
	}
}

// captureRT records the outbound request and returns a canned 200.
type captureRT struct{ url string }

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.url = r.URL.Path
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       http.NoBody,
	}, nil
}

func TestProxyTransportPath(t *testing.T) {
	rt := &captureRT{}
	cfg := &rest.Config{
		Host:    "https://k8s.test",
		APIPath: "/api",
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &corev1.SchemeGroupVersion,
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
		Transport: rt,
	}
	rc, err := rest.RESTClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewProxyTransport(rc, ServiceCandidate{Namespace: "monitoring", Name: "prometheus-operated", Port: "9090", Scheme: "http"})
	_, _, _ = tr.q.InstantQuery(context.Background(), "vector(1)")
	want := "/api/v1/namespaces/monitoring/services/http:prometheus-operated:9090/proxy/api/v1/query"
	if rt.url != want {
		t.Fatalf("proxy path:\n got %s\nwant %s", rt.url, want)
	}
}
