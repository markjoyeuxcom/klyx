package appbridge

type OwnerDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type PodDTO struct {
	Name     string `json:"name"`
	UID      string `json:"uid,omitempty"`
	Ready    bool   `json:"ready"`
	Restarts int    `json:"restarts"`
	Reason   string `json:"reason"`
	Node     string `json:"node"`
	// AgeSeconds is the age at a list read; the rows transport never sends
	// it (slice 3 addendum §3.1). Created (unix) anchors the age the lens
	// ticks (1c slice 3 §3).
	AgeSeconds int   `json:"ageSeconds,omitempty"`
	Created    int64 `json:"createdUnix,omitempty"`
}

// ResourceCellDTO is one resource (cpu cores / memory bytes). Nil = JSON null ->
// UI renders "—" (or "no limit" for a nil Limit with matched pods). Never 0.
type ResourceCellDTO struct {
	Usage   *float64 `json:"usage"`
	Request *float64 `json:"request"`
	Limit   *float64 `json:"limit"`
}

type WorkloadResourcesDTO struct {
	CPU ResourceCellDTO `json:"cpu"`
	Mem ResourceCellDTO `json:"mem"`
}

type WorkloadMetricsStatusDTO struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updatedAt"` // RFC3339; "" when never succeeded
}

type WorkloadUsageDTO struct {
	CPUUsage *float64 `json:"cpuUsage"`
	MemUsage *float64 `json:"memUsage"`
}

type WorkloadMetricsResultDTO struct {
	Status WorkloadMetricsStatusDTO    `json:"status"`
	Usage  map[string]WorkloadUsageDTO `json:"usage"` // keyed "<kind>/<ns>/<name>"
}

// ReloadTargetsDTO declares the ConfigMaps/Secrets a workload reloads on via
// Stakater Reloader annotations. Slices are always non-nil ([] not null) for
// clean JSON. Auto is the reload-on-any-mounted mode.
type ReloadTargetsDTO struct {
	Auto       bool     `json:"auto"`
	ConfigMaps []string `json:"configMaps"`
	Secrets    []string `json:"secrets"`
}

type WorkloadDTO struct {
	Kind      string               `json:"kind"`
	Namespace string               `json:"namespace"`
	Name      string               `json:"name"`
	UID       string               `json:"uid,omitempty"`
	GVR       string               `json:"gvr,omitempty"`
	Desired   int                  `json:"desired"`
	Ready     int                  `json:"ready"`
	Available int                  `json:"available"`
	Updated   int                  `json:"updated"`
	Restarts  int                  `json:"restarts"`
	Reason    string               `json:"reason"`
	Rank      string               `json:"rank"` // "unhealthy"|"degraded"|"restarts"|"healthy"
	GitOps    *OwnerDTO            `json:"gitops"`
	Pods      []PodDTO             `json:"pods"`
	Resources WorkloadResourcesDTO `json:"resources"`
	// Stale: spec changed, controller has not observed it - status/rank reflect
	// the previous spec; render the row as transitioning ("dim = not yet
	// realized"), same staleness language as Flux resources and Gateways.
	Stale              bool  `json:"stale,omitempty"`
	Generation         int64 `json:"generation,omitempty"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Reloader is the forward "reloads on" declaration; nil when the workload
	// carries no Reloader annotation.
	Reloader *ReloadTargetsDTO `json:"reloader,omitempty"`
	// Health is the shared verdict; Rank/Reason are legacy fields kept until
	// every consumer has migrated.
	Health HealthDTO `json:"health"`
	// Paused: Deployment spec.paused - an intended operator state (rollouts
	// held), not an error. Always false for StatefulSet/DaemonSet.
	Paused bool `json:"paused,omitempty"`
}

func (w WorkloadDTO) rowUID() string { return w.UID }

// RolloutRevisionDTO mirrors workloads.RolloutRevision - one rollout history
// entry.
type RolloutRevisionDTO struct {
	Revision    int64    `json:"revision"`
	AgeSeconds  int      `json:"ageSeconds"`
	Images      []string `json:"images"`
	Current     bool     `json:"current"`
	ChangeCause string   `json:"changeCause,omitempty"`
}

// RolloutHistoryResultDTO is RolloutHistory's payload. Error is the fold-in
// failure channel; Revisions is never nil.
type RolloutHistoryResultDTO struct {
	Revisions []RolloutRevisionDTO `json:"revisions"`
	// UID is the workload the history was read from, AsOfUnix when: the
	// lens accepts a history only for the selected row's UID (§5b).
	UID      string `json:"uid,omitempty"`
	AsOfUnix int64  `json:"asOfUnix,omitempty"`
	Error    string `json:"error,omitempty"`
}

// CompareContainerDTO is one container's drift-relevant identity in the fleet
// compare.
type CompareContainerDTO struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Resources string `json:"resources"`
}

// WorkloadCompareDTO is one cluster's view of a workload for the fleet compare.
// found=false is an honest "this cluster does not run it"; error carries a
// degraded lookup (RBAC, disconnect) so it is never mistaken for absence.
type WorkloadCompareDTO struct {
	Found           bool                  `json:"found"`
	Kind            string                `json:"kind"`
	Namespace       string                `json:"namespace"`
	Name            string                `json:"name"`
	DesiredReplicas int32                 `json:"desiredReplicas"`
	ReadyReplicas   int32                 `json:"readyReplicas"`
	Containers      []CompareContainerDTO `json:"containers"`
	SpecHash        string                `json:"specHash"`
	OwnerKind       string                `json:"ownerKind"`
	OwnerNamespace  string                `json:"ownerNamespace"`
	OwnerName       string                `json:"ownerName"`
	OwnerRevision   string                `json:"ownerRevision"`
	// OwnerRevisionNote: why ownerRevision is empty for an owned workload.
	OwnerRevisionNote string `json:"ownerRevisionNote,omitempty"`
	Error             string `json:"error"`
}
