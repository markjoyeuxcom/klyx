// Package helmcli is a pure adapter over the helm CLI. It shells out to the
// helm binary and parses the output. All functions accept a Runner so tests
// can inject a fake without spawning real processes.
package helmcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/clibin"
)

// Runner executes a helm invocation and returns stdout. The real runner shells
// out; tests fake it.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner is the production Runner that executes helm via exec.CommandContext.
// On a non-zero exit, the error message includes stderr so callers can surface
// it to the UI without a separate stderr read.
//
// Env holds extra environment entries (KEY=VALUE) appended to the inherited
// environment - the fleet layer sets KUBECONFIG here so helm resolves the same
// kubeconfig selection client-go used. Nil inherits the environment unchanged.
type ExecRunner struct {
	Env []string
}

// helmBin is the shared binary-resolution stack (env override -> PATH ->
// package-manager fallbacks). Tests swap its lookup hooks.
var helmBin = clibin.Binary{
	Name:          "helm",
	EnvVar:        "KLYX_HELM_PATH",
	FallbackPaths: defaultHelmFallbackPaths,
}

// Run executes helm with the given args. If helm exits non-zero it returns an
// error whose message includes the combined stderr text.
func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin, ok := Resolve()
	if !ok {
		bin = "helm"
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec
	if len(r.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("helm %s: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

// Detect reports whether the helm binary is available to Klyx.
func Detect() bool {
	return helmBin.Detect()
}

// Resolve returns the helm executable Klyx should use. macOS .app launches do
// not inherit the user's interactive shell PATH, so resolution also checks the
// usual Homebrew and package-manager locations (see internal/clibin).
func Resolve() (string, bool) {
	return helmBin.Resolve()
}

func defaultHelmFallbackPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/opt/homebrew/bin/helm",
			"/usr/local/bin/helm",
			"/opt/local/bin/helm",
		}
	case "linux":
		return []string{
			"/usr/local/bin/helm",
			"/usr/bin/helm",
			"/snap/bin/helm",
		}
	case "windows":
		var paths []string
		if root := strings.TrimSpace(os.Getenv("ChocolateyInstall")); root != "" {
			paths = append(paths, filepath.Join(root, "bin", "helm.exe"))
		}
		if root := strings.TrimSpace(os.Getenv("ProgramData")); root != "" {
			paths = append(paths, filepath.Join(root, "chocolatey", "bin", "helm.exe"))
		}
		if root := strings.TrimSpace(os.Getenv("ProgramFiles")); root != "" {
			paths = append(paths, filepath.Join(root, "Helm", "bin", "helm.exe"))
		}
		return paths
	default:
		return nil
	}
}

// Release is one entry from `helm list -A -o json`.
type Release struct {
	Name        string
	Namespace   string
	Chart       string // verbatim, e.g. "nginx-1.2.3"
	AppVersion  string
	Status      string
	Revision    int
	UpdatedUnix int64 // seconds since epoch; 0 if unparseable
}

// helmListEntry is the raw shape helm v4 emits. Revision can be a JSON number
// or a quoted string depending on the helm version; we handle both via a
// custom unmarshaler below.
type helmListEntry struct {
	Name       string       `json:"name"`
	Namespace  string       `json:"namespace"`
	Revision   jsonRevision `json:"revision"`
	Updated    string       `json:"updated"`
	Status     string       `json:"status"`
	Chart      string       `json:"chart"`
	AppVersion string       `json:"app_version"`
}

// jsonRevision handles helm's revision field, which is an integer in current
// versions but appeared as a quoted string in older builds.
type jsonRevision int

func (r *jsonRevision) UnmarshalJSON(b []byte) error {
	// Unquoted number (common case).
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*r = jsonRevision(n)
		return nil
	}
	// Quoted string fallback.
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("helmcli: unmarshal revision %q: %w", b, err)
	}
	var n2 int
	if _, err := fmt.Sscan(s, &n2); err != nil {
		return fmt.Errorf("helmcli: parse revision string %q: %w", s, err)
	}
	*r = jsonRevision(n2)
	return nil
}

// parseUpdated tries multiple timestamp formats helm has used across versions.
// Returns 0 on failure - we never invent a timestamp.
func parseUpdated(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// helm v3.8+ / v4: RFC3339 with optional nanoseconds.
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	// helm v3 legacy: "2006-01-02 15:04:05.999999999 -0700 MST"
	if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s); err == nil {
		return t.Unix()
	}
	// Shorter legacy variant without fractional seconds.
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s); err == nil {
		return t.Unix()
	}
	return 0
}

// ListReleases returns all releases across all namespaces for the given
// kubeContext. `--max 0` lifts helm's default cap of 256 releases, which would
// otherwise silently truncate the list on large clusters.
func ListReleases(ctx context.Context, r Runner, kubeContext string) ([]Release, error) {
	out, err := r.Run(ctx, "list", "-A", "-o", "json", "--max", "0", "--kube-context", kubeContext)
	if err != nil {
		return nil, err
	}

	var entries []helmListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("helmcli: parse list output: %w", err)
	}

	releases := make([]Release, 0, len(entries))
	for _, e := range entries {
		releases = append(releases, Release{
			Name:        e.Name,
			Namespace:   e.Namespace,
			Chart:       e.Chart,
			AppVersion:  e.AppVersion,
			Status:      e.Status,
			Revision:    int(e.Revision),
			UpdatedUnix: parseUpdated(e.Updated),
		})
	}
	return releases, nil
}

// HistoryEntry is one row from `helm history <release> -o json`.
type HistoryEntry struct {
	Revision    int
	Status      string
	Chart       string
	AppVersion  string
	Description string
	UpdatedUnix int64
}

// helmHistoryEntry is the raw JSON shape from `helm history`.
type helmHistoryEntry struct {
	Revision    jsonRevision `json:"revision"`
	Updated     string       `json:"updated"`
	Status      string       `json:"status"`
	Chart       string       `json:"chart"`
	AppVersion  string       `json:"app_version"`
	Description string       `json:"description"`
}

// History returns the revision history for a release, sorted newest-first
// (descending revision number).
func History(ctx context.Context, r Runner, kubeContext, namespace, release string) ([]HistoryEntry, error) {
	out, err := r.Run(ctx, "history", release, "-n", namespace, "-o", "json", "--kube-context", kubeContext)
	if err != nil {
		return nil, err
	}

	var raw []helmHistoryEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("helmcli: parse history output: %w", err)
	}

	entries := make([]HistoryEntry, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, HistoryEntry{
			Revision:    int(e.Revision),
			Status:      e.Status,
			Chart:       e.Chart,
			AppVersion:  e.AppVersion,
			Description: e.Description,
			UpdatedUnix: parseUpdated(e.Updated),
		})
	}

	// Newest first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Revision > entries[j].Revision
	})
	return entries, nil
}

// GetValues returns the user-supplied values for a release as a YAML string.
// When helm reports "null\n" (no user values were set), GetValues returns ""
// so callers can render a clean "no user values" state.
func GetValues(ctx context.Context, r Runner, kubeContext, namespace, release string) (string, error) {
	out, err := r.Run(ctx, "get", "values", release, "-n", namespace, "-o", "yaml", "--kube-context", kubeContext)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "null" {
		return "", nil
	}
	return s, nil
}

// Rollback rolls back a release to the specified revision. It passes --wait
// and --timeout 120s matching the kubectl drain pattern used in DrainNodeCmd.
// RollbackOptions are the user-composed helm rollback flags (previously
// hardcoded to --wait --timeout 120s).
type RollbackOptions struct {
	Wait           bool // wait for resources to become ready before returning
	NoHooks        bool // skip pre/post-rollback hooks
	TimeoutSeconds int  // 0 -> 120s default; only meaningful with Wait
}

func Rollback(ctx context.Context, r Runner, kubeContext, namespace, release string, revision int, opts RollbackOptions) error {
	timeout := opts.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	args := []string{
		"rollback", release, fmt.Sprintf("%d", revision),
		"-n", namespace,
		"--kube-context", kubeContext,
	}
	if opts.Wait {
		args = append(args, "--wait", "--timeout", fmt.Sprintf("%ds", timeout))
	}
	if opts.NoHooks {
		args = append(args, "--no-hooks")
	}
	_, err := r.Run(ctx, args...)
	return err
}
