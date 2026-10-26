package appbridge

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/policy"
	"github.com/moomora/klyx/internal/workloads"
)

// Codex's review of S2a (2026-10-26): its seven probes, inverted against
// the fixes.

// F1: a chat whose connection is retired between its lease's admission and
// its registration is still ended explicitly - never left running on a
// retired connection with no end said.
func TestAChatRetiredBeforeItsLeaseReturnsIsEnded(t *testing.T) {
	was := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(was)
	r := newLifeRig(t, true)
	f := newToolsFixture(t, answer("unused"))
	done := make(chan error, 1)
	f.svc.setLeaser(func(c string, w Work) (*Lease, error) {
		le, err := r.life.Acquire(c, w)
		if err != nil {
			return nil, err
		}
		le.Conn = ChatConn(f.conn)
		go func() {
			p, err := r.reg.Prepare(config.ClusterConfig{Name: c})
			if err == nil {
				err = r.life.Reconnect(context.Background(), p, "prd reconnected")
			}
			done <- err
		}()
		runtime.Gosched() // the end runs before Start has its lease back
		return le, nil
	})
	if st := f.svc.Start("prd"); st.Error != "" {
		t.Fatal(st.Error)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !waitUntil(t, 3*time.Second, func() bool {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		return strings.Contains(f.svc.convs["prd"].ended, "prd reconnected")
	}) {
		t.Fatal("a chat retired before its registration was not ended")
	}
}

// F2: an old connection's end closes its own subscription only - never a
// replacement opened on the same key - and its ended notice never lands
// after the replacement opened.
func TestAnOldEventsEndCannotCloseANewSubscription(t *testing.T) {
	was := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(was)
	c := &fakeEventsConn{}
	em := &liveStatusRec{}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, em)
	defer s.CloseAll()
	var end func(string)
	s.setLeaser(func(cluster string, w Work) (*Lease, error) {
		end = w.End
		return PlainLease(cluster, EventsConn(c)), nil
	})
	if result := s.OpenLiveEvents("prd", "shop"); !result.OK {
		t.Fatal(result)
	}
	oldEnd := end
	oldEnd("prd reconnected")
	if result := s.OpenLiveEvents("prd", "shop"); !result.OK {
		t.Fatal(result)
	}
	time.Sleep(50 * time.Millisecond) // any deferred cleanup has run
	s.live.mu.Lock()
	current := s.live.subs["events:prd:shop"]
	s.live.mu.Unlock()
	if current == nil {
		t.Fatal("the old connection's end closed the replacement's subscription")
	}
	oldEnd("prd reconnected") // a second end of the old: nothing
	s.live.mu.Lock()
	still := s.live.subs["events:prd:shop"] == current
	s.live.mu.Unlock()
	em.mu.Lock()
	n := len(em.ended)
	em.mu.Unlock()
	if !still || n != 1 {
		t.Fatalf("replacement kept %v, ended notices %d", still, n)
	}
}

// F3: a comparison member's connection is the investigation's work on that
// member: listed under it, and retiring it ends the investigation; the
// member's lease goes with the investigation.
func TestAComparisonIsWorkOnItsMember(t *testing.T) {
	r := newLifeRig(t, true)
	for _, name := range []string{"x", "stg"} {
		r.pol.Seed(name, false)
		if err := r.reg.Add(r.ctx, config.ClusterConfig{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	f := newFleetFixture(t)
	f.svc.setLeaser(func(c string, w Work) (*Lease, error) {
		le, err := r.life.Acquire(c, w)
		if err == nil {
			le.Conn = ChatConn(f.conns[c])
		}
		return le, err
	})
	st := f.svc.StartInvestigation("x", podAnchor)
	if st.Error != "" {
		t.Fatal(st.Error)
	}
	if st = f.svc.SetComparison("x", st.ConversationID, []string{"stg"}); st.Notice != "" || len(st.Comparison) != 1 {
		t.Fatal(st)
	}
	if ends, _ := r.life.ClusterWork("stg"); len(ends) != 1 || ends[0].Kind != kindInvestigation {
		t.Fatalf("the member's work: %+v", ends)
	}
	// a replaced set releases the old member's lease
	if st = f.svc.SetComparison("x", st.ConversationID, nil); st.Notice != "" {
		t.Fatal(st.Notice)
	}
	if ends, _ := r.life.ClusterWork("stg"); len(ends) != 0 {
		t.Fatalf("a dropped member still lists the investigation: %+v", ends)
	}
	if st = f.svc.SetComparison("x", st.ConversationID, []string{"stg"}); st.Notice != "" {
		t.Fatal(st.Notice)
	}
	if err := r.life.Remove("stg", "stg removed"); err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Lock()
	why := f.svc.convs[invKey("x")].ended
	f.svc.mu.Unlock()
	if !strings.Contains(why, "stg removed") {
		t.Fatalf("the member's removal did not end the investigation: %q", why)
	}
	if ends, _ := r.life.ClusterWork("x"); len(ends) != 0 {
		t.Fatalf("an ended investigation is still work on its cluster: %+v", ends)
	}
}

// F4: a credential activation changes the connection current inside the
// fleet lock, never one captured before a reconnect held it.
func TestACredentialUsesTheConnectionInsideTheFleetLock(t *testing.T) {
	e := newEndpointsEnv(t)
	startup, err := config.Load(e.path)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewConfigService(e.path, startup, nil)
	e.conn.SetMetricsConfig(*startup.Clusters[0].Metrics)
	old := e.conn
	newer := &endpointConnFake{cfg: *startup.Clusters[0].Metrics}
	var mu sync.Mutex
	current := old
	e.svc.lookup = func(c string) (EndpointConn, bool) {
		mu.Lock()
		defer mu.Unlock()
		return boundConnFake{endpointConnFake: current, cluster: c, revs: e.revs}, true
	}
	WireEndpointActivation(e.svc, cs)
	const next = "https://new-mimir.example/prometheus"
	e.edit(t, "https://mimir.example/prometheus", next)
	var row EndpointRowDTO
	for _, r := range e.svc.GetEndpoints().Rows {
		if r.Cluster == "prd" && r.Kind == config.EndpointMetrics {
			row = r
		}
	}
	entered := make(chan struct{})
	e.svc.fleetLock = func() func() { close(entered); return FleetApplyLock(cs) }
	unlock := FleetApplyLock(cs)
	done := make(chan EndpointActionDTO, 1)
	go func() { done <- e.svc.SetCredential(row.Cluster, row.Kind, row.Revision, "in-memory-test-key") }()
	<-entered
	mu.Lock()
	current = newer // a reconnect, under the held fleet lock
	mu.Unlock()
	unlock()
	if result := <-done; !result.OK {
		t.Fatal(result)
	}
	if got := newer.MetricsConfig().Endpoint; got != next {
		t.Fatalf("the activation changed the retired connection: current %s, retired %s", got, old.MetricsConfig().Endpoint)
	}
	if cc, ok := LiveClusterConfig(cs)("prd"); !ok || cc.Metrics == nil || cc.Metrics.Endpoint != next {
		t.Fatalf("the running entry was published from another connection: %+v", cc.Metrics)
	}
}

// F6: a removal forgets the cluster's conversations, freeing their slots.
func TestARemovalForgetsConversations(t *testing.T) {
	r := newLifeRig(t, true)
	f := newToolsFixture(t, answer("unused"))
	UseLeases(r.life, f.svc)
	r.life.OnForget(ForgetChats(f.svc))
	if st := f.svc.Start("prd"); st.Error != "" {
		t.Fatal(st.Error)
	}
	if err := r.life.Remove("prd", "prd removed"); err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Lock()
	n := len(f.svc.convs)
	f.svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("a removed cluster keeps %d conversation(s)", n)
	}
}

// F7: an endpoint reader built before a removal is never current again,
// even once the name is added back, as main wires it.
func TestARemovalInvalidatesEndpointReadersAcrossReAdd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	revs := endpointcred.NewRegistry()
	reg := fleet.NewRegistry(&config.Config{Clusters: []config.ClusterConfig{{Name: "prd"}}}, func(cc config.ClusterConfig) (fleet.Conn, error) {
		c, err := lifeFactory(cc)
		if err == nil {
			c.(*fleet.ClusterConn).WithCredentials(fleet.Credentials{Revisions: revs}).WithLoki(&config.EndpointConfig{Endpoint: "https://logs.example"})
		}
		return c, err
	})
	pol := policy.New()
	pol.Seed("prd", true)
	life := NewLifecycle(reg, pol, nil, nil)
	life.OnForget(ForgetEndpointRevisions(revs))
	reg.Start(ctx)
	gates := map[string]func() bool{}
	for _, kind := range config.EndpointKinds {
		gates[kind] = revs.Live("prd", kind, revs.Revision("prd", kind))
	}
	old, _ := reg.Conn("prd")
	reader, err := old.(*fleet.ClusterConn).Loki()
	if err != nil {
		t.Fatal(err)
	}
	if !reader.Current() {
		t.Fatal("rig: the reader starts current")
	}
	if err := life.Remove("prd", "prd removed"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(ctx, config.ClusterConfig{Name: "prd"}); err != nil {
		t.Fatal(err)
	}
	pol.Seed("prd", true)
	for kind, live := range gates {
		if live() {
			t.Errorf("a %s client built before the removal is still current", kind)
		}
	}
	if reader.Current() {
		t.Error("the old Loki reader is still current after removal and re-add")
	}
	if revs.Revoked("prd", config.EndpointLoki) {
		t.Error("a removal revoked a kept credential")
	}
}

type wordRec struct {
	mu    sync.Mutex
	words []string
}

func (w *wordRec) OnSummary(s observe.Summary) {
	w.mu.Lock()
	w.words = append(w.words, s.Connection)
	w.mu.Unlock()
}
func (w *wordRec) OnRows(observe.Rows)                   {}
func (w *wordRec) OnTransitions(observe.TransitionBatch) {}

func (w *wordRec) connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.words {
		if strings.HasPrefix(s, "connected") {
			return true
		}
	}
	return false
}

// F5 through the lifecycle: the Observer is told the connection is usable
// only once the lifecycle has published the move - and then is told.
func TestTheLifecycleHandsThePublishedStateToHealth(t *testing.T) {
	r := newLifeRigWith(t, false, true)
	rec := &wordRec{}
	r.sink.AddListener(rec)
	if !waitUntil(t, 5*time.Second, func() bool {
		r.life.qmu.Lock()
		defer r.life.qmu.Unlock()
		return len(r.life.queue) == 1
	}) {
		t.Fatal("no usability event")
	}
	time.Sleep(300 * time.Millisecond)
	if rec.connected() {
		t.Fatal("health heard connected before the lifecycle published it")
	}
	go r.life.Run(r.ctx)
	if !waitUntil(t, 5*time.Second, rec.connected) {
		t.Fatal("health never heard the published state")
	}
}

// F2's reconciliation: a subscription whose connection ended before it was
// recorded is refused, and nothing of it keeps running.
func TestLiveEventsEndedBeforeTheyOpenAreRefused(t *testing.T) {
	c := &fakeEventsConn{}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, &liveStatusRec{})
	defer s.CloseAll()
	s.setLeaser(func(cluster string, w Work) (*Lease, error) {
		w.End("prd reconnected") // the end runs before the open returns
		return PlainLease(cluster, EventsConn(c)), nil
	})
	if res := s.OpenLiveEvents("prd", "shop"); res.OK || !strings.Contains(res.Error, "prd reconnected") {
		t.Fatalf("opened on an ended connection: %+v", res)
	}
	s.live.mu.Lock()
	n := len(s.live.subs)
	s.live.mu.Unlock()
	s.lmu.Lock()
	m := len(s.leases)
	s.lmu.Unlock()
	if n != 0 || m != 0 {
		t.Fatalf("subscriptions %d, entries %d", n, m)
	}
}

// F3's refusal: a comparison set refused part-way holds no member's lease.
func TestARefusedComparisonHoldsNoMembersLease(t *testing.T) {
	r := newLifeRig(t, true)
	for _, name := range []string{"x", "stg"} {
		r.pol.Seed(name, false)
		if err := r.reg.Add(r.ctx, config.ClusterConfig{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	f := newFleetFixture(t)
	f.svc.setLeaser(func(c string, w Work) (*Lease, error) {
		le, err := r.life.Acquire(c, w)
		if err == nil {
			le.Conn = ChatConn(f.conns[c])
		}
		return le, err
	})
	st := f.svc.StartInvestigation("x", podAnchor)
	if st = f.svc.SetComparison("x", st.ConversationID, []string{"stg", "x"}); st.Notice == "" {
		t.Fatal("the investigated cluster was accepted as a member")
	}
	if ends, _ := r.life.ClusterWork("stg"); len(ends) != 0 {
		t.Fatalf("a refused set kept a member's lease: %+v", ends)
	}
}

// F4's audit: removing a credential is serialized with the fleet Apply
// lock, like every credential action.
func TestRemovingACredentialWaitsForTheFleetLock(t *testing.T) {
	e := newEndpointsEnv(t)
	startup, err := config.Load(e.path)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewConfigService(e.path, startup, nil)
	WireEndpointActivation(e.svc, cs)
	var row EndpointRowDTO
	for _, r := range e.svc.GetEndpoints().Rows {
		if r.Cluster == "prd" && r.Kind == config.EndpointMetrics {
			row = r
		}
	}
	unlock := FleetApplyLock(cs)
	done := make(chan EndpointActionDTO, 1)
	go func() { done <- e.svc.RemoveCredential(row.Cluster, row.Kind, row.Revision) }()
	select {
	case res := <-done:
		unlock()
		t.Fatalf("removed beside an Apply holding the fleet lock: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	<-done
	if !e.revs.Revoked("prd", config.EndpointMetrics) {
		t.Fatal("not revoked")
	}
}

// Codex's re-review of S2a (2026-10-26): its three probes, and two more
// cases.

type countingEvents struct {
	fakeEventsConn
	mu      sync.Mutex
	reads   int
	onWatch func()
}

func (c *countingEvents) ListEvents(ctx context.Context, ns string) ([]workloads.EventSummary, bool, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.fakeEventsConn.ListEvents(ctx, ns)
}

func (c *countingEvents) WatchDirty(ctx context.Context, ns string, kinds []string, onDirty func(), onLive func(bool)) (func(), error) {
	if c.onWatch != nil {
		c.onWatch()
	}
	return c.fakeEventsConn.WatchDirty(ctx, ns, kinds, onDirty, onLive)
}

func (c *countingEvents) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

type eventsEm struct {
	mu          sync.Mutex
	data, ended int
}

func (e *eventsEm) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if name == "liveEvents:prd:shop" {
		e.data++
	}
	if st, ok := data.(liveStatusDTO); ok && st.Ended != "" {
		e.ended++
	}
}

func (e *eventsEm) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.data, e.ended
}

// RR1: an open its connection ended before it bound starts nothing - no
// read, no emit - and is refused.
func TestAnEventsOpenEndedBeforeItStartsNeitherReadsNorEmits(t *testing.T) {
	r := newLifeRig(t, true)
	c := &countingEvents{}
	em := &eventsEm{}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, em)
	defer s.CloseAll()
	s.setLeaser(func(cluster string, w Work) (*Lease, error) {
		le, err := r.life.Acquire(cluster, w)
		if err != nil {
			return nil, err
		}
		le.Conn = EventsConn(c)
		r.reconnect() // its end runs before the open resumes
		return le, nil
	})
	if res := s.OpenLiveEvents("prd", "shop"); res.OK {
		t.Fatal("an open on a retired connection succeeded")
	}
	if data, _ := em.counts(); c.readCount() != 0 || data != 0 {
		t.Fatalf("reads %d, data emits %d", c.readCount(), data)
	}
}

// RR1: an end that comes while the subscription starts stops it before it
// reads or emits; the page is told once, and the open is refused.
func TestAnEventsOpenEndedWhileItStartsNeitherReadsNorEmits(t *testing.T) {
	c := &countingEvents{}
	em := &eventsEm{}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, em)
	defer s.CloseAll()
	var end func(string)
	s.setLeaser(func(cluster string, w Work) (*Lease, error) {
		end = w.End
		return PlainLease(cluster, EventsConn(c)), nil
	})
	c.onWatch = func() { end("prd reconnected") } // registered, its watch starting
	if res := s.OpenLiveEvents("prd", "shop"); res.OK || !strings.Contains(res.Error, "prd reconnected") {
		t.Fatalf("got %+v", res)
	}
	time.Sleep(50 * time.Millisecond)
	data, ended := em.counts()
	s.live.mu.Lock()
	n := len(s.live.subs)
	s.live.mu.Unlock()
	if c.readCount() != 0 || data != 0 || ended != 1 || n != 0 {
		t.Fatalf("reads %d, data %d, ended notices %d, subscriptions %d", c.readCount(), data, ended, n)
	}
}

// RR2: a member never committed to the comparison - its set refused -
// cannot end the investigation later, nor can one whose connection retired
// while the set was chosen.
func TestAnUncommittedMemberCannotEndTheInvestigation(t *testing.T) {
	for _, chosen := range [][]string{{"stg", "x"}, {"stg"}} {
		t.Run(strings.Join(chosen, ","), func(t *testing.T) {
			r := newLifeRig(t, true)
			for _, name := range []string{"x", "stg"} {
				r.pol.Seed(name, false)
				if err := r.reg.Add(r.ctx, config.ClusterConfig{Name: name}); err != nil {
					t.Fatal(err)
				}
			}
			f := newFleetFixture(t)
			acquired, returnLease := make(chan struct{}), make(chan struct{})
			queued, deliver := make(chan struct{}), make(chan struct{})
			f.svc.setLeaser(func(cluster string, w Work) (*Lease, error) {
				if cluster == "stg" {
					original := w.End
					w.End = func(why string) { close(queued); <-deliver; original(why) }
				}
				le, err := r.life.Acquire(cluster, w)
				if err != nil {
					return nil, err
				}
				le.Conn = ChatConn(f.conns[cluster])
				if cluster == "stg" {
					close(acquired)
					<-returnLease
				}
				return le, nil
			})
			inv := f.svc.StartInvestigation("x", podAnchor)
			if inv.Error != "" {
				t.Fatal(inv.Error)
			}
			done := make(chan ChatStateDTO, 1)
			go func() { done <- f.svc.SetComparison("x", inv.ConversationID, chosen) }()
			<-acquired
			removed := make(chan error, 1)
			go func() { removed <- r.life.Remove("stg", "stg removed") }()
			<-queued
			close(returnLease)
			if refused := <-done; refused.Notice == "" || len(refused.Comparison) != 0 {
				t.Fatalf("expected a refused comparison: %+v", refused)
			}
			close(deliver)
			if err := <-removed; err != nil {
				t.Fatal(err)
			}
			f.svc.mu.Lock()
			why := f.svc.convs[invKey("x")].ended
			f.svc.mu.Unlock()
			if why != "" {
				t.Fatalf("an uncommitted member ended the investigation: %s", why)
			}
		})
	}
}

// RR3: a token move changes nothing - keychain, file - before it holds the
// fleet lock.
func TestTheTokenMoveWaitsBeforeItChangesAnything(t *testing.T) {
	e := newEndpointsEnv(t)
	cfg, err := config.Load(e.path)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewConfigService(e.path, cfg, nil)
	WireEndpointActivation(e.svc, cs)
	var row EndpointRowDTO
	for _, r := range e.svc.GetEndpoints().Rows {
		if r.Cluster == "dev" && r.Kind == config.EndpointMetrics {
			row = r
		}
	}
	before, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	revBefore := e.revs.Revision("dev", config.EndpointMetrics)
	entered := make(chan struct{})
	e.svc.fleetLock = func() func() { close(entered); return FleetApplyLock(cs) }
	unlock := FleetApplyLock(cs)
	done := make(chan EndpointActionDTO, 1)
	go func() { done <- e.svc.MoveToKeychain(row.Cluster, row.Revision) }()
	<-entered
	time.Sleep(50 * time.Millisecond)
	_, keyErr := e.store.Get(endpointcred.Account("dev", config.EndpointMetrics))
	during, err := os.ReadFile(e.path)
	revDuring := e.revs.Revision("dev", config.EndpointMetrics)
	unlock()
	result := <-done
	if err != nil {
		t.Fatal(err)
	}
	if keyErr == nil || string(during) != string(before) || revDuring != revBefore {
		t.Fatalf("the move changed state before it held the fleet lock: key %v, file changed %v, revision moved %v",
			keyErr == nil, string(during) != string(before), revDuring != revBefore)
	}
	if !result.OK {
		t.Fatalf("the move: %+v", result)
	}
}

// RR1's delivery gate: a compute already running when its subscription is
// detached delivers nothing, and the detach's notice is the last thing the
// subscription said.
func TestADetachedSubscriptionDeliversNothingMore(t *testing.T) {
	r := newLiveRegistry()
	var mu sync.Mutex
	var said []string
	say := func(what string) { mu.Lock(); said = append(said, what); mu.Unlock() }
	computing, finish := make(chan struct{}), make(chan struct{})
	sub := newLiveSub()
	go r.openSub("k", sub,
		func(func(), func(bool)) (func(), error) { return func() {}, nil },
		func() (any, bool) { close(computing); <-finish; return "rows", true },
		func(any) { say("data") },
		func(bool) { say("live") },
	)
	<-computing
	if !r.detach("k", sub, func() { say("ended") }) {
		t.Fatal("not detached")
	}
	close(finish)
	r.stopSub(sub)
	mu.Lock()
	defer mu.Unlock()
	if len(said) != 1 || said[0] != "ended" {
		t.Fatalf("said %v after its detach", said)
	}
}

// RR1's windows: an end just before the subscription registers stops it
// there - no registration, no read - and the open is refused.
func TestAnEventsOpenEndedJustBeforeItRegistersStartsNothing(t *testing.T) {
	c := &countingEvents{}
	em := &eventsEm{}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, em)
	defer s.CloseAll()
	var end func(string)
	s.setLeaser(func(cluster string, w Work) (*Lease, error) {
		end = w.End
		return PlainLease(cluster, EventsConn(c)), nil
	})
	watched := false
	c.onWatch = func() { watched = true }
	s.live.stage = func(stage string, _ *liveSub) {
		if stage == "register" {
			end("prd reconnected")
		}
	}
	if res := s.OpenLiveEvents("prd", "shop"); res.OK {
		t.Fatal("opened")
	}
	s.live.mu.Lock()
	n := len(s.live.subs)
	s.live.mu.Unlock()
	if watched || c.readCount() != 0 || n != 0 {
		t.Fatalf("watched %v, reads %d, subscriptions %d", watched, c.readCount(), n)
	}
}

// A subscription stopped before it registers never replaces the one in its
// place; one stopped as it registers never watches or computes, and leaves.
func TestAStoppedSubscriptionNeverStarts(t *testing.T) {
	r := newLiveRegistry()
	noop := func(func(), func(bool)) (func(), error) { return func() {}, nil }
	old := r.open("k", noop, func() (any, bool) { return nil, true }, func(any) {}, func(bool) {})
	defer r.closeAll()
	early := newLiveSub()
	early.stop()
	r.openSub("k", early, noop, func() (any, bool) { return nil, true }, func(any) {}, func(bool) {})
	r.mu.Lock()
	kept := r.subs["k"] == old
	r.mu.Unlock()
	if !kept {
		t.Fatal("a stopped subscription replaced the one in its place")
	}
	late := newLiveSub()
	r.stage = func(stage string, sub *liveSub) {
		if stage == "watch" && sub == late {
			sub.stop()
		}
	}
	watched, computed := false, false
	r.openSub("j", late, func(func(), func(bool)) (func(), error) { watched = true; return func() {}, nil },
		func() (any, bool) { computed = true; return nil, true }, func(any) {}, func(bool) {})
	r.mu.Lock()
	_, left := r.subs["j"]
	r.mu.Unlock()
	if watched || computed || left {
		t.Fatalf("watched %v, computed %v, registered %v", watched, computed, left)
	}
}

type blockingEvents struct {
	fakeEventsConn
	reading chan struct{}
	ended   chan error
}

func (c *blockingEvents) ListEvents(ctx context.Context, _ string) ([]workloads.EventSummary, bool, error) {
	close(c.reading)
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
	}
	c.ended <- ctx.Err()
	return nil, false, ctx.Err()
}

// A live subscription's read runs under its lease: a read in flight when
// its connection is retired ends with it, at once.
func TestALiveEventsReadEndsWithItsConnection(t *testing.T) {
	r := newLifeRig(t, true)
	c := &blockingEvents{reading: make(chan struct{}), ended: make(chan error, 1)}
	s := NewEventsService(func(string) (EventsConn, bool) { return c, true }, &eventsEm{})
	defer s.CloseAll()
	s.setLeaser(r.leasedVia(EventsConn(c)))
	go s.OpenLiveEvents("prd", "shop")
	<-c.reading
	r.reconnect()
	select {
	case err := <-c.ended:
		if err == nil {
			t.Fatal("the read ended without its connection's cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("a read in flight outlived its connection")
	}
}

// A member replaced in the set before its in-flight end lands does not end
// the investigation: only the committed set's members do.
func TestAReplacedMembersEndActsOnNothing(t *testing.T) {
	r := newLifeRig(t, true)
	for _, name := range []string{"x", "stg"} {
		r.pol.Seed(name, false)
		if err := r.reg.Add(r.ctx, config.ClusterConfig{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	f := newFleetFixture(t)
	queued, deliver := make(chan struct{}), make(chan struct{})
	f.svc.setLeaser(func(cluster string, w Work) (*Lease, error) {
		if cluster == "stg" {
			original := w.End
			w.End = func(why string) { close(queued); <-deliver; original(why) }
		}
		le, err := r.life.Acquire(cluster, w)
		if err == nil {
			le.Conn = ChatConn(f.conns[cluster])
		}
		return le, err
	})
	inv := f.svc.StartInvestigation("x", podAnchor)
	if st := f.svc.SetComparison("x", inv.ConversationID, []string{"stg"}); st.Notice != "" {
		t.Fatal(st.Notice)
	}
	removed := make(chan error, 1)
	go func() { removed <- r.life.Remove("stg", "stg removed") }()
	<-queued // stg's end is in flight
	if st := f.svc.SetComparison("x", inv.ConversationID, nil); st.Notice != "" {
		t.Fatal(st.Notice)
	}
	close(deliver)
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Lock()
	why := f.svc.convs[invKey("x")].ended
	f.svc.mu.Unlock()
	if why != "" {
		t.Fatalf("a replaced member ended the investigation: %s", why)
	}
}
