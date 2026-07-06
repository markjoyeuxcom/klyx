package appbridge

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
	Namespace  string                `json:"namespace"`
	Name       string                `json:"name"`
	Ready      bool                  `json:"ready"`
	Phase      string                `json:"phase"`
	Reason     string                `json:"reason"`
	Rank       string                `json:"rank"` // "unhealthy"|"degraded"|"restarts"|"healthy"
	Restarts   int                   `json:"restarts"`
	Node       string                `json:"node"`
	IP         string                `json:"ip"`
	OwnerKind  string                `json:"ownerKind"`
	OwnerName  string                `json:"ownerName"`
	AgeSeconds int                   `json:"ageSeconds"`
	Containers []ContainerSummaryDTO `json:"containers"`
}

// PodsResultDTO is the response from ListPods.
type PodsResultDTO struct {
	// Namespaces is the sorted distinct set of pod namespaces. Populated ONLY
	// when namespace=="" (all-namespaces load); empty slice otherwise.
	Namespaces []string        `json:"namespaces"`
	Pods       []PodSummaryDTO `json:"pods"`
}

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
}
