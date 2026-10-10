package health

import (
	"testing"
	"time"
)

func healthyPod(name string) OwnedPod {
	return OwnedPod{Namespace: "ns", Name: name, UID: name, Verdict: Verdict{Kind: OK, Label: "Running"}}
}

func faultPod(name, label string, rank int) OwnedPod {
	return OwnedPod{Namespace: "ns", Name: name, UID: name, Verdict: Verdict{Kind: Fault, Label: label, Reason: label, Rank: rank}}
}

func deploy(mut func(*WorkloadInput)) WorkloadInput {
	in := WorkloadInput{
		Kind: "Deployment", Generation: 2, ObservedGeneration: 2,
		Desired: 3, Ready: 3, Updated: 3, Strategy: StrategyRolling,
		Progressing:      &Condition{Status: "True", Reason: "NewReplicaSetAvailable", LastUpdateTime: t0},
		ProgressDeadline: 600 * time.Second,
		Pods:             []OwnedPod{healthyPod("a"), healthyPod("b"), healthyPod("c")},
		PodsComplete:     true,
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func sts(mut func(*WorkloadInput)) WorkloadInput {
	in := deploy(nil)
	in.Kind, in.Progressing, in.ProgressDeadline = "StatefulSet", nil, 0
	if mut != nil {
		mut(&in)
	}
	return in
}

func ds(mut func(*WorkloadInput)) WorkloadInput {
	in := sts(nil)
	in.Kind = "DaemonSet"
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestWorkloadTable(t *testing.T) {
	cases := []struct {
		name  string
		in    WorkloadInput
		now   time.Time
		kind  Kind
		label string
		rule  string
	}{
		{"healthy deployment", deploy(nil), t0, OK, "Available", "workload.ready"},
		{"generation lag with a current hard pod failure", deploy(func(in *WorkloadInput) {
			in.Generation = 3
			in.Pods[1] = faultPod("b", "CrashLoopBackOff", 70)
		}), t0, Fault, "CrashLoopBackOff", "workload.pod-failure"},
		{"worst pod chosen by rank then name", deploy(func(in *WorkloadInput) {
			in.Pods = []OwnedPod{faultPod("z", "Unschedulable", 30), faultPod("b", "ErrImagePull", 50), faultPod("a", "ErrImagePull", 50)}
		}), t0, Fault, "ErrImagePull", "workload.pod-failure"},
		{"replica failure", deploy(func(in *WorkloadInput) {
			in.ReplicaFailure = &Condition{Status: "True", Reason: "FailedCreate", Message: "quota"}
		}), t0, Fault, "FailedCreate", "workload.replica-failure"},
		{"stuck rollout: controller deadline exceeded", deploy(func(in *WorkloadInput) {
			in.Updated, in.Ready = 1, 2
			in.Progressing = &Condition{Status: "False", Reason: "ProgressDeadlineExceeded", LastUpdateTime: t0}
		}), t0, Fault, "ProgressDeadlineExceeded", "workload.progress-failed"},
		{"explicitly zero-scaled deployment", deploy(func(in *WorkloadInput) { in.Desired, in.Ready, in.Updated, in.Pods = 0, 0, 0, nil }),
			t0, Paused, "Scaled to 0", "workload.scaled-to-zero"},
		{"zero-eligible DaemonSet, current status", ds(func(in *WorkloadInput) { in.Desired, in.Ready, in.Updated, in.Pods = 0, 0, 0, nil }),
			t0, OK, "0 eligible nodes", "workload.ds-zero-eligible"},
		{"zero DaemonSet on stale status is not proof", ds(func(in *WorkloadInput) {
			in.Desired, in.Ready, in.Updated, in.Pods = 0, 0, 0, nil
			in.Generation = 3
		}), t0, Unknown, "Not yet scheduled", "workload.ds-zero-unobserved"},
		{"never-observed generation", deploy(func(in *WorkloadInput) { in.ObservedGeneration, in.Generation = 0, 1 }),
			t0, Unknown, "Not yet observed", "workload.never-observed"},
		{"new healthy rollout", deploy(func(in *WorkloadInput) {
			in.Updated = 1
			in.Progressing = &Condition{Status: "True", Reason: "ReplicaSetUpdated", LastUpdateTime: t0}
		}), t0.Add(time.Minute), Progressing, "Rolling out · 1/3 updated", "workload.rollout"},
		{"all-ready old replicas but updated < target", deploy(func(in *WorkloadInput) {
			in.Updated = 0
			in.Progressing = &Condition{Status: "True", Reason: "ReplicaSetUpdated", LastUpdateTime: t0}
		}), t0, Progressing, "Rolling out · 0/3 updated", "workload.rollout"},
		{"stalled rollout the controller never marks", deploy(func(in *WorkloadInput) {
			in.Updated, in.Ready = 1, 2
			in.Progressing = &Condition{Status: "True", Reason: "ReplicaSetUpdated", LastUpdateTime: t0}
		}), t0.Add(600*time.Second + RolloutStallGrace), Degraded, "Rollout stalled", "workload.rollout-stalled"},
		{"rollout with no progress condition and a shortfall", deploy(func(in *WorkloadInput) {
			in.Updated, in.Ready, in.Progressing = 1, 1, nil
		}), t0, Unknown, "Rolling out · 1/3 updated", "workload.rollout-unanchored"},
		{"desired > 0 with zero owned pods", deploy(func(in *WorkloadInput) { in.Ready, in.Pods = 0, nil }),
			t0, Fault, "0/3 ready", "workload.none-ready"},
		{"pod churn never resets an STS rollout into progressing while short", sts(func(in *WorkloadInput) {
			in.Updated, in.Ready, in.RevisionDrift = 1, 2, true
		}), t0, Degraded, "Rolling out · 2/3 ready", "workload.rollout-short"},
		{"STS rollout fully serving", sts(func(in *WorkloadInput) { in.Updated, in.RevisionDrift = 1, true }),
			t0, Progressing, "Rolling out · 1/3 updated", "workload.rollout-serving"},
		{"healthy OnDelete revision drift", sts(func(in *WorkloadInput) {
			in.Strategy, in.Updated, in.RevisionDrift = StrategyOnDelete, 0, true
		}), t0, OK, "Ready", "workload.ready"},
		{"unready OnDelete without hard reason", sts(func(in *WorkloadInput) {
			in.Strategy, in.Updated, in.Ready, in.RevisionDrift = StrategyOnDelete, 0, 2, true
		}), t0, Degraded, "2/3 ready", "workload.partially-ready"},
		{"healthy partition p >= replicas", sts(func(in *WorkloadInput) {
			in.Partition, in.Updated, in.RevisionDrift = 3, 0, true
		}), t0, OK, "Ready", "workload.ready"},
		{"partial-ready satisfied partition", sts(func(in *WorkloadInput) {
			in.Partition, in.Updated, in.Ready, in.RevisionDrift = 2, 1, 2, true
		}), t0, Degraded, "2/3 ready", "workload.partially-ready"},
		{"unsatisfied partition is an active rollout", sts(func(in *WorkloadInput) {
			in.Partition, in.Updated, in.RevisionDrift = 1, 1, true
		}), t0, Progressing, "Rolling out · 1/2 updated", "workload.rollout-serving"},
		{"DaemonSet OnDelete drift", ds(func(in *WorkloadInput) {
			in.Strategy, in.Updated, in.RevisionDrift = StrategyOnDelete, 1, true
		}), t0, OK, "Ready", "workload.ready"},
		{"paused Deployment with missing readiness still reports the outage", deploy(func(in *WorkloadInput) {
			in.Paused, in.Ready, in.Updated = true, 0, 1
		}), t0, Fault, "0/3 ready", "workload.none-ready"},
		{"incomplete pod evidence never reads healthy", deploy(func(in *WorkloadInput) { in.PodsComplete = false }),
			t0, Unknown, "Available", "workload.pods-incomplete"},
		{"incomplete evidence still reports a known fault", deploy(func(in *WorkloadInput) {
			in.PodsComplete = false
			in.Pods[0] = faultPod("a", "ErrImagePull", 50)
		}), t0, Fault, "ErrImagePull", "workload.pod-failure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Workload(c.in, c.now)
			if v.Kind != c.kind || v.Label != c.label || v.Rule != c.rule {
				t.Fatalf("got %s %q (%s), want %s %q (%s)", v.Kind, v.Label, v.Rule, c.kind, c.label, c.rule)
			}
		})
	}
}

func TestWorkloadNotes(t *testing.T) {
	cases := map[string]struct {
		in   WorkloadInput
		note string
	}{
		"OnDelete":  {sts(func(in *WorkloadInput) { in.Strategy, in.Updated, in.RevisionDrift = StrategyOnDelete, 0, true }), "update pending (OnDelete)"},
		"partition": {sts(func(in *WorkloadInput) { in.Partition, in.Updated, in.RevisionDrift = 3, 0, true }), "partitioned at 3"},
		"paused":    {deploy(func(in *WorkloadInput) { in.Paused = true }), "rollouts paused"},
		"paused with pending changes": {deploy(func(in *WorkloadInput) {
			in.Paused, in.Generation = true, 3
		}), "rollout paused with changes pending"},
	}
	for name, c := range cases {
		if v := Workload(c.in, t0); v.Note != c.note {
			t.Errorf("%s: note %q, want %q", name, v.Note, c.note)
		}
	}
}

func TestWorkloadRolloutBoundary(t *testing.T) {
	in := deploy(func(in *WorkloadInput) {
		in.Updated = 1
		in.Progressing = &Condition{Status: "True", Reason: "ReplicaSetUpdated", LastUpdateTime: t0}
	})
	v := Workload(in, t0)
	if v.ReclassifyAt == nil || !v.ReclassifyAt.Equal(t0.Add(in.ProgressDeadline+RolloutStallGrace)) {
		t.Fatalf("ReclassifyAt = %v", v.ReclassifyAt)
	}
	if got := Workload(in, *v.ReclassifyAt).Kind; got != Degraded {
		t.Fatalf("at boundary got %s, want degraded", got)
	}
}
