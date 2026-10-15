package observe

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// The Line slice 9 note §3: the role view reads the committed ledger on
// the loop - current, last observed (frozen, with its time), unverified -
// never a healthy role it cannot stand behind.

func roles(t *testing.T, h *harness, qs ...RoleQuery) []RoleView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vs, ok := h.o.RolesNow(ctx, qs)
	if !ok {
		t.Fatal("RolesNow refused")
	}
	return vs
}

func view(v RoleView) string {
	if v.State == RoleUnverified {
		return v.State + "(" + v.Reason + ")"
	}
	return v.State + ":" + v.Role
}

func TestRolesNowCurrentAndUnverified(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	src.put("b", map[string]string{"bad": "1"})
	src.put("c", map[string]string{"skip": "1"})
	h := ledgerHarness(t, src, Config{})
	h.baseline("a:absent>ok b:absent>fault c:absent>unevaluated")
	got := roles(t, h, RoleQuery{"a", "cms"}, RoleQuery{"b", "cms"}, RoleQuery{"c", "cms"}, RoleQuery{"zz", "cms"}, RoleQuery{"a", "nope"})
	want := []string{"current:ok", "current:fault", "unverified(not evaluated)", "unverified(not observed)", "unverified(no ledger for this kind)"}
	for i, w := range want {
		if view(got[i]) != w {
			t.Fatalf("%d: %s, want %s", i, view(got[i]), w)
		}
	}
	if got[0].At == nil {
		t.Fatal("a current role names its commit")
	}
}

// Healthy before a gap is never healthy during it: the frozen partition
// reads "last observed" with its time; recovery makes it current again.
func TestRolesNowLastObservedAcrossAGap(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	h.o.SetConnection("unreachable")
	_, i := h.waitBatch(next, kindIs(BatchLoss), "down")
	v := roles(t, h, RoleQuery{"a", "cms"})[0]
	if v.State != RoleLastObserved || v.Role != "ok" || v.At == nil {
		t.Fatalf("during the gap: %+v", v)
	}
	h.o.SetConnection("connected")
	h.waitBatch(i+1, kindIs(BatchAfterGap), "up")
	if v := roles(t, h, RoleQuery{"a", "cms"})[0]; v.State != RoleCurrent {
		t.Fatalf("after recovery: %+v", v)
	}
	// relisting: a watch error freezes until the list
	src.mu.Lock()
	src.listErr = apierrors.NewInternalError(fmt.Errorf("etcd down"))
	src.mu.Unlock()
	src.liveWatcher().Error(&apierrors.NewInternalError(fmt.Errorf("stream broke")).ErrStatus)
	h.waitBatch(0, func(b TransitionBatch) bool { return b.Kind == BatchLoss && b.Loss.Reason == LossCoverage }, "stale")
	if v := roles(t, h, RoleQuery{"a", "cms"})[0]; v.State != RoleLastObserved {
		t.Fatalf("while relisting: %+v", v)
	}
}

func TestRolesNowCappedIsUnverified(t *testing.T) {
	src := newSource()
	for _, n := range []string{"a", "b", "c", "d"} {
		src.put(n, nil)
	}
	h := ledgerHarness(t, src, Config{RoleCap: 3})
	h.waitBatch(0, func(b TransitionBatch) bool { return b.Kind == BatchLoss && b.Loss.Reason == LossCapped }, "capped")
	if v := roles(t, h, RoleQuery{"a", "cms"})[0]; view(v) != "unverified(the role list is capped)" {
		t.Fatalf("capped: %+v", v)
	}
}

// Final: RolesNow refuses once the Observer ended; a cancelled caller is
// answered at once.
func TestRolesNowEndAndCancel(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	h.baseline("a:absent>ok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := h.o.RolesNow(ctx, []RoleQuery{{"a", "cms"}}); ok {
		t.Fatal("a cancelled caller")
	}
	h.cancel()
	<-h.o.Done()
	if _, ok := h.o.RolesNow(context.Background(), []RoleQuery{{"a", "cms"}}); ok {
		t.Fatal("an ended Observer")
	}
}

// Under -race: role reads interleave with commits without a race; every
// answer is one the ledger committed.
func TestRolesNowConcurrentWithCommits(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	h.baseline("a:absent>ok")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				src.put("a", map[string]string{"bad": "1"})
			} else {
				src.put("a", nil)
			}
			h.clk.Step(2 * time.Second)
			time.Sleep(time.Millisecond)
		}
	}()
	for i := 0; i < 200; i++ {
		v := roles(t, h, RoleQuery{"a", "cms"})[0]
		if v.State != RoleCurrent || (v.Role != "ok" && v.Role != "fault") {
			close(stop)
			t.Fatalf("read %d: %+v", i, v)
		}
	}
	close(stop)
	wg.Wait()
}

// Review of 9b: a change the Observer recorded but has not yet committed -
// here a lost connection, before the throttled evaluation - is reconciled
// before any role is read: never current across a recorded loss.
func TestRolesNowReconcilesARecordedLoss(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	h.o.SetConnection("unreachable")
	v := roles(t, h, RoleQuery{"a", "cms"})[0] // the clock has not advanced: no publish ran
	if v.State != RoleLastObserved || v.Role != "ok" {
		t.Fatalf("a recorded disconnect: %+v", v)
	}
	// the loss was committed and emitted, once, by the role read
	n := 0
	for _, b := range h.sink.allBatches()[next:] {
		if b.Kind == BatchLoss {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d loss batches", n)
	}
	h.settle(300 * time.Millisecond)
	for _, b := range h.sink.allBatches()[next+1:] {
		if b.Kind == BatchLoss {
			t.Fatalf("the publish repeated the loss: %+v", b)
		}
	}
}
