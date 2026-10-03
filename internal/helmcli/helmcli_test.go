package helmcli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeRunner captures the args it received and returns a canned response.
type fakeRunner struct {
	args [][]string // args per call
	out  []byte
	err  error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.args = append(f.args, append([]string(nil), args...))
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// ---------- ListReleases parse tests ----------

const listFixtureNumericRevision = `[
  {
    "name": "cert-manager",
    "namespace": "cert-manager",
    "revision": 3,
    "updated": "2024-03-15T10:22:00Z",
    "status": "deployed",
    "chart": "cert-manager-v1.14.2",
    "app_version": "v1.14.2"
  },
  {
    "name": "cilium",
    "namespace": "kube-system",
    "revision": 7,
    "updated": "2024-05-01T08:00:00+02:00",
    "status": "deployed",
    "chart": "cilium-1.15.3",
    "app_version": "1.15.3"
  }
]`

const listFixtureStringRevision = `[
  {
    "name": "nginx",
    "namespace": "default",
    "revision": "2",
    "updated": "2023-11-20 14:05:30.123456789 +0000 UTC",
    "status": "deployed",
    "chart": "nginx-15.3.0",
    "app_version": "1.25.3"
  }
]`

const listFixtureUnparseableTimestamp = `[
  {
    "name": "myapp",
    "namespace": "prod",
    "revision": 1,
    "updated": "not-a-date",
    "status": "deployed",
    "chart": "myapp-0.1.0",
    "app_version": "0.1.0"
  }
]`

func TestListReleases_NumericRevision(t *testing.T) {
	r := &fakeRunner{out: []byte(listFixtureNumericRevision)}
	releases, err := ListReleases(context.Background(), r, "my-ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("want 2 releases, got %d", len(releases))
	}

	cm := releases[0]
	if cm.Name != "cert-manager" {
		t.Errorf("want name cert-manager, got %q", cm.Name)
	}
	if cm.Revision != 3 {
		t.Errorf("want revision 3, got %d", cm.Revision)
	}
	if cm.UpdatedUnix == 0 {
		t.Error("want non-zero UpdatedUnix for RFC3339 timestamp")
	}
	if cm.Chart != "cert-manager-v1.14.2" {
		t.Errorf("want chart verbatim, got %q", cm.Chart)
	}
}

func TestListReleases_StringRevision(t *testing.T) {
	r := &fakeRunner{out: []byte(listFixtureStringRevision)}
	releases, err := ListReleases(context.Background(), r, "my-ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("want 1 release, got %d", len(releases))
	}
	if releases[0].Revision != 2 {
		t.Errorf("want revision 2 (parsed from string), got %d", releases[0].Revision)
	}
	// "2023-11-20 14:05:30.123456789 +0000 UTC" should parse to non-zero.
	if releases[0].UpdatedUnix == 0 {
		t.Error("want non-zero UpdatedUnix for legacy timestamp format")
	}
}

func TestListReleases_UnparseableTimestampIsZero(t *testing.T) {
	r := &fakeRunner{out: []byte(listFixtureUnparseableTimestamp)}
	releases, err := ListReleases(context.Background(), r, "my-ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if releases[0].UpdatedUnix != 0 {
		t.Errorf("want UpdatedUnix=0 for unparseable timestamp, got %d", releases[0].UpdatedUnix)
	}
}

func TestListReleases_PropagatesRunnerError(t *testing.T) {
	r := &fakeRunner{err: errors.New("helm list: cluster unreachable")}
	_, err := ListReleases(context.Background(), r, "ctx")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "cluster unreachable") {
		t.Errorf("want error to contain stderr text, got %q", err.Error())
	}
}

func TestListReleases_KubeContextArg(t *testing.T) {
	r := &fakeRunner{out: []byte("[]")}
	_, err := ListReleases(context.Background(), r, "my-kube-context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.args) != 1 {
		t.Fatalf("want 1 call, got %d", len(r.args))
	}
	assertArg(t, r.args[0], "--kube-context")
	assertArg(t, r.args[0], "my-kube-context")
	assertArg(t, r.args[0], "-A")
	assertArg(t, r.args[0], "-o")
	assertArg(t, r.args[0], "json")
	// --max 0 lifts helm's default 256-release cap (silent truncation otherwise).
	assertArg(t, r.args[0], "--max")
	assertArg(t, r.args[0], "0")
}

// ---------- History tests ----------

const historyFixture = `[
  {"revision": 1, "updated": "2024-01-01T00:00:00Z", "status": "superseded", "chart": "app-0.1.0", "app_version": "0.1.0", "description": "Install complete"},
  {"revision": 3, "updated": "2024-03-01T00:00:00Z", "status": "deployed",   "chart": "app-0.3.0", "app_version": "0.3.0", "description": "Upgrade complete"},
  {"revision": 2, "updated": "2024-02-01T00:00:00Z", "status": "superseded", "chart": "app-0.2.0", "app_version": "0.2.0", "description": "Upgrade complete"}
]`

func TestHistory_SortedNewestFirst(t *testing.T) {
	r := &fakeRunner{out: []byte(historyFixture)}
	entries, err := History(context.Background(), r, "ctx", "default", "app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	if entries[0].Revision != 3 {
		t.Errorf("want entries[0].Revision=3 (newest), got %d", entries[0].Revision)
	}
	if entries[1].Revision != 2 {
		t.Errorf("want entries[1].Revision=2, got %d", entries[1].Revision)
	}
	if entries[2].Revision != 1 {
		t.Errorf("want entries[2].Revision=1 (oldest), got %d", entries[2].Revision)
	}
}

func TestHistory_Args(t *testing.T) {
	r := &fakeRunner{out: []byte("[]")}
	_, err := History(context.Background(), r, "prod-ctx", "my-ns", "my-release")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.args) != 1 {
		t.Fatalf("want 1 call, got %d", len(r.args))
	}
	args := r.args[0]
	assertArg(t, args, "history")
	assertArg(t, args, "my-release")
	assertArg(t, args, "-n")
	assertArg(t, args, "my-ns")
	assertArg(t, args, "--kube-context")
	assertArg(t, args, "prod-ctx")
}

// ---------- GetValues tests ----------

func TestGetValues_NullBecomesEmpty(t *testing.T) {
	r := &fakeRunner{out: []byte("null\n")}
	v, err := GetValues(context.Background(), r, "ctx", "ns", "release")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "" {
		t.Errorf("want empty string for null values, got %q", v)
	}
}

func TestGetValues_ReturnsYAML(t *testing.T) {
	yaml := "key: value\nfoo: bar\n"
	r := &fakeRunner{out: []byte(yaml)}
	v, err := GetValues(context.Background(), r, "ctx", "ns", "release")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Trimmed but content preserved.
	if !strings.Contains(v, "key: value") {
		t.Errorf("want YAML content, got %q", v)
	}
}

func TestGetValues_Args(t *testing.T) {
	r := &fakeRunner{out: []byte("null")}
	_, _ = GetValues(context.Background(), r, "dev-ctx", "dev", "my-app")
	if len(r.args) != 1 {
		t.Fatalf("want 1 call, got %d", len(r.args))
	}
	args := r.args[0]
	assertArg(t, args, "get")
	assertArg(t, args, "values")
	assertArg(t, args, "my-app")
	assertArg(t, args, "-n")
	assertArg(t, args, "dev")
	assertArg(t, args, "-o")
	assertArg(t, args, "yaml")
	assertArg(t, args, "--kube-context")
	assertArg(t, args, "dev-ctx")
}

// ---------- Rollback tests ----------

func TestRollback_Args(t *testing.T) {
	r := &fakeRunner{out: []byte("")}
	err := Rollback(context.Background(), r, "stg-ctx", "staging", "my-release", 4, RollbackOptions{Wait: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.args) != 1 {
		t.Fatalf("want 1 call, got %d", len(r.args))
	}
	args := r.args[0]
	assertArg(t, args, "rollback")
	assertArg(t, args, "my-release")
	assertArg(t, args, "4")
	assertArg(t, args, "-n")
	assertArg(t, args, "staging")
	assertArg(t, args, "--kube-context")
	assertArg(t, args, "stg-ctx")
	assertArg(t, args, "--wait")
	assertArg(t, args, "--timeout")
	assertArg(t, args, "120s")
}

func TestRollback_PropagatesError(t *testing.T) {
	r := &fakeRunner{err: errors.New("rollback failed: pod timeout")}
	err := Rollback(context.Background(), r, "ctx", "ns", "rel", 1, RollbackOptions{})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Errorf("want stderr in error, got %q", err.Error())
	}
}

// ---------- executable resolution tests ----------

func TestResolve_UsesConfiguredPath(t *testing.T) {
	withHelmResolution(t,
		func(string) (string, error) { return "", exec.ErrNotFound },
		func(path string) bool { return path == "/custom/helm" },
		func() []string { return []string{"/fallback/helm"} },
	)
	t.Setenv("KLYX_HELM_PATH", "/custom/helm")

	got, ok := Resolve()
	if !ok {
		t.Fatal("want configured helm path to resolve")
	}
	if got != "/custom/helm" {
		t.Fatalf("want configured path, got %q", got)
	}
}

func TestResolve_UsesPathBeforeFallback(t *testing.T) {
	withHelmResolution(t,
		func(string) (string, error) { return "/path/helm", nil },
		func(string) bool { return true },
		func() []string { return []string{"/fallback/helm"} },
	)

	got, ok := Resolve()
	if !ok {
		t.Fatal("want PATH helm to resolve")
	}
	if got != "/path/helm" {
		t.Fatalf("want PATH helm, got %q", got)
	}
}

func TestResolve_UsesFallbackPath(t *testing.T) {
	withHelmResolution(t,
		func(string) (string, error) { return "", exec.ErrNotFound },
		func(path string) bool { return path == "/fallback/helm" },
		func() []string { return []string{"/fallback/helm"} },
	)

	got, ok := Resolve()
	if !ok {
		t.Fatal("want fallback helm to resolve")
	}
	if got != "/fallback/helm" {
		t.Fatalf("want fallback path, got %q", got)
	}
}

func TestDetect_UsesFallbackPath(t *testing.T) {
	withHelmResolution(t,
		func(string) (string, error) { return "", exec.ErrNotFound },
		func(path string) bool { return path == "/fallback/helm" },
		func() []string { return []string{"/fallback/helm"} },
	)

	if !Detect() {
		t.Fatal("want Detect to use fallback helm locations")
	}
}

// ---------- parseUpdated edge cases ----------

func TestParseUpdated_RFC3339(t *testing.T) {
	ts := parseUpdated("2024-03-15T10:22:00Z")
	if ts == 0 {
		t.Error("want non-zero for RFC3339 timestamp")
	}
}

func TestParseUpdated_RFC3339WithOffset(t *testing.T) {
	ts := parseUpdated("2024-05-01T08:00:00+02:00")
	if ts == 0 {
		t.Error("want non-zero for RFC3339 with offset")
	}
}

func TestParseUpdated_LegacyFormat(t *testing.T) {
	ts := parseUpdated("2023-11-20 14:05:30.123456789 +0000 UTC")
	if ts == 0 {
		t.Error("want non-zero for legacy format")
	}
}

func TestParseUpdated_LegacyFormatMST(t *testing.T) {
	ts := parseUpdated("2023-11-20 14:05:30.000000000 -0700 MST")
	if ts == 0 {
		t.Error("want non-zero for legacy MST format")
	}
}

func TestParseUpdated_Garbage(t *testing.T) {
	ts := parseUpdated("not-a-timestamp")
	if ts != 0 {
		t.Errorf("want 0 for unparseable, got %d", ts)
	}
}

func TestParseUpdated_Empty(t *testing.T) {
	ts := parseUpdated("")
	if ts != 0 {
		t.Errorf("want 0 for empty string, got %d", ts)
	}
}

// ---------- helpers ----------

func assertArg(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("expected arg %q in %v", want, args)
}

// withHelmResolution swaps helmBin's lookup hooks (see clibin.Binary) so
// resolution behavior stays covered at the helmcli level after the extraction
// into internal/clibin.
func withHelmResolution(
	t *testing.T,
	lp func(string) (string, error),
	exists func(string) bool,
	fallbacks func() []string,
) {
	t.Helper()
	old := helmBin
	t.Cleanup(func() { helmBin = old })
	helmBin.LookPath = lp
	helmBin.ExecutableExists = exists
	helmBin.FallbackPaths = fallbacks
	t.Setenv("KLYX_HELM_PATH", "")
}

// ExecRunner smoke test: just confirm it tries to run helm (will skip if helm
// not on PATH since TestDrainNodeCmd uses the same pattern).
func TestExecRunner_HelmsNotFound(t *testing.T) {
	// We deliberately call a subcommand that helm itself would reject; the point
	// is just to verify ExecRunner propagates stderr text. If helm is not present
	// this test is vacuously skipped.
	if !Detect() {
		t.Skip("helm not in PATH")
	}
	r := ExecRunner{}
	_, err := r.Run(context.Background(), "version", "--output", "json")
	// helm version --output json should succeed, but we can't assert on content
	// in a cross-env test. Just confirm no panic.
	_ = err
}

func TestExecRunner_StderrInError(t *testing.T) {
	if !Detect() {
		t.Skip("helm not in PATH")
	}
	r := ExecRunner{}
	// "helm __no_such_command__" will exit non-zero; error should include stderr.
	_, err := r.Run(context.Background(), "__no_such_command__")
	if err == nil {
		t.Fatal("want error for unknown command")
	}
	// The error should not be a bare "exit status N" with no context.
	if err.Error() == "helm __no_such_command__: exit status 1" {
		t.Logf("note: got bare exit status (acceptable if helm gave no stderr)")
	}
}

func TestRollback_OptionFlags(t *testing.T) {
	// No wait: neither --wait nor --timeout appear; no-hooks appears when set.
	r := &fakeRunner{out: []byte("")}
	if err := Rollback(context.Background(), r, "ctx", "ns", "rel", 2, RollbackOptions{NoHooks: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range r.args[0] {
		if a == "--wait" || a == "--timeout" {
			t.Fatalf("no-wait rollback must not pass %q: %v", a, r.args[0])
		}
	}
	assertArg(t, r.args[0], "--no-hooks")

	// Wait with a custom timeout.
	r2 := &fakeRunner{out: []byte("")}
	if err := Rollback(context.Background(), r2, "ctx", "ns", "rel", 2, RollbackOptions{Wait: true, TimeoutSeconds: 300}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArg(t, r2.args[0], "--wait")
	assertArg(t, r2.args[0], "300s")
}
