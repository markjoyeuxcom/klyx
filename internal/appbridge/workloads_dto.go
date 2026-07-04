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
}

type WorkloadsResultDTO struct {
	FluxPresent bool          `json:"fluxPresent"`
	Namespaces  []string      `json:"namespaces"` // populated only when namespace==""
	Workloads   []WorkloadDTO `json:"workloads"`
}
