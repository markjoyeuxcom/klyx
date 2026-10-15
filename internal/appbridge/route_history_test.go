package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// The Line slice 7b: rule fingerprints recorded in the observation log, and
// each rule's proven interval read back - baseline, change, gap, gone.

var t0 = time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)

func routeRows(inc uint64, at time.Time, live bool, routes map[string][]string) observe.Rows {
	cov := observe.Live
	if !live {
		cov = observe.Stale
	}
	var rows []healthsrc.Row[observe.Attached]
	for uid, fps := range routes {
		rows = append(rows, healthsrc.Row[observe.Attached]{Ref: healthsrc.RowRef{UID: uid}, Data: observe.Attached{UID: uid, Rules: fps}})
	}
	return observe.Rows{Cluster: "prd", Incarnation: inc, Task: healthsrc.TaskGateway, EvaluatedAt: at,
		Part: observe.Part{ID: "routes", Kinds: []observe.KindCoverage{{Kind: healthsrc.KindHTTPRoutes, Coverage: cov}, {Kind: healthsrc.KindGateways, Coverage: observe.Live}}},
		Rows: healthsrc.RowSet[observe.Attached]{Rows: rows}}
}

func TestRuleIntervals(t *testing.T) {
	log := obslog.New()
	h := newRouteHistory(log)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	// First sight: the interval starts there, basis unknown.
	h.OnRows(routeRows(1, at(0), true, map[string][]string{"r1": {"fpA", "fpB"}}))
	iv, ok := h.Interval("prd", "r1", 0)
	if !ok || !iv.Start.Equal(at(0)) || !iv.Cutoff.Equal(at(1)) || iv.Basis != obslog.ProofUnknown {
		t.Fatalf("first sight: %+v %v", iv, ok)
	}
	// A fingerprint change starts that rule's interval at the change, observed.
	h.OnRows(routeRows(1, at(5), true, map[string][]string{"r1": {"fpA", "fpB2"}}))
	if iv, ok := h.Interval("prd", "r1", 1); !ok || !iv.Start.Equal(at(5)) || iv.Basis != obslog.ProofObserved {
		t.Fatalf("after a change: %+v %v", iv, ok)
	}
	if iv, ok := h.Interval("prd", "r1", 0); !ok || !iv.Start.Equal(at(0)) {
		t.Fatalf("the unchanged rule keeps its start: %+v %v", iv, ok)
	}
	// A coverage gap: no interval while lost; afterwards it starts at the
	// after-gap, for every rule.
	h.OnRows(routeRows(1, at(8), false, map[string][]string{"r1": {"fpA", "fpB2"}}))
	if _, ok := h.Interval("prd", "r1", 0); ok {
		t.Fatal("an interval during a loss")
	}
	h.OnRows(routeRows(1, at(10), true, map[string][]string{"r1": {"fpA", "fpB2"}}))
	if iv, ok := h.Interval("prd", "r1", 0); !ok || !iv.Start.Equal(at(10)) || !iv.Cutoff.Equal(at(11)) {
		t.Fatalf("after the gap: %+v %v", iv, ok)
	}
	// The Observer's own routes partition losing coverage breaks it too.
	h.OnTransitions(observe.TransitionBatch{Cluster: "prd", Incarnation: 1, Task: healthsrc.TaskGateway, Part: "routes",
		Kind: observe.BatchLoss, At: at(12), Loss: &observe.Loss{Reason: observe.LossCoverage}})
	if _, ok := h.Interval("prd", "r1", 0); ok {
		t.Fatal("an interval after the routes partition was lost")
	}
	h.OnRows(routeRows(1, at(14), true, map[string][]string{"r1": {"fpA", "fpB2"}}))
	// A recreated route is a new UID: the old one's rules are gone.
	h.OnRows(routeRows(1, at(20), true, map[string][]string{"r2": {"fpA", "fpB2"}}))
	if _, ok := h.Interval("prd", "r1", 0); ok {
		t.Fatal("a gone route kept its interval")
	}
	if iv, ok := h.Interval("prd", "r2", 0); !ok || !iv.Start.Equal(at(20)) {
		t.Fatalf("the recreated route: %+v %v", iv, ok)
	}
	// A new incarnation re-baselines.
	h.OnRows(routeRows(2, at(30), true, map[string][]string{"r2": {"fpA", "fpB2"}}))
	if iv, ok := h.Interval("prd", "r2", 0); !ok || !iv.Start.Equal(at(30)) {
		t.Fatalf("a new incarnation: %+v %v", iv, ok)
	}
	// Unknown keys and clusters.
	if _, ok := h.Interval("prd", "r2", 7); ok {
		t.Fatal("an unknown rule index")
	}
	if _, ok := h.Interval("stg", "r2", 0); ok {
		t.Fatal("an unknown cluster")
	}
}

func TestRuleHistoryNeedsTheGatewaysLive(t *testing.T) {
	h := newRouteHistory(obslog.New())
	r := routeRows(1, t0, true, map[string][]string{"r1": {"fpA"}})
	r.Part.Kinds[1].Coverage = observe.Checking // the Gateways not read: rows are not the truth yet
	h.OnRows(r)
	if _, ok := h.Interval("prd", "r1", 0); ok {
		t.Fatal("an interval with the Gateways unread")
	}
}

// The Observer's end drops the cluster's fingerprints and keeps only the
// incarnation fence: a late end of an older incarnation clears nothing, and a
// late row of the ended one is refused (design review of 682f9cb).
func TestRuleHistoryEnd(t *testing.T) {
	log := obslog.New()
	h := newRouteHistory(log)
	h.OnRows(routeRows(1, t0, true, map[string][]string{"r1": {"fpA"}}))
	h.OnRows(routeRows(2, t0.Add(time.Minute), true, map[string][]string{"r1": {"fpA"}}))
	h.OnTransitions(observe.TransitionBatch{Cluster: "prd", Incarnation: 1, Kind: observe.BatchEnd, At: t0.Add(2 * time.Minute)})
	if c := h.st["prd"]; c.inc != 2 || len(c.prev) != 1 || c.ended {
		t.Fatalf("a late end of an older incarnation cleared newer state: %+v", c)
	}
	end := observe.TransitionBatch{Cluster: "prd", Incarnation: 2, Kind: observe.BatchEnd, At: t0.Add(3 * time.Minute)}
	log.Apply(end) // the health sink hands the log the same end
	h.OnTransitions(end)
	if c := h.st["prd"]; c.inc != 2 || c.prev != nil || !c.ended {
		t.Fatalf("the end kept the fingerprints: %+v", c)
	}
	h.OnRows(routeRows(2, t0.Add(4*time.Minute), true, map[string][]string{"r1": {"fpA"}}))
	if c := h.st["prd"]; c.prev != nil || c.seen {
		t.Fatalf("a late row of the ended incarnation: %+v", c)
	}
	if _, ok := h.Interval("prd", "r1", 0); ok {
		t.Fatal("an interval after the end")
	}
	h.OnRows(routeRows(3, t0.Add(5*time.Minute), true, map[string][]string{"r1": {"fpA"}}))
	if iv, ok := h.Interval("prd", "r1", 0); !ok || !iv.Start.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("a new incarnation after the end: %+v %v", iv, ok)
	}
}
