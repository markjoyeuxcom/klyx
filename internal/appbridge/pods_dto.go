package appbridge

import (
	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/extres"
	"github.com/moomora/klyx/internal/fleet"
)

// ContainerSummaryDTO is a single container within a pod row.
type ContainerSummaryDTO struct {
	Name     string             `json:"name"`
	Image    string             `json:"image"`
	Ready    bool               `json:"ready"`
	Restarts int                `json:"restarts"`
	State    string             `json:"state"` // "running" | "waiting:<Reason>" | "terminated:<Reason>" | ""
	Init     bool               `json:"init"`
	Ports    []ContainerPortDTO `json:"ports"`
	// Last termination state - zero values when the container never terminated.
	LastReason       string `json:"lastReason"` // e.g. "OOMKilled"
	LastExitCode     int    `json:"lastExitCode"`
	LastFinishedUnix int64  `json:"lastFinishedUnix"` // FinishedAt.Unix(), 0 when absent
	// Declared resources (cpu cores, memory bytes); null = unset on the spec.
	CPURequest *float64 `json:"cpuRequest"`
	CPULimit   *float64 `json:"cpuLimit"`
	MemRequest *float64 `json:"memRequest"`
	MemLimit   *float64 `json:"memLimit"`
}

// ContainerPortDTO is one declared container port - the forward popover offers
// these as one-click target suggestions.
type ContainerPortDTO struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// PodSummaryDTO is the pods-lens row.
type PodSummaryDTO struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"` // the pod object: verbs are armed against it (§5b)
	Ready      bool   `json:"ready"`
	Phase      string `json:"phase"`
	Reason     string `json:"reason"`
	Rank       string `json:"rank"` // "unhealthy"|"degraded"|"restarts"|"healthy"
	Restarts   int    `json:"restarts"`
	Node       string `json:"node"`
	IP         string `json:"ip"`
	OwnerKind  string `json:"ownerKind"`
	OwnerName  string `json:"ownerName"`
	OwnerUID   string `json:"ownerUid,omitempty"` // the owner a restart is armed against (§5b)
	GVR        string `json:"gvr,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	AgeSeconds int    `json:"ageSeconds"`
	// Created (unix) anchors the age the lens ticks (1c slice 3 §3);
	// AgeSeconds is the age at the read or capture.
	Created    int64                 `json:"createdUnix,omitempty"`
	Containers []ContainerSummaryDTO `json:"containers"`
	// Health is the shared verdict; Rank/Reason/Phase are legacy fields kept
	// until every consumer has migrated.
	Health HealthDTO `json:"health"`
	// Coverage S6: the generation the kubelet acknowledged (0: not
	// reported), the resize state, and the reservation figures.
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	Resize             *ResizeDTO `json:"resize,omitempty"`
	Sched              SchedDTO   `json:"sched"`
}

// PodRowDTO is a pod on the rows transport (1c slice 3 addendum §3.2): what
// the list, its filters, the palette and the verbs read. It is bounded and
// time-stable - no relative age (the lens ticks it from createdUnix) and no
// legacy rank/reason/phase. The verdict message and each container's image,
// ports, resources and last termination are read from the UID-fenced detail
// (GetPodDetail, whose summary is the full PodSummaryDTO).
type PodRowDTO struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	GVR        string `json:"gvr,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	OwnerKind  string `json:"ownerKind"`
	OwnerName  string `json:"ownerName"`
	OwnerUID   string `json:"ownerUid,omitempty"`
	Node       string `json:"node"`
	IP         string `json:"ip"`
	Ready      bool   `json:"ready"`
	Restarts   int    `json:"restarts"`
	Created    int64  `json:"createdUnix,omitempty"`
	// Health is the captured verdict without its message.
	Health     HealthDTO            `json:"health"`
	Containers []PodRowContainerDTO `json:"containers"`
	// Coverage S6: the acknowledged generation (0: not reported) and the
	// resize state; the Syncing presentation reads them. EvidenceRev
	// revises the detail's resize and debug evidence, so a status-only
	// change re-reads the selected pod's detail.
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	Resize             *ResizeDTO `json:"resize,omitempty"`
	EvidenceRev        string     `json:"evidenceRev,omitempty"`
}

// PodRowContainerDTO is a container on a pod row: its live state only.
type PodRowContainerDTO struct {
	Name     string `json:"name"`
	Init     bool   `json:"init,omitempty"`
	Ready    bool   `json:"ready"`
	Restarts int    `json:"restarts"`
	State    string `json:"state"`
}

func (r PodRowDTO) rowUID() string { return r.UID }

// PodDetailDTO is the full per-pod detail view.
type PodDetailDTO struct {
	Summary        PodSummaryDTO     `json:"summary"`
	Labels         map[string]string `json:"labels"`
	Annotations    map[string]string `json:"annotations"`
	Conditions     []ConditionDTO    `json:"conditions"`
	Events         []EventDTO        `json:"events"`
	YAML           string            `json:"yaml"`
	QosClass       string            `json:"qosClass"`
	ServiceAccount string            `json:"serviceAccount"`
	// AsOfUnix is when the pod was read; Summary.UID is the object read. The
	// inspector shows a detail only for the selected row's UID (design §5b).
	AsOfUnix int64 `json:"asOfUnix,omitempty"`
	// Error is set when the detail read failed; the zero DTO is never a pod.
	Error string `json:"error,omitempty"`
	// Coverage S6: every resizable unit, the debug containers (at most 20,
	// ephemeralMore not listed).
	Resources     []ResizeResourcesDTO `json:"resources"`
	Ephemeral     []EphemeralDTO       `json:"ephemeral"`
	EphemeralMore int                  `json:"ephemeralMore,omitempty"`
	// Coverage S11a §6: the pod's extended requests, bounded.
	Extended ExtendedPodDTO `json:"extended"`
	// Coverage S11b §3.5: the claims the pod references (joined to the
	// claims rows in the inspector), and its device health.
	Claims       []PodClaimRefDTO  `json:"claims"`
	DeviceHealth []DeviceHealthDTO `json:"deviceHealth"`
	// DeviceHealthOmitted: status entries past the bound, so a claim's
	// health among them is not established
	DeviceHealthOmitted int `json:"deviceHealthOmitted,omitempty"`
}

// PodClaimRefDTO is one claim a pod references. State is "resolved",
// "none-needed" (upstream skips it on purpose) or "not-created".
type PodClaimRefDTO struct {
	PodClaim string `json:"podClaim"`
	Source   string `json:"source"`
	Claim    string `json:"claim,omitempty"`
	State    string `json:"state"`
}

// DeviceHealthDTO is one container's allocatedResourcesStatus entry.
type DeviceHealthDTO struct {
	Container string                 `json:"container"`
	Init      bool                   `json:"init,omitempty"`
	Name      string                 `json:"name"`
	Devices   []DeviceHealthEntryDTO `json:"devices"`
	// counts over every device the entry reports, kept or omitted
	Unhealthy      int `json:"unhealthy"`
	Unknown        int `json:"unknown"`
	Healthy        int `json:"healthy"`
	DevicesOmitted int `json:"devicesOmitted,omitempty"`
}

type DeviceHealthEntryDTO struct {
	ID      string `json:"id"`
	Health  string `json:"health"`
	Message string `json:"message,omitempty"`
}

func toPodClaimsDTO(refs []dra.PodClaimRef) []PodClaimRefDTO {
	out := make([]PodClaimRefDTO, 0, len(refs))
	for _, r := range refs {
		out = append(out, PodClaimRefDTO{PodClaim: r.PodClaim, Source: r.Source, Claim: r.Claim, State: r.State})
	}
	return out
}

func toDeviceHealthDTO(hs []fleet.DeviceHealth) []DeviceHealthDTO {
	out := make([]DeviceHealthDTO, 0, len(hs))
	for _, h := range hs {
		d := DeviceHealthDTO{Container: h.Container, Init: h.Init, Name: h.Name, Devices: make([]DeviceHealthEntryDTO, 0, len(h.Devices)),
			Unhealthy: h.Unhealthy, Unknown: h.Unknown, Healthy: h.Healthy, DevicesOmitted: h.DevicesOmitted}
		for _, e := range h.Devices {
			d.Devices = append(d.Devices, DeviceHealthEntryDTO{ID: e.ID, Health: e.Health, Message: e.Message})
		}
		out = append(out, d)
	}
	return out
}

// ExtendedPodEntryDTO is one container's extended request; Quantity is a
// decimal string ("-" when not established).
type ExtendedPodEntryDTO struct {
	Name        string `json:"name"`
	Quantity    string `json:"quantity"`
	ServedByDRA bool   `json:"servedByDra,omitempty"`
}

// ExtendedContainerDTO is one container's extended requests.
type ExtendedContainerDTO struct {
	Container string                `json:"container"`
	Init      bool                  `json:"init,omitempty"`
	Entries   []ExtendedPodEntryDTO `json:"entries"`
	Omitted   int                   `json:"omitted,omitempty"`
}

// ExtendedPodDTO is a pod's extended requests; Claim names the special
// ResourceClaim serving its DRA-mapped pairs.
type ExtendedPodDTO struct {
	Containers []ExtendedContainerDTO `json:"containers"`
	Omitted    int                    `json:"omitted,omitempty"`
	Claim      string                 `json:"claim,omitempty"`
}

func toExtendedPodDTO(r extres.PodRequests) ExtendedPodDTO {
	out := ExtendedPodDTO{Containers: make([]ExtendedContainerDTO, 0, len(r.Containers)), Omitted: r.Omitted, Claim: r.Claim}
	for _, c := range r.Containers {
		ce := ExtendedContainerDTO{Container: c.Container, Init: c.Init, Entries: make([]ExtendedPodEntryDTO, 0, len(c.Entries)), Omitted: c.Omitted}
		for _, e := range c.Entries {
			q := e.Quantity
			ce.Entries = append(ce.Entries, ExtendedPodEntryDTO{Name: e.Name, Quantity: figureDTO(&q), ServedByDRA: e.ServedByDRA})
		}
		out.Containers = append(out.Containers, ce)
	}
	return out
}
