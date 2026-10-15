package appbridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
)

// LineService end to end (The Line slice 3 note §2-§3): real Observers over
// fake Flux clusters, a shared HealthSink, and the line on top.

const lineRepo = "https://github.com/acme/fleet"

func lineSha(c byte) string { return strings.Repeat(string(c), 40) }

var ksGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}

func lineListKinds() map[schema.GroupVersionResource]string {
	m := map[schema.GroupVersionResource]string{
		ksGVR: "KustomizationList",
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}: "HelmReleaseList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:      "ApplicationList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}:   "ApplicationSetList",
	}
	for _, r := range []string{"gitrepositories", "ocirepositories", "buckets", "helmrepositories", "helmcharts"} {
		m[schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: r}] = "SourceList"
	}
	return m
}

func kustomization(rev string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system", "uid": "uid-apps", "generation": int64(1)},
		"spec":     map[string]any{"path": "./apps", "sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"}},
		"status": map[string]any{"observedGeneration": int64(1), "lastAppliedRevision": "main@sha1:" + rev,
			"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded"}}},
	}}
}

func gitRepository(rev string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "fleet", "namespace": "flux-system", "uid": "uid-fleet", "generation": int64(1)},
		"spec":     map[string]any{"url": lineRepo, "ref": map[string]any{"branch": "main"}},
		"status": map[string]any{"observedGeneration": int64(1), "artifact": map[string]any{"revision": "main@sha1:" + rev},
			"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Succeeded"}}},
	}}
}

type lineCluster struct {
	name   string
	dyn    *dynamicfake.FakeDynamicClient
	obs    *observe.Observer
	cancel context.CancelFunc
}

// startLineCluster runs a GitOps Observer over one fake cluster applied at
// rev. gate, when set, holds every rows evaluation of flux/reconciliations.
func startLineCluster(t *testing.T, sink *HealthSink, name, rev string, gate chan struct{}) *lineCluster {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lineListKinds(), kustomization(rev), gitRepository(rev))
	versions := map[string]string{"kustomize.toolkit.fluxcd.io": "v1", "helm.toolkit.fluxcd.io": "v2", "source.toolkit.fluxcd.io": "v1"}
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		if v, ok := versions[group]; ok {
			return observe.Probe{Served: true, Version: v}, nil
		}
		return observe.Probe{Served: false}, nil // no Argo
	}
	kinds, domains := healthsrc.GitOps(healthsrc.NativeDeps{Cluster: name, Dyn: dyn, Served: served})
	if gate != nil {
		for i := range domains {
			for j := range domains[i].Parts {
				p := &domains[i].Parts[j]
				if domains[i].ID == healthsrc.TaskFlux && p.ID == "reconciliations" {
					inner := p.Evaluate
					p.Evaluate = func(c observe.Capture) observe.PartResult {
						if c.Rows {
							<-gate
						}
						return inner(c)
					}
				}
			}
		}
	}
	o := observe.New(observe.Config{Cluster: name, Sink: sink, Kinds: kinds, Domains: domains, Throttle: 10 * time.Millisecond, ReprobeEvery: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	lc := &lineCluster{name: name, dyn: dyn, obs: o, cancel: cancel}
	t.Cleanup(func() { cancel(); <-o.Done() })
	return lc
}

func (lc *lineCluster) apply(t *testing.T, rev string) {
	t.Helper()
	if _, err := lc.dyn.Resource(ksGVR).Namespace("flux-system").Update(context.Background(), kustomization(rev), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

type lineHarness struct {
	t      *testing.T
	sink   *HealthSink
	svc    *LineService
	mu     sync.Mutex
	fleet  []config.ClusterConfig
	obs    map[string]*observe.Observer
	snaps  map[string]fleet.Snapshot
	cancel context.CancelFunc
}

func newLineHarness(t *testing.T, sink *HealthSink, clusters ...config.ClusterConfig) *lineHarness {
	h := &lineHarness{t: t, sink: sink, fleet: clusters, obs: map[string]*observe.Observer{}, snaps: map[string]fleet.Snapshot{}}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	h.svc = NewLineService(ctx, LineDeps{Sink: sink,
		Clusters: func() []config.ClusterConfig {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]config.ClusterConfig(nil), h.fleet...)
		},
		Snapshots: func() []fleet.Snapshot {
			h.mu.Lock()
			defer h.mu.Unlock()
			var out []fleet.Snapshot
			for _, s := range h.snaps {
				out = append(out, s)
			}
			return out
		},
		Observer:  func(name string) *observe.Observer { h.mu.Lock(); defer h.mu.Unlock(); return h.obs[name] },
		ScanEvery: 30 * time.Millisecond, MeshEvery: time.Hour, Coalesce: 5 * time.Millisecond, BootstrapTimeout: 5 * time.Second})
	return h
}

func (h *lineHarness) setObserver(name string, o *observe.Observer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if o == nil {
		delete(h.obs, name)
		return
	}
	h.obs[name] = o
}

func (h *lineHarness) waitLine(what string, cond func(LineDTO) bool) LineDTO {
	h.t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if d := h.svc.Line(); cond(d) {
			return d
		}
	}
	h.t.Fatalf("timed out: %s; last %+v", what, h.svc.Line())
	return LineDTO{}
}

func stationIn(d LineDTO, cluster string) (LineStationDTO, bool) {
	for _, ss := range [][]LineStationDTO{d.Stations, d.Siding} {
		for _, s := range ss {
			if s.Cluster == cluster {
				return s, true
			}
		}
	}
	return LineStationDTO{}, false
}

func stateIs(cluster, state string) func(LineDTO) bool {
	return func(d LineDTO) bool { s, ok := stationIn(d, cluster); return ok && s.State == state }
}

func clusterCfg(name, env string) config.ClusterConfig {
	return config.ClusterConfig{Name: name, Environment: env}
}

func TestLineServiceComparesAndFollowsChanges(t *testing.T) {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	h := newLineHarness(t, sink, clusterCfg("dev", "dev"), clusterCfg("prd", "prd"))
	dev := startLineCluster(t, sink, "dev", lineSha('a'), nil)
	prd := startLineCluster(t, sink, "prd", lineSha('a'), nil)
	h.setObserver("dev", dev.obs) // an Observer already running before the scan finds it
	h.setObserver("prd", prd.obs)
	d := h.waitLine("both match a", func(d LineDTO) bool {
		return d.R == lineSha('a') && stateIs("dev", line.StateMatches)(d) && stateIs("prd", line.StateMatches)(d)
	})
	if d.Key == nil || d.Key.Repo != "github.com/acme/fleet" || d.KeyHow != line.KeyByMostStations {
		t.Fatalf("key: %+v", d.Key)
	}
	v := d.Version
	dev.apply(t, lineSha('b'))
	d = h.waitLine("dev now differs", func(d LineDTO) bool {
		s, _ := stationIn(d, "dev")
		return d.Version > v && s.Single == lineSha('b') && (s.State == line.StateDiffers || s.State == line.StateMatches)
	})
	if s, _ := stationIn(d, "dev"); s.Incarnation != dev.obs.Incarnation() || s.ReadAt == nil {
		t.Fatalf("station detail: %+v", s)
	}
}

func TestLineServiceStationsWithoutObservers(t *testing.T) {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	h := newLineHarness(t, sink, clusterCfg("dev", "dev"), clusterCfg("broken", "stg"), clusterCfg("late", "prd"))
	h.mu.Lock()
	h.snaps["broken"] = fleet.Snapshot{Name: "broken", State: fleet.Failed, Reason: "no route"}
	h.mu.Unlock()
	d := h.waitLine("every configured cluster is a station", func(d LineDTO) bool {
		_, a := stationIn(d, "dev")
		b, bok := stationIn(d, "broken")
		_, c := stationIn(d, "late")
		return a && bok && c && b.Read == line.ReadError
	})
	if s, _ := stationIn(d, "late"); s.Read != line.ReadChecking || s.State != line.StateChecking {
		t.Fatalf("no Observer yet: %+v", s)
	}
	// An Observer that becomes available later is attached by the scan.
	late := startLineCluster(t, sink, "late", lineSha('a'), nil)
	h.setObserver("late", late.obs)
	h.waitLine("late attached", func(d LineDTO) bool { s, _ := stationIn(d, "late"); return s.Read == line.ReadOK })
}

func TestLineServiceConnectionOutageGatesMatching(t *testing.T) {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	h := newLineHarness(t, sink, clusterCfg("dev", "dev"), clusterCfg("prd", "prd"))
	dev := startLineCluster(t, sink, "dev", lineSha('a'), nil)
	prd := startLineCluster(t, sink, "prd", lineSha('a'), nil)
	h.setObserver("dev", dev.obs)
	h.setObserver("prd", prd.obs)
	h.waitLine("matching", stateIs("prd", line.StateMatches))
	prd.obs.SetConnection("unreachable") // rows stay live, at the same Part.Ver
	// The Observer's per-partition losses arrive before the summary that
	// carries the connection word: wait for the settled state.
	d := h.waitLine("prd checking on its connection", func(d LineDTO) bool {
		s, _ := stationIn(d, "prd")
		return s.State == line.StateChecking && s.Read == line.ReadChecking && len(s.ReadReasons) > 0 && strings.HasPrefix(s.ReadReasons[0], "connection:")
	})
	if s, _ := stationIn(d, "prd"); s.AsOf == nil {
		t.Fatalf("checking with last good facts as of: %+v", s)
	}
	prd.obs.SetConnection("connected · flux degraded") // usable again, rows unchanged
	h.waitLine("prd matches again", stateIs("prd", line.StateMatches))
}

func TestLineServiceNewIncarnationAndFinal(t *testing.T) {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	h := newLineHarness(t, sink, clusterCfg("dev", "dev"), clusterCfg("prd", "prd"))
	dev := startLineCluster(t, sink, "dev", lineSha('a'), nil)
	prd := startLineCluster(t, sink, "prd", lineSha('a'), nil)
	h.setObserver("dev", dev.obs)
	h.setObserver("prd", prd.obs)
	h.waitLine("matching", stateIs("prd", line.StateMatches))
	// A reconnect: a new Observer (incarnation) for prd replaces the old.
	prd2 := startLineCluster(t, sink, "prd", lineSha('a'), nil)
	h.setObserver("prd", prd2.obs)
	h.waitLine("the new incarnation attached", func(d LineDTO) bool {
		s, _ := stationIn(d, "prd")
		return s.Incarnation == prd2.obs.Incarnation() && s.State == line.StateMatches
	})
	// The old Observer ending later changes nothing.
	prd.cancel()
	<-prd.obs.Done()
	time.Sleep(50 * time.Millisecond)
	if s, _ := stationIn(h.svc.Line(), "prd"); s.State != line.StateMatches {
		t.Fatalf("an old incarnation's Final is ignored: %+v", s)
	}
	// The current one ending is terminal for it.
	prd2.cancel()
	<-prd2.obs.Done()
	h.waitLine("prd checking after Final", stateIs("prd", line.StateChecking))
}

// A held bootstrap never blocks the worker: another station's outage is
// published while it waits, and removing the held station cancels its job
// (note §2.2).
func TestLineServiceHeldBootstrapDoesNotBlock(t *testing.T) {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	gate := make(chan struct{})
	h := newLineHarness(t, sink, clusterCfg("dev", "dev"), clusterCfg("prd", "prd"))
	dev := startLineCluster(t, sink, "dev", lineSha('a'), gate) // its reconciliations bootstrap hangs
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) }) // before the Observer's cleanup waits on it, on any failure
	prd := startLineCluster(t, sink, "prd", lineSha('a'), nil)
	h.setObserver("dev", dev.obs)
	h.setObserver("prd", prd.obs)
	h.waitLine("prd read", func(d LineDTO) bool { s, _ := stationIn(d, "prd"); return s.Read == line.ReadOK })
	if s, _ := stationIn(h.svc.Line(), "dev"); s.Read != line.ReadChecking {
		t.Fatalf("dev is attaching: %+v", s)
	}
	prd.obs.SetConnection("unreachable")
	h.waitLine("prd's outage is published while dev's job waits", func(d LineDTO) bool { s, _ := stationIn(d, "prd"); return s.Read == line.ReadChecking })
	h.mu.Lock()
	h.fleet = []config.ClusterConfig{clusterCfg("prd", "prd")}
	h.mu.Unlock()
	h.svc.Rescan()
	h.waitLine("dev removed", func(d LineDTO) bool { _, ok := stationIn(d, "dev"); return !ok })
	release.Do(func() { close(gate) }) // let the Observer finish its evaluation
}

// The mesh feed (note §6): sampled off the worker, every outcome published,
// the last good graph kept on a failure, cancellation reaching the reads.
func TestLineServiceMeshFeed(t *testing.T) {
	var mu sync.Mutex
	mode := "ok"
	note := ""
	cancelled := make(chan struct{}, 4)
	reader := func(ctx context.Context) []clustermesh.Member {
		mu.Lock()
		m, n := mode, note
		mu.Unlock()
		if m == "hang" {
			<-ctx.Done() // the sample's deadline reaches the read
			cancelled <- struct{}{}
			return nil
		}
		return []clustermesh.Member{
			{Cluster: "prd-a", Identity: clustermesh.Identity{Name: "a"}, Peers: []string{"b"}, Present: true, Installed: true, ReadNote: n},
			{Cluster: "prd-b", Identity: clustermesh.Identity{Name: "b"}, Peers: []string{"a"}, Present: true, Installed: true},
		}
	}
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfgs := []config.ClusterConfig{{Name: "prd-a", Environment: "prd", Tags: map[string]string{"region": "a"}}, {Name: "prd-b", Environment: "prd", Tags: map[string]string{"region": "b"}}}
	svc := NewLineService(ctx, LineDeps{Sink: sink, Clusters: func() []config.ClusterConfig { return cfgs },
		Observer: func(string) *observe.Observer { return nil }, Mesh: NewMeshService(reader),
		ScanEvery: time.Hour, MeshEvery: 40 * time.Millisecond, MeshTimeout: 30 * time.Millisecond, Coalesce: 2 * time.Millisecond})
	h := &lineHarness{t: t, svc: svc}
	d := h.waitLine("a drawn mutual link", func(d LineDTO) bool { return len(d.Mesh.Drawn) == 1 && d.Mesh.Drawn[0].Mutual && d.Mesh.ReadAt != nil })
	first := *d.Mesh.ReadAt
	h.waitLine("a refreshed readAt on a quiet fleet", func(d LineDTO) bool { return d.Mesh.ReadAt != nil && d.Mesh.ReadAt.After(first) })

	mu.Lock()
	mode = "hang"
	mu.Unlock()
	d = h.waitLine("a failed sample is disclosed, the last good graph kept", func(d LineDTO) bool { return d.Mesh.Error != "" && len(d.Mesh.Drawn) == 1 })
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the deadline never reached the member read")
	}

	mu.Lock()
	mode, note = "ok", "forbidden"
	mu.Unlock()
	h.waitLine("recovery, with a node-only change", func(d LineDTO) bool {
		return d.Mesh.Error == "" && len(d.Mesh.Unknown) == 1 && d.Mesh.Unknown[0] == "prd-a"
	})
}

// A large supported fleet's recompute cost (note §3): 50 clusters with 500
// Kustomizations each.
//
//	go test ./internal/appbridge -run '^$' -bench LineRecompute -benchmem
func BenchmarkLineRecompute(b *testing.B) {
	sink := NewHealthSink(&rowsSinkRec{rows: map[string][]RowsDTO{}})
	s := &LineService{d: LineDeps{Sink: sink, Now: time.Now}, stations: map[string]*lineStation{}, hist: newLineHistory(sink.ObservationLog())}
	envs := []string{"dev", "test", "stg", "prd"}
	for c := 0; c < 50; c++ {
		name := "cluster-" + strings.Repeat("x", c%3) + string(rune('a'+c%26)) + string(rune('a'+c/26))
		var ks []healthsrc.Row[flux.Resource]
		for i := 0; i < 500; i++ {
			ks = append(ks, healthsrc.Row[flux.Resource]{Ref: healthsrc.RowRef{UID: name + "-k" + string(rune(i))},
				Data: flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "k" + strings.Repeat("i", i%7),
					Ready: flux.Ready, Revision: "main@sha1:" + lineSha('a'), SourceKind: "GitRepository", SourceName: "fleet", SourceNamespace: "flux-system"}})
		}
		src := []healthsrc.Row[flux.Source]{{Ref: healthsrc.RowRef{UID: name + "-src"}, Data: flux.Source{Kind: "GitRepository", Namespace: "flux-system",
			Name: "fleet", Ready: flux.Ready, URL: lineRepo, RefKind: "branch", RefName: "main", Revision: "main@sha1:" + lineSha('a')}}}
		live := func(kinds ...string) []observe.KindCoverage {
			var out []observe.KindCoverage
			for _, k := range kinds {
				out = append(out, observe.KindCoverage{Kind: k, Role: "counted", Coverage: observe.Live})
			}
			return out
		}
		st := &lineStation{cfg: config.ClusterConfig{Name: name, Environment: envs[c%4], Tags: map[string]string{"region": "r" + string(rune('a'+c%5))}},
			order: c, inc: 1, usable: true, booted: map[string]bool{}, parts: map[partitionRef]*stationPartition{},
			att: &lineAttachment{}, rows: map[string]observe.Rows{
				"flux/reconciliations": {Incarnation: 1, Task: healthsrc.TaskFlux, Part: observe.Part{ID: "reconciliations", Kinds: live("kustomizations", "helmreleases")},
					Rows: healthsrc.RowSet[flux.Resource]{Rows: ks}},
				"flux/sources": {Incarnation: 1, Task: healthsrc.TaskFlux, Part: observe.Part{ID: "sources",
					Kinds: live("gitrepositories", "helmrepositories", "ocirepositories", "buckets", "helmcharts")}, Rows: healthsrc.RowSet[flux.Source]{Rows: src}},
				"argo/applications": {Incarnation: 1, Task: healthsrc.TaskArgo, Part: observe.Part{ID: "applications",
					Kinds: []observe.KindCoverage{{Kind: "applications", Role: "counted", Coverage: observe.Absent}}}},
			}}
		s.stations[name] = st
	}
	s.recompute()
	if d := s.Line(); d.Applied != 50 {
		b.Fatalf("fixture: %d of %d applied", d.Applied, d.Compared)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.recompute()
	}
}
