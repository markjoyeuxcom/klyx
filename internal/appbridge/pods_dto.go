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

// PodsResultDTO is the response from ListPods.
type PodsResultDTO struct {
	// Namespaces is the sorted distinct set of pod namespaces. Populated ONLY
	// when namespace=="" (all-namespaces load); empty slice otherwise.
	Namespaces []string        `json:"namespaces"`
	Pods       []PodSummaryDTO `json:"pods"`
	// Truncated: the list hit the server cap, so the rows are an incomplete
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
}
