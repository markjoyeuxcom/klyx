package fleet

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Codex's review of S2a, F5: a pending move into usable is not handed to
// the Observer (nor the index, in the same handoff) until the lifecycle
// has published it; once acknowledged and published, it is.
func TestPendingUsabilityReachesHealthOnlyOncePublished(t *testing.T) {
	c := fakeClusterConn("a")
	c.SetHealthSink(&summarySink{})
	evs := make(chan UsabilityEvent, 10)
	c.SetOnUsable(func(ev UsabilityEvent) { evs <- ev })
	c.Start(context.Background())
	defer c.Stop(5 * time.Second)
	var ev UsabilityEvent
	select {
	case ev = <-evs:
	case <-time.After(5 * time.Second):
		t.Fatal("no usability")
	}
	connected := func() bool {
		h := c.Health()
		if h == nil {
			return false
		}
		sum, ok := h.Latest()
		return ok && strings.HasPrefix(sum.Connection, "connected")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if connected() {
			t.Fatalf("a pending incarnation reached health while Snapshot is %s", c.Snapshot().State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !c.AckUsable(ev.Inc) {
		t.Fatal("the move was not acknowledged")
	}
	c.Published()
	waitFor(t, 5*time.Second, connected)
	if !c.Snapshot().State.Usable() {
		t.Fatal("published but not usable")
	}
}
