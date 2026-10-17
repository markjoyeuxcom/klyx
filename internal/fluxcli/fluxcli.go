// Package fluxcli is a pure adapter over the flux CLI. It shells out to the
// flux binary for the one operation that genuinely needs it - an on-demand,
// local-build live-vs-Git diff (`flux diff kustomization`). The CLI inherits
// the shell's per-cloud auth, so SOPS via age/GPG + AWS/Azure/GCP KMS all work
// with no provider-specific code here. All functions accept a Runner so tests
// can inject a fake without spawning real processes.
package fluxcli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moomora/klyx/internal/clibin"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Runner executes a flux invocation. Unlike a plain runner it returns stdout AND
// stderr AND the run error separately, because `flux diff` writes the diff to
// stdout and exits non-zero when drift exists - so stdout must not be discarded
// on a non-zero exit.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout []byte, stderr string, err error)
}

// ExecRunner is the production Runner that executes flux via exec.CommandContext.
// Env holds extra environment entries (KEY=VALUE) appended to the inherited
// environment - the fleet layer sets KUBECONFIG here so flux resolves the same
// kubeconfig selection client-go used. Nil inherits the environment unchanged.
type ExecRunner struct {
	Env []string
}

// fluxBin is the shared binary-resolution stack (env override -> PATH ->
// package-manager fallbacks). Tests swap its lookup hooks.
var fluxBin = clibin.Binary{
	Name:          "flux",
	EnvVar:        "KLYX_FLUX_PATH",
	FallbackPaths: defaultFluxFallbackPaths,
}

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, string, error) {
	bin, ok := Resolve()
	if !ok {
		bin = "flux"
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec
	if len(r.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), strings.TrimSpace(stderr.String()), err
}

// Detect reports whether the flux binary is available to Klyx.
func Detect() bool {
	return fluxBin.Detect()
}

// Resolve returns the flux executable Klyx should use. macOS .app launches do
// not inherit the user's interactive shell PATH, so resolution also checks the
// usual package-manager locations (see internal/clibin).
func Resolve() (string, bool) {
	return fluxBin.Resolve()
}

func defaultFluxFallbackPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin/flux", "/usr/local/bin/flux", "/opt/local/bin/flux"}
	case "linux":
		return []string{"/usr/local/bin/flux", "/usr/bin/flux", "/snap/bin/flux"}
	case "windows":
		var paths []string
		if root := strings.TrimSpace(os.Getenv("ChocolateyInstall")); root != "" {
			paths = append(paths, filepath.Join(root, "bin", "flux.exe"))
		}
		if root := strings.TrimSpace(os.Getenv("ProgramFiles")); root != "" {
			paths = append(paths, filepath.Join(root, "flux", "flux.exe"))
		}
		return paths
	default:
		return nil
	}
}

// DiffResult is the outcome of `flux diff kustomization`.
type DiffResult struct {
	Output     string // the diff text (when HasChanges)
	HasChanges bool
	Warning    string // stderr from an errored run that still produced output: the diff may be incomplete
	Err        string // a real failure (flux missing, build/decrypt/auth error)
}

// DiffKustomization runs `flux diff kustomization <name> -n <ns> --path <path>`
// against the given kubeContext. path is a LOCAL filesystem path to the built
// manifests (flux builds locally and dry-runs against the cluster - no clone).
//
// flux writes the diff to stdout and exits non-zero when drift exists, so we
// treat any stdout content as the diff (HasChanges) regardless of exit code, and
// only surface Err when there is no diff output and the command failed. A run
// that errored mid-diff (e.g. one resource failing its dry-run) leaves partial
// stdout plus an explanatory stderr: that stderr becomes Warning so the UI can
// flag the diff as possibly incomplete instead of silently trusting it.
func DiffKustomization(ctx context.Context, r Runner, kubeContext, ns, name, path string) DiffResult {
	args := []string{"diff", "kustomization", name, "-n", ns, "--path", path}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	stdout, stderr, err := r.Run(ctx, args...)
	out := strings.TrimSpace(string(stdout))
	if out != "" {
		res := DiffResult{Output: sanitizeDiff(out), HasChanges: true}
		if err != nil && stderr != "" && !isDriftExit(stderr) {
			res.Warning = sanitizeError(stderr)
		}
		return res
	}
	if err != nil {
		if stderr == "" {
			// Go's own error (the binary missing, an exit status): no flux text
			return DiffResult{Err: urlscrub.Text(err.Error()).Text}
		}
		return DiffResult{Err: sanitizeError(stderr)}
	}
	return DiffResult{HasChanges: false}
}

// isDriftExit reports whether stderr is flux's expected drift signal. On drift
// flux exits 1 and prints "identified at least one change, exited with non-zero
// exit code" to stderr - that is the normal HasChanges outcome, not a warning
// about an incomplete diff.
func isDriftExit(stderr string) bool {
	return strings.Contains(stderr, "identified at least one change")
}
