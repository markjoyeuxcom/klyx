package appbridge

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The F2a re-review 2 (Codex's probes, adopted): a confirmed request's
// work entry belongs to its own admission.

// retryConn starts a second client's review and run from the first run's
// context cancellation, after its reservation was released and before its
// deferred cleanup; the second run's write blocks until released.
type retryConn struct {
	*stepsConn
	armed   atomic.Bool
	retry   func()
	started chan struct{}
	release chan struct{}
}

func (c *retryConn) ChatGet(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	u, err := c.stepsConn.ChatGet(ctx, gvr, ns, name)
	if c.armed.CompareAndSwap(true, false) {
		context.AfterFunc(ctx, c.retry)
		for range 5000 { // widen the window between the release and the cleanup
			context.AfterFunc(ctx, func() {})
		}
	}
	return u, err
}

func (c *retryConn) ScaleWorkload(ctx context.Context, kind, ns, name, uid string, n int32) error {
	close(c.started)
	select {
	case <-c.release:
		return c.note("scale retry %d", n)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// R2-1: an older, refused run's cleanup never removes a newer run's
// request while its write is in flight.
func TestAnOlderRunsCleanupKeepsANewerRequest(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		s.settleEffect(c, p, n, stepEffectUnverified, "test complete")
	}
	t.Cleanup(func() { verifyStepHook = prev })
	for attempt := range 10 {
		sf := newStepsFixture(t, scaleWeb)
		c := &retryConn{stepsConn: sf.sc, started: make(chan struct{}), release: make(chan struct{})}
		sf.mu.Lock()
		sf.cur = c
		sf.mu.Unlock()
		sf.svc.mu.Lock()
		sf.svc.convs[invKey("x")].conn = c
		sf.svc.mu.Unlock()
		rv := sf.review(1)
		if !rv.Offered {
			t.Fatal(rv.Reason)
		}
		_ = unstructured.SetNestedField(sf.web().Object, int64(2), "spec", "replicas")
		done := make(chan string, 1)
		c.retry = func() {
			r := sf.review(1)
			if !r.Offered {
				done <- "review failed: " + r.Reason
				return
			}
			if rr := sf.run(1, r.Token); !rr.Ran {
				done <- "the second run was refused: " + rr.Reason
				return
			}
			done <- ""
		}
		c.armed.Store(true)
		if first := sf.run(1, rv.Token); first.Ran {
			close(c.release)
			t.Fatal("control: the first run refuses before its write")
		}
		select {
		case <-c.started:
		case err := <-done:
			close(c.release)
			t.Fatal(err)
		case <-time.After(3 * time.Second):
			close(c.release)
			t.Fatal("the second run did not start")
		}
		w := sf.svc.aiWork()
		close(c.release)
		select {
		case err := <-done:
			if err != "" {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the second run did not return")
		}
		if len(w.continues) == 0 {
			t.Fatalf("attempt %d: the older run's cleanup erased the newer request", attempt)
		}
		if w := sf.svc.aiWork(); len(w.continues) != 0 {
			t.Fatalf("attempt %d: a returned request is still named: %+v", attempt, w.continues)
		}
	}
}

// Codex's control: Clear keeps an in-flight request until it returns.
func TestClearKeepsAnInFlightRequestUntilItReturns(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		s.settleEffect(c, p, n, stepEffectUnverified, "test complete")
	}
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb)
	c := &blockingPause{stepsConn: sf.sc, reached: make(chan struct{}), release: make(chan struct{})}
	sf.mu.Lock()
	sf.cur = c
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	sf.svc.convs[invKey("x")].conn = c
	sf.svc.mu.Unlock()
	rv := sf.review(1)
	if !rv.Offered {
		t.Fatal(rv.Reason)
	}
	done := make(chan StepRunDTO, 1)
	go func() { done <- sf.run(1, rv.Token) }()
	select {
	case <-c.reached:
	case <-time.After(3 * time.Second):
		t.Fatal("no request")
	}
	sf.svc.Clear("x", sf.conv)
	w := sf.svc.aiWork()
	close(c.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("no return")
	}
	if len(w.continues) != 1 {
		t.Fatalf("Clear lost a live request: %+v", w)
	}
	if w := sf.svc.aiWork(); len(w.continues) != 0 {
		t.Fatalf("the request is named after it returned: %+v", w)
	}
}
