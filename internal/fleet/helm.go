package fleet

import (
	"context"
	"errors"
	"fmt"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/observe"
)

// newHelmRunner builds the helm Runner for one call, carrying the cluster's
// subprocess environment (its KUBECONFIG selection). It is the package-level
// seam: tests replace it to capture the env and return a fake.
var newHelmRunner = func(env []string) helmcli.Runner { return helmcli.ExecRunner{Env: env} }

// helmDetect is the health snapshot's helm CLI check (a test seam).
var helmDetect = helmcli.Detect

// HelmReleases returns all Helm releases across all namespaces for this
// cluster. Requires a non-empty kubeContext.
// HelmReleases is the Helm lens's read (on open, on Refresh, and the re-read
// after a rollback). It is the health snapshot's own read: every helm list
// of this cluster - the 5m cadence included - is one single-flight read,
// applied in order, and a failure here opens the summary's freshness gap
// like any other (Observer.ReadSnapshot). Without a running Observer it
// reads directly.
func (c *ClusterConn) HelmReleases(ctx context.Context) ([]helmcli.Release, error) {
	c.mu.RLock()
	h := c.health
	c.mu.RUnlock()
	if h != nil {
		r, err := h.ReadSnapshot(ctx, healthsrc.SnapHelm)
		switch {
		case errors.Is(err, observe.ErrSnapshotNotRunning):
		case err != nil:
			return nil, err
		case r.Err != nil:
			return nil, r.Err
		default:
			return healthsrc.ReleasesOf(r.Items), nil
		}
	}
	return c.listHelm(ctx)
}

// listHelm runs helm list for this cluster: the health snapshot's Read.
func (c *ClusterConn) listHelm(ctx context.Context) ([]helmcli.Release, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("HelmReleases: kubeContext not set on conn %q", c.name)
	}
	return helmcli.ListReleases(ctx, newHelmRunner(c.cliEnv()), c.kubeContext)
}

// HelmHistory returns the revision history for a release, newest-first.
func (c *ClusterConn) HelmHistory(ctx context.Context, namespace, release string) ([]helmcli.HistoryEntry, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("HelmHistory: kubeContext not set on conn %q", c.name)
	}
	if err := validateHelmNames(namespace, release); err != nil {
		return nil, err
	}
	return helmcli.History(ctx, newHelmRunner(c.cliEnv()), c.kubeContext, namespace, release)
}

// HelmValues returns the user-supplied values for a release as a YAML string.
// An empty string means no user values were set (helm reported "null").
func (c *ClusterConn) HelmValues(ctx context.Context, namespace, release string) (string, error) {
	if c.kubeContext == "" {
		return "", fmt.Errorf("HelmValues: kubeContext not set on conn %q", c.name)
	}
	if err := validateHelmNames(namespace, release); err != nil {
		return "", err
	}
	return helmcli.GetValues(ctx, newHelmRunner(c.cliEnv()), c.kubeContext, namespace, release)
}

// HelmRollback rolls back a release to the given revision with the
// user-composed options.
func (c *ClusterConn) HelmRollback(ctx context.Context, namespace, release string, revision int, opts helmcli.RollbackOptions) error {
	if c.kubeContext == "" {
		return fmt.Errorf("HelmRollback: kubeContext not set on conn %q", c.name)
	}
	if err := validateHelmNames(namespace, release); err != nil {
		return err
	}
	return helmcli.Rollback(ctx, newHelmRunner(c.cliEnv()), c.kubeContext, namespace, release, revision, opts)
}

// validateHelmNames guards the helm CLI argv: namespace and release names come
// from the webview and are spliced into a command line. Helm release names are
// a strict subset of DNS-1123 subdomains, so real inputs always pass.
func validateHelmNames(namespace, release string) error {
	if err := validateK8sName("namespace", namespace); err != nil {
		return err
	}
	return validateK8sName("release", release)
}
