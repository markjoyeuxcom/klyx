package appbridge

import (
	"github.com/moomora/klyx/internal/workloads"
)

// Coverage S6: the resize and debug-container shapes on the pod DTOs.

// ResizeConditionDTO is one active resize condition.
type ResizeConditionDTO struct {
	Reason     string `json:"reason"`
	Message    string `json:"message,omitempty"`
	Generation int64  `json:"generation"` // 0: not reported
	SinceUnix  int64  `json:"sinceUnix,omitempty"`
}

// ResizeDTO is a pod's resize state.
type ResizeDTO struct {
	Pending    *ResizeConditionDTO `json:"pending,omitempty"`
	InProgress *ResizeConditionDTO `json:"inProgress,omitempty"`
	Legacy     string              `json:"legacy,omitempty"`
}

func toResizeCondDTO(c *workloads.ResizeCondition) *ResizeConditionDTO {
	if c == nil {
		return nil
	}
	return &ResizeConditionDTO{Reason: c.Reason, Message: c.Message, Generation: c.Generation, SinceUnix: unixOrZero(c.Since)}
}

// toResizeDTO is nil when the pod has no resize state: rows stay small.
func toResizeDTO(r workloads.ResizeState) *ResizeDTO {
	if !r.Active() && r.Legacy == "" {
		return nil
	}
	return &ResizeDTO{Pending: toResizeCondDTO(r.Pending), InProgress: toResizeCondDTO(r.InProgress), Legacy: r.Legacy}
}

// TripleDTO is one resource's desired, allocated and actual values.
type TripleDTO struct {
	Desired   *float64 `json:"desired"`
	Allocated *float64 `json:"allocated"`
	Actual    *float64 `json:"actual"`
	Differs   bool     `json:"differs,omitempty"`
}

func toTripleDTO(t workloads.ResourceTriple) TripleDTO {
	return TripleDTO{Desired: t.Desired, Allocated: t.Allocated, Actual: t.Actual, Differs: t.Differs()}
}

// ResizeResourcesDTO is one resizable unit (a container, a sidecar, or the
// pod level when name is "").
type ResizeResourcesDTO struct {
	Name          string    `json:"name"`
	Sidecar       bool      `json:"sidecar,omitempty"`
	CPUReq        TripleDTO `json:"cpuReq"`
	MemReq        TripleDTO `json:"memReq"`
	CPULim        TripleDTO `json:"cpuLim"`
	MemLim        TripleDTO `json:"memLim"`
	CPURestart    string    `json:"cpuRestart,omitempty"`
	MemoryRestart string    `json:"memoryRestart,omitempty"`
}

// SchedDTO is the scheduler's reservation under the assumed model, the
// desired request, and how much more the alternative model would reserve.
type SchedDTO struct {
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	DesiredCPU float64 `json:"desiredCpu"`
	DesiredMem float64 `json:"desiredMem"`
	AltCPU     float64 `json:"altCpu,omitempty"`
	AltMem     float64 `json:"altMem,omitempty"`
	Assumption string  `json:"assumption"`
}

// EphemeralDTO is one debug container.
type EphemeralDTO struct {
	Name         string   `json:"name"`
	Image        string   `json:"image"`
	Target       string   `json:"target,omitempty"`
	Privileged   bool     `json:"privileged,omitempty"`
	CapsAdded    []string `json:"capsAdded,omitempty"`
	RunAsNonRoot *bool    `json:"runAsNonRoot,omitempty"`
	State        string   `json:"state"`
	Reason       string   `json:"reason,omitempty"`
	ExitCode     int32    `json:"exitCode,omitempty"`
	StartedUnix  int64    `json:"startedUnix,omitempty"`
	FinishedUnix int64    `json:"finishedUnix,omitempty"`
}

func toS6Detail(resources []workloads.ResizeResources, eph []workloads.EphemeralContainer) ([]ResizeResourcesDTO, []EphemeralDTO) {
	rs := make([]ResizeResourcesDTO, 0, len(resources))
	for _, r := range resources {
		rs = append(rs, ResizeResourcesDTO{Name: r.Name, Sidecar: r.Sidecar, CPUReq: toTripleDTO(r.CPUReq), MemReq: toTripleDTO(r.MemReq),
			CPULim: toTripleDTO(r.CPULim), MemLim: toTripleDTO(r.MemLim), CPURestart: r.CPURestart, MemoryRestart: r.MemoryRestart})
	}
	es := make([]EphemeralDTO, 0, len(eph))
	for _, e := range eph {
		es = append(es, EphemeralDTO{Name: e.Name, Image: e.Image, Target: e.Target, Privileged: e.Privileged, CapsAdded: e.CapsAdded, RunAsNonRoot: e.RunAsNonRoot,
			State: e.State, Reason: e.Reason, ExitCode: e.ExitCode, StartedUnix: unixOrZero(e.StartedAt), FinishedUnix: unixOrZero(e.FinishedAt)})
	}
	return rs, es
}
