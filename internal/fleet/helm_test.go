package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/helmcli"
)

// captureRunner records all calls (and the subprocess env each was built
// with) and returns a canned response.
type captureRunner struct {
	calls [][]string
	envs  [][]string
	out   []byte
	err   error
}

func (r *captureRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.err != nil {
		return nil, r.err
	}
	return r.out, nil
}

// newHelmTestConn builds a minimal ClusterConn suitable for helm tests. It
// avoids starting informers (no typed/meta clients needed). kubeContext is the
// only field the helm methods read.
func newHelmTestConn(kubeCtx string) *ClusterConn {
	return &ClusterConn{name: "test", kubeContext: kubeCtx}
}

// withHelmRunner swaps the package-level runner factory and returns a restore
// func. A *captureRunner also records the env each runner was built with.
func withHelmRunner(r helmcli.Runner) func() {
	old := newHelmRunner
	newHelmRunner = func(env []string) helmcli.Runner {
		if cr, ok := r.(*captureRunner); ok {
			cr.envs = append(cr.envs, env)
		}
		return r
	}
	return func() { newHelmRunner = old }
}

func TestHelmReleases_ContextThreaded(t *testing.T) {
	r := &captureRunner{out: []byte(`[
		{"name":"nginx","namespace":"default","revision":1,
		 "updated":"2024-01-01T00:00:00Z","status":"deployed",
		 "chart":"nginx-15.3.0","app_version":"1.25.3"}
	]`)}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("stg-ctx")
	releases, err := conn.HelmReleases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("want 1 release, got %d", len(releases))
	}
	if releases[0].Name != "nginx" {
		t.Errorf("want nginx, got %q", releases[0].Name)
	}
	// Verify kubeContext was threaded through.
	if len(r.calls) == 0 {
		t.Fatal("want runner called")
	}
	assertArgInCall(t, r.calls[0], "stg-ctx")
}

func TestHelmReleases_EmptyKubeContext(t *testing.T) {
	r := &captureRunner{out: []byte("[]")}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("")
	_, err := conn.HelmReleases(context.Background())
	if err == nil {
		t.Fatal("want error for empty kubeContext")
	}
	if !strings.Contains(err.Error(), "kubeContext not set") {
		t.Errorf("want descriptive error, got %q", err.Error())
	}
	// Runner must not have been called.
	if len(r.calls) > 0 {
		t.Error("want runner NOT called when kubeContext empty")
	}
}

func TestHelmHistory_ContextThreaded(t *testing.T) {
	r := &captureRunner{out: []byte(`[
		{"revision":2,"updated":"2024-02-01T00:00:00Z","status":"deployed","chart":"app-0.2.0","app_version":"0.2.0","description":"upgrade"},
		{"revision":1,"updated":"2024-01-01T00:00:00Z","status":"superseded","chart":"app-0.1.0","app_version":"0.1.0","description":"install"}
	]`)}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("prd-ctx")
	entries, err := conn.HelmHistory(context.Background(), "my-ns", "my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Newest first.
	if entries[0].Revision != 2 {
		t.Errorf("want revision 2 first, got %d", entries[0].Revision)
	}
	if len(r.calls) == 0 {
		t.Fatal("want runner called")
	}
	assertArgInCall(t, r.calls[0], "prd-ctx")
	assertArgInCall(t, r.calls[0], "my-ns")
	assertArgInCall(t, r.calls[0], "my-app")
}

func TestHelmHistory_EmptyKubeContext(t *testing.T) {
	r := &captureRunner{}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("")
	_, err := conn.HelmHistory(context.Background(), "ns", "rel")
	if err == nil {
		t.Fatal("want error for empty kubeContext")
	}
}

func TestHelmValues_ContextThreaded(t *testing.T) {
	r := &captureRunner{out: []byte("replicaCount: 3\n")}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("dev-ctx")
	v, err := conn.HelmValues(context.Background(), "ns", "rel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(v, "replicaCount") {
		t.Errorf("want YAML values, got %q", v)
	}
	assertArgInCall(t, r.calls[0], "dev-ctx")
}

func TestHelmValues_EmptyKubeContext(t *testing.T) {
	r := &captureRunner{}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("")
	_, err := conn.HelmValues(context.Background(), "ns", "rel")
	if err == nil {
		t.Fatal("want error for empty kubeContext")
	}
}

func TestHelmRollback_ContextThreaded(t *testing.T) {
	r := &captureRunner{out: []byte("")}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("prd-ctx")
	err := conn.HelmRollback(context.Background(), "prod", "my-app", 3, helmcli.RollbackOptions{Wait: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgInCall(t, r.calls[0], "prd-ctx")
	assertArgInCall(t, r.calls[0], "prod")
	assertArgInCall(t, r.calls[0], "my-app")
	assertArgInCall(t, r.calls[0], "3")
}

func TestHelmRollback_PropagatesError(t *testing.T) {
	r := &captureRunner{err: errors.New("rollback: pod stuck")}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("ctx")
	err := conn.HelmRollback(context.Background(), "ns", "rel", 1, helmcli.RollbackOptions{})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "rollback: pod stuck") {
		t.Errorf("want error text propagated, got %q", err.Error())
	}
}

func TestHelmRollback_EmptyKubeContext(t *testing.T) {
	r := &captureRunner{}
	defer withHelmRunner(r)()

	conn := newHelmTestConn("")
	err := conn.HelmRollback(context.Background(), "ns", "rel", 1, helmcli.RollbackOptions{})
	if err == nil {
		t.Fatal("want error for empty kubeContext")
	}
}

func assertArgInCall(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("expected arg %q in %v", want, args)
}
