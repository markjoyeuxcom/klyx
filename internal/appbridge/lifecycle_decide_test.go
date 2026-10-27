package appbridge

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/policy"
)

// Settings S2 §4.5 and §12: the commit gate's decision. An Apply decides
// under the gate against the activation its plan bound; a usability queued
// at the gate is applied after the decision - stale when the decision
// retired its identity, applied once when it did not.

func (r *lifeRig) prepare(context string) *fleet.Prepared {
	r.t.Helper()
	p, err := r.reg.Prepare(config.ClusterConfig{Name: "prd", Context: context})
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

// queued takes the queued usability event of id out of the lifecycle's
// queue, once it is there.
func (r *lifeRig) queued(id fleet.ConnID) fleet.UsabilityEvent {
	r.t.Helper()
	var ev fleet.UsabilityEvent
	if !waitUntil(r.t, 5*time.Second, func() bool {
		r.life.qmu.Lock()
		defer r.life.qmu.Unlock()
		for i, e := range r.life.queue {
			if e.ID == id {
				ev = e
				r.life.queue = append(r.life.queue[:i], r.life.queue[i+1:]...)
				return true
			}
		}
		return false
	}) {
		r.t.Fatal("no usability event")
	}
	return ev
}

type releaseRec struct {
	mu  sync.Mutex
	ids []fleet.ConnID
}

func (rr *releaseRec) on(_ string, id fleet.ConnID) {
	rr.mu.Lock()
	rr.ids = append(rr.ids, id)
	rr.mu.Unlock()
}

func (rr *releaseRec) got() []fleet.ConnID {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]fleet.ConnID(nil), rr.ids...)
}

// heldFor holds prd for a new attempt through the reconnect class's
// decision, and returns it.
func (r *lifeRig) heldFor(context string) *fleet.Prepared {
	r.t.Helper()
	tok, err := r.pol.Reserve("prd")
	if err != nil {
		r.t.Fatal(err)
	}
	defer r.pol.Unreserve(tok)
	b, _ := r.life.BoundOf("prd")
	p := r.prepare(context)
	if err := r.life.Switch(r.ctx, tok, b, p, "connecting with context "+context, "prd reconnected", true, func() error { return nil }); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func TestADecisionRefusesAMovedActivation(t *testing.T) {
	r := newLifeRig(t, false)
	b, _ := r.life.BoundOf("prd")
	r.pol.ConnUp("prd") // a recovery applied since the plan
	ran := false
	if err := r.life.Decide("prd", b, func() error { ran = true; return nil }); !errors.Is(err, ErrMoved) || ran {
		t.Fatalf("decided on a moved activation: %v, ran %v", err, ran)
	}
	b, _ = r.life.BoundOf("prd")
	if err := r.life.Decide("prd", b, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("a fresh bound: %v", err)
	}
	// an absent name binds nothing; a name that exists by now refuses that
	if err := r.life.Decide("new", Bound{}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.life.Decide("prd", Bound{}, func() error { return nil }); !errors.Is(err, ErrMoved) {
		t.Fatalf("an addition bound decided on an existing cluster: %v", err)
	}
	// the commit's own failure is returned
	boom := errors.New("write failed")
	b, _ = r.life.BoundOf("prd")
	if err := r.life.Decide("prd", b, func() error { return boom }); err != boom {
		t.Fatal(err)
	}
	// a cluster forgotten since its plan bound it refuses
	r.pol.Forget("prd")
	if err := r.life.Decide("prd", b, func() error { ran = false; return nil }); !errors.Is(err, ErrMoved) || !ran {
		t.Fatalf("decided on a forgotten cluster: %v", err)
	}
}

// A held attempt is released by its own first usability, exactly once,
// with the protection its hold recorded - during another Apply's
// reservation too; that Apply then refuses, and unreserving undoes nothing.
func TestAHeldAttemptIsReleasedOnceByItsFirstUsability(t *testing.T) {
	r := newLifeRig(t, false)
	rec := &releaseRec{}
	r.life.OnReleased(rec.on)
	p := r.heldFor("prd-2")
	s, _ := r.pol.Snapshot("prd")
	if s.State != policy.Held || s.Attempt != uint64(p.ID()) || !strings.Contains(s.Reason, "prd-2") {
		t.Fatalf("not held for the attempt: %+v", s)
	}
	revert, _ := r.pol.Reserve("prd") // a Revert being decided
	held, _ := r.life.BoundOf("prd")
	go r.life.Run(r.ctx) // the retired connection's queued move is dropped; the attempt's applied
	if !waitUntil(t, 5*time.Second, func() bool { s, _ := r.pol.Snapshot("prd"); return s.State == policy.Active }) {
		t.Fatal("never released")
	}
	s, _ = r.pol.Snapshot("prd")
	if !s.Protected || !s.Reserved || s.Attempt != 0 {
		t.Fatalf("released: %+v", s)
	}
	if got := rec.got(); len(got) != 1 || got[0] != p.ID() {
		t.Fatalf("releases recorded %v, want [%d]", got, p.ID())
	}
	if err := r.life.Decide("prd", held, func() error { return nil }); !errors.Is(err, ErrMoved) {
		t.Fatalf("the Revert decided over the release: %v", err)
	}
	r.pol.Unreserve(revert)
	if now, _ := r.pol.Snapshot("prd"); now.State != policy.Active || now.Epoch != s.Epoch {
		t.Fatalf("unreserving undid the release: %+v", now)
	}
}

// Codex's checkpoint (S2 §12): a usability delivered at the gate while a
// decision holds it is applied after the decision - stale once the
// decision retired its identity, and applied once when the decision's
// write failed and retired nothing.
func TestAUsabilityAtTheGateIsAppliedAfterTheDecision(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "failed write"}[failWrite], func(t *testing.T) {
			r := newLifeRig(t, false)
			rec := &releaseRec{}
			r.life.OnReleased(rec.on)
			a := r.heldFor("prd-2")
			evA := r.queued(a.ID())
			tok, _ := r.pol.Reserve("prd")
			defer r.pol.Unreserve(tok)
			bound, _ := r.life.BoundOf("prd")
			b := r.prepare("prd-1") // the Revert's own attempt
			inWrite, proceed := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- r.life.Switch(r.ctx, tok, bound, b, "connecting with context prd-1", "prd reverted", false, func() error {
					close(inWrite)
					<-proceed
					if failWrite {
						return errors.New("fleet.yaml changed")
					}
					return nil
				})
			}()
			<-inWrite
			applied := make(chan struct{})
			go func() { r.life.applyUsable(evA); close(applied) }() // waits at the gate
			select {
			case <-applied:
				t.Fatal("a usability was applied inside the decision")
			case <-time.After(50 * time.Millisecond):
			}
			close(proceed)
			err := <-done
			<-applied
			s, _ := r.pol.Snapshot("prd")
			if failWrite {
				if err == nil || s.State != policy.Active || len(rec.got()) != 1 || rec.got()[0] != a.ID() {
					t.Fatalf("after a failed write: %v, %+v, releases %v", err, s, rec.got())
				}
				return
			}
			if err != nil || s.State != policy.Held || s.Attempt != uint64(b.ID()) || len(rec.got()) != 0 {
				t.Fatalf("after the decision: %v, %+v, releases %v", err, s, rec.got())
			}
		})
	}
}

// A placement or protection change keeps its connection's identity, so a
// later recovery of that identity publishes its epoch as usual.
func TestADecisionWithoutAReconnectKeepsTheIdentity(t *testing.T) {
	r := newLifeRig(t, false)
	ev := r.queued(r.id())
	tok, _ := r.pol.Reserve("prd")
	b, _ := r.life.BoundOf("prd")
	if err := r.life.Decide("prd", b, func() error {
		_, err := r.pol.Advance(tok, false) // a protection change
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.pol.Unreserve(tok)
	before, _ := r.pol.Snapshot("prd")
	r.life.applyUsable(ev)
	after, _ := r.pol.Snapshot("prd")
	if _, conn := after.Epoch.Moved(before.Epoch); !conn {
		t.Fatal("the kept identity's recovery did not publish its epoch")
	}
	c, _ := r.reg.Conn("prd")
	if !c.Snapshot().State.Usable() {
		t.Fatal("the kept identity's move was not made visible")
	}
}

func TestUnseatRemovesOnlyAfterItsDecision(t *testing.T) {
	r := newLifeRig(t, true)
	stale, _ := r.life.BoundOf("prd")
	r.pol.ConnUp("prd")
	wrote := false
	if err := r.life.Unseat(stale, "prd", "prd removed", func() error { wrote = true; return nil }); !errors.Is(err, ErrMoved) || wrote {
		t.Fatalf("removed on a moved activation: %v", err)
	}
	if _, ok := r.reg.Conn("prd"); !ok {
		t.Fatal("a refused removal removed")
	}
	b, _ := r.life.BoundOf("prd")
	if err := r.life.Unseat(b, "prd", "prd removed", func() error { wrote = true; return nil }); err != nil || !wrote {
		t.Fatal(err)
	}
	if _, ok := r.pol.Snapshot("prd"); ok {
		t.Fatal("not forgotten")
	}
	if _, ok := r.reg.Conn("prd"); ok {
		t.Fatal("still connected")
	}
}

// AddClusters goes through the reservation: nothing is admitted on a new
// cluster until its addition is committed, and a failed connect leaves
// the name absent.
func TestAnAdditionAdmitsNothingUntilCommitted(t *testing.T) {
	pol := installRegistry(t, false)
	cs := WithPolicy(NewConfigService("", &config.Config{Clusters: []config.ClusterConfig{{Name: "a"}}}, nil), pol)
	cs.appendClusters = func(string, []string) error { return nil }
	reached, release := make(chan struct{}), make(chan struct{})
	cs.connect = func(cc config.ClusterConfig) error {
		close(reached)
		<-release
		if cc.Name == "bad" {
			return errors.New("no such context")
		}
		return nil
	}
	done := make(chan ActionResultDTO, 1)
	go func() { done <- cs.AddClusters([]string{"kind-new"}) }()
	<-reached
	if _, err := guardFor("kind-new"); err == nil {
		t.Fatal("a cluster still being added admitted a guard")
	}
	if s, ok := pol.Snapshot("kind-new"); !ok || !s.Reserved {
		t.Fatalf("seeded over its placeholder, reserved: %+v %v", s, ok)
	}
	close(release)
	if r := <-done; !r.OK {
		t.Fatal(r.Error)
	}
	if s, _ := pol.Snapshot("kind-new"); s.Reserved || s.State != policy.Active {
		t.Fatalf("after the addition: %+v", s)
	}
	reached, release = make(chan struct{}), make(chan struct{})
	close(release)
	if r := cs.AddClusters([]string{"bad"}); r.OK {
		t.Fatal("a failed connect reported OK")
	}
	if _, ok := pol.Snapshot("bad"); ok {
		t.Fatal("a failed addition left its name")
	}
	if _, ok := LiveClusterConfig(cs)("bad"); ok {
		t.Fatal("a failed addition is in the running fleet")
	}
}

// A cluster whose connection failed to build starts Held, saying why, and
// a Retry through the reconnect class connects it.
func TestABuildFailureStartsHeldUntilARetryConnects(t *testing.T) {
	ctxRun := newLifeRig(t, true) // for its context and cleanup only
	builds := 0
	var mu sync.Mutex
	reg := fleet.NewRegistry(&config.Config{Clusters: []config.ClusterConfig{{Name: "prd"}}}, func(cc config.ClusterConfig) (fleet.Conn, error) {
		mu.Lock()
		builds++
		n := builds
		mu.Unlock()
		if n == 1 {
			return nil, errors.New("exec plugin not found")
		}
		return lifeFactory(cc)
	})
	pol := policy.New()
	pol.Seed("prd", false)
	life := NewLifecycle(reg, pol, nil, nil)
	go life.Run(ctxRun.ctx)
	reg.Start(ctxRun.ctx)
	HoldBuildFailures(reg, pol)
	s, _ := pol.Snapshot("prd")
	if s.State != policy.Held || !strings.Contains(s.Reason, "exec plugin not found") {
		t.Fatalf("a build failure: %+v", s)
	}
	tok, _ := pol.Reserve("prd")
	b, _ := life.BoundOf("prd")
	p, err := reg.Prepare(config.ClusterConfig{Name: "prd"})
	if err != nil {
		t.Fatal(err)
	}
	if err := life.Switch(ctxRun.ctx, tok, b, p, "connecting", "retried", false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	pol.Unreserve(tok)
	if !waitUntil(t, 5*time.Second, func() bool { s, _ := pol.Snapshot("prd"); return s.State == policy.Active }) {
		t.Fatal("the retry never connected")
	}
}
