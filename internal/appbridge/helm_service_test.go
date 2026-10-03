package appbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/helmcli"
)

// fakeHelmConn is the test double for HelmConn.
type fakeHelmConn struct {
	releases    []helmcli.Release
	releasesErr error
	history     []helmcli.HistoryEntry
	historyErr  error
	values      string
	valuesErr   error
	rollbackErr error

	rollbackCalls []rollbackCall
}

type rollbackCall struct {
	namespace string
	release   string
	revision  int
	budget    time.Duration // time left on the context when helm was invoked
}

func (f *fakeHelmConn) HelmReleases(_ context.Context) ([]helmcli.Release, error) {
	return f.releases, f.releasesErr
}

func (f *fakeHelmConn) HelmHistory(_ context.Context, _, _ string) ([]helmcli.HistoryEntry, error) {
	return f.history, f.historyErr
}

func (f *fakeHelmConn) HelmValues(_ context.Context, _, _ string) (string, error) {
	return f.values, f.valuesErr
}

func (f *fakeHelmConn) HelmRollback(ctx context.Context, namespace, release string, revision int, opts helmcli.RollbackOptions) error {
	var budget time.Duration
	if dl, ok := ctx.Deadline(); ok {
		budget = time.Until(dl)
	}
	f.rollbackCalls = append(f.rollbackCalls, rollbackCall{namespace: namespace, release: release, revision: revision, budget: budget})
	return f.rollbackErr
}

// newHelmTestSvc creates a HelmService wired to the given conn under "test".
func newHelmTestSvc(conn HelmConn) *HelmService {
	return NewHelmService(func(cluster string) (HelmConn, bool) {
		if cluster == "test" {
			return conn, true
		}
		return nil, false
	})
}

// withDetectFunc swaps DetectFunc and returns a restore func.
func withDetectFunc(fn func() bool) func() {
	old := DetectFunc
	DetectFunc = fn
	return func() { DetectFunc = old }
}

// ---------- ListHelmReleases ----------

func TestListHelmReleases_Available(t *testing.T) {
	defer withDetectFunc(func() bool { return true })()

	conn := &fakeHelmConn{
		releases: []helmcli.Release{
			{Name: "nginx", Namespace: "default", Chart: "nginx-15.3.0", AppVersion: "1.25.3", Status: "deployed", Revision: 2, UpdatedUnix: 1700000000},
			{Name: "cert-manager", Namespace: "cert-manager", Chart: "cert-manager-v1.14.2", AppVersion: "v1.14.2", Status: "deployed", Revision: 1, UpdatedUnix: 1710000000},
		},
	}
	svc := newHelmTestSvc(conn)

	result := svc.ListHelmReleases("test")
	if !result.Available {
		t.Fatalf("want Available=true, got false; message: %q", result.Message)
	}
	if len(result.Releases) != 2 {
		t.Fatalf("want 2 releases, got %d", len(result.Releases))
	}
	if result.Releases[0].Name != "nginx" {
		t.Errorf("want nginx first, got %q", result.Releases[0].Name)
	}
	if result.Releases[0].Chart != "nginx-15.3.0" {
		t.Errorf("want chart verbatim, got %q", result.Releases[0].Chart)
	}
	if result.Message != "" {
		t.Errorf("want empty message on success, got %q", result.Message)
	}
}

func TestListHelmReleases_BinaryAbsent(t *testing.T) {
	defer withDetectFunc(func() bool { return false })()

	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	result := svc.ListHelmReleases("test")
	if result.Available {
		t.Error("want Available=false when binary absent")
	}
	if !strings.Contains(result.Message, "helm not found") {
		t.Errorf("want explanatory message, got %q", result.Message)
	}
	if result.Releases == nil {
		t.Error("want non-nil Releases slice even on unavailable")
	}
}

func TestListHelmReleases_ClusterMiss(t *testing.T) {
	defer withDetectFunc(func() bool { return true })()

	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	result := svc.ListHelmReleases("missing-cluster")
	if result.Available {
		t.Error("want Available=false for cluster miss")
	}
	if !strings.Contains(result.Message, "missing-cluster") {
		t.Errorf("want cluster name in message, got %q", result.Message)
	}
}

func TestListHelmReleases_RunnerError(t *testing.T) {
	defer withDetectFunc(func() bool { return true })()

	conn := &fakeHelmConn{releasesErr: errors.New("connection refused")}
	svc := newHelmTestSvc(conn)

	result := svc.ListHelmReleases("test")
	if result.Available {
		t.Error("want Available=false on runner error")
	}
	if !strings.Contains(result.Message, "connection refused") {
		t.Errorf("want error text in message, got %q", result.Message)
	}
}

// ---------- GetHelmHistory ----------

func TestGetHelmHistory_Success(t *testing.T) {
	conn := &fakeHelmConn{
		history: []helmcli.HistoryEntry{
			{Revision: 3, Status: "deployed", Chart: "app-0.3.0", AppVersion: "0.3.0", Description: "Upgrade complete", UpdatedUnix: 1712000000},
			{Revision: 2, Status: "superseded", Chart: "app-0.2.0", AppVersion: "0.2.0", Description: "Upgrade complete", UpdatedUnix: 1709000000},
			{Revision: 1, Status: "superseded", Chart: "app-0.1.0", AppVersion: "0.1.0", Description: "Install complete", UpdatedUnix: 1706000000},
		},
	}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmHistory("test", "default", "my-app")
	if result.Error != "" {
		t.Fatalf("unexpected error: %q", result.Error)
	}
	if len(result.History) != 3 {
		t.Fatalf("want 3 history entries, got %d", len(result.History))
	}
	if result.History[0].Revision != 3 {
		t.Errorf("want revision 3 first, got %d", result.History[0].Revision)
	}
}

func TestGetHelmHistory_ClusterMiss(t *testing.T) {
	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmHistory("missing", "ns", "rel")
	if result.Error == "" {
		t.Error("want non-empty Error for cluster miss")
	}
	if result.History == nil {
		t.Error("want non-nil History slice on error")
	}
}

func TestGetHelmHistory_RunnerError(t *testing.T) {
	conn := &fakeHelmConn{historyErr: errors.New("secret not found")}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmHistory("test", "ns", "rel")
	if result.Error == "" {
		t.Error("want non-empty Error on runner error")
	}
	if !strings.Contains(result.Error, "secret not found") {
		t.Errorf("want runner error propagated, got %q", result.Error)
	}
}

// ---------- GetHelmValues ----------

func TestGetHelmValues_Success(t *testing.T) {
	conn := &fakeHelmConn{values: "replicaCount: 3\nimage:\n  tag: v1.2.3\n"}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmValues("test", "default", "my-app")
	if result.Error != "" {
		t.Fatalf("unexpected error: %q", result.Error)
	}
	if !strings.Contains(result.Values, "replicaCount") {
		t.Errorf("want YAML values, got %q", result.Values)
	}
}

func TestGetHelmValues_NoUserValues(t *testing.T) {
	conn := &fakeHelmConn{values: ""} // helmcli already normalised null -> ""
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmValues("test", "default", "my-app")
	if result.Error != "" {
		t.Fatalf("unexpected error: %q", result.Error)
	}
	if result.Values != "" {
		t.Errorf("want empty Values for no-user-values, got %q", result.Values)
	}
}

func TestGetHelmValues_ClusterMiss(t *testing.T) {
	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmValues("missing", "ns", "rel")
	if result.Error == "" {
		t.Error("want non-empty Error for cluster miss")
	}
}

func TestGetHelmValues_RunnerError(t *testing.T) {
	conn := &fakeHelmConn{valuesErr: errors.New("release not found")}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmValues("test", "ns", "rel")
	if result.Error == "" {
		t.Error("want non-empty Error on runner error")
	}
}

// ---------- HelmRollback ----------

func TestHelmRollback_Success(t *testing.T) {
	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	result := svc.HelmRollback("test", "staging", "my-app", 3, RollbackOptionsDTO{Wait: true})
	if !result.OK {
		t.Fatalf("want OK, got error: %q", result.Error)
	}
	if len(conn.rollbackCalls) != 1 {
		t.Fatalf("want 1 rollback call, got %d", len(conn.rollbackCalls))
	}
	rc := conn.rollbackCalls[0]
	if rc.namespace != "staging" || rc.release != "my-app" || rc.revision != 3 {
		t.Errorf("wrong rollback args: %+v", rc)
	}
}

// Cross-review C: the context must outlast the helm timeout the user chose -
// a fixed 150s killed a 600s --wait mid-rollback (release stuck in
// pending-rollback).
func TestHelmRollback_DeadlineOutlastsChosenTimeout(t *testing.T) {
	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	if r := svc.HelmRollback("test", "ns", "rel", 2, RollbackOptionsDTO{Wait: true, NoHooks: true, TimeoutSeconds: 600}); !r.OK {
		t.Fatalf("rollback: %q", r.Error)
	}
	if got := conn.rollbackCalls[0].budget; got <= 600*time.Second {
		t.Fatalf("context budget %s does not outlast helm's 600s --timeout", got)
	}
}

func TestHelmRollback_ClusterMiss(t *testing.T) {
	conn := &fakeHelmConn{}
	svc := newHelmTestSvc(conn)

	result := svc.HelmRollback("missing", "ns", "rel", 1, RollbackOptionsDTO{})
	if result.OK {
		t.Error("want error for cluster miss")
	}
	if result.Error == "" {
		t.Error("want non-empty Error")
	}
}

func TestHelmRollback_PropagatesError(t *testing.T) {
	conn := &fakeHelmConn{rollbackErr: errors.New("pod stuck in Terminating")}
	svc := newHelmTestSvc(conn)

	result := svc.HelmRollback("test", "ns", "rel", 2, RollbackOptionsDTO{})
	if result.OK {
		t.Error("want error")
	}
	if !strings.Contains(result.Error, "pod stuck") {
		t.Errorf("want error text propagated, got %q", result.Error)
	}
}

// ---------- GetHelmValues masking (P0 secret-egress) ----------

func TestGetHelmValues_MasksSecrets(t *testing.T) {
	conn := &fakeHelmConn{values: "replicaCount: 3\npostgresql:\n  auth:\n    username: app\n    password: hunter2\napiKey: abc123\n"}
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmValues("test", "default", "my-app")
	if result.Error != "" {
		t.Fatalf("unexpected error: %q", result.Error)
	}
	// The secret bytes must not appear anywhere in what reaches the frontend/clipboard.
	for _, leak := range []string{"hunter2", "abc123"} {
		if strings.Contains(result.Values, leak) {
			t.Fatalf("GetHelmValues leaked secret %q:\n%s", leak, result.Values)
		}
	}
	// Non-sensitive values survive.
	if !strings.Contains(result.Values, "replicaCount") || !strings.Contains(result.Values, "app") {
		t.Fatalf("non-sensitive values dropped:\n%s", result.Values)
	}
	// The redacted paths are reported for the reveal affordance.
	paths := map[string]bool{}
	for _, p := range result.MaskedPaths {
		paths[p] = true
	}
	if !paths["postgresql.auth.password"] || !paths["apiKey"] {
		t.Fatalf("masked paths incomplete: %v", result.MaskedPaths)
	}
}

func TestGetHelmValues_FailsClosedOnUnparseableYAML(t *testing.T) {
	conn := &fakeHelmConn{values: "a: b:\n  - ]["} // not valid YAML
	svc := newHelmTestSvc(conn)

	result := svc.GetHelmValues("test", "default", "my-app")
	if result.Error == "" {
		t.Fatal("unparseable values must surface an error, not fall back to raw")
	}
	if result.Values != "" {
		t.Fatalf("no raw values may leak on a masking failure, got %q", result.Values)
	}
}

func TestRevealHelmValue(t *testing.T) {
	conn := &fakeHelmConn{values: "postgresql:\n  auth:\n    password: hunter2\n"}
	svc := newHelmTestSvc(conn)

	r := svc.RevealHelmValue("test", "default", "my-app", "postgresql.auth.password")
	if r.Error != "" || r.Value != "hunter2" {
		t.Fatalf("reveal must return the unmasked value, got %+v", r)
	}
	if miss := svc.RevealHelmValue("test", "default", "my-app", "nope.here"); miss.Error == "" {
		t.Fatalf("reveal of a missing path must error, got %+v", miss)
	}
	if cm := svc.RevealHelmValue("missing", "default", "my-app", "x"); cm.Error == "" {
		t.Fatal("reveal on an unconnected cluster must error")
	}
}
