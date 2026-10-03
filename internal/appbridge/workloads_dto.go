package appbridge

type OwnerDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type PodDTO struct {
	Name       string `json:"name"`
	Ready      bool   `json:"ready"`
	Restarts   int    `json:"restarts"`
	Reason     string `json:"reason"`
	Node       string `json:"node"`
	AgeSeconds int    `json:"ageSeconds"`
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
	// Paused: Deployment spec.paused - an intended operator state (rollouts
	// held), not an error. Always false for StatefulSet/DaemonSet.
	Paused bool `json:"paused,omitempty"`
}

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
	Error     string               `json:"error,omitempty"`
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
	Error           string                `json:"error"`
}

type WorkloadsResultDTO struct {
	FluxPresent bool          `json:"fluxPresent"`
	Namespaces  []string      `json:"namespaces"` // populated only when namespace==""
	Workloads   []WorkloadDTO `json:"workloads"`
	// Truncated: a list hit the server cap, so these rows are an incomplete
	// (pre-ranking) slice - the UI must tell the user to scope by namespace for
	// complete broken-first triage, never imply this is the whole picture.
	Truncated bool `json:"truncated,omitempty"`
	// Error is set when the read FAILED (cluster not connected, list/get
	// error). The collections stay empty-but-non-nil, so a caller that ignores
	// it still cannot crash - but an error must never read as "nothing found":
	// the triage tape and Overview treat a source with Error as unreadable,
	// not quiet (review 2026-10-03 #10).
	Error string `json:"error,omitempty"`
}
