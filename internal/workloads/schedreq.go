package workloads

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Coverage S6 §1-§2: the scheduler's reservation for a pod. kube-scheduler
// computes it with component-helpers' PodRequests, under options from its
// own feature gates, and the helper changed shape across minors (1.37
// takes the max of aggregates where 1.33-1.36 took a per-container max).
// Klyx models each minor at its default gates; golden values computed by
// each unmodified upstream version prove the models
// (testdata/schedreq_golden.json). The model is an assumption about the
// deployed scheduler: the API server's minor does not prove it (the skew
// policy allows a scheduler one minor behind), so the previous minor's
// model is kept beside it as the alternative.

// SchedModel is one kube-scheduler minor's request rule at default gates.
type SchedModel struct {
	// Minor is the minor modelled: 32 stands for 1.32 and older.
	Minor int
	// Unknown: the server version was not read, so the reservation is the
	// per-resource maximum over every model.
	Unknown bool
	// Newer: the server is newer than the newest model, which is assumed.
	Newer int

	status         bool // UseStatusResources (InPlacePodVerticalScaling, on from 1.33)
	podLevel       bool // !SkipPodLevelResources (PodLevelResources, on from 1.34)
	podLevelStatus bool // InPlacePodLevelResourcesVerticalScalingEnabled (on from 1.36)
	aggregate      bool // the 1.37 aggregate rule
}

var schedModels = []SchedModel{
	{Minor: 32},
	{Minor: 33, status: true},
	{Minor: 34, status: true, podLevel: true},
	{Minor: 35, status: true, podLevel: true},
	{Minor: 36, status: true, podLevel: true, podLevelStatus: true},
	{Minor: 37, status: true, podLevel: true, podLevelStatus: true, aggregate: true},
}

// SchedUnknown is the model when the server version was not read.
var SchedUnknown = SchedModel{Unknown: true}

// SchedModelFor returns the model of a server minor.
func SchedModelFor(minor int) SchedModel {
	switch {
	case minor <= 0:
		return SchedUnknown
	case minor <= schedModels[0].Minor:
		return schedModels[0]
	}
	last := schedModels[len(schedModels)-1]
	if minor > last.Minor {
		m := last
		m.Newer = minor
		return m
	}
	for _, m := range schedModels {
		if m.Minor == minor {
			return m
		}
	}
	return SchedUnknown
}

// SchedModelForVersion parses a server GitVersion ("v1.37.1", "v1.34.2-eks-1")
// to its model; an unparsable version is unknown.
func SchedModelForVersion(git string) SchedModel {
	v := strings.TrimPrefix(strings.TrimSpace(git), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || parts[0] != "1" {
		return SchedUnknown
	}
	digits := parts[1]
	for i, r := range digits {
		if r < '0' || r > '9' {
			digits = digits[:i]
			break
		}
	}
	minor, err := strconv.Atoi(digits)
	if err != nil {
		return SchedUnknown
	}
	return SchedModelFor(minor)
}

// Alternative is the model the skew policy also allows (a scheduler one
// minor behind); ok false when there is none distinct (1.32 and older,
// or unknown, which already takes every model's maximum).
func (m SchedModel) Alternative() (SchedModel, bool) {
	if m.Unknown || m.Minor <= schedModels[0].Minor {
		return SchedModel{}, false
	}
	return SchedModelFor(m.Minor - 1), true
}

// Assumption says what the model assumes, for the UI.
func (m SchedModel) Assumption() string {
	switch {
	case m.Unknown:
		return "scheduler version unknown: the largest reservation any supported version would make"
	case m.Newer > 0:
		return fmt.Sprintf("Kubernetes 1.%d is newer than Klyx's models: assuming kube-scheduler 1.%d's rule at its default gates", m.Newer, m.Minor)
	case m.Minor == schedModels[0].Minor:
		return "assuming kube-scheduler 1.32 or older at its default gates (spec requests only)"
	}
	return fmt.Sprintf("assuming kube-scheduler 1.%d at its default gates", m.Minor)
}

// Requests is the reservation for every resource the rule yields.
func (m SchedModel) Requests(p *corev1.Pod) corev1.ResourceList {
	if m.Unknown {
		out := corev1.ResourceList{}
		for _, each := range schedModels {
			maxResourceList(out, each.Requests(p))
		}
		return out
	}
	var reqs corev1.ResourceList
	if m.aggregate {
		reqs = m.aggregateRequests(p)
	} else {
		reqs = m.perContainerRequests(p)
	}
	if m.podLevel && podLevelRequestsSet(p) {
		if m.aggregate {
			eff := p.Spec.Resources.Requests
			if m.podLevelStatus && m.status && p.Status.Resources != nil {
				spec := p.Spec.Resources.Requests
				if resizeInfeasible(p) {
					spec = nil
				}
				eff = maxList(spec, p.Status.Resources.Requests, p.Status.AllocatedResources)
			}
			for name, q := range eff {
				if podLevelResource(name) {
					reqs[name] = q.DeepCopy()
				}
			}
		} else {
			var eff corev1.ResourceList
			if m.podLevelStatus && m.status && p.Status.Resources != nil {
				eff = effective(p, p.Spec.Resources.Requests, p.Status.Resources.Requests, p.Status.AllocatedResources)
			}
			for name, q := range p.Spec.Resources.Requests {
				if podLevelResource(name) {
					reqs[name] = q.DeepCopy()
					if eff != nil {
						// a key the effective map lacks is written as zero,
						// as upstream does
						reqs[name] = eff[name]
					}
				}
			}
		}
	}
	if p.Spec.Overhead != nil {
		addResourceList(reqs, p.Spec.Overhead)
	}
	return reqs
}

// Desired is the pod's desired request: spec only (init, sidecars,
// pod-level as the model reads it, overhead), what a replacement pod is
// admitted with.
func (m SchedModel) Desired(p *corev1.Pod) corev1.ResourceList {
	d := SchedModel{Minor: m.Minor, podLevel: m.podLevel || m.Unknown}
	return d.Requests(p)
}

// effective is 1.33-1.36's per-state rule (determineContainerReqs,
// determineEffectiveRequests).
func effective(p *corev1.Pod, spec, actuated, allocated corev1.ResourceList) corev1.ResourceList {
	if resizeInfeasible(p) {
		return maxList(actuated, allocated)
	}
	return maxList(spec, actuated, allocated)
}

func (m SchedModel) statusIndex(p *corev1.Pod) map[string]*corev1.ContainerStatus {
	if !m.status {
		return nil
	}
	idx := make(map[string]*corev1.ContainerStatus, len(p.Status.ContainerStatuses)+len(p.Status.InitContainerStatuses))
	for i := range p.Status.ContainerStatuses {
		idx[p.Status.ContainerStatuses[i].Name] = &p.Status.ContainerStatuses[i]
	}
	for i := range p.Status.InitContainerStatuses {
		idx[p.Status.InitContainerStatuses[i].Name] = &p.Status.InitContainerStatuses[i]
	}
	return idx
}

// perContainerRequests is 1.32-1.36's AggregateContainerRequests: each
// container's effective request (from status only when it reports
// status.resources), sidecars likewise, regular init containers by spec.
func (m SchedModel) perContainerRequests(p *corev1.Pod) corev1.ResourceList {
	idx := m.statusIndex(p)
	reqs := corev1.ResourceList{}
	for _, c := range p.Spec.Containers {
		r := c.Resources.Requests
		if m.status {
			if cs, ok := idx[c.Name]; ok && cs.Resources != nil {
				r = effective(p, c.Resources.Requests, cs.Resources.Requests, cs.AllocatedResources)
			}
		}
		addResourceList(reqs, r)
	}
	restartable, inits := corev1.ResourceList{}, corev1.ResourceList{}
	for _, c := range p.Spec.InitContainers {
		r := c.Resources.Requests
		sidecar := c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
		if m.status && sidecar {
			if cs, ok := idx[c.Name]; ok && cs.Resources != nil {
				r = effective(p, c.Resources.Requests, cs.Resources.Requests, cs.AllocatedResources)
			}
		}
		if sidecar {
			addResourceList(reqs, r)
			addResourceList(restartable, r)
			r = restartable
		} else {
			tmp := corev1.ResourceList{}
			addResourceList(tmp, r)
			addResourceList(tmp, restartable)
			r = tmp
		}
		maxResourceList(inits, r)
	}
	maxResourceList(reqs, inits)
	return reqs
}

// aggregateRequests is 1.37's AggregateContainerRequests: three pod
// totals (spec, allocated, actuated) by the init/sidecar formula, then
// their per-resource maximum; Infeasible drops spec. Pod-level status
// maps, when both are reported, replace the allocated and actuated totals.
func (m SchedModel) aggregateRequests(p *corev1.Pod) corev1.ResourceList {
	infeasible := resizeInfeasible(p)
	spec := m.aggregateBy(p, infeasible, func(c *corev1.Container, _ *corev1.ContainerStatus, _ bool) corev1.ResourceList {
		return c.Resources.Requests
	})
	var allocated, actuated corev1.ResourceList
	if m.podLevelStatus && p.Status.AllocatedResources != nil && p.Status.Resources != nil && p.Status.Resources.Requests != nil {
		allocated, actuated = p.Status.AllocatedResources, p.Status.Resources.Requests
	} else {
		allocated = m.aggregateBy(p, infeasible, func(c *corev1.Container, cs *corev1.ContainerStatus, inf bool) corev1.ResourceList {
			switch {
			case cs != nil && cs.AllocatedResources != nil:
				return cs.AllocatedResources
			case inf:
				return nil
			}
			return c.Resources.Requests
		})
		actuated = m.aggregateBy(p, infeasible, func(c *corev1.Container, cs *corev1.ContainerStatus, inf bool) corev1.ResourceList {
			switch {
			case cs != nil && cs.Resources != nil && cs.Resources.Requests != nil:
				return cs.Resources.Requests
			case cs != nil && cs.AllocatedResources != nil:
				return cs.AllocatedResources
			case inf:
				return nil
			}
			return c.Resources.Requests
		})
	}
	if infeasible {
		return maxList(actuated, allocated)
	}
	return maxList(spec, actuated, allocated)
}

func (m SchedModel) aggregateBy(p *corev1.Pod, infeasible bool, get func(*corev1.Container, *corev1.ContainerStatus, bool) corev1.ResourceList) corev1.ResourceList {
	find := func(name string) *corev1.ContainerStatus {
		if !m.status {
			return nil
		}
		for i := range p.Status.ContainerStatuses {
			if p.Status.ContainerStatuses[i].Name == name {
				return &p.Status.ContainerStatuses[i]
			}
		}
		for i := range p.Status.InitContainerStatuses {
			if p.Status.InitContainerStatuses[i].Name == name {
				return &p.Status.InitContainerStatuses[i]
			}
		}
		return nil
	}
	result := corev1.ResourceList{}
	for i := range p.Spec.Containers {
		c := &p.Spec.Containers[i]
		addResourceList(result, get(c, find(c.Name), infeasible))
	}
	restartable, inits := corev1.ResourceList{}, corev1.ResourceList{}
	for i := range p.Spec.InitContainers {
		c := &p.Spec.InitContainers[i]
		r := get(c, find(c.Name), infeasible)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			addResourceList(result, r)
			addResourceList(restartable, r)
			r = restartable
		} else {
			tmp := corev1.ResourceList{}
			addResourceList(tmp, r)
			addResourceList(tmp, restartable)
			r = tmp
		}
		maxResourceList(inits, r)
	}
	maxResourceList(result, inits)
	return result
}

// resizeInfeasible mirrors IsPodResizeInfeasible: the first
// PodResizePending condition decides, whatever its status.
func resizeInfeasible(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodResizePending {
			return c.Reason == corev1.PodReasonInfeasible
		}
	}
	return false
}

func podLevelResource(name corev1.ResourceName) bool {
	return name == corev1.ResourceCPU || name == corev1.ResourceMemory || strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix)
}

func podLevelRequestsSet(p *corev1.Pod) bool {
	if p.Spec.Resources == nil {
		return false
	}
	for name := range p.Spec.Resources.Requests {
		if podLevelResource(name) {
			return true
		}
	}
	return false
}

// maxList is upstream's max(a, b...): a copy of a (or empty), raised by
// each of b per resource.
func maxList(a corev1.ResourceList, b ...corev1.ResourceList) corev1.ResourceList {
	var out corev1.ResourceList
	if a != nil {
		out = a.DeepCopy()
	} else {
		out = corev1.ResourceList{}
	}
	for _, o := range b {
		maxResourceList(out, o)
	}
	return out
}

func addResourceList(list, add corev1.ResourceList) {
	for name, q := range add {
		if v, ok := list[name]; !ok {
			list[name] = q.DeepCopy()
		} else {
			v.Add(q)
			list[name] = v
		}
	}
}

func maxResourceList(list, other corev1.ResourceList) {
	for name, q := range other {
		if v, ok := list[name]; !ok || q.Cmp(v) > 0 {
			list[name] = q.DeepCopy()
		}
	}
}

// cpuMem reads cores and bytes from a request list.
func cpuMem(l corev1.ResourceList) (float64, float64) {
	cpu, mem := l[corev1.ResourceCPU], l[corev1.ResourceMemory]
	return cpu.AsApproximateFloat64(), mem.AsApproximateFloat64()
}
