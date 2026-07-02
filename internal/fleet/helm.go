package fleet

import (
	"context"
	"fmt"

	"github.com/moomora/klyx/internal/helmcli"
)

// helmRunner is the package-level Runner seam. Tests replace it with a fake;
// production leaves it as the exec-based runner.
var helmRunner helmcli.Runner = helmcli.ExecRunner{}

// HelmReleases returns all Helm releases across all namespaces for this
// cluster. Requires a non-empty kubeContext.
func (c *ClusterConn) HelmReleases(ctx context.Context) ([]helmcli.Release, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("HelmReleases: kubeContext not set on conn %q", c.name)
	}
	return helmcli.ListReleases(ctx, helmRunner, c.kubeContext)
}

// HelmHistory returns the revision history for a release, newest-first.
func (c *ClusterConn) HelmHistory(ctx context.Context, namespace, release string) ([]helmcli.HistoryEntry, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("HelmHistory: kubeContext not set on conn %q", c.name)
	}
	if err := validateHelmNames(namespace, release); err != nil {
		return nil, err
	}
	return helmcli.History(ctx, helmRunner, c.kubeContext, namespace, release)
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
	return helmcli.GetValues(ctx, helmRunner, c.kubeContext, namespace, release)
}

// HelmRollback rolls back a release to the given revision.
func (c *ClusterConn) HelmRollback(ctx context.Context, namespace, release string, revision int) error {
	if c.kubeContext == "" {
		return fmt.Errorf("HelmRollback: kubeContext not set on conn %q", c.name)
	}
	if err := validateHelmNames(namespace, release); err != nil {
		return err
	}
	return helmcli.Rollback(ctx, helmRunner, c.kubeContext, namespace, release, revision)
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
