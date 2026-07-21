package appbridge

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

const maxScaleReplicas = 10000

// ScaleWorkload sets the replica count on a Deployment or StatefulSet. replicas
// must be in [0, 10000]; negative or out-of-range values are rejected here
// before the network round-trip. kind must be Deployment or StatefulSet.
func (s *WorkloadsService) ScaleWorkload(cluster, kind, namespace, name string, replicas int) ActionResultDTO {
	if replicas < 0 || replicas > maxScaleReplicas {
		return ActionResultDTO{Error: fmt.Sprintf("replicas %d out of range [0, %d]", replicas, maxScaleReplicas)}
	}
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.ScaleWorkload(ctx, kind, namespace, name, int32(replicas)); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// RolloutHistory returns a workload's revision history, newest first.
// Read-only: no protected gating.
func (s *WorkloadsService) RolloutHistory(cluster, kind, namespace, name string) RolloutHistoryResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return RolloutHistoryResultDTO{Revisions: []RolloutRevisionDTO{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	revs, err := conn.RolloutHistory(ctx, kind, namespace, name)
	if err != nil {
		return RolloutHistoryResultDTO{Revisions: []RolloutRevisionDTO{}, Error: err.Error()}
	}
	out := RolloutHistoryResultDTO{Revisions: make([]RolloutRevisionDTO, 0, len(revs))}
	for _, r := range revs {
		images := r.Images
		if images == nil {
			images = []string{}
		}
		out.Revisions = append(out.Revisions, RolloutRevisionDTO{
			Revision: r.Revision, AgeSeconds: r.AgeSeconds, Images: images,
			Current: r.Current, ChangeCause: r.ChangeCause,
		})
	}
	return out
}

// RolloutUndo rolls a workload back to a prior revision. Destructive: consumes
// the protected-cluster grant, and the frontend confirm carries the
// GitOps-owned-will-be-reverted warning.
func (s *WorkloadsService) RolloutUndo(cluster, kind, namespace, name string, revision int) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.RolloutUndo(ctx, kind, namespace, name, int64(revision)); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// RolloutPause pauses/resumes a Deployment's rollouts (spec.paused). Reversible
// but still a spec mutation on a possibly-protected cluster, so it consumes the
// grant like restart/scale.
func (s *WorkloadsService) RolloutPause(cluster, namespace, name string, pause bool) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.RolloutPause(ctx, namespace, name, pause); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// RolloutRestart triggers a rolling restart for a workload by patching the
// pod-template restartedAt annotation. kind must be Deployment, StatefulSet,
// or DaemonSet.
func (s *WorkloadsService) RolloutRestart(cluster, kind, namespace, name string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.RolloutRestart(ctx, kind, namespace, name); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

const workloadsTimeout = 30 * time.Second

type WorkloadsConn interface {
	ListWorkloads(ctx context.Context, namespace string) ([]workloads.Workload, bool, bool, error)
	WorkloadMetrics(ctx context.Context, namespace string) (map[string]workloads.Usage, workloads.UsageStatus)
	WorkloadCompare(ctx context.Context, kind, ns, name string) (fleet.WorkloadCompareInfo, error)
	RolloutRestart(ctx context.Context, kind, namespace, name string) error
	RolloutHistory(ctx context.Context, kind, namespace, name string) ([]workloads.RolloutRevision, error)
	RolloutUndo(ctx context.Context, kind, namespace, name string, revision int64) error
	RolloutPause(ctx context.Context, namespace, name string, pause bool) error
	ScaleWorkload(ctx context.Context, kind, namespace, name string, replicas int32) error
	WatchDirty(ctx context.Context, namespace string, kinds []string, onDirty func(), onLive func(bool)) (stop func(), err error)
}

type WorkloadsService struct {
	lookup func(string) (WorkloadsConn, bool)
	em     Emitter
	live   *liveRegistry
}

func NewWorkloadsService(lookup func(string) (WorkloadsConn, bool), em Emitter) *WorkloadsService {
	return &WorkloadsService{lookup: lookup, em: em, live: newLiveRegistry()}
}

// ListWorkloads returns the health-ranked workloads for a cluster, scoped to
// namespace ("" = all). Namespaces is the sorted distinct set of workload
// namespaces, populated ONLY on the all-namespaces load (dropdown source).
func (s *WorkloadsService) ListWorkloads(cluster, namespace string) WorkloadsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return WorkloadsResultDTO{Namespaces: []string{}, Workloads: []WorkloadDTO{}}
	}
	out, _ := computeWorkloads(conn, namespace)
	return out
}

// computeWorkloads lists workloads on conn and builds the WorkloadsResultDTO
// with the same flux-present, namespace-set, and mapping rules ListWorkloads
// uses. ok=false means the list failed (returns non-nil empties); the live
// runner uses ok to gate emit/liveness.
func computeWorkloads(conn WorkloadsConn, namespace string) (WorkloadsResultDTO, bool) {
	out := WorkloadsResultDTO{Namespaces: []string{}, Workloads: []WorkloadDTO{}}
	ctx, cancel := context.WithTimeout(context.Background(), workloadsTimeout)
	defer cancel()
	wl, fluxPresent, truncated, err := conn.ListWorkloads(ctx, namespace)
	if err != nil {
		return out, false
	}
	out.FluxPresent = fluxPresent
	out.Truncated = truncated

	nsSet := map[string]bool{}
	for _, w := range wl {
		nsSet[w.Namespace] = true
		out.Workloads = append(out.Workloads, toWorkloadDTO(w))
	}
	if namespace == "" {
		for ns := range nsSet {
			out.Namespaces = append(out.Namespaces, ns)
		}
		sort.Strings(out.Namespaces)
	}
	return out, true
}

// workloadWatchKinds are the resource kinds a live workloads subscription
// watches: the controllers plus pods (a pod flip changes a workload's ready/
// reason without touching the controller object).
var workloadWatchKinds = []string{"pods", "deployments", "statefulsets", "daemonsets"}

// OpenLiveWorkloads starts (or replaces) a watch-backed live subscription for
// the cluster+namespace. It emits liveWorkloads:<cluster>:<ns>
// (WorkloadsResultDTO) on each debounced change and liveWorkloadsStatus:...
// ({live:bool}) on liveness edges. Cluster miss returns an error.
func (s *WorkloadsService) OpenLiveWorkloads(cluster, namespace string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	key := "workloads:" + cluster + ":" + namespace
	dataEvent := "liveWorkloads:" + cluster + ":" + namespace
	liveEvent := "liveWorkloadsStatus:" + cluster + ":" + namespace

	s.live.open(key,
		func(onDirty func(), onLive func(bool)) (func(), error) {
			return conn.WatchDirty(context.Background(), namespace, workloadWatchKinds, onDirty, onLive)
		},
		func() (any, bool) { return computeWorkloads(conn, namespace) },
		func(payload any) { s.em.Emit(dataEvent, payload) },
		func(live bool) { s.em.Emit(liveEvent, liveStatusDTO{Live: live}) },
	)
	return ActionResultDTO{OK: true}
}

// CloseLiveWorkloads stops the live subscription. Idempotent.
func (s *WorkloadsService) CloseLiveWorkloads(cluster, namespace string) {
	s.live.close("workloads:" + cluster + ":" + namespace)
}

// CloseAll stops every live workload subscription. Called on app shutdown.
func (s *WorkloadsService) CloseAll() { s.live.closeAll() }

func toWorkloadDTO(w workloads.Workload) WorkloadDTO {
	d := WorkloadDTO{
		Kind: w.Kind, Namespace: w.Namespace, Name: w.Name,
		Desired: w.Desired, Ready: w.Ready, Available: w.Available, Updated: w.Updated,
		Restarts: w.Restarts, Reason: w.Reason, Rank: w.Rank.String(),
		Pods:  make([]PodDTO, 0, len(w.Pods)),
		Stale: w.Generation > w.ObservedGeneration, Generation: w.Generation, ObservedGeneration: w.ObservedGeneration,
		Paused: w.Paused,
	}
	if w.GitOps != nil {
		d.GitOps = &OwnerDTO{Kind: w.GitOps.Kind, Namespace: w.GitOps.Namespace, Name: w.GitOps.Name}
	}
	for _, p := range w.Pods {
		d.Pods = append(d.Pods, PodDTO{Name: p.Name, Ready: p.Ready, Restarts: p.Restarts, Reason: p.Reason, Node: p.Node, AgeSeconds: p.AgeSeconds})
	}
	d.Resources = WorkloadResourcesDTO{
		CPU: ResourceCellDTO{Usage: w.Resources.CPU.Usage, Request: w.Resources.CPU.Request, Limit: w.Resources.CPU.Limit},
		Mem: ResourceCellDTO{Usage: w.Resources.Mem.Usage, Request: w.Resources.Mem.Request, Limit: w.Resources.Mem.Limit},
	}
	if w.Reloader != nil {
		// Default the slices to [] so the frontend never sees a null array.
		cms, secs := w.Reloader.ConfigMaps, w.Reloader.Secrets
		if cms == nil {
			cms = []string{}
		}
		if secs == nil {
			secs = []string{}
		}
		d.Reloader = &ReloadTargetsDTO{Auto: w.Reloader.Auto, ConfigMaps: cms, Secrets: secs}
	}
	return d
}

// WorkloadCompare returns one cluster's drift-relevant view of a workload for
// the fleet compare (images, replicas, resources, GitOps owner + resolved
// revision, spec hash). Read-only. Errors fold into the DTO so a degraded
// column renders as degraded, never as "not present".
func (s *WorkloadsService) WorkloadCompare(cluster, kind, namespace, name string) WorkloadCompareDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return WorkloadCompareDTO{Kind: kind, Namespace: namespace, Name: name, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), workloadsTimeout)
	defer cancel()
	info, err := conn.WorkloadCompare(ctx, kind, namespace, name)
	if err != nil {
		return WorkloadCompareDTO{Kind: kind, Namespace: namespace, Name: name, Error: err.Error()}
	}
	dto := WorkloadCompareDTO{
		Found: info.Found, Kind: info.Kind, Namespace: info.Namespace, Name: info.Name,
		DesiredReplicas: info.DesiredReplicas, ReadyReplicas: info.ReadyReplicas,
		SpecHash:  info.SpecHash,
		OwnerKind: info.OwnerKind, OwnerNamespace: info.OwnerNamespace, OwnerName: info.OwnerName,
		OwnerRevision: info.OwnerRevision,
		Containers:    []CompareContainerDTO{},
	}
	for _, ct := range info.Containers {
		dto.Containers = append(dto.Containers, CompareContainerDTO{Name: ct.Name, Image: ct.Image, Resources: ct.Resources})
	}
	return dto
}

// GetWorkloadMetrics returns live per-workload cpu/memory usage keyed by
// "<kind>/<ns>/<name>" plus a status. On-demand; the frontend polls it. Usage
// only - requests/limits already ship with ListWorkloads. Cluster miss / failure
// returns a non-nil empty map with an unavailable status (never panics on null).
func (s *WorkloadsService) GetWorkloadMetrics(cluster, namespace string) WorkloadMetricsResultDTO {
	empty := WorkloadMetricsResultDTO{Usage: map[string]WorkloadUsageDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		empty.Status = WorkloadMetricsStatusDTO{Available: false, Message: "cluster not connected"}
		return empty
	}
	ctx, cancel := context.WithTimeout(context.Background(), workloadsTimeout)
	defer cancel()
	usage, st := conn.WorkloadMetrics(ctx, namespace)
	out := make(map[string]WorkloadUsageDTO, len(usage))
	for k, u := range usage {
		out[k] = WorkloadUsageDTO{CPUUsage: u.CPU, MemUsage: u.Mem}
	}
	updatedAt := ""
	if !st.UpdatedAt.IsZero() {
		updatedAt = st.UpdatedAt.Format(time.RFC3339)
	}
	return WorkloadMetricsResultDTO{
		Status: WorkloadMetricsStatusDTO{Available: st.Available, Message: st.Message, UpdatedAt: updatedAt},
		Usage:  out,
	}
}
