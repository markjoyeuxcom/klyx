package workloads

import (
	"sort"
	"time"

	"github.com/moomora/klyx/internal/health"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// PodSummary is the pods-lens row: one pod classified with the same severity
// engine the workloads lens uses (one interpretation of "broken", never two).
type PodSummary struct {
	Namespace, Name string
	UID             types.UID // object identity: a same-name recreation is a different pod
	Generation      int64
	Ready           bool
	Phase           string // pod phase as reported (Running/Pending/Succeeded/Failed/Unknown)
	Reason          string // worst container/pod reason, "" if clean
	Rank            HealthRank
	Restarts        int
	Node, IP        string
	OwnerKind       string // top controller ownerRef kind ("Deployment" is NOT resolved here — ReplicaSet/StatefulSet/DaemonSet/Job/Node/"")
	OwnerName       string
	OwnerUID        types.UID // the controller ownerRef's UID; ancestry joins by UID, never by name
	AgeSeconds      int       // age at the summary's now
	Created         time.Time // creation time: the anchor a live view ticks the age from
	Containers      []ContainerSummary
	// SchedCPU/SchedMem are the scheduler-effective requests (cores, bytes):
	// what the scheduler reserves on a node for this pod (podRequests).
	SchedCPU, SchedMem float64
	// Health is the shared verdict (internal/health); Rank/Reason are legacy
	// and stay only until every consumer has migrated.
	Health health.Verdict
}

// ContainerSummary is a single container row within a PodSummary.
type ContainerSummary struct {
	Name     string
	Image    string
	Ready    bool
	Restarts int
	State    string // "running" | "waiting:<Reason>" | "terminated:<Reason>" | ""
	Init     bool
	Ports    []ContainerPort // declared spec ports (may be empty - not all containers declare them)
	// Last termination (ContainerStatus.LastTerminationState.Terminated):
	// why the previous run died ("OOMKilled") without opening the YAML.
	// Zero values when the container has never terminated.
	LastReason       string
	LastExitCode     int
	LastFinishedUnix int64 // FinishedAt.Unix(), 0 when absent
	// Declared resources from the container spec (cpu cores, memory bytes).
	// nil = not set on the spec - the rightsizing delta needs "unset" and "0"
	// to stay distinguishable.
	CPURequest *float64
	CPULimit   *float64
	MemRequest *float64
	MemLimit   *float64
}

// ContainerPort is one declared port on a container spec. Port-forward
// suggestions are built from these so the user never has to open the YAML to
// find a containerPort.
type ContainerPort struct {
	Name     string // spec port name ("http", "metrics"), "" when unnamed
	Port     int    // containerPort
	Protocol string // "TCP" | "UDP" | "SCTP" (defaulted to TCP when unset)
}

// rankPod maps a single pod's state to a HealthRank.
//
// Rank semantics (ordered by precedence):
//   - phase Succeeded → Healthy  (a completed pod is not broken)
//   - sev==sevHard OR phase==Failed → Unhealthy
//   - not ready AND phase != Succeeded → Degraded (pending/initialising/not-ready)
//   - recentlyTerminated → Restarts (info tier, recency-gated)
//   - else → Healthy
func rankPod(p *corev1.Pod, sev severity, recent bool) HealthRank {
	if p.Status.Phase == corev1.PodSucceeded {
		return Healthy
	}
	if sev == sevHard || p.Status.Phase == corev1.PodFailed {
		return Unhealthy
	}
	if !podReady(p) {
		return Degraded
	}
	if recent {
		return Restarts
	}
	return Healthy
}

// SummarizePods classifies each pod using the shared severity engine and
// returns them sorted by Rank (worst first), then Namespace, then Name.
func SummarizePods(pods []corev1.Pod, now time.Time) []PodSummary {
	out := make([]PodSummary, 0, len(pods))
	for i := range pods {
		p := &pods[i]
		reason, sev := worstPodReasonOf([]*corev1.Pod{p})
		recent := recentlyTerminated([]*corev1.Pod{p}, now)
		rank := rankPod(p, sev, recent)

		// Succeeded pods with no reason string get the conventional "Completed" label.
		if p.Status.Phase == corev1.PodSucceeded && reason == "" {
			reason = "Completed"
		}

		ps := PodSummary{
			Namespace:  p.Namespace,
			Name:       p.Name,
			Ready:      podReady(p),
			Phase:      string(p.Status.Phase),
			Reason:     reason,
			Rank:       rank,
			Restarts:   podRestarts(p),
			Node:       p.Spec.NodeName,
			IP:         p.Status.PodIP,
			AgeSeconds: ageSeconds(p.CreationTimestamp.Time, now),
			Created:    p.CreationTimestamp.Time,
			Containers: buildContainerSummaries(p),
			Health:     health.Pod(p, now),
			SchedCPU:   podRequest(p, corev1.ResourceCPU),
			SchedMem:   podRequest(p, corev1.ResourceMemory),
			UID:        p.UID,
			Generation: p.Generation,
		}

		// Owner: first ownerRef where Controller is set and true.
		for _, ref := range p.OwnerReferences {
			if ref.Controller != nil && *ref.Controller {
				ps.OwnerKind = ref.Kind
				ps.OwnerName = ref.Name
				ps.OwnerUID = ref.UID
				break
			}
		}

		out = append(out, ps)
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Health.Kind != out[b].Health.Kind {
			return out[a].Health.Kind < out[b].Health.Kind
		}
		if out[a].Health.Rank != out[b].Health.Rank {
			return out[a].Health.Rank > out[b].Health.Rank
		}
		if out[a].Namespace != out[b].Namespace {
			return out[a].Namespace < out[b].Namespace
		}
		return out[a].Name < out[b].Name
	})

	return out
}

// buildContainerSummaries returns init containers first (Init:true) then regular
// containers. A spec container with no matching ContainerStatus still appears
// (state ""). Statuses are matched to spec containers by name.
func buildContainerSummaries(p *corev1.Pod) []ContainerSummary {
	// Index statuses by container name for O(1) lookup.
	initStatuses := make(map[string]corev1.ContainerStatus, len(p.Status.InitContainerStatuses))
	for _, cs := range p.Status.InitContainerStatuses {
		initStatuses[cs.Name] = cs
	}
	mainStatuses := make(map[string]corev1.ContainerStatus, len(p.Status.ContainerStatuses))
	for _, cs := range p.Status.ContainerStatuses {
		mainStatuses[cs.Name] = cs
	}

	var out []ContainerSummary

	for _, c := range p.Spec.InitContainers {
		cs, ok := initStatuses[c.Name]
		sum := ContainerSummary{
			Name:  c.Name,
			Image: c.Image,
			Init:  true,
		}
		applyContainerResources(&sum, c)
		if ok {
			sum.Ready = cs.Ready
			sum.Restarts = int(cs.RestartCount)
			sum.State = containerStateString(cs.State)
			applyLastTermination(&sum, cs)
		}
		out = append(out, sum)
	}

	for _, c := range p.Spec.Containers {
		cs, ok := mainStatuses[c.Name]
		sum := ContainerSummary{
			Name:  c.Name,
			Image: c.Image,
			Init:  false,
			Ports: containerPorts(c.Ports),
		}
		applyContainerResources(&sum, c)
		if ok {
			sum.Ready = cs.Ready
			sum.Restarts = int(cs.RestartCount)
			sum.State = containerStateString(cs.State)
			applyLastTermination(&sum, cs)
		}
		out = append(out, sum)
	}

	return out
}

// containerPorts maps spec ports onto the summary shape. Init containers are
// skipped by the caller (they never serve traffic); protocol defaults to TCP
// per the API convention.
// podRequest is the scheduler-effective request for one resource, the
// kube-scheduler's podRequests rule (Codex review 2026-10-13: init requests
// were skipped, so a 4-core init container "fit" a 2-core node):
//   - the app containers' sum, plus every restartable init container
//     (sidecar), which keeps running beside them;
//   - at least the init peak: each regular init container runs alone
//     beside the sidecars started before it;
//   - pod-level requests replace the container sum when set;
//   - plus the RuntimeClass overhead.
func podRequest(p *corev1.Pod, name corev1.ResourceName) float64 {
	q := func(l corev1.ResourceList) float64 {
		if v, ok := l[name]; ok {
			return v.AsApproximateFloat64()
		}
		return 0
	}
	var apps float64
	for _, c := range p.Spec.Containers {
		apps += q(c.Resources.Requests)
	}
	var sidecars, initPeak float64
	for _, c := range p.Spec.InitContainers {
		r := q(c.Resources.Requests)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars += r
			initPeak = max(initPeak, sidecars)
			continue
		}
		initPeak = max(initPeak, sidecars+r)
	}
	total := max(apps+sidecars, initPeak)
	if p.Spec.Resources != nil {
		if v, ok := p.Spec.Resources.Requests[name]; ok {
			total = v.AsApproximateFloat64()
		}
	}
	return total + q(p.Spec.Overhead)
}

// applyContainerResources copies the spec's declared cpu/memory requests and
// limits (cores, bytes). Absent quantities stay nil - never a fabricated zero.
func applyContainerResources(sum *ContainerSummary, c corev1.Container) {
	if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
		v := q.AsApproximateFloat64()
		sum.CPURequest = &v
	}
	if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
		v := q.AsApproximateFloat64()
		sum.CPULimit = &v
	}
	if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
		v := q.AsApproximateFloat64()
		sum.MemRequest = &v
	}
	if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
		v := q.AsApproximateFloat64()
		sum.MemLimit = &v
	}
}

func containerPorts(ports []corev1.ContainerPort) []ContainerPort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]ContainerPort, 0, len(ports))
	for _, p := range ports {
		proto := string(p.Protocol)
		if proto == "" {
			proto = "TCP"
		}
		out = append(out, ContainerPort{Name: p.Name, Port: int(p.ContainerPort), Protocol: proto})
	}
	return out
}

// applyLastTermination copies the previous run's termination record onto the
// summary. Only Terminated carries data (the API leaves the other members nil
// for a prior run); everything stays zero when the container never terminated,
// so the UI can never fabricate a "last state".
func applyLastTermination(sum *ContainerSummary, cs corev1.ContainerStatus) {
	t := cs.LastTerminationState.Terminated
	if t == nil {
		return
	}
	sum.LastReason = t.Reason
	sum.LastExitCode = int(t.ExitCode)
	if !t.FinishedAt.IsZero() {
		sum.LastFinishedUnix = t.FinishedAt.Unix()
	}
}

// containerStateString encodes a ContainerState as the canonical state string.
// Returns "running", "waiting:<Reason>", "terminated:<Reason>", or "".
func containerStateString(s corev1.ContainerState) string {
	if s.Running != nil {
		return "running"
	}
	if s.Waiting != nil {
		return "waiting:" + s.Waiting.Reason
	}
	if s.Terminated != nil {
		return "terminated:" + s.Terminated.Reason
	}
	return ""
}

// ResolveReplicaSetOwners rewrites each pod's ReplicaSet owner to that
// ReplicaSet's own controller (Deployment, Argo Rollout, ...). controllers
// maps a ReplicaSet UID to its controller reference: the join is by the
// pod's owner UID, so a recreated same-name ReplicaSet never adopts another
// ReplicaSet's pods. Pods under a bare ReplicaSet keep OwnerKind=ReplicaSet.
func ResolveReplicaSetOwners(sums []PodSummary, controllers map[types.UID]metav1.OwnerReference) {
	for i := range sums {
		if sums[i].OwnerKind != "ReplicaSet" || sums[i].OwnerUID == "" {
			continue
		}
		if ref, ok := controllers[sums[i].OwnerUID]; ok {
			sums[i].OwnerKind, sums[i].OwnerName, sums[i].OwnerUID = ref.Kind, ref.Name, ref.UID
		}
	}
}
