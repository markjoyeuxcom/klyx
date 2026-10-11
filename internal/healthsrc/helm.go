package healthsrc

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/observe"
)

// The Helm task (1c spec §1.1): never live. `helm list -A --max 0` every
// HelmCadence, single-flight, bounded by HelmReadTimeout. The Helm lens's
// reads - on open, on Refresh, and the re-read after a Klyx rollback - are
// requests for this snapshot's own read (Observer.ReadSnapshot): one helm
// list at a time per cluster, applied in order, shared by the lens and the
// summary.
const (
	SnapHelm = "helm"
	TaskHelm = "helm"

	HelmCadence     = 5 * time.Minute
	HelmReadTimeout = 60 * time.Second
)

// HelmDeps are what the Helm snapshot needs. List is the cluster's own
// `helm list` (its kubeContext and per-cluster kubeconfig).
type HelmDeps struct {
	Cluster string
	// Detect reports whether the helm CLI resolves. A missing binary is
	// unreadable with class cliMissing, never absent: Klyx cannot tell.
	Detect func() bool
	List   func(ctx context.Context) ([]helmcli.Release, error)
}

// Helm returns the Helm snapshot and task. Output carries no values.
func Helm(d HelmDeps) ([]observe.SnapshotSpec, []observe.DomainSpec) {
	snap := observe.SnapshotSpec{Name: SnapHelm, Scope: "all namespaces", Cadence: HelmCadence, Timeout: HelmReadTimeout,
		Read: func(ctx context.Context) ([]any, bool, error) {
			if !d.Detect() {
				return nil, false, fmt.Errorf("helm CLI not found - install helm or set KLYX_HELM_PATH: %w", exec.ErrNotFound)
			}
			rs, err := d.List(ctx)
			if err != nil {
				return nil, false, err
			}
			return HelmItems(rs), false, nil
		}}
	domains := []observe.DomainSpec{{ID: TaskHelm, Parts: []observe.PartSpec{
		{ID: "releases", Entity: "Helm releases", Scope: "all namespaces", Classifier: "native",
			Counted: []string{SnapHelm}, Evaluate: evalReleases(d.Cluster)},
	}}}
	return []observe.SnapshotSpec{snap}, domains
}

// HelmItems is the snapshot's form of a release list: a sorted copy, so an
// unchanged list is an unchanged snapshot.
func HelmItems(rs []helmcli.Release) []any {
	sorted := append([]helmcli.Release(nil), rs...)
	sort.Slice(sorted, func(a, b int) bool {
		if sorted[a].Namespace != sorted[b].Namespace {
			return sorted[a].Namespace < sorted[b].Namespace
		}
		return sorted[a].Name < sorted[b].Name
	})
	items := make([]any, len(sorted))
	for i := range sorted {
		items[i] = sorted[i]
	}
	return items
}

// ReleasesOf turns a snapshot read's items back into releases.
func ReleasesOf(items []any) []helmcli.Release {
	out := make([]helmcli.Release, 0, len(items))
	for _, o := range items {
		out = append(out, o.(helmcli.Release))
	}
	return out
}

// evalReleases applies the relocated lens rule at the capture's time, so a
// pending release crosses its deadline on the Observer's reclassification
// timer (NativeEval.Next) with no new read. A release has no single object
// UID: its identity is namespace/name, and its ref has no GVR (the CLI).
func evalReleases(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []helmcli.Release
		for _, o := range c.Objects[SnapHelm] {
			r := o.(helmcli.Release)
			items = append(items, nativeItem{helmcli.ReleaseRule(r, c.Now), refInput{ns: r.Namespace, name: r.Name}})
			rows = append(rows, r)
		}
		res := nativeResult(cluster, TaskHelm, "failed or appear stuck", items)
		if c.Rows {
			res.Rows = rows
		}
		return res
	}
}
