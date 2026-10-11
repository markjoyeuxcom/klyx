package healthsrc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"

	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/observe"
)

func startHelm(t *testing.T, clk *testingclock.FakeClock, d HelmDeps) *sink {
	t.Helper()
	snaps, domains := Helm(d)
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Clock: clk, Sink: s, Snapshots: snaps, Domains: domains, Throttle: time.Millisecond, InitialSyncTimeout: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	return s
}

// stepUntil advances the fake clock in small steps until cond holds.
func stepUntil(t *testing.T, clk *testingclock.FakeClock, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(4 * time.Millisecond) {
		if cond() {
			return
		}
		clk.Step(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func TestHelmTaskCountsTheLensRuleAndCrossesTheDeadlineWithoutARead(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clk := testingclock.NewFakeClock(t0)
	var reads atomic.Int32
	list := func(context.Context) ([]helmcli.Release, error) {
		reads.Add(1)
		return []helmcli.Release{
			{Namespace: "shop", Name: "web", Status: "deployed", UpdatedUnix: t0.Unix() - 3600},
			{Namespace: "shop", Name: "api", Status: "failed", UpdatedUnix: t0.Unix() - 60},
			{Namespace: "infra", Name: "ingress", Status: "pending-upgrade", UpdatedUnix: t0.Unix() - 60},
		}, nil
	}
	s := startHelm(t, clk, HelmDeps{Cluster: "c1", Detect: func() bool { return true }, List: list})
	part := func() observe.Part { p, _ := nativePart(s, TaskHelm, "releases"); return p }
	stepUntil(t, clk, "the first read is live: only the failed release is included", func() bool {
		p := part()
		return p.Coverage == observe.Live && p.Native != nil && p.Native.Total == 3 && p.Native.Included == 1 &&
			p.Native.Roles.Progressing == 1 && p.Top[0].Name == "api" && p.Top[0].Label == "failed" && p.Top[0].GVR == ""
	})
	boundary := time.Unix(t0.Unix()-60+601, 0)
	if p := part(); p.Next == nil || !p.Next.Equal(boundary) {
		t.Fatalf("part Next = %v, want the pending deadline %v", p.Next, boundary)
	}
	// Just short of the deadline nothing has moved; at it, the pending
	// release is stuck. Reads may happen meanwhile, but they return the
	// same data: the step is the reclassification timer's.
	clk.SetTime(boundary.Add(-time.Second))
	time.Sleep(50 * time.Millisecond)
	if p := part(); p.Native.Included != 1 {
		t.Fatalf("crossed early: %+v", p.Native)
	}
	readsBefore := reads.Load()
	clk.SetTime(boundary)
	stepUntil(t, clk, "the pending release appears stuck at its deadline", func() bool {
		p := part()
		return p.Native.Included == 2 && p.Native.Roles.Degraded == 1 && p.Next == nil
	})
	if reads.Load() != readsBefore {
		t.Fatalf("the deadline crossing needed a read (%d -> %d)", readsBefore, reads.Load())
	}
}

func TestHelmCLIMissingIsUnreadableNeverAbsent(t *testing.T) {
	clk := testingclock.NewFakeClock(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	var listed atomic.Bool
	s := startHelm(t, clk, HelmDeps{Cluster: "c1", Detect: func() bool { return false },
		List: func(context.Context) ([]helmcli.Release, error) { listed.Store(true); return nil, nil }})
	stepUntil(t, clk, "unreadable with class cliMissing", func() bool {
		tk, ok := task(s, TaskHelm)
		if !ok || len(tk.Parts) != 1 || len(tk.Parts[0].Kinds) != 1 {
			return false
		}
		k := tk.Parts[0].Kinds[0]
		return tk.Coverage == observe.Unreadable && k.Coverage == observe.Unreadable && k.Class == observe.ClassCLIMissing &&
			tk.Parts[0].Native != nil && tk.Parts[0].Native.Total == 0 // no evidence; consumers read coverage, never a zero
	})
	if listed.Load() {
		t.Fatal("helm list ran without a resolvable helm CLI")
	}
}
