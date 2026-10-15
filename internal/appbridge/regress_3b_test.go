package appbridge

// Regressions from Codex's review of slice 3b (0af176c): history follows
// every accepted change, at capture times; seeding keeps a newer recovery;
// an unneeded source loss keeps a Git failure proof; Final publishes; a
// configured cluster missing from a mesh sample is unknown.

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

func regress3bKinds(ks ...string) []observe.KindCoverage {
	var out []observe.KindCoverage
	for _, k := range ks {
		out = append(out, observe.KindCoverage{Kind: k, Role: "counted", Coverage: observe.Live})
	}
	return out
}
func regress3bRecon(at time.Time, v uint64, failed bool) observe.Rows {
	ready := flux.Ready
	if failed {
		ready = flux.Failed
	}
	return observe.Rows{Cluster: "c1", Incarnation: 1, Task: "flux", EvaluatedAt: at, Part: observe.Part{ID: "reconciliations", Ver: v, Kinds: regress3bKinds("kustomizations", "helmreleases")}, Rows: healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{{Ref: healthsrc.RowRef{UID: "u1"}, Data: flux.Resource{Kind: flux.KustomizationKind, Name: "apps", Namespace: "ns", Ready: ready, Revision: "main@sha1:" + lineSha('a'), SourceKind: "GitRepository", SourceNamespace: "ns", SourceName: "repo"}}}}}
}
func regress3bSvc(now *time.Time) *LineService {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	st := &lineStation{cfg: clusterCfg("c1", "dev"), inc: 1, usable: true, att: &lineAttachment{cancel: func() {}}, parts: map[partitionRef]*stationPartition{}, booted: map[string]bool{}, rows: map[string]observe.Rows{
		"flux/reconciliations": regress3bRecon(*now, 1, false),
		"flux/sources":         {Cluster: "c1", Incarnation: 1, Task: "flux", EvaluatedAt: *now, Part: observe.Part{ID: "sources", Ver: 1, Kinds: regress3bKinds("gitrepositories", "buckets")}, Rows: healthsrc.RowSet[flux.Source]{Rows: []healthsrc.Row[flux.Source]{{Ref: healthsrc.RowRef{UID: "src"}, Data: flux.Source{Kind: "GitRepository", Namespace: "ns", Name: "repo", URL: lineRepo, RefKind: "branch", RefName: "main", Ready: flux.Ready}}}}},
		"argo/applications":    {Cluster: "c1", Incarnation: 1, Task: "argo", EvaluatedAt: *now, Part: observe.Part{ID: "applications", Ver: 1, Kinds: []observe.KindCoverage{{Kind: "applications", Role: "counted", Coverage: observe.Absent}}}},
	}}
	return &LineService{d: LineDeps{Sink: sink, Now: func() time.Time { return *now }}, ctx: context.Background(), tap: newLineTap(), stations: map[string]*lineStation{"c1": st}, hist: newLineHistory(sink.ObservationLog()), selector: &gitKey}
}
func regress3bFail(t *testing.T, s *LineService, now *time.Time) {
	t.Helper()
	s.recompute()
	*now = now.Add(time.Second)
	s.tap.OnRows(regress3bRecon(*now, 2, true))
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	if ds.FailingSince == nil || ds.FailingSince.Basis != obslog.ProofObserved {
		t.Fatalf("fixture: %+v", ds)
	}
}
func TestRegress3bDrainedRowsCanHideAFlipBeforeRecompute(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	regress3bFail(t, s, &now)
	now = now.Add(100 * time.Millisecond)
	s.tap.OnRows(regress3bRecon(now, 3, false))
	s.applyDrain()
	now = now.Add(100 * time.Millisecond)
	s.tap.OnRows(regress3bRecon(now, 4, true))
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	if ds.FailingSince != nil && ds.FailingSince.Basis == obslog.ProofObserved && ds.FailingSince.Since.Before(now) {
		t.Fatalf("old continuous proof survived observed recovery/failure across two drains: %+v", ds.FailingSince)
	}
}
func TestRegress3bQuietRecomputesDoNotAdvanceEvidenceLowerBound(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.recompute()
	captured := now.Add(time.Second)
	now = now.Add(2 * time.Second)
	s.recompute() // no newer row evidence
	now = now.Add(time.Second)
	s.tap.OnRows(regress3bRecon(captured, 2, true))
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	p := ds.FailingSince
	if p != nil && p.NotBefore != nil && p.NotBefore.After(captured) {
		t.Fatalf("claims failure began after %v although failing rows were captured at %v", p.NotBefore, captured)
	}
}
func TestRegress3bSeedKeepsNewerRecoveryVersion(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	st := s.stations["c1"]
	s.d.Sink.ObservationLog().Apply(observe.TransitionBatch{Cluster: "c1", Incarnation: 1, Task: "flux", Part: "sources", Source: "gitrepositories", Kind: observe.BatchBaseline, PartVer: 3, At: now})
	s.seed("c1", st, st.rows["flux/sources"])
	pr := partitionRef{"flux", "sources", "gitrepositories"}
	if st.parts[pr].ver != 3 {
		t.Fatalf("bootstrap v1 masks recovery v3: %+v", st.parts[pr])
	}
	_, read, _ := s.input(st)
	if read == line.ReadOK {
		t.Fatal("rows preceding recovery were accepted as current")
	}
}
func TestRegress3bUnneededSourceLossKeepsFailureProof(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	regress3bFail(t, s, &now)
	now = now.Add(time.Second)
	b := batchOn(observe.BatchLoss, 1, 2, observe.LossCoverage)
	b.Source = "buckets"
	s.tap.OnTransitions(b)
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	if ds.Read != line.ReadOK {
		t.Fatalf("fixture: bucket is unrelated: %+v", ds)
	}
	if ds.FailingSince == nil || ds.FailingSince.Basis != obslog.ProofObserved {
		t.Fatalf("unneeded bucket loss destroyed Git failure proof: %+v", ds.FailingSince)
	}
}
func TestRegress3bFinalSchedulesPublication(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.recompute()
	s.tap.OnSummary(observe.Summary{Cluster: "c1", Incarnation: 1, Seq: 2, Connection: "connected", Final: true})
	s.applyDrain()
	if !s.dirty {
		t.Fatal("Final changes ended state but schedules no DTO; matches stays published until next 5s scan")
	}
}
func TestRegress3bMissingMeshMemberIsUnknown(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.stations["offline"] = &lineStation{cfg: clusterCfg("offline", "prd"), rows: map[string]observe.Rows{}}
	s.applyMesh(meshResult{graph: clustermesh.BuildGraph([]clustermesh.Member{{Cluster: "c1", Present: true, Installed: true}})})
	s.recompute()
	for _, n := range s.Line().Mesh.Unknown {
		if n == "offline" {
			return
		}
	}
	t.Fatalf("configured cluster omitted by the member reader has no unknown disclosure: %+v", s.Line().Mesh)
}

func TestRegress3bDifferentPartsCanHideAFailureGap(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.recompute()
	source := func(v uint64, failed bool) observe.Rows {
		r := s.stations["c1"].rows["flux/sources"]
		r.Part.Ver = v
		r.EvaluatedAt = now
		rs := r.Rows.(healthsrc.RowSet[flux.Source])
		rs.Rows = append([]healthsrc.Row[flux.Source](nil), rs.Rows...)
		rs.Rows[0].Data.Ready = flux.Ready
		if failed {
			rs.Rows[0].Data.Ready = flux.Failed
		}
		r.Rows = rs
		return r
	}
	now = now.Add(time.Second)
	s.tap.OnRows(source(2, true))
	s.applyDrain()
	s.recompute()
	before, _ := stationIn(s.Line(), "c1")
	if before.FailingSince == nil || before.FailingSince.Basis != obslog.ProofObserved {
		t.Fatalf("fixture: %+v", before)
	}
	// Distinct Observer parts can publish independently inside one 250ms service window.
	now = now.Add(2 * time.Second)
	s.tap.OnRows(source(3, false))
	s.applyDrain()
	now = now.Add(100 * time.Millisecond)
	s.tap.OnRows(regress3bRecon(now, 2, true))
	s.applyDrain()
	s.recompute()
	after, _ := stationIn(s.Line(), "c1")
	if p := after.FailingSince; p != nil && p.Basis == obslog.ProofObserved && p.Since.Equal(*before.FailingSince.Since) {
		t.Fatalf("source healed before unit failed but old failing proof survived: %+v", p)
	}
}

// A bootstrap caught while Argo's probe is still checking seeds a loss; a
// source absent from the start commits no batch, so rows at or after that
// loss carry the absent word. A loss newer than the rows still reads checking.
func TestRegress3bAbsentFromStartClearsABootstrapLoss(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	st := s.stations["c1"]
	apps := func(v uint64, cov observe.Coverage) observe.Rows {
		return observe.Rows{Cluster: "c1", Incarnation: 1, Task: "argo", EvaluatedAt: now, Part: observe.Part{ID: "applications", Ver: v, Kinds: []observe.KindCoverage{{Kind: "applications", Role: "counted", Coverage: cov}}}}
	}
	delete(st.rows, "argo/applications")
	st.rows["argo/applications"] = apps(1, observe.Checking)
	s.seed("c1", st, st.rows["argo/applications"])
	if _, read, _ := s.input(st); read == line.ReadOK {
		t.Fatal("fixture: a mid-probe bootstrap reads checking")
	}
	s.tap.OnRows(apps(2, observe.Absent))
	s.applyDrain()
	if _, read, reasons := s.input(st); read != line.ReadOK {
		t.Fatalf("Argo absent from the start stays checking: %s %v", read, reasons)
	}
	b := batchOn(observe.BatchLoss, 1, 3, observe.LossCoverage)
	b.Task, b.Part, b.Source = "argo", "applications", "applications"
	s.tap.OnTransitions(b)
	s.applyDrain()
	if _, read, _ := s.input(st); read == line.ReadOK {
		t.Fatal("a loss newer than the absent rows reads ok")
	}
}

// Codex's review of bd385e7: cross-part order within a drain, per-partition
// evidence times, HelmRelease source failure inputs, the absence fence and
// station-state history.

func regress3bSource(s *LineService, at time.Time, v uint64, failed bool) observe.Rows {
	r := s.stations["c1"].rows["flux/sources"]
	r.Part.Ver = v
	r.EvaluatedAt = at
	rs := r.Rows.(healthsrc.RowSet[flux.Source])
	rs.Rows = append([]healthsrc.Row[flux.Source](nil), rs.Rows...)
	rs.Rows[0].Data.Ready = flux.Ready
	if failed {
		rs.Rows[0].Data.Ready = flux.Failed
	}
	r.Rows = rs
	return r
}
func TestRegress3bBDSameDrainAcrossPartsDoesNotPreserveOldProof(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.recompute()
	now = now.Add(time.Second)
	s.tap.OnRows(regress3bSource(s, now, 2, true))
	s.applyDrain()
	s.recompute()
	before, _ := stationIn(s.Line(), "c1")
	if before.FailingSince == nil || before.FailingSince.Basis != obslog.ProofObserved {
		t.Fatalf("fixture: %+v", before)
	}
	now = now.Add(2 * time.Second)
	s.tap.OnRows(regress3bSource(s, now, 3, false))
	now = now.Add(100 * time.Millisecond)
	s.tap.OnRows(regress3bRecon(now, 2, true))
	s.applyDrain()
	s.recompute()
	after, _ := stationIn(s.Line(), "c1")
	if p := after.FailingSince; p != nil && p.Basis == obslog.ProofObserved && p.Since.Equal(*before.FailingSince.Since) {
		t.Fatalf("healed source and then failed unit coalesced across parts; original proof survives: %+v", p)
	}
}
func TestRegress3bBDUnrelatedCaptureDoesNotDateFailureProof(t *testing.T) {
	now := time.Now()
	base := now
	s := regress3bSvc(&now)
	ar := s.stations["c1"].rows["argo/applications"]
	ar.EvaluatedAt = base.Add(20 * time.Second)
	s.stations["c1"].rows["argo/applications"] = ar
	now = base.Add(21 * time.Second)
	s.recompute()
	captured := base.Add(15 * time.Second)
	now = base.Add(22 * time.Second)
	s.tap.OnRows(regress3bRecon(captured, 2, true))
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	p := ds.FailingSince
	if p == nil {
		t.Fatal("no failure proof")
	}
	if p.NotBefore != nil && p.NotBefore.After(captured) {
		t.Fatalf("Argo capture dates Flux interval after already-failing Flux evidence: %+v; capture %v", p, captured)
	}
}
func TestRegress3bBDChartSourceFailureLosesProofOnSourceGap(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	st := s.stations["c1"]
	r := st.rows["flux/reconciliations"]
	r.Rows = healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{{Ref: healthsrc.RowRef{UID: "hr"}, Data: flux.Resource{Kind: flux.HelmReleaseKind, Name: "web", Namespace: "ns", Ready: flux.Ready, SourceKind: "GitRepository", SourceNamespace: "ns", SourceName: "repo", History: []flux.HelmHistory{{ChartName: "web", ChartVersion: "1", Status: "deployed"}}}}}}
	st.rows["flux/reconciliations"] = r
	s.selector = &line.Key{Kind: line.KeyChartName, RefName: "web"}
	s.recompute()
	now = now.Add(time.Second)
	s.tap.OnRows(regress3bSource(s, now, 2, true))
	s.applyDrain()
	s.recompute()
	before, _ := stationIn(s.Line(), "c1")
	if before.FailingSince == nil || before.FailingSince.Basis != obslog.ProofObserved {
		t.Fatalf("fixture: source alone makes release failing: %+v", before)
	}
	now = now.Add(time.Second)
	s.tap.OnTransitions(batchOn(observe.BatchLoss, 1, 2, observe.LossRelist))
	s.applyDrain()
	s.recompute()
	after, _ := stationIn(s.Line(), "c1")
	if after.Read != line.ReadOK {
		t.Fatalf("chart comparison must remain available: %+v", after)
	}
	p, ok, _ := s.d.Sink.ObservationLog().ProofOf("c1", "line/unit/hr#0")
	if ok && !p.Suspended && p.Basis == obslog.ProofObserved {
		t.Fatalf("source-derived failure remains proven through an unread source: %+v", p)
	}
}
func TestRegress3bBDAbsenceDoesNotPromoteRetainedRows(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.recompute()
	before, _ := stationIn(s.Line(), "c1")
	if before.State != line.StateMatches {
		t.Fatalf("fixture: %+v", before)
	}
	b := batchOn(observe.BatchLoss, 1, 2, observe.LossAbsent)
	b.Part = "reconciliations"
	b.Source = "kustomizations"
	s.tap.OnTransitions(b)
	s.applyDrain()
	s.recompute()
	after, _ := stationIn(s.Line(), "c1")
	if after.State == line.StateMatches || s.Line().Applied > 0 {
		t.Fatalf("absent kind's v1 retained objects still count while waiting for v2 rows: %+v", after)
	}
}
func TestRegress3bBDStationStateStillEntersObservationLog(t *testing.T) {
	now := time.Now()
	s := regress3bSvc(&now)
	s.recompute()
	now = now.Add(time.Second)
	r := regress3bRecon(now, 2, false)
	rs := r.Rows.(healthsrc.RowSet[flux.Resource])
	rs.Rows[0].Data.Revision = ""
	r.Rows = rs
	s.tap.OnRows(r)
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	if ds.State != line.StateAppliedUnknown {
		t.Fatalf("fixture: expected state change: %+v", ds)
	}
	found := false
	for _, e := range s.d.Sink.ObservationLog().Recent("c1", 100) {
		if e.Task == lineTask && e.Key == "line/state" {
			found = true
		}
	}
	if !found {
		t.Fatal("station state is no longer recorded by line history")
	}
}

// Codex's review of 5755f84: quiet inputs take part in the capture-order
// fence, and an overwrite gap lasts until its part's newer rows.

func TestRegress3b5755QuietInputStillFencesCaptureOrder(t *testing.T) {
	for _, mode := range []string{"late-earlier-capture", "simultaneous-captures"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			base := now
			s := regress3bSvc(&now)
			s.recompute()
			now = base.Add(time.Second)
			s.tap.OnRows(regress3bSource(s, now, 2, true))
			s.applyDrain()
			s.recompute()
			before, _ := stationIn(s.Line(), "c1")
			if before.FailingSince == nil || before.FailingSince.Basis != obslog.ProofObserved {
				t.Fatal("fixture lacks observed source fault")
			}
			if mode == "late-earlier-capture" {
				// The unit's failure at t5 does not change the aggregate (source still fails).
				now = base.Add(5 * time.Second)
				s.tap.OnRows(regress3bRecon(now, 2, true))
				s.applyDrain()
				// A delayed bootstrap proves the source had healed at t3, before that unit failure.
				s.applyResult(bootResult{att: s.stations["c1"].att.id, cluster: "c1", part: partRef{"flux", "sources"}, rows: regress3bSource(s, base.Add(3*time.Second), 3, false), ok: true})
			} else {
				// Source healing and unit failing have equal capture clocks: order is unknown.
				now = base.Add(5 * time.Second)
				s.tap.OnRows(regress3bSource(s, now, 3, false))
				s.tap.OnRows(regress3bRecon(now, 2, true))
				s.applyDrain()
			}
			s.recompute()
			after, _ := stationIn(s.Line(), "c1")
			p := after.FailingSince
			if p != nil && p.Basis == obslog.ProofObserved && p.Since.Equal(*before.FailingSince.Since) {
				t.Fatalf("old continuous source-failure proof survives an ambiguous or reversed handover to the unit: %+v", p)
			}
		})
	}
}

func TestRegress3b5755OverwriteGapLastsUntilAffectedRows(t *testing.T) {
	now := time.Now()
	base := now
	s := regress3bSvc(&now)
	s.recompute()
	// The first captured failed unit is overwritten before the worker drains.
	s.tap.OnRows(regress3bRecon(base.Add(time.Second), 2, true))
	// This other input is installed first, while the held unit is still healthy t0.
	s.tap.OnRows(regress3bSource(s, base.Add(2*time.Second), 2, false))
	s.tap.OnRows(regress3bRecon(base.Add(3*time.Second), 3, true))
	now = base.Add(4 * time.Second)
	s.applyDrain()
	s.recompute()
	ds, _ := stationIn(s.Line(), "c1")
	p := ds.FailingSince
	if p == nil {
		t.Fatal("fixture lacks failure proof")
	}
	if p.Basis == obslog.ProofObserved {
		t.Fatalf("overwrite gap consumed before the affected row: got observed (%v,%v], despite failed capture at %v having been discarded", p.NotBefore, p.Since, base.Add(time.Second))
	}
	// Continuity may resume after the final affected row, not before it.
	s.tap.OnRows(regress3bRecon(base.Add(5*time.Second), 4, false))
	s.applyDrain()
	s.tap.OnRows(regress3bRecon(base.Add(6*time.Second), 5, true))
	s.applyDrain()
	s.recompute()
	ds, _ = stationIn(s.Line(), "c1")
	if ds.FailingSince == nil || ds.FailingSince.Basis != obslog.ProofObserved {
		t.Fatalf("new clean transition must remain observable: %+v", ds.FailingSince)
	}
}
