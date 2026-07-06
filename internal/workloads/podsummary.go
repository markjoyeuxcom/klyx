package workloads

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodSummary is the pods-lens row: one pod classified with the same severity
// engine the workloads lens uses (one interpretation of "broken", never two).
type PodSummary struct {
	Namespace, Name string
	Ready           bool
	Phase           string // pod phase as reported (Running/Pending/Succeeded/Failed/Unknown)
	Reason          string // worst container/pod reason, "" if clean
	Rank            HealthRank
	Restarts        int
	Node, IP        string
	OwnerKind       string // top controller ownerRef kind ("Deployment" is NOT resolved here — ReplicaSet/StatefulSet/DaemonSet/Job/Node/"")
	OwnerName       string
	AgeSeconds      int
	Containers      []ContainerSummary
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
		reason, sev := worstPodReason([]corev1.Pod{*p})
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
			Containers: buildContainerSummaries(p),
		}

		// Owner: first ownerRef where Controller is set and true.
		for _, ref := range p.OwnerReferences {
			if ref.Controller != nil && *ref.Controller {
				ps.OwnerKind = ref.Kind
				ps.OwnerName = ref.Name
				break
			}
		}

		out = append(out, ps)
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Rank != out[b].Rank {
			return out[a].Rank < out[b].Rank
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
