package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// Codex's review of S3a-2: its probes, as regression tests.

func waitFor2s(t *testing.T, f func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatal("the fixture did not reach its handoff")
}

// R1: a member whose subscription changes while its read is in flight is
// re-keyed, never served the share read under the old scope; an unchanged
// member is served.
func TestASubscriptionMoveDuringAReadIsNotServed(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed"}[changed], func(t *testing.T) {
			r := newBucketRig(t, hostedSource(), "dev")
			r.am.hold, r.am.entered = make(chan struct{}), make(chan struct{}, 1)
			var once sync.Once
			release := func() { once.Do(func() { close(r.am.hold) }) }
			t.Cleanup(release)
			c := r.conns["dev"]
			key, _, _, _ := r.b.memberKey("dev")
			done := make(chan error, 1)
			go func() { _, _, err := c.ReadAlertsAt(context.Background(), key); done <- err }()
			<-r.am.entered
			r.b.mu.Lock()
			var bu *bucket
			for _, x := range r.b.buckets {
				bu = x
			}
			r.b.mu.Unlock()
			member := config.ClusterConfig{Name: "dev"}
			if changed {
				member.Alertmanager = &config.EndpointConfig{Source: "lgtm-we", Match: map[string]string{"cluster": "dev", "team": "new"}}
			}
			if _, err := r.sources.Apply([]config.SourceConfig{hostedSource()}, []config.ClusterConfig{{Name: "prd"}, member}); err != nil {
				t.Fatal(err)
			}
			release()
			err := <-done
			if !changed {
				if err != nil {
					t.Fatalf("an unchanged subscription was not served: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("the old-scope share was served: %v", err)
			}
			// the read itself gave the moved member nothing: its rows matched
			// no member
			bu.mu.Lock()
			o := bu.outcome
			bu.mu.Unlock()
			if o == nil {
				t.Fatal("fixture: the read did not land")
			}
			if _, in := o.cohort[c.ID()]; in || len(o.shares[c.ID()]) > 0 || o.unmatched != 3 {
				t.Errorf("the read kept the moved member: cohort %v, shares %d, unmatched %d", o.cohort, len(o.shares[c.ID()]), o.unmatched)
			}
			again, _ := r.mustRead("dev")
			if !strings.Contains(again.Source, "team=new") {
				t.Fatalf("the re-keyed read is not under the new scope: %q", again.Source)
			}
		})
	}
}

// R2: waiting for the installation's turn is within the read's budget, and
// a cancelled bucket stops waiting.
func TestTheGateWaitIsBoundedAndCancellable(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	r.b.timeout = 30 * time.Millisecond
	gate := r.b.gate("lgtm-we")
	gate <- struct{}{} // another bucket's worker holds the installation
	defer func() { <-gate }()
	key, sub, access, _ := r.b.memberKey("dev")
	bu, ok := r.b.join(r.conns["dev"].ID(), bucketKey{Source: sub.Source.ID, InstRev: sub.InstRev, Access: access, Tenant: sub.Tenant}, sub, key)
	if !ok {
		t.Fatal("fixture: the member did not join")
	}
	bu.nudge()
	waitFor2s(t, func() bool {
		bu.mu.Lock()
		defer bu.mu.Unlock()
		return bu.outcome != nil && bu.outcome.err != nil && strings.Contains(bu.outcome.err.Error(), "was not read in time")
	})
	if r.reads() != 0 {
		t.Fatal("a read ran without its turn")
	}
	bu.nudge()
	time.Sleep(5 * time.Millisecond)
	bu.cancel()
	select {
	case <-bu.done:
	case <-time.After(time.Second):
		t.Fatal("a cancelled bucket stayed waiting for the installation's turn")
	}
}

// R3: a stopped connection's late read registers nothing.
func TestAStoppedMemberCannotJoinAgain(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	r.mustRead("dev")
	c := r.conns["dev"]
	c.am.readMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := c.ReadAlertsAt(ctx, c.AlertsIdentity()); done <- err }()
	c.Stop(time.Second)
	cancel()
	c.am.readMu.Unlock()
	if err := <-done; err == nil {
		t.Error("the ended read succeeded")
	}
	// a read that was not cancelled, after the stop, joins nothing either
	if _, _, err := c.ReadAlertsAt(context.Background(), c.AlertsIdentity()); err == nil {
		t.Error("a stopped connection read again")
	}
	r.b.mu.Lock()
	_, joined := r.b.members[c.ID()]
	n := len(r.b.buckets)
	r.b.mu.Unlock()
	if joined || n != 0 {
		t.Fatalf("a stopped connection joined again: member=%v buckets=%d", joined, n)
	}
}

// R4: one join asks for one early read: a member waiting for the follow-up
// already in flight asks for no other.
func TestOneJoinAsksForOneFollowup(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	first, second := make(chan struct{}), make(chan struct{})
	enteredFirst, enteredSecond := make(chan struct{}), make(chan struct{})
	var n atomic.Int32
	var once1, once2 sync.Once
	release1 := func() { once1.Do(func() { close(first) }) }
	release2 := func() { once2.Do(func() { close(second) }) }
	api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/monitoring/services/http:am:web/proxy", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/api/v2/alerts") {
			switch n.Add(1) {
			case 1:
				close(enteredFirst)
				<-first
			case 2:
				close(enteredSecond)
				<-second
			}
			_, _ = w.Write([]byte(bucketAlerts))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})))
	t.Cleanup(api.Close)
	t.Cleanup(release2)
	t.Cleanup(release1)
	r.host.restConfig.Host = api.URL
	dev, stg := make(chan error, 1), make(chan error, 1)
	go func() { _, _, err := r.read("dev"); dev <- err }()
	<-enteredFirst
	go func() { _, _, err := r.read("stg"); stg <- err }()
	var bu *bucket
	waitFor2s(t, func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		for _, b := range r.b.buckets {
			b.mu.Lock()
			cnt := len(b.cohort)
			b.mu.Unlock()
			if cnt == 2 {
				bu = b
				return true
			}
		}
		return false
	})
	release1()
	<-enteredSecond
	waitFor2s(t, func() bool { bu.mu.Lock(); defer bu.mu.Unlock(); return len(bu.waiters) > 0 })
	release2()
	if err := <-dev; err != nil {
		t.Fatal(err)
	}
	if err := <-stg; err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if got := n.Load(); got != 2 {
		t.Fatalf("one join made %d reads; want the first and one follow-up", got)
	}
}

// hostLeases records the host leases a bucket takes: how many are held.
type hostLeases struct {
	mu    sync.Mutex
	held  int
	whats []string
	ctx   context.Context
}

func (h *hostLeases) leaser(conn func() *ClusterConn) HostLeaser {
	return func(_, what string) (Conn, context.Context, func(), error) {
		h.mu.Lock()
		h.held++
		h.whats = append(h.whats, what)
		h.mu.Unlock()
		var once sync.Once
		return conn(), h.ctx, func() {
			once.Do(func() { h.mu.Lock(); h.held--; h.mu.Unlock() })
		}, nil
	}
}

func (h *hostLeases) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held
}

// R6: a hosted bucket depends on its host's connection for its whole life:
// a lease held between reads, which the host's plans list, and whose
// retirement ends the idle bucket; its members join a new one.
func TestAnIdleBucketDependsOnItsHost(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	hostctx, retire := context.WithCancel(context.Background())
	defer retire()
	h := &hostLeases{ctx: hostctx}
	r.b.leaser = h.leaser(func() *ClusterConn { return r.host })
	r.mustRead("dev")
	r.b.mu.Lock()
	var bu *bucket
	for _, x := range r.b.buckets {
		bu = x
	}
	r.b.mu.Unlock()
	waitFor2s(t, func() bool { return h.count() == 1 }) // the read's own lease is released; the bucket's is held
	r.host.Stop(time.Second)
	retire()
	select {
	case <-bu.done:
	case <-time.After(time.Second):
		t.Fatal("the host was retired, but its idle bucket kept running")
	}
	waitFor2s(t, func() bool { return h.count() == 0 })
	r.b.mu.Lock()
	_, member := r.b.members[r.conns["dev"].ID()]
	n := len(r.b.buckets)
	r.b.mu.Unlock()
	if member || n != 0 {
		t.Fatalf("the retired bucket kept its members: member=%v buckets=%d", member, n)
	}
}

// R6: the bucket's lease is on the connection it is keyed to; a host
// reconnected before the bucket could hold it leaves the bucket obsolete.
func TestABucketKeyedToAnOldHostIsRetired(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	next := NewClusterConn("prd", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	usable(next, 1)
	h := &hostLeases{ctx: context.Background()}
	r.b.leaser = h.leaser(func() *ClusterConn { return next })
	key, sub, access, _ := r.b.memberKey("dev") // keyed to r.host
	bu, ok := r.b.join(r.conns["dev"].ID(), bucketKey{Source: sub.Source.ID, InstRev: sub.InstRev, Access: access, Tenant: sub.Tenant}, sub, key)
	if !ok {
		t.Fatal("fixture: the member did not join")
	}
	select {
	case <-bu.done:
	case <-time.After(time.Second):
		t.Fatal("a bucket keyed to an old host connection held the new one")
	}
	if h.count() != 0 {
		t.Fatal("the lease on the new connection was kept")
	}
}

// R3: a read whose context has ended registers nothing, on a connection
// still running too.
func TestAnEndedReadRegistersNothing(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	c := r.conns["dev"]
	key, _, _, _ := r.b.memberKey("dev")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.ReadAlertsAt(ctx, key); err == nil {
		t.Fatal("an ended read succeeded")
	}
	r.b.mu.Lock()
	_, joined := r.b.members[c.ID()]
	n := len(r.b.buckets)
	r.b.mu.Unlock()
	if joined || n != 0 {
		t.Fatalf("an ended read registered: member=%v buckets=%d", joined, n)
	}
}

// R4: a read that starts covers its whole cohort, so an early read asked
// for meanwhile - its cadence came first - is spent, not run after it.
func TestAStartingReadSpendsTheEarlyRead(t *testing.T) {
	r := newBucketRig(t, hostedSource())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// no run goroutine: the read is the cadence's, started by hand
	bu := &bucket{b: r.b, ctx: ctx, cancel: cancel, early: make(chan struct{}, 1), cohort: map[ConnID]memberScope{}}
	bu.pending = true
	bu.nudge()
	bu.readOnce()
	if len(bu.early) != 0 || bu.pending {
		t.Fatalf("the early read asked for before this one is still owed: early=%d pending=%v", len(bu.early), bu.pending)
	}
}

// R6: a host that could not be leased when the bucket began is leased
// before its next read, and held between reads.
func TestAHostLeasedLaterIsHeld(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	h := &hostLeases{ctx: context.Background()}
	var calls atomic.Int32
	inner := h.leaser(func() *ClusterConn { return r.host })
	r.b.leaser = func(host, what string) (Conn, context.Context, func(), error) {
		if calls.Add(1) == 1 {
			return nil, nil, nil, errors.New("prd is applying a change to its settings")
		}
		return inner(host, what)
	}
	r.mustRead("dev")
	waitFor2s(t, func() bool { return h.count() == 1 })
}
