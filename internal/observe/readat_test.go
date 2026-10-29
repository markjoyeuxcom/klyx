package observe

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Settings S3a §3.2: a share of a read made earlier for several readers
// keeps that read's time - its freshness, and its staleness, follow it.

func readAtHarness(t *testing.T, at func() time.Time) *harness {
	snap := SnapshotSpec{Name: "alerts", Scope: "this cluster", Cadence: 30 * time.Second, Timeout: 30 * time.Second,
		ReadKeyedAt: func(context.Context, string) ([]any, bool, time.Time, error) { return []any{"a1"}, false, at(), nil }}
	eval := func(c Capture) PartResult { n := len(c.Objects["alerts"]); return PartResult{Evidence: &n} }
	return start(t, Config{Snapshots: []SnapshotSpec{snap}, Domains: []DomainSpec{{ID: "alerts", Parts: []PartSpec{
		{ID: "alerts", Classifier: "alerts", Counted: []string{"alerts"}, Evaluate: eval}}}}})
}

func TestASharedReadKeepsItsTime(t *testing.T) {
	var mu sync.Mutex
	var acquired time.Time
	h := readAtHarness(t, func() time.Time { mu.Lock(); defer mu.Unlock(); return acquired })
	mu.Lock()
	acquired = h.clk.Now().Add(-10 * time.Second)
	want := acquired
	mu.Unlock()
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool {
		return p.Coverage == Live && p.AsOf != nil && !p.AsOf.After(want.Add(time.Second))
	}), "live as of the shared read's time, not the time it landed")
}

func TestASharedReadAlreadyOverdueIsStale(t *testing.T) {
	var mu sync.Mutex
	var acquired time.Time
	h := readAtHarness(t, func() time.Time { mu.Lock(); defer mu.Unlock(); return acquired })
	mu.Lock()
	acquired = h.clk.Now().Add(-2 * time.Minute) // older than twice the cadence
	mu.Unlock()
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Stale }), "an overdue share is stale")
	// and never live first: no summary published the read as live
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	for _, sum := range h.sink.sums {
		for _, task := range sum.Tasks {
			for _, p := range task.Parts {
				if task.ID == "alerts" && p.ID == "alerts" && p.Coverage == Live {
					t.Fatalf("an overdue share was published live first: %+v", p)
				}
			}
		}
	}
}

// A zero time is the time the read landed, as ReadKeyed's.
func TestAZeroReadTimeIsTheLandingTime(t *testing.T) {
	h := readAtHarness(t, func() time.Time { return time.Time{} })
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Live && p.AsOf != nil }), "live")
}
