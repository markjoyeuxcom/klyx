package appbridge

// MetricsDTO is the on-demand cluster metrics payload. Nil fractions serialize
// as JSON null (the UI renders "—"), never 0.
type MetricsDTO struct {
	Available   bool     `json:"available"`
	Mode        string   `json:"mode"`
	Source      string   `json:"source"`
	Warning     string   `json:"warning"`
	Reason      string   `json:"reason"`
	CPUFraction *float64 `json:"cpuFraction"`
	MemFraction *float64 `json:"memFraction"`
	// Sentence says where the metrics came from and what makes them this
	// cluster's; Lineage keys that source, so a view refuses a late reply
	// read under another (Settings S3b §3.2, §3.4)
	Sentence string `json:"sentence,omitempty"`
	Lineage  string `json:"lineage,omitempty"`
	// NoData: both reads answered with no series under the cluster's
	// scope (S3b §3.3), said rather than shown as nothing
	NoData string `json:"noData,omitempty"`
}

// PointDTO is one timestamped sample (unix seconds). Consecutive points may be
// more than one step apart — Prometheus omits empty steps and the UI renders
// the gap, never interpolates across it.
type PointDTO struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

// SparklinesDTO carries a 30m cpu/mem series pair. Available=false means no
// usable data (Message says why); empty slices with Available=true mean "no
// samples in the window", which is real information, not an error.
type SparklinesDTO struct {
	Available bool       `json:"available"`
	Message   string     `json:"message,omitempty"`
	CPU       []PointDTO `json:"cpu"`
	Mem       []PointDTO `json:"mem"`
	// Sentence says where the metrics came from and what makes them this
	// cluster's; Lineage keys that source, so a view refuses a late reply
	// read under another (Settings S3b §3.2, §3.4)
	Sentence string `json:"sentence,omitempty"`
	Lineage  string `json:"lineage,omitempty"`
	NoData   string `json:"noData,omitempty"`
}

// NodeUsageDTO mirrors workloads.NodeUsage: one node's pod-attributed live
// usage. Nil cpu/mem serialize as JSON null - "no sample", never zero.
type NodeUsageDTO struct {
	Node        string   `json:"node"`
	CPU         *float64 `json:"cpu"`
	Mem         *float64 `json:"mem"`
	SampledPods int      `json:"sampledPods"`
}

// NodeUtilizationResultDTO is GetNodeUtilization's payload. Available=true may
// still carry a Message (e.g. the pod-list cap under-count warning) - render
// it as a caption, not an error.
type NodeUtilizationResultDTO struct {
	Available     bool           `json:"available"`
	Message       string         `json:"message,omitempty"`
	UpdatedAtUnix int64          `json:"updatedAtUnix"`
	Items         []NodeUsageDTO `json:"items"`
	// Sentence says where the metrics came from and what makes them this
	// cluster's; Lineage keys that source, so a view refuses a late reply
	// read under another (Settings S3b §3.2, §3.4)
	Sentence string `json:"sentence,omitempty"`
	Lineage  string `json:"lineage,omitempty"`
	// NoData: the read answered with no series under the cluster's scope
	// (S3b §3.3), apart from Message's caveats
	NoData string `json:"noData,omitempty"`
}

// ContainerUsageDTO is one container's live usage inside a pod.
type ContainerUsageDTO struct {
	Container string   `json:"container"`
	CPU       *float64 `json:"cpu"`
	Mem       *float64 `json:"mem"`
}

// PodUsageResultDTO is GetPodContainerUsage's payload.
type PodUsageResultDTO struct {
	Available     bool                `json:"available"`
	Message       string              `json:"message,omitempty"`
	UpdatedAtUnix int64               `json:"updatedAtUnix"`
	Items         []ContainerUsageDTO `json:"items"`
	// Sentence says where the metrics came from and what makes them this
	// cluster's; Lineage keys that source, so a view refuses a late reply
	// read under another (Settings S3b §3.2, §3.4)
	Sentence string `json:"sentence,omitempty"`
	Lineage  string `json:"lineage,omitempty"`
	// NoData: the read answered with no series under the cluster's scope
	// (S3b §3.3), apart from Message's caveats
	NoData string `json:"noData,omitempty"`
}
