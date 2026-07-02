package appbridge

// NodeSummaryDTO is the nodes-lens row.
type NodeSummaryDTO struct {
	Name           string   `json:"name"`
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
	AgeSeconds     int      `json:"ageSeconds"`
}

// NodesResultDTO is the response from ListNodes.
type NodesResultDTO struct {
	Nodes []NodeSummaryDTO `json:"nodes"`
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
}
