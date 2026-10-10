package health

import (
	"fmt"
	"sort"
	"time"
)

// Strategy is a controller's update strategy.
type Strategy string

const (
	StrategyRolling  Strategy = "RollingUpdate"
	StrategyRecreate Strategy = "Recreate"
	StrategyOnDelete Strategy = "OnDelete"
)

// Condition is the subset of a controller condition the evaluator reads.
type Condition struct {
	Status         string // "True" | "False" | "Unknown"
	Reason         string
	Message        string
	LastUpdateTime time.Time
}

// OwnedPod is one pod attributed to a workload, already classified by Pod.
type OwnedPod struct {
	Namespace, Name, UID string
	Verdict              Verdict
}

// WorkloadInput is everything the workload evaluator reads. It is built by
// the caller from the typed controller object; the evaluator stays pure.
type WorkloadInput struct {
	Kind               string // Deployment | StatefulSet | DaemonSet
	Generation         int64
	ObservedGeneration int64
	// Desired is spec.replicas (Deployment/StatefulSet, nil defaulted to 1)
	// or status.desiredNumberScheduled (DaemonSet).
	Desired   int
	Ready     int
	Available int // Deployment status.availableReplicas (honours minReadySeconds)
	Updated   int
	// Strategy and Partition (StatefulSet RollingUpdate partition; 0 if none).
	Strategy  Strategy
	Partition int
	// OrdinalStart is StatefulSet spec.ordinals.start (0 when unset): the
	// replicas are ordinals [OrdinalStart, OrdinalStart+Desired).
	OrdinalStart int
	// RevisionDrift: the controller reports pods on an older revision
	// (StatefulSet currentRevision != updateRevision; DaemonSet
	// updatedNumberScheduled < desiredNumberScheduled).
	RevisionDrift bool
	// Paused is Deployment spec.paused (rollouts held by the operator).
	Paused bool
	// Deployment conditions; nil when absent.
	ReplicaFailure *Condition
	Progressing    *Condition
	// ProgressDeadline is Deployment spec.progressDeadlineSeconds.
	ProgressDeadline time.Duration
	// Pods are the pods attributed to this workload by owner reference.
	Pods []OwnedPod
	// PodsComplete is false when the pod observation behind Pods may be
	// missing members (truncated or unreadable list). A workload is never
	// reported healthy on incomplete evidence.
	PodsComplete bool
}

// Workload classifies one controller (spec rows W1-W10, first match wins;
// precedence: current hard failure > explicit controller failure >
// intentional zero > controller not yet observing > active rollout >
// serving shortfall > ok).
func Workload(in WorkloadInput, now time.Time) Verdict {
	v := workloadVerdict(in, now)
	if v.Native == "" {
		v.Native = fmt.Sprintf("%d/%d ready · %d updated · generation %d/%d", in.Ready, in.Desired, in.Updated, in.ObservedGeneration, in.Generation)
		if in.Kind == "Deployment" {
			v.Native = fmt.Sprintf("%d/%d ready · %d available · %d updated · generation %d/%d", in.Ready, in.Desired, in.Available, in.Updated, in.ObservedGeneration, in.Generation)
		}
	}
	if v.Kind == OK && !in.PodsComplete {
		// The controller's own counts stay visible in Native; the headline
		// must not claim health the evidence cannot support.
		v = Verdict{Kind: Unknown, Label: "Unverified", Reason: "PodsIncomplete", Rule: "workload.pods-incomplete", Native: v.Native,
			Message: "pod or ReplicaSet evidence was truncated or unreadable; healthy cannot be confirmed"}
	}
	return v
}

func workloadVerdict(in WorkloadInput, now time.Time) Verdict {
	// W1: any attributed pod failing now. Never concealed by generation lag
	// or a rollout.
	if p, ok := worstFaultPod(in.Pods); ok {
		msg := p.Namespace + "/" + p.Name
		if p.Verdict.Message != "" {
			msg += " - " + p.Verdict.Message
		}
		return Verdict{Kind: Fault, Label: p.Verdict.Label, Reason: p.Verdict.Reason, Message: msg, Rule: "workload.pod-failure", Rank: p.Verdict.Rank}
	}

	// W2: the Deployment controller says it failed - about the CURRENT spec
	// only; a condition from before the latest generation was observed is
	// history.
	current := in.ObservedGeneration >= in.Generation
	if c := in.ReplicaFailure; current && c != nil && c.Status == "True" {
		return Verdict{Kind: Fault, Label: orDefault(c.Reason, "ReplicaFailure"), Reason: "ReplicaFailure", Message: c.Message, Rule: "workload.replica-failure"}
	}
	if c := in.Progressing; current && c != nil && c.Status == "False" {
		return Verdict{Kind: Fault, Label: orDefault(c.Reason, "ProgressDeadlineExceeded"), Reason: "ProgressFailed", Message: c.Message, Rule: "workload.progress-failed"}
	}

	// W3/W4: zero desired.
	if in.Desired == 0 {
		if in.Kind == "DaemonSet" {
			if in.ObservedGeneration < in.Generation {
				return Verdict{Kind: Unknown, Label: "Not yet scheduled", Reason: "NotObserved", Rule: "workload.ds-zero-unobserved"}
			}
			return Verdict{Kind: OK, Label: "0 eligible nodes", Reason: "NoEligibleNodes", Rule: "workload.ds-zero-eligible"}
		}
		return Verdict{Kind: Paused, Label: "Scaled to 0", Reason: "ScaledToZero", Rule: "workload.scaled-to-zero"}
	}

	// The controller has never observed this object: its status says nothing.
	if in.ObservedGeneration == 0 && in.Generation > 0 {
		return Verdict{Kind: Unknown, Label: "Not yet observed", Reason: "NotObserved", Rule: "workload.never-observed",
			Message: "the controller has not reported status for this workload yet"}
	}

	// W5: an automatic rollout is active.
	if rolloutActive(in) {
		target := updateTarget(in)
		label := fmt.Sprintf("Rolling out · %d/%d updated", in.Updated, target)
		if in.Kind == "Deployment" {
			// The Deployment controller enforces its own progress deadline and
			// flips Progressing=False (W2). Trust it while it is current;
			// without the condition, no trustworthy anchor exists.
			c := activeProgress(in)
			if c == nil {
				if in.Ready >= in.Desired {
					return Verdict{Kind: Progressing, Label: label, Reason: "RollingOut", Rule: "workload.rollout-unanchored-serving"}
				}
				return Verdict{Kind: Unknown, Label: label, Reason: "RolloutUnanchored", Rule: "workload.rollout-unanchored",
					Message: fmt.Sprintf("%d/%d ready and the controller reports no rollout progress", in.Ready, in.Desired)}
			}
			in2, deadline := within(c.LastUpdateTime, in.ProgressDeadline+RolloutStallGrace, now)
			if in2 {
				return Verdict{Kind: Progressing, Label: label, Reason: "RollingOut", Message: c.Message, Rule: "workload.rollout", ReclassifyAt: at(deadline)}
			}
			return Verdict{Kind: Degraded, Label: "Rollout stalled", Reason: "RolloutStalled", Rule: "workload.rollout-stalled",
				Message: fmt.Sprintf("no rollout progress for %s, past the %s progress deadline", short(now.Sub(c.LastUpdateTime)), short(in.ProgressDeadline))}
		}
		// StatefulSet / DaemonSet: no controller deadline and no trustworthy
		// episode anchor in this slice. Fully serving = progressing; serving
		// short = degraded (honest: it IS partially serving), never hidden.
		if in.Ready >= in.Desired {
			return Verdict{Kind: Progressing, Label: label, Reason: "RollingOut", Rule: "workload.rollout-serving"}
		}
		return Verdict{Kind: Degraded, Label: fmt.Sprintf("Rolling out · %d/%d ready", in.Ready, in.Desired), Reason: "RollingOutShort", Rule: "workload.rollout-short"}
	}

	// W8/W9: serving shortfall outside a rollout (incl. OnDelete, partition
	// and paused workloads - their strategy never excuses unready replicas).
	if in.Ready == 0 {
		return Verdict{Kind: Fault, Label: fmt.Sprintf("0/%d ready", in.Desired), Reason: "NoneReady", Rule: "workload.none-ready"}
	}
	if in.Ready < in.Desired {
		return Verdict{Kind: Degraded, Label: fmt.Sprintf("%d/%d ready", in.Ready, in.Desired), Reason: "PartiallyReady", Rule: "workload.partially-ready"}
	}

	// W6/W7/W10: serving fully. A Deployment claims "Available" only from
	// its own availability count (minReadySeconds can hold ready pods back).
	if in.Kind == "Deployment" && in.Available < in.Desired {
		return Verdict{Kind: Progressing, Label: fmt.Sprintf("Ready · %d/%d available", in.Available, in.Desired), Reason: "BecomingAvailable", Rule: "workload.becoming-available",
			Message: "pods are ready but not yet counted available (minReadySeconds not elapsed, or availability status lagging)"}
	}
	v := Verdict{Kind: OK, Reason: "Ready", Rule: "workload.ready", Label: "Ready"}
	if in.Kind == "Deployment" {
		v.Label = "Available"
	}
	switch {
	case in.Paused && (in.Updated < in.Desired || in.ObservedGeneration < in.Generation):
		v.Note = "rollout paused with changes pending"
	case in.Paused:
		v.Note = "rollouts paused"
	case in.Strategy == StrategyOnDelete && in.RevisionDrift:
		v.Note = "update pending (OnDelete)"
	case in.Partition > 0 && in.RevisionDrift:
		v.Note = fmt.Sprintf("partitioned at %d", in.Partition)
	}
	return v
}

// rolloutActive: the controller is (or should be) automatically moving pods.
// OnDelete and satisfied partitions never are; a paused Deployment is not.
func rolloutActive(in WorkloadInput) bool {
	if in.Paused || in.Strategy == StrategyOnDelete {
		return false
	}
	if in.ObservedGeneration < in.Generation {
		return true
	}
	return in.Updated < updateTarget(in)
}

// updateTarget is how many replicas an automatic rollout must update: the
// ordinals in [OrdinalStart, OrdinalStart+Desired) at or above a StatefulSet
// partition (ordinals below it stay on the old revision).
func updateTarget(in WorkloadInput) int {
	hi := in.OrdinalStart + in.Desired
	lo := in.Partition
	if lo < in.OrdinalStart {
		lo = in.OrdinalStart
	}
	t := hi - lo
	if t < 0 {
		return 0
	}
	if t > in.Desired {
		return in.Desired
	}
	return t
}

// activeProgressReasons are the Deployment controller's Progressing=True
// reasons for a rollout in flight. NewReplicaSetAvailable means the LAST
// rollout completed; it can never start a new episode's stall clock.
var activeProgressReasons = map[string]bool{
	"NewReplicaSetCreated": true,
	"FoundNewReplicaSet":   true,
	"ReplicaSetUpdated":    true,
}

// activeProgress returns the Progressing condition only when it is positive
// evidence of the current rollout moving: True, an in-flight reason, a
// timestamp, and about the observed generation. Anything else is unanchored.
func activeProgress(in WorkloadInput) *Condition {
	c := in.Progressing
	if c == nil || c.Status != "True" || !activeProgressReasons[c.Reason] || c.LastUpdateTime.IsZero() {
		return nil
	}
	if in.ObservedGeneration < in.Generation {
		return nil
	}
	return c
}

// worstFaultPod picks the failing pod that headlines a workload, with stable
// tie-breaks so informer order never chooses it.
func worstFaultPod(pods []OwnedPod) (OwnedPod, bool) {
	var faults []OwnedPod
	for _, p := range pods {
		if p.Verdict.Kind == Fault {
			faults = append(faults, p)
		}
	}
	if len(faults) == 0 {
		return OwnedPod{}, false
	}
	sort.SliceStable(faults, func(a, b int) bool {
		if faults[a].Verdict.Rank != faults[b].Verdict.Rank {
			return faults[a].Verdict.Rank > faults[b].Verdict.Rank
		}
		if faults[a].Namespace != faults[b].Namespace {
			return faults[a].Namespace < faults[b].Namespace
		}
		if faults[a].Name != faults[b].Name {
			return faults[a].Name < faults[b].Name
		}
		return faults[a].UID < faults[b].UID
	})
	return faults[0], true
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
