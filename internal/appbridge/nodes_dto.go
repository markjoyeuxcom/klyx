package appbridge

// NodeSummaryDTO is the nodes-lens row.
type NodeSummaryDTO struct {
	Name           string   `json:"name"`
	UID            string   `json:"uid"`
	GVR            string   `json:"gvr,omitempty"`
	Generation     int64    `json:"generation"`
	Roles          []string `json:"roles"`
	Ready          bool     `json:"ready"`
	Unschedulable  bool     `json:"unschedulable"`
	Problems       []string `json:"problems"`
	Version        string   `json:"version"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	TaintCount     int      `json:"taintCount"`
	CPUCapacity    float64  `json:"cpuCapacity"`
	CPUAllocatable float64  `json:"cpuAllocatable"`
	MemCapacity    float64  `json:"memCapacity"`
	MemAllocatable float64  `json:"memAllocatable"`
	PodCapacity    int64    `json:"podCapacity"`
	// AgeSeconds is the age at a list read. The rows transport never sends
	// it (time-stable rows, slice 3 addendum §3.1): the lens ticks the age
	// from Created (unix, 1c slice 3 §3).
	AgeSeconds int    `json:"ageSeconds,omitempty"`
	Created    int64  `json:"createdUnix,omitempty"`
	Pool       string `json:"pool"`
	Zone       string `json:"zone"`
	// Health is the shared verdict; Ready/Problems are legacy fields kept
	// until every consumer has migrated.
	Health HealthDTO `json:"health"`
}

func (n NodeSummaryDTO) rowUID() string { return n.UID }

// HeadroomPodRefDTO mirrors workloads.PodRef (an unplaced pod + its requests).
type HeadroomPodRefDTO struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	CPUReq    float64 `json:"cpuReq"`
	MemReq    float64 `json:"memReq"`
}

// PDBBlockerDTO mirrors fleet.PDBBlocker.
type PDBBlockerDTO struct {
	Namespace          string `json:"namespace"`
	Name               string `json:"name"`
	DisruptionsAllowed int    `json:"disruptionsAllowed"`
}

// HeadroomResultDTO mirrors workloads.HeadroomResult.
type HeadroomResultDTO struct {
	LostNodes     int                 `json:"lostNodes"`
	SurvivorNodes int                 `json:"survivorNodes"`
	Displaced     int                 `json:"displaced"`
	Rescheduled   int                 `json:"rescheduled"`
	Unplaced      []HeadroomPodRefDTO `json:"unplaced"`
	CPUShortfall  float64             `json:"cpuShortfall"`
	MemShortfall  float64             `json:"memShortfall"`
}

// HeadroomReportDTO is GetNodeHeadroom's payload.
type HeadroomReportDTO struct {
	Result        HeadroomResultDTO `json:"result"`
	PDBBlockers   []PDBBlockerDTO   `json:"pdbBlockers"`
	PodListCapped bool              `json:"podListCapped"`
	Error         string            `json:"error,omitempty"`
}

// NodesResultDTO is the response from ListNodes.
type NodesResultDTO struct {
	Nodes []NodeSummaryDTO `json:"nodes"`
	// Error is set when the read FAILED (cluster not connected, list/get
	// error). The collections stay empty-but-non-nil, so a caller that ignores
	// it still cannot crash - but an error must never read as "nothing found":
	// the triage tape and Overview treat a source with Error as unreadable,
	// not quiet (review 2026-10-03 #10).
	Error string `json:"error,omitempty"`
}

// NodeTaintDTO is a single taint.
type NodeTaintDTO struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// PodOnNodeDTO is a pod reference for the pods-on-node list.
type PodOnNodeDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
}

// NodeDetailDTO is the full per-node detail view.
type NodeDetailDTO struct {
	Summary     NodeSummaryDTO    `json:"summary"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Taints      []NodeTaintDTO    `json:"taints"`
	Conditions  []ConditionDTO    `json:"conditions"`
	Events      []EventDTO        `json:"events"`
	YAML        string            `json:"yaml"`
	PodsOnNode  []PodOnNodeDTO    `json:"podsOnNode"`
	// AsOfUnix is when the node was read; Summary.UID is the object read. The
	// inspector shows a detail only for the selected row's UID (design §5b).
	AsOfUnix int64 `json:"asOfUnix,omitempty"`
	// Error is set when the detail read failed; the zero DTO is never a node.
	Error string `json:"error,omitempty"`
}
