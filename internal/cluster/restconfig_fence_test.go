package cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/readfence"
)

// AI A1 implementation review, finding 3: every client built from a
// cluster's REST config checks its context's read fence before each
// request; a context without one is unchanged.
func TestRESTConfigFencesEachRequest(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "config")
	kc := "apiVersion: v1\nkind: Config\nclusters:\n- name: lab\n  cluster:\n    server: " + srv.URL +
		"\ncontexts:\n- name: lab\n  context:\n    cluster: lab\n    user: lab\nusers:\n- name: lab\n  user:\n    token: t\ncurrent-context: lab\n"
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := RESTConfig(config.ClusterConfig{Name: "lab", Context: "lab", Kubeconfig: path})
	if err != nil {
		t.Fatal(err)
	}
	hc, err := rest.HTTPClientFor(rc)
	if err != nil {
		t.Fatal(err)
	}
	get := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api", nil)
		resp, err := hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	changed := errors.New("the fence changed")
	if err := get(readfence.With(context.Background(), func() error { return changed })); !errors.Is(err, changed) || hits != 0 {
		t.Fatalf("a changed fence sends nothing: %v %d", err, hits)
	}
	if err := get(context.Background()); err != nil || hits != 1 {
		t.Fatalf("no fence: %v %d", err, hits)
	}
}
