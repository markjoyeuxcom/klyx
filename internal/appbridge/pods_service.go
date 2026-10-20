package appbridge

import (
	"context"
	"github.com/moomora/klyx/internal/urlscrub"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

const podsTimeout = 30 * time.Second

// PodsConn is the per-cluster surface PodsService needs. No fleet import
// in the interface itself — the lookup closure bridges to the real fleet.Conn.
type PodsConn interface {
	LaunchDiagnosticPod(ctx context.Context, name, image string, ttlSeconds int64) fleet.AdhocResult
	PodDetail(ctx context.Context, namespace, name string) (fleet.PodDetail, error)
	DeletePod(ctx context.Context, namespace, name, uid string) error
}

// PodsService is bound to JS: pod detail, the pod verbs and the diagnostic
// pod. The Pods lens reads the Observer's rows (1c slice 3 §5.9).
type PodsService struct {
	lookup func(string) (PodsConn, bool)
	now    func() time.Time
}

// NewPodsService creates a PodsService with the given cluster-lookup function.
func NewPodsService(lookup func(string) (PodsConn, bool)) *PodsService {
	return &PodsService{lookup: lookup, now: time.Now}
}

// GetPodDetail returns the full pod detail. Cluster miss or error returns a
// zero DTO with empty-but-non-nil collections (never panics on null).
func (s *PodsService) GetPodDetail(cluster, namespace, name string) PodDetailDTO {
	empty := PodDetailDTO{
		Labels:      map[string]string{},
		Annotations: map[string]string{},
		Conditions:  []ConditionDTO{},
		Events:      []EventDTO{},
		Extended:    ExtendedPodDTO{Containers: []ExtendedContainerDTO{}},
		Claims:      []PodClaimRefDTO{}, DeviceHealth: []DeviceHealthDTO{},
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
		events = append(events, EventDTO{Type: e.Type, Reason: e.Reason, Message: urlscrub.Text(e.Message).Text, Count: int(e.Count), LastSeen: rfc3339(e.Last)})
	}
	resources, eph := toS6Detail(d.Resources, d.Ephemeral)
	summary := toPodSummaryDTO(d.Summary)
	summary.Sched.Assumption = d.SchedAssumption
	return PodDetailDTO{
		Summary:        summary,
		Resources:      resources,
		Ephemeral:      eph,
		EphemeralMore:  d.EphemeralMore,
		Extended:       toExtendedPodDTO(d.Extended),
		Claims:         toPodClaimsDTO(d.Claims),
		DeviceHealth:   toDeviceHealthDTO(d.DeviceHealth),
		Labels:         labels,
		Annotations:    annotations,
		Conditions:     conds,
		Events:         events,
		YAML:           d.YAML,
		QosClass:       d.QoSClass,
		ServiceAccount: d.ServiceAccount,
		AsOfUnix:       s.now().Unix(),
	}
}

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

// DeletePod deletes the pod OBJECT the verb was armed against (uid, design
// §5b) - never a same-name replacement. The owning controller recreates it;
// this is the standard imperative bounce.
func (s *PodsService) DeletePod(cluster, namespace, name, uid string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.DeletePod(ctx, namespace, name, uid); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// toPodRowDTO is the bounded rows-transport form of a pod (addendum §3.2).
func toPodRowDTO(p workloads.PodSummary) PodRowDTO {
	containers := make([]PodRowContainerDTO, 0, len(p.Containers))
	for _, c := range p.Containers {
		containers = append(containers, PodRowContainerDTO{Name: c.Name, Init: c.Init, Ready: c.Ready, Restarts: c.Restarts, State: c.State})
	}
	h := toHealthDTO(p.Health)
	h.Message = "" // read from the detail
	return PodRowDTO{
		Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), Generation: p.Generation,
		OwnerKind: p.OwnerKind, OwnerName: p.OwnerName, OwnerUID: string(p.OwnerUID),
		Node: p.Node, IP: p.IP, Ready: p.Ready, Restarts: p.Restarts, Created: unixOrZero(p.Created),
		Health: h, Containers: containers,
		ObservedGeneration: p.ObservedGeneration, Resize: toResizeDTO(p.Resize), EvidenceRev: p.EvidenceRev,
	}
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
		Namespace:          p.Namespace,
		Name:               p.Name,
		UID:                string(p.UID),
		Ready:              p.Ready,
		Phase:              p.Phase,
		Reason:             p.Reason,
		Rank:               p.Rank.String(),
		Restarts:           p.Restarts,
		Node:               p.Node,
		IP:                 p.IP,
		OwnerKind:          p.OwnerKind,
		OwnerName:          p.OwnerName,
		OwnerUID:           string(p.OwnerUID),
		Generation:         p.Generation,
		Created:            unixOrZero(p.Created),
		AgeSeconds:         p.AgeSeconds,
		Containers:         containers,
		Health:             toHealthDTO(p.Health),
		ObservedGeneration: p.ObservedGeneration,
		Resize:             toResizeDTO(p.Resize),
		Sched: SchedDTO{CPU: p.SchedCPU, Mem: p.SchedMem, DesiredCPU: p.DesiredCPU, DesiredMem: p.DesiredMem,
			AltCPU: p.SchedAltCPU, AltMem: p.SchedAltMem},
	}
}
