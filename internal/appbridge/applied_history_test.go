package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// The Line slice 8d: each unit's typed applied value in the observation
// log; an observed change only between two known values of the same UID.

const shaA, shaB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type unitRow struct {
	uid, name, rev string
}

func unitRows(inc uint64, at time.Time, live bool, units ...unitRow) observe.Rows {
	cov := observe.Live
	if !live {
		cov = observe.Stale
	}
	var rows []healthsrc.Row[flux.Resource]
	for _, u := range units {
		rows = append(rows, healthsrc.Row[flux.Resource]{Ref: healthsrc.RowRef{UID: u.uid},
			Data: flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: u.name, SourceKind: "GitRepository", Revision: u.rev}})
	}
	return observe.Rows{Cluster: "prd", Incarnation: inc, Task: healthsrc.TaskFlux, EvaluatedAt: at,
		Part: observe.Part{ID: "reconciliations", Kinds: []observe.KindCoverage{{Kind: healthsrc.KindKustomizations, Coverage: cov}, {Kind: healthsrc.KindHelmReleases, Coverage: observe.Live}}},
		Rows: healthsrc.RowSet[flux.Resource]{Rows: rows}}
}

func TestAppliedHistory(t *testing.T) {
	h := newAppliedHistory(obslog.New())
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	rev := func(sha string) string { return "main@sha1:" + sha }

	// First sight: known, basis unknown, no observed change.
	h.OnRows(unitRows(1, at(0), true, unitRow{"u1", "apps", rev(shaA)}, unitRow{"u2", "infra", ""}))
	c, ok := h.Change("prd", "u1")
	if !ok || c.Kind != "git" || c.Value != shaA || c.Basis != obslog.ProofUnknown || c.ObservedAt != nil || !c.LiveSince.Equal(at(0)) {
		t.Fatalf("first sight: %+v %v", c, ok)
	}
	if _, ok := h.Change("prd", "u2"); ok {
		t.Fatal("an unknown value has a change")
	}
	// Known -> known: the only observed change.
	h.OnRows(unitRows(1, at(5), true, unitRow{"u1", "apps", rev(shaB)}, unitRow{"u2", "infra", ""}))
	if c, ok := h.Change("prd", "u1"); !ok || c.Value != shaB || c.Basis != obslog.ProofObserved || !c.ObservedAt.Equal(at(5)) {
		t.Fatalf("known -> known: %+v %v", c, ok)
	}
	// Unknown -> known: a first sighting, never an observed change.
	h.OnRows(unitRows(1, at(6), true, unitRow{"u1", "apps", rev(shaB)}, unitRow{"u2", "infra", rev(shaA)}))
	if c, ok := h.Change("prd", "u2"); !ok || c.Basis != obslog.ProofUnknown || c.ObservedAt != nil {
		t.Fatalf("unknown -> known: %+v %v", c, ok)
	}
	// Known -> unknown -> known: the proof breaks, the new value starts afresh.
	h.OnRows(unitRows(1, at(7), true, unitRow{"u1", "apps", "main"}, unitRow{"u2", "infra", rev(shaA)}))
	if _, ok := h.Change("prd", "u1"); ok {
		t.Fatal("an unknown value kept the proof")
	}
	h.OnRows(unitRows(1, at(8), true, unitRow{"u1", "apps", rev(shaA)}, unitRow{"u2", "infra", rev(shaA)}))
	if c, ok := h.Change("prd", "u1"); !ok || c.Basis != obslog.ProofUnknown || c.ObservedAt != nil {
		t.Fatalf("known -> unknown -> known: %+v %v", c, ok)
	}
	// A replacement is a new UID: it starts afresh.
	h.OnRows(unitRows(1, at(9), true, unitRow{"u3", "apps", rev(shaB)}, unitRow{"u2", "infra", rev(shaA)}))
	if c, ok := h.Change("prd", "u3"); !ok || c.Basis != obslog.ProofUnknown {
		t.Fatalf("replacement: %+v %v", c, ok)
	}
	if uid, ok := h.UnitFor("prd", "Kustomization", "flux-system", "apps"); !ok || uid != "u3" {
		t.Fatalf("UnitFor: %q %v", uid, ok)
	}
	// A coverage break: no change while lost, and none observed across it.
	h.OnRows(unitRows(1, at(10), false, unitRow{"u3", "apps", rev(shaB)}))
	if _, ok := h.Change("prd", "u3"); ok {
		t.Fatal("a change during a loss")
	}
	if _, ok := h.UnitFor("prd", "Kustomization", "flux-system", "apps"); ok {
		t.Fatal("a unit resolved from unreadable rows")
	}
	h.OnRows(unitRows(1, at(12), true, unitRow{"u3", "apps", rev(shaA)}))
	if c, ok := h.Change("prd", "u3"); !ok || c.Basis != obslog.ProofUnknown || !c.LiveSince.Equal(at(12)) {
		t.Fatalf("after the gap: %+v %v", c, ok)
	}
	// Two rows of one name resolve to nothing.
	h.OnRows(unitRows(1, at(13), true, unitRow{"u3", "apps", rev(shaA)}, unitRow{"u4", "apps", rev(shaA)}))
	if _, ok := h.UnitFor("prd", "Kustomization", "flux-system", "apps"); ok {
		t.Fatal("a name that is not a single row")
	}
	// The Observer's end: only the fence remains.
	h.OnTransitions(observe.TransitionBatch{Cluster: "prd", Incarnation: 1, Kind: observe.BatchEnd, At: at(14)})
	if c := h.st["prd"]; !c.ended || c.prev != nil || c.units != nil {
		t.Fatalf("end: %+v", c)
	}
}
