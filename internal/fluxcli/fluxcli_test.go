package fluxcli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeRunner returns canned stdout/stderr/err and records args.
type fakeRunner struct {
	args   [][]string
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, string, error) {
	f.args = append(f.args, append([]string(nil), args...))
	return []byte(f.stdout), f.stderr, f.err
}

func TestDiffKustomizationHasChanges(t *testing.T) {
	// flux prints the diff to stdout and exits non-zero when drift exists.
	r := &fakeRunner{stdout: "► Deployment/default/podinfo drifted\n± spec.replicas: 2 -> 3\n", err: errors.New("exit status 1")}
	res := DiffKustomization(context.Background(), r, "kind-dev", "flux-system", "apps", "./apps")
	if !res.HasChanges {
		t.Fatalf("want HasChanges, got %+v", res)
	}
	if res.Err != "" {
		t.Fatalf("a diff with output is not an error: %+v", res)
	}
	if !strings.Contains(res.Output, "podinfo drifted") {
		t.Fatalf("output: %q", res.Output)
	}
	// args carry context + path
	got := strings.Join(r.args[0], " ")
	for _, want := range []string{"diff", "kustomization", "apps", "-n flux-system", "--path ./apps", "--context kind-dev"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args %q missing %q", got, want)
		}
	}
}

func TestDiffKustomizationNoChanges(t *testing.T) {
	r := &fakeRunner{stdout: "", err: nil}
	res := DiffKustomization(context.Background(), r, "", "flux-system", "apps", "./apps")
	if res.HasChanges || res.Err != "" {
		t.Fatalf("want clean result, got %+v", res)
	}
}

func TestDiffKustomizationRealError(t *testing.T) {
	// Non-zero exit with no stdout and a stderr message is a real failure.
	r := &fakeRunner{stdout: "", stderr: "unable to decrypt: no key", err: errors.New("exit status 1")}
	res := DiffKustomization(context.Background(), r, "", "flux-system", "apps", "./apps")
	if res.HasChanges {
		t.Fatalf("not a diff: %+v", res)
	}
	if !strings.Contains(res.Err, "decrypt") {
		t.Fatalf("want stderr surfaced, got %+v", res)
	}
}

func TestDiffKustomizationPartialOutputWithError(t *testing.T) {
	// A run that errors mid-diff leaves partial stdout plus an explanatory
	// stderr. The partial diff must still render, but the stderr must surface
	// as Warning so the UI can flag "diff may be incomplete" - previously it
	// was silently swallowed.
	r := &fakeRunner{
		stdout: "► Deployment/default/podinfo drifted\n",
		stderr: "failed to dry-run Secret/default/creds: admission webhook denied",
		err:    errors.New("exit status 1"),
	}
	res := DiffKustomization(context.Background(), r, "", "flux-system", "apps", "./apps")
	if !res.HasChanges || !strings.Contains(res.Output, "podinfo drifted") {
		t.Fatalf("partial diff must still render: %+v", res)
	}
	if res.Err != "" {
		t.Fatalf("partial output is not a hard failure: %+v", res)
	}
	if !strings.Contains(res.Warning, "admission webhook denied") {
		t.Fatalf("want stderr surfaced as Warning, got %+v", res)
	}
}

func TestDiffKustomizationDriftExitStderrIsNotAWarning(t *testing.T) {
	// On drift flux exits 1 and prints its standard notice to stderr. That is
	// the normal HasChanges outcome and must not produce a Warning.
	r := &fakeRunner{
		stdout: "► Deployment/default/podinfo drifted\n",
		stderr: "✗ identified at least one change, exited with non-zero exit code",
		err:    errors.New("exit status 1"),
	}
	res := DiffKustomization(context.Background(), r, "", "flux-system", "apps", "./apps")
	if !res.HasChanges || res.Err != "" {
		t.Fatalf("want plain drift result, got %+v", res)
	}
	if res.Warning != "" {
		t.Fatalf("drift notice must not become a warning: %+v", res)
	}
}

// ---------- executable resolution tests ----------

// withFluxResolution swaps fluxBin's lookup hooks (see clibin.Binary) so
// resolution behavior is covered at the fluxcli level - it was untested before
// the extraction into internal/clibin.
func withFluxResolution(
	t *testing.T,
	lp func(string) (string, error),
	exists func(string) bool,
	fallbacks func() []string,
) {
	t.Helper()
	old := fluxBin
	t.Cleanup(func() { fluxBin = old })
	fluxBin.LookPath = lp
	fluxBin.ExecutableExists = exists
	fluxBin.FallbackPaths = fallbacks
	t.Setenv("KLYX_FLUX_PATH", "")
}

func TestResolve_UsesConfiguredPath(t *testing.T) {
	withFluxResolution(t,
		func(string) (string, error) { return "", exec.ErrNotFound },
		func(path string) bool { return path == "/custom/flux" },
		func() []string { return []string{"/fallback/flux"} },
	)
	t.Setenv("KLYX_FLUX_PATH", "/custom/flux")

	got, ok := Resolve()
	if !ok {
		t.Fatal("want configured flux path to resolve")
	}
	if got != "/custom/flux" {
		t.Fatalf("want configured path, got %q", got)
	}
}

func TestResolve_UsesPathBeforeFallback(t *testing.T) {
	withFluxResolution(t,
		func(string) (string, error) { return "/path/flux", nil },
		func(string) bool { return true },
		func() []string { return []string{"/fallback/flux"} },
	)

	got, ok := Resolve()
	if !ok {
		t.Fatal("want PATH flux to resolve")
	}
	if got != "/path/flux" {
		t.Fatalf("want PATH flux, got %q", got)
	}
}

func TestResolve_UsesFallbackPath(t *testing.T) {
	withFluxResolution(t,
		func(string) (string, error) { return "", exec.ErrNotFound },
		func(path string) bool { return path == "/fallback/flux" },
		func() []string { return []string{"/fallback/flux"} },
	)

	got, ok := Resolve()
	if !ok {
		t.Fatal("want fallback flux to resolve")
	}
	if got != "/fallback/flux" {
		t.Fatalf("want fallback path, got %q", got)
	}
}

func TestDetect_UsesFallbackPath(t *testing.T) {
	withFluxResolution(t,
		func(string) (string, error) { return "", exec.ErrNotFound },
		func(path string) bool { return path == "/fallback/flux" },
		func() []string { return []string{"/fallback/flux"} },
	)

	if !Detect() {
		t.Fatal("want Detect to use fallback flux locations")
	}
}
