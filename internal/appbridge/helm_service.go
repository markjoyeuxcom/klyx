package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/helmcli"
)

// DetectFunc is the seam for helm binary detection. Tests replace it with a
// stub that returns false (binary absent) or true (present) without depending
// on the host environment. Production leaves it as helmcli.Detect.
var DetectFunc = helmcli.Detect

// HelmConn is the per-cluster surface HelmService needs.
type HelmConn interface {
	HelmReleases(ctx context.Context) ([]helmcli.Release, error)
	HelmHistory(ctx context.Context, namespace, release string) ([]helmcli.HistoryEntry, error)
	HelmValues(ctx context.Context, namespace, release string) (string, error)
	HelmRollback(ctx context.Context, namespace, release string, revision int, opts helmcli.RollbackOptions) error
}

// HelmService is bound to JS. It provides read-only release inspection and
// rollback; install/uninstall are intentionally excluded (Git owns desired state).
type HelmService struct {
	lookup func(string) (HelmConn, bool)
}

// NewHelmService creates a HelmService with the given cluster-lookup function.
func NewHelmService(lookup func(string) (HelmConn, bool)) *HelmService {
	return &HelmService{lookup: lookup}
}

// ListHelmReleases returns all Helm releases for a cluster. If the helm binary
// is absent or the cluster is not connected, Available is false and Message
// explains why. This mirrors the capability-gated degradation pattern used
// elsewhere (e.g. MetricsService).
func (s *HelmService) ListHelmReleases(cluster string) HelmReleasesResultDTO {
	if !DetectFunc() {
		return HelmReleasesResultDTO{
			Available: false,
			Message:   "helm not found",
			Releases:  []HelmReleaseDTO{},
		}
	}

	conn, ok := s.lookup(cluster)
	if !ok {
		return HelmReleasesResultDTO{
			Available: false,
			Message:   "cluster not connected: " + cluster,
			Releases:  []HelmReleaseDTO{},
			Error:     "cluster not connected: " + cluster,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), helmTimeout)
	defer cancel()

	releases, err := conn.HelmReleases(ctx)
	if err != nil {
		return HelmReleasesResultDTO{
			Available: false,
			Message:   err.Error(),
			Releases:  []HelmReleaseDTO{},
			Error:     err.Error(),
		}
	}

	dtos := make([]HelmReleaseDTO, 0, len(releases))
	for _, r := range releases {
		dtos = append(dtos, HelmReleaseDTO{
			Name:        r.Name,
			Namespace:   r.Namespace,
			Chart:       r.Chart,
			AppVersion:  r.AppVersion,
			Status:      r.Status,
			Revision:    r.Revision,
			UpdatedUnix: r.UpdatedUnix,
		})
	}
	return HelmReleasesResultDTO{
		Available: true,
		Releases:  dtos,
	}
}

// GetHelmHistory returns the revision history for a named release.
func (s *HelmService) GetHelmHistory(cluster, namespace, release string) HelmHistoryResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return HelmHistoryResultDTO{
			History: []HelmHistoryEntryDTO{},
			Error:   "cluster not connected: " + cluster,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), helmTimeout)
	defer cancel()

	entries, err := conn.HelmHistory(ctx, namespace, release)
	if err != nil {
		return HelmHistoryResultDTO{
			History: []HelmHistoryEntryDTO{},
			Error:   err.Error(),
		}
	}

	dtos := make([]HelmHistoryEntryDTO, 0, len(entries))
	for _, e := range entries {
		dtos = append(dtos, HelmHistoryEntryDTO{
			Revision:    e.Revision,
			Status:      e.Status,
			Chart:       e.Chart,
			AppVersion:  e.AppVersion,
			Description: e.Description,
			UpdatedUnix: e.UpdatedUnix,
		})
	}
	return HelmHistoryResultDTO{History: dtos}
}

// GetHelmValues returns the user-supplied values for a release as a MASKED YAML
// string: secret-shaped leaves (password/token/*Key/credential ...) are redacted
// before the values leave the backend, so no secret reaches the UI or the
// clipboard. The redacted paths are returned so the frontend can offer per-value
// reveal (RevealHelmValue). On a parse failure it FAILS CLOSED - an error, never
// the raw document.
func (s *HelmService) GetHelmValues(cluster, namespace, release string) HelmValuesResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return HelmValuesResultDTO{Error: "cluster not connected: " + cluster}
	}

	ctx, cancel := context.WithTimeout(context.Background(), helmTimeout)
	defer cancel()

	values, err := conn.HelmValues(ctx, namespace, release)
	if err != nil {
		return HelmValuesResultDTO{Error: err.Error()}
	}
	masked, paths, err := helmcli.MaskValues(values)
	if err != nil {
		// Never fall back to the raw (unmasked) document.
		return HelmValuesResultDTO{Error: "could not render values safely: " + err.Error()}
	}
	return HelmValuesResultDTO{Values: masked, MaskedPaths: paths}
}

// RevealHelmValue fetches a single unmasked helm value on demand - the reveal
// affordance next to a redacted key. Mirroring CRDService.RevealSecretKey, it
// re-fetches the release values (they are never held in memory) and returns the
// scalar at path; the value is returned only on explicit request and is never
// logged.
func (s *HelmService) RevealHelmValue(cluster, namespace, release, path string) RevealResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return RevealResultDTO{Error: "cluster not connected: " + cluster}
	}

	ctx, cancel := context.WithTimeout(context.Background(), helmTimeout)
	defer cancel()

	values, err := conn.HelmValues(ctx, namespace, release)
	if err != nil {
		return RevealResultDTO{Error: err.Error()}
	}
	v, found, err := helmcli.ValueAtPath(values, path)
	if err != nil {
		return RevealResultDTO{Error: "could not read value: " + err.Error()}
	}
	if !found {
		return RevealResultDTO{Error: "no value at " + path}
	}
	return RevealResultDTO{Value: v}
}

// RollbackOptionsDTO carries the user-composed rollback flags from the
// rollback composer dialog.
type RollbackOptionsDTO struct {
	Wait           bool `json:"wait"`
	NoHooks        bool `json:"noHooks"`
	TimeoutSeconds int  `json:"timeoutSeconds"`
}

// HelmRollback rolls back a release to the specified revision.
func (s *HelmService) HelmRollback(cluster, namespace, release string, revision int, opts RollbackOptionsDTO) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}

	ctx, cancel := context.WithTimeout(context.Background(), helmRollbackTimeout)
	defer cancel()

	if err := conn.HelmRollback(ctx, namespace, release, revision, helmcli.RollbackOptions{Wait: opts.Wait, NoHooks: opts.NoHooks, TimeoutSeconds: opts.TimeoutSeconds}); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// helmTimeout is the default timeout for read-only helm operations. Helm
// queries talk to the cluster's Secrets store (release metadata) so they need
// a real network budget.
const helmTimeout = actionTimeout // 30s, same as other read ops

// helmRollbackTimeout is longer because rollback also waits for pods to be
// ready (--wait --timeout 120s). Give the outer context headroom beyond that.
const helmRollbackTimeout = 150 * time.Second
