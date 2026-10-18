package workloads

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Coverage S6 §3: a pod's in-place resize state, as evidence. Desired is
// the spec; allocated is what the kubelet admitted
// (status.containerStatuses[].allocatedResources); actual is what the
// runtime has configured (status.containerStatuses[].resources). A value
// the kubelet does not report stays nil: never zero, never the spec.

// ResizeCondition is one active (status True) resize condition.
type ResizeCondition struct {
	Reason  string
	Message string
	// Generation is the condition's observedGeneration: for Pending, the
	// allocation last attempted; for InProgress, the generation the ongoing
	// actuation started at (kept until it clears). 0: not reported.
	Generation int64
	Since      time.Time
}

// ResizeState is the pod's resize conditions and the legacy field.
type ResizeState struct {
	Pending    *ResizeCondition // PodResizePending (Deferred | Infeasible)
	InProgress *ResizeCondition // PodResizeInProgress ("" | Error)
	// Legacy is status.resize (1.27-1.32), shown only.
	Legacy string
}

// ResizeStateOf reads the active resize conditions; a condition whose
// status is not True is not active (the kubelet only writes True) and is
// left to the generic conditions list.
func ResizeStateOf(p *corev1.Pod) ResizeState {
	out := ResizeState{Legacy: string(p.Status.Resize)} //nolint:staticcheck // the deprecated field is shown, never classified
	for _, c := range p.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		rc := &ResizeCondition{Reason: c.Reason, Message: c.Message, Generation: c.ObservedGeneration, Since: c.LastTransitionTime.Time}
		switch c.Type {
		case corev1.PodResizePending:
			if out.Pending == nil {
				out.Pending = rc
			}
		case corev1.PodResizeInProgress:
			if out.InProgress == nil {
				out.InProgress = rc
			}
		}
	}
	return out
}

// Active reports any active resize condition.
func (r ResizeState) Active() bool { return r.Pending != nil || r.InProgress != nil }

// ResourceTriple is one resource's desired, allocated and actual values
// (cores or bytes); nil is not set (desired) or not reported.
type ResourceTriple struct {
	Desired   *float64
	Allocated *float64
	Actual    *float64
}

// Differs reports a reported allocated or actual value that is not the
// desired one.
func (r ResourceTriple) Differs() bool {
	ne := func(a, b *float64) bool {
		if a == nil || b == nil {
			return a != b
		}
		return *a != *b
	}
	return (r.Allocated != nil && ne(r.Allocated, r.Desired)) || (r.Actual != nil && ne(r.Actual, r.Desired))
}

// ResizeResources is one resizable unit's requests and limits: a
// container, a sidecar, or the pod level.
type ResizeResources struct {
	Name                      string // "" for the pod level
	Sidecar                   bool
	CPUReq, MemReq            ResourceTriple
	CPULim, MemLim            ResourceTriple // allocated is never reported for limits
	CPURestart, MemoryRestart string         // resizePolicy: NotRequired | RestartContainer; "" unread
}

func quantity(l corev1.ResourceList, name corev1.ResourceName) *float64 {
	if l == nil {
		return nil
	}
	q, ok := l[name]
	if !ok {
		return nil
	}
	v := q.AsApproximateFloat64()
	return &v
}

// ResizeResourcesOf reads every resizable unit of a pod: app containers,
// sidecars (restartable init containers), and the pod level when set.
// Regular init containers cannot be resized and are not listed.
func ResizeResourcesOf(p *corev1.Pod) []ResizeResources {
	statuses := map[string]*corev1.ContainerStatus{}
	for i := range p.Status.ContainerStatuses {
		statuses[p.Status.ContainerStatuses[i].Name] = &p.Status.ContainerStatuses[i]
	}
	for i := range p.Status.InitContainerStatuses {
		statuses[p.Status.InitContainerStatuses[i].Name] = &p.Status.InitContainerStatuses[i]
	}
	unit := func(c corev1.Container, sidecar bool) ResizeResources {
		r := ResizeResources{Name: c.Name, Sidecar: sidecar}
		r.CPUReq.Desired, r.MemReq.Desired = quantity(c.Resources.Requests, corev1.ResourceCPU), quantity(c.Resources.Requests, corev1.ResourceMemory)
		r.CPULim.Desired, r.MemLim.Desired = quantity(c.Resources.Limits, corev1.ResourceCPU), quantity(c.Resources.Limits, corev1.ResourceMemory)
		if cs := statuses[c.Name]; cs != nil {
			r.CPUReq.Allocated, r.MemReq.Allocated = quantity(cs.AllocatedResources, corev1.ResourceCPU), quantity(cs.AllocatedResources, corev1.ResourceMemory)
			if cs.Resources != nil {
				r.CPUReq.Actual, r.MemReq.Actual = quantity(cs.Resources.Requests, corev1.ResourceCPU), quantity(cs.Resources.Requests, corev1.ResourceMemory)
				r.CPULim.Actual, r.MemLim.Actual = quantity(cs.Resources.Limits, corev1.ResourceCPU), quantity(cs.Resources.Limits, corev1.ResourceMemory)
			}
		}
		for _, rp := range c.ResizePolicy {
			switch rp.ResourceName {
			case corev1.ResourceCPU:
				r.CPURestart = string(rp.RestartPolicy)
			case corev1.ResourceMemory:
				r.MemoryRestart = string(rp.RestartPolicy)
			}
		}
		return r
	}
	var out []ResizeResources
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, unit(c, true))
		}
	}
	for _, c := range p.Spec.Containers {
		out = append(out, unit(c, false))
	}
	if p.Spec.Resources != nil && (len(p.Spec.Resources.Requests) > 0 || len(p.Spec.Resources.Limits) > 0) {
		r := ResizeResources{}
		r.CPUReq.Desired, r.MemReq.Desired = quantity(p.Spec.Resources.Requests, corev1.ResourceCPU), quantity(p.Spec.Resources.Requests, corev1.ResourceMemory)
		r.CPULim.Desired, r.MemLim.Desired = quantity(p.Spec.Resources.Limits, corev1.ResourceCPU), quantity(p.Spec.Resources.Limits, corev1.ResourceMemory)
		r.CPUReq.Allocated, r.MemReq.Allocated = quantity(p.Status.AllocatedResources, corev1.ResourceCPU), quantity(p.Status.AllocatedResources, corev1.ResourceMemory)
		if p.Status.Resources != nil {
			r.CPUReq.Actual, r.MemReq.Actual = quantity(p.Status.Resources.Requests, corev1.ResourceCPU), quantity(p.Status.Resources.Requests, corev1.ResourceMemory)
			r.CPULim.Actual, r.MemLim.Actual = quantity(p.Status.Resources.Limits, corev1.ResourceCPU), quantity(p.Status.Resources.Limits, corev1.ResourceMemory)
		}
		out = append(out, r)
	}
	return out
}

// MaxEphemeral bounds the debug containers listed per pod (S6 §5).
const MaxEphemeral = 20

// EphemeralContainer is one debug container: evidence, never the
// application's failure (health.Pod never reads its status).
type EphemeralContainer struct {
	Name, Image, Target string
	// Profile evidence from its security context.
	Privileged   bool
	CapsAdded    []string
	RunAsNonRoot *bool
	// State: running | waiting | terminated | "" (no status yet).
	State      string
	Reason     string
	ExitCode   int32
	StartedAt  time.Time
	FinishedAt time.Time
}

// EphemeralOf lists a pod's debug containers, newest last, at most
// MaxEphemeral (the newest kept); more says how many were not listed.
func EphemeralOf(p *corev1.Pod) (out []EphemeralContainer, more int) {
	statuses := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.EphemeralContainerStatuses {
		statuses[cs.Name] = cs
	}
	ecs := p.Spec.EphemeralContainers
	if len(ecs) > MaxEphemeral {
		more = len(ecs) - MaxEphemeral
		ecs = ecs[more:]
	}
	for _, ec := range ecs {
		e := EphemeralContainer{Name: ec.Name, Image: ec.Image, Target: ec.TargetContainerName}
		if sc := ec.SecurityContext; sc != nil {
			e.Privileged = sc.Privileged != nil && *sc.Privileged
			if sc.Capabilities != nil {
				for _, c := range sc.Capabilities.Add {
					e.CapsAdded = append(e.CapsAdded, string(c))
				}
			}
			e.RunAsNonRoot = sc.RunAsNonRoot
		}
		if cs, ok := statuses[ec.Name]; ok {
			switch st := cs.State; {
			case st.Running != nil:
				e.State, e.StartedAt = "running", st.Running.StartedAt.Time
			case st.Waiting != nil:
				e.State, e.Reason = "waiting", st.Waiting.Reason
			case st.Terminated != nil:
				e.State, e.Reason, e.ExitCode = "terminated", st.Terminated.Reason, st.Terminated.ExitCode
				e.StartedAt, e.FinishedAt = st.Terminated.StartedAt.Time, st.Terminated.FinishedAt.Time
			}
		}
		out = append(out, e)
	}
	return out, more
}

// EvidenceRev is a bounded revision of what the inspector's Resources and
// Debug containers blocks show and the row's other fields do not
// (implementation review 1): the acknowledged generation, the resize
// conditions, each container's allocated and actual resources, the
// pod-level status maps, the legacy resize field and each debug
// container's state. A status-only change (an acknowledgement, an
// actuation, a debug container exiting) changes it, so the selected pod's
// detail is read again. 16 hex characters (FNV-64a); never a value.
func EvidenceRev(p *corev1.Pod) string {
	type cs struct {
		Name      string
		Allocated corev1.ResourceList
		Resources *corev1.ResourceRequirements
	}
	type eph struct {
		Name, State, Reason string
		ExitCode            int32
		Started, Finished   time.Time
	}
	type cond struct {
		Type, Status, Reason, Message string
		Generation                    int64
	}
	var e struct {
		Observed     int64
		Conds        []cond
		Containers   []cs
		PodAllocated corev1.ResourceList
		PodResources *corev1.ResourceRequirements
		Legacy       string
		Ephemeral    []eph
	}
	e.Observed = p.Status.ObservedGeneration
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodResizePending || c.Type == corev1.PodResizeInProgress {
			e.Conds = append(e.Conds, cond{string(c.Type), string(c.Status), c.Reason, c.Message, c.ObservedGeneration})
		}
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, c := range list {
			e.Containers = append(e.Containers, cs{c.Name, c.AllocatedResources, c.Resources})
		}
	}
	e.PodAllocated, e.PodResources = p.Status.AllocatedResources, p.Status.Resources
	e.Legacy = string(p.Status.Resize) //nolint:staticcheck // the deprecated field is shown, so it is part of the evidence
	for _, c := range p.Status.EphemeralContainerStatuses {
		x := eph{Name: c.Name}
		switch st := c.State; {
		case st.Running != nil:
			x.State, x.Started = "running", st.Running.StartedAt.Time
		case st.Waiting != nil:
			x.State, x.Reason = "waiting", st.Waiting.Reason
		case st.Terminated != nil:
			x.State, x.Reason, x.ExitCode = "terminated", st.Terminated.Reason, st.Terminated.ExitCode
			x.Started, x.Finished = st.Terminated.StartedAt.Time, st.Terminated.FinishedAt.Time
		}
		e.Ephemeral = append(e.Ephemeral, x)
	}
	raw, _ := json.Marshal(e) // ResourceList keys marshal sorted; quantities canonically
	h := fnv.New64a()
	_, _ = h.Write(raw)
	return fmt.Sprintf("%016x", h.Sum64())
}
