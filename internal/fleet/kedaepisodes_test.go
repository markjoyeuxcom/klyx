package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

// fakeKEDAObs is the Observer as the episode tracker reads it: each kind's
// state, completeness and objects, the registered watches, and every
// episodes fact published.
type fakeKEDAObs struct {
	states   map[string]observe.KindState
	complete map[string]bool
	objs     map[string][]any
	watches  map[string]func(observe.KindState)
	facts    []map[string]map[keda.EpisodeKind]keda.Episode
}

func newFakeKEDAObs() *fakeKEDAObs {
	f := &fakeKEDAObs{states: map[string]observe.KindState{}, complete: map[string]bool{}, objs: map[string][]any{}, watches: map[string]func(observe.KindState){}}
	for _, k := range []string{healthsrc.KindScaledObjects, healthsrc.KindScaledJobs, healthsrc.KindHPAs} {
		f.states[k] = observe.KindState{Kind: k, Generation: 1, Epoch: 1, Seq: 1, Coverage: observe.Live, CountKnown: true}
		f.complete[k] = true
	}
	return f
}

func (f *fakeKEDAObs) WatchKind(kind string, fn func(observe.KindState)) func() {
	f.watches[kind] = fn
	return func() {}
}

func (f *fakeKEDAObs) VisitKind(kind string, visit func(obj any) bool) (observe.KindState, bool, bool) {
	st, ok := f.states[kind]
	if !ok {
		return observe.KindState{}, false, false
	}
	for _, o := range f.objs[kind] {
		if !visit(o) {
			break
		}
	}
	return st, f.complete[kind], true
}

func (f *fakeKEDAObs) SetFact(name string, v any) {
	if name == healthsrc.FactKEDAEpisodes {
		f.facts = append(f.facts, v.(map[string]map[keda.EpisodeKind]keda.Episode))
	}
}

// bump advances a kind's watermark by n changes and fires its watch.
func (f *fakeKEDAObs) bump(kind string, n uint64) {
	st := f.states[kind]
	st.Seq += n
	f.states[kind] = st
	f.watches[kind](st)
}

func (f *fakeKEDAObs) last() map[string]map[keda.EpisodeKind]keda.Episode {
	return f.facts[len(f.facts)-1]
}

func epScaledObject(uid, rv string, paused bool) *keda.ScaledObject {
	so := &keda.ScaledObject{Scaler: keda.Scaler{Meta: keda.Meta{Namespace: "apps", Name: "web", UID: uid, RV: rv},
		Conds: []keda.Cond{{Type: keda.CondReady, Status: "True"}}}, TargetName: "web"}
	if paused {
		t := "true"
		so.Pause.Paused = &t
	}
	return so
}

func epKEDAHPA(soUID string) *hpa.HPA {
	return &hpa.HPA{Namespace: "apps", Name: "keda-hpa-web", UID: "h1", Controller: &hpa.Owner{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "web", UID: soUID},
		Target: hpa.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}}
}

// TestKEDAEpisodesThroughTheObserver drives startKEDAEpisodes with a fake
// Observer (S9 §3.1): a lower-bound start, a stretch carried while the
// scaler is unwritten, a broken read and an HPA relist restarting it, an
// observed entry, and awaiting-HPA only on a proven HPA absence.
func TestKEDAEpisodesThroughTheObserver(t *testing.T) {
	v := keda.Version{Known: true, Major: 2, Minor: 21}
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	now := t0
	f := newFakeKEDAObs()
	startKEDAEpisodes(context.Background(), f, func() time.Time { return now }, func() keda.Version { return v })

	// a pause requested while KEDA still has its HPA: pausing, first seen,
	// so a lower bound
	f.objs[healthsrc.KindScaledObjects] = []any{epScaledObject("so1", "10", true)}
	f.objs[healthsrc.KindHPAs] = []any{epKEDAHPA("so1")}
	f.bump(healthsrc.KindScaledObjects, 1)
	ep, ok := f.last()["so1"][keda.EpPausing]
	if !ok || !ep.Start.Equal(t0) || ep.Observed {
		t.Fatalf("first sight: %+v (ok %v), want a lower bound at %v", ep, ok, t0)
	}

	// unwritten and one change later: the same stretch
	now = t0.Add(30 * time.Second)
	f.bump(healthsrc.KindHPAs, 1)
	if ep := f.last()["so1"][keda.EpPausing]; !ep.Start.Equal(t0) {
		t.Fatalf("carried: start %v, want %v", ep.Start, t0)
	}

	// the scaler kind not live: the tracker breaks and publishes nothing for it
	now = t0.Add(time.Minute)
	st := f.states[healthsrc.KindScaledObjects]
	st.Coverage = observe.Stale
	f.states[healthsrc.KindScaledObjects] = st
	f.bump(healthsrc.KindScaledObjects, 1)
	if eps, ok := f.last()["so1"]; ok {
		t.Fatalf("a stale read published episodes: %+v", eps)
	}
	// live again: restarted as a lower bound, never the old start
	now = t0.Add(90 * time.Second)
	st.Coverage = observe.Live
	f.states[healthsrc.KindScaledObjects] = st
	f.bump(healthsrc.KindScaledObjects, 1)
	if ep := f.last()["so1"][keda.EpPausing]; !ep.Start.Equal(now) || ep.Observed {
		t.Fatalf("after the break: %+v, want a lower bound at %v", ep, now)
	}

	// an HPA relist (a new epoch) breaks the ScaledObjects' continuity
	now = t0.Add(2 * time.Minute)
	hst := f.states[healthsrc.KindHPAs]
	hst.Epoch++
	f.states[healthsrc.KindHPAs] = hst
	f.bump(healthsrc.KindHPAs, 1)
	if ep := f.last()["so1"][keda.EpPausing]; !ep.Start.Equal(now) {
		t.Fatalf("after an HPA relist: start %v, want %v", ep.Start, now)
	}

	// many changes between visits while the scaler was rewritten: restarted
	now = t0.Add(3 * time.Minute)
	f.objs[healthsrc.KindScaledObjects] = []any{epScaledObject("so1", "11", true)}
	f.bump(healthsrc.KindScaledObjects, 3)
	if ep := f.last()["so1"][keda.EpPausing]; !ep.Start.Equal(now) || ep.Observed {
		t.Fatalf("rewritten across several changes: %+v, want a lower bound at %v", ep, now)
	}
}

func TestKEDAEpisodesObservedEntryAndProvenAbsence(t *testing.T) {
	v := keda.Version{Known: true, Major: 2, Minor: 21}
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	now := t0
	f := newFakeKEDAObs()
	startKEDAEpisodes(context.Background(), f, func() time.Time { return now }, func() keda.Version { return v })

	// not paused, its HPA present: no episode
	f.objs[healthsrc.KindScaledObjects] = []any{epScaledObject("so1", "10", false)}
	f.objs[healthsrc.KindHPAs] = []any{epKEDAHPA("so1")}
	f.bump(healthsrc.KindScaledObjects, 1)
	if eps := f.last()["so1"]; len(eps) != 0 {
		t.Fatalf("a healthy scaler has episodes: %+v", eps)
	}
	// the pause written in the one change since: seen entering the state
	now = t0.Add(10 * time.Second)
	f.objs[healthsrc.KindScaledObjects] = []any{epScaledObject("so1", "11", true)}
	f.bump(healthsrc.KindScaledObjects, 1)
	if ep := f.last()["so1"][keda.EpPausing]; !ep.Observed || !ep.Start.Equal(now) {
		t.Fatalf("an observed entry: %+v, want observed at %v", ep, now)
	}

	// Ready with no HPA in a proven read: awaiting its HPA
	f.objs[healthsrc.KindScaledObjects] = []any{epScaledObject("so2", "20", false)}
	f.objs[healthsrc.KindHPAs] = nil
	f.bump(healthsrc.KindHPAs, 1)
	if _, ok := f.last()["so2"][keda.EpAwaitingHPA]; !ok {
		t.Fatalf("no awaiting-HPA episode on a proven absence: %+v", f.last())
	}
	// the HPA read truncated: absence is not proven, so no episode
	hst := f.states[healthsrc.KindHPAs]
	hst.Truncated = true
	f.states[healthsrc.KindHPAs] = hst
	f.bump(healthsrc.KindHPAs, 1)
	if eps := f.last()["so2"]; len(eps) != 0 {
		t.Fatalf("an unproven absence opened episodes: %+v", eps)
	}
	hst.Truncated = false
	f.states[healthsrc.KindHPAs] = hst
	f.complete[healthsrc.KindHPAs] = false
	f.bump(healthsrc.KindHPAs, 1)
	if eps := f.last()["so2"]; len(eps) != 0 {
		t.Fatalf("an incomplete HPA read opened episodes: %+v", eps)
	}

	// a ScaledJob's pause requested, Paused not yet reported: pausing
	f.objs[healthsrc.KindScaledJobs] = []any{&keda.ScaledJob{Scaler: keda.Scaler{Meta: keda.Meta{Namespace: "apps", Name: "batch", UID: "sj1", RV: "5"},
		Pause: keda.Pause{Paused: epStr("true")}, Conds: []keda.Cond{{Type: keda.CondReady, Status: "True"}}}}}
	f.bump(healthsrc.KindScaledJobs, 1)
	if _, ok := f.last()["sj1"][keda.EpPausing]; !ok {
		t.Fatalf("no ScaledJob pausing episode: %+v", f.last())
	}
}

func TestKEDAEpisodesStopWithTheirContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeKEDAObs()
	startKEDAEpisodes(ctx, f, time.Now, func() keda.Version { return keda.Version{} })
	cancel()
	f.bump(healthsrc.KindScaledObjects, 1)
	if len(f.facts) != 0 {
		t.Fatalf("a cancelled tracker published %d facts", len(f.facts))
	}
}

func epStr(s string) *string { return &s }
