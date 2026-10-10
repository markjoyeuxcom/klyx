package appbridge

import (
	"context"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

const podsTimeout = 30 * time.Second

// PodsConn is the per-cluster surface PodsService needs. No fleet import
// in the interface itself — the lookup closure bridges to the real fleet.Conn.
type PodsConn interface {
	ListPods(ctx context.Context, namespace string) ([]workloads.PodSummary, bool, error)
	LaunchDiagnosticPod(ctx context.Context, name, image string, ttlSeconds int64) fleet.AdhocResult
	PodDetail(ctx context.Context, namespace, name string) (fleet.PodDetail, error)
	DeletePod(ctx context.Context, namespace, name string) error
	WatchDirty(ctx context.Context, namespace string, kinds []string, onDirty func(), onLive func(bool)) (stop func(), err error)
}

// PodsService is bound to JS. ListPods/GetPodDetail are request/response;
// OpenLivePods/CloseLivePods drive a watch-backed live subscription that emits
// livePods:<cluster>:<ns> on change.
type PodsService struct {
	lookup func(string) (PodsConn, bool)
	em     Emitter
	live   *liveRegistry
}

// NewPodsService creates a PodsService with the given cluster-lookup function
// and event emitter (for the live-list subscription).
func NewPodsService(lookup func(string) (PodsConn, bool), em Emitter) *PodsService {
	return &PodsService{lookup: lookup, em: em, live: newLiveRegistry()}
}

// ListPods returns health-ranked pod rows for a cluster, scoped to namespace
// ("" = all). Namespaces is the sorted distinct set, populated ONLY on the
// all-namespaces load (dropdown source). Cluster miss returns non-nil empties.
func (s *PodsService) ListPods(cluster, namespace string) PodsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return PodsResultDTO{Namespaces: []string{}, Pods: []PodSummaryDTO{}, Error: "cluster not connected: " + cluster}
	}
	out, _ := computePods(conn, namespace)
	return out
}

// computePods lists pods on conn and builds the PodsResultDTO with the same
// namespace-set and mapping rules ListPods uses. ok=false means the list failed
// (returns non-nil empties); the live runner uses ok to gate emit/liveness.
func computePods(conn PodsConn, namespace string) (PodsResultDTO, bool) {
	out := PodsResultDTO{Namespaces: []string{}, Pods: []PodSummaryDTO{}}
	ctx, cancel := context.WithTimeout(context.Background(), podsTimeout)
	defer cancel()
	pods, truncated, err := conn.ListPods(ctx, namespace)
	if err != nil {
		out.Error = err.Error()
		return out, false
	}
	out.Truncated = truncated

	nsSet := map[string]bool{}
	for _, p := range pods {
		nsSet[p.Namespace] = true
		out.Pods = append(out.Pods, toPodSummaryDTO(p))
	}
	if namespace == "" {
		for ns := range nsSet {
			out.Namespaces = append(out.Namespaces, ns)
		}
		sort.Strings(out.Namespaces)
	}
	return out, true
}

// OpenLivePods starts (or replaces) a watch-backed live subscription for the
// cluster+namespace. It emits livePods:<cluster>:<ns> (PodsResultDTO) on each
// debounced change and livePodsStatus:<cluster>:<ns> ({live:bool}) on liveness
// edges. Cluster miss returns an error and starts nothing.
func (s *PodsService) OpenLivePods(cluster, namespace string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	key := "pods:" + cluster + ":" + namespace
	dataEvent := "livePods:" + cluster + ":" + namespace
	liveEvent := "livePodsStatus:" + cluster + ":" + namespace

	s.live.open(key,
		func(onDirty func(), onLive func(bool)) (func(), error) {
			ctx := context.Background()
			return conn.WatchDirty(ctx, namespace, []string{"pods"}, onDirty, onLive)
		},
		func() (any, bool) { return computePods(conn, namespace) },
		func(payload any) { s.em.Emit(dataEvent, payload) },
		func(live bool) { s.em.Emit(liveEvent, liveStatusDTO{Live: live}) },
	)
	return ActionResultDTO{OK: true}
}

// CloseLivePods stops the live subscription for the cluster+namespace.
// Idempotent.
func (s *PodsService) CloseLivePods(cluster, namespace string) {
	s.live.close("pods:" + cluster + ":" + namespace)
}

// CloseAll stops every live pod subscription. Called on app shutdown.
func (s *PodsService) CloseAll() { s.live.closeAll() }

// GetPodDetail returns the full pod detail. Cluster miss or error returns a
// zero DTO with empty-but-non-nil collections (never panics on null).
func (s *PodsService) GetPodDetail(cluster, namespace, name string) PodDetailDTO {
	empty := PodDetailDTO{
		Labels:      map[string]string{},
		Annotations: map[string]string{},
		Conditions:  []ConditionDTO{},
		Events:      []EventDTO{},
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		empty.Error = "cluster not connected: " + cluster
		return empty
	}
	ctx, cancel := context.WithTimeout(context.Background(), podsTimeout)
	defer cancel()
	d, err := conn.PodDetail(ctx, namespace, name)
	if err != nil {
		empty.Error = err.Error()
		return empty
	}

	labels := d.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := d.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	conds := make([]ConditionDTO, 0, len(d.Conditions))
	for _, c := range d.Conditions {
		conds = append(conds, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message})
	}
	events := make([]EventDTO, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, EventDTO{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: rfc3339(e.Last)})
	}
	return PodDetailDTO{
		Summary:        toPodSummaryDTO(d.Summary),
		Labels:         labels,
		Annotations:    annotations,
		Conditions:     conds,
		Events:         events,
		YAML:           d.YAML,
		QosClass:       d.QoSClass,
		ServiceAccount: d.ServiceAccount,
	}
}

// DeletePod deletes a single pod via the cluster connection. The owning
// controller recreates it; this is the standard imperative bounce.
// LaunchDiagnosticPod creates a TTL-bounded scratch debugging pod in the
// klyx-scratch namespace (created on first use). A create is a mutation, so it
// consumes the protected-cluster grant like every destructive verb.
func (s *PodsService) LaunchDiagnosticPod(cluster, name, image string, ttlSeconds int64) AdhocResultDTO {
	if err := checkProtected(cluster); err != nil {
		return AdhocResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return AdhocResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	r := conn.LaunchDiagnosticPod(ctx, name, image, ttlSeconds)
	return AdhocResultDTO{Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, OK: r.OK, Error: r.Error}
}

func (s *PodsService) DeletePod(cluster, namespace, name string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.DeletePod(ctx, namespace, name); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

func toPodSummaryDTO(p workloads.PodSummary) PodSummaryDTO {
	containers := make([]ContainerSummaryDTO, 0, len(p.Containers))
	for _, c := range p.Containers {
		ports := make([]ContainerPortDTO, 0, len(c.Ports))
		for _, pt := range c.Ports {
			ports = append(ports, ContainerPortDTO{Name: pt.Name, Port: pt.Port, Protocol: pt.Protocol})
		}
		containers = append(containers, ContainerSummaryDTO{
			Name:             c.Name,
			Image:            c.Image,
			Ready:            c.Ready,
			Restarts:         c.Restarts,
			State:            c.State,
			Init:             c.Init,
			Ports:            ports,
			LastReason:       c.LastReason,
			LastExitCode:     c.LastExitCode,
			LastFinishedUnix: c.LastFinishedUnix,
			CPURequest:       c.CPURequest,
			CPULimit:         c.CPULimit,
			MemRequest:       c.MemRequest,
			MemLimit:         c.MemLimit,
		})
	}
	return PodSummaryDTO{
		Namespace:  p.Namespace,
		Name:       p.Name,
		Ready:      p.Ready,
		Phase:      p.Phase,
		Reason:     p.Reason,
		Rank:       p.Rank.String(),
		Restarts:   p.Restarts,
		Node:       p.Node,
		IP:         p.IP,
		OwnerKind:  p.OwnerKind,
		OwnerName:  p.OwnerName,
		AgeSeconds: p.AgeSeconds,
		Containers: containers,
		Health:     toHealthDTO(p.Health),
	}
}
