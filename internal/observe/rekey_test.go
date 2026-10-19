package observe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// keyedSource scripts a keyed snapshot: each key's next outcome, and an
// optional hold a read of that key waits on.
type keyedSource struct {
	mu       sync.Mutex
	outcome  map[string]SnapshotRead
	hold     map[string]chan struct{}
	entered  chan string
	keysSeen []string
}

func newKeyed() *keyedSource {
	return &keyedSource{outcome: map[string]SnapshotRead{}, hold: map[string]chan struct{}{}, entered: make(chan string, 16)}
}

func (k *keyedSource) set(key string, r SnapshotRead) { k.mu.Lock(); k.outcome[key] = r; k.mu.Unlock() }
func (k *keyedSource) holdKey(key string) chan struct{} {
	ch := make(chan struct{})
	k.mu.Lock()
	k.hold[key] = ch
	k.mu.Unlock()
	return ch
}

func (k *keyedSource) read(ctx context.Context, key string) ([]any, bool, error) {
	k.mu.Lock()
	k.keysSeen = append(k.keysSeen, key)
	hold := k.hold[key]
	k.mu.Unlock()
	k.entered <- key
	if hold != nil {
		<-hold
	}
	k.mu.Lock()
	r := k.outcome[key]
	k.mu.Unlock()
	return r.Items, r.Truncated, r.Err
}

func keyedHarness(t *testing.T, src *keyedSource) *harness {
	snap := SnapshotSpec{Name: "alerts", Scope: "this cluster", Cadence: time.Hour, Timeout: 30 * time.Second, ReadKeyed: src.read}
	eval := func(c Capture) PartResult { n := len(c.Objects["alerts"]); return PartResult{Evidence: &n} }
	return start(t, Config{Snapshots: []SnapshotSpec{snap}, Domains: []DomainSpec{{ID: "alerts", Parts: []PartSpec{
		{ID: "alerts", Classifier: "alerts", Counted: []string{"alerts"}, Evaluate: eval}}}}})
}

func evidence(p Part) int {
	if p.Evidence == nil {
		return -1
	}
	return *p.Evidence
}

// Coverage S8a §4: a read that finds the source not installed is absent -
// no evidence, no gap - and a later failure is unreadable, never stale.
func TestSourceAbsentIsAbsentNeverAGap(t *testing.T) {
	src := newKeyed()
	src.set("", SnapshotRead{Err: fmt.Errorf("%w: no Alertmanager", ErrSourceAbsent)})
	h := keyedHarness(t, src)
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Absent && p.Evidence == nil && kindOf(p).Error == "" }), "absent")
	src.set("", SnapshotRead{Err: errors.New("unreachable")})
	h.o.Refresh("alerts")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Unreadable && p.AsOf == nil }), "a failure after absent is unreadable")
	src.set("", SnapshotRead{Items: []any{"a1"}})
	h.o.Refresh("alerts")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Live && evidence(p) == 1 }), "then live")
	src.set("", SnapshotRead{Err: fmt.Errorf("%w", ErrSourceAbsent)})
	h.o.Refresh("alerts")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Absent && p.Evidence == nil }), "absent again drops the items")
}

// Coverage S8a §4 (Codex's sequence): after a prior *empty* success of A, a
// re-key to B is checking at once, B failing is unreadable with nothing of
// A, and B succeeding empty is live and empty. Read receives the key.
func TestRekeyStartsAnUnreadCollection(t *testing.T) {
	src := newKeyed()
	src.set("A", SnapshotRead{}) // an empty success
	h := keyedHarness(t, src)
	<-src.entered // the initial generation's read ("")
	h.o.RekeySnapshot("alerts", "A")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Live && evidence(p) == 0 && p.AsOf != nil }), "A read empty")
	holdB := src.holdKey("B")
	src.set("B", SnapshotRead{Err: errors.New("B unreachable")})
	h.o.RekeySnapshot("alerts", "B")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Checking && p.AsOf == nil }), "checking before B replies")
	close(holdB)
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Unreadable && p.AsOf == nil && kindOf(p).LastGoodAt == nil }), "B failing has no A freshness")
	src.mu.Lock()
	delete(src.hold, "B")
	src.mu.Unlock()
	src.set("B", SnapshotRead{})
	h.o.Refresh("alerts")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Live && evidence(p) == 0 && p.AsOf != nil }), "B empty is live")
	src.mu.Lock()
	defer src.mu.Unlock()
	seen := fmt.Sprint(src.keysSeen)
	if seen != "[ A B B]" {
		t.Fatalf("keys read: %s", seen)
	}
}

// The ended generation's read completing after the re-key is discarded:
// its items, its error and its freshness never reach the part.
func TestRekeyDiscardsTheEndedGenerationsRead(t *testing.T) {
	src := newKeyed()
	holdA := src.holdKey("A")
	src.set("A", SnapshotRead{Items: []any{"a1", "a2"}})
	src.set("B", SnapshotRead{Items: []any{"b1"}})
	holdB := src.holdKey("B")
	h := keyedHarness(t, src)
	<-src.entered
	h.o.RekeySnapshot("alerts", "A")
	for k := <-src.entered; k != "A"; k = <-src.entered {
	}
	h.o.RekeySnapshot("alerts", "B")
	close(holdA) // A's read completes after its generation ended
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Checking }), "B checking")
	time.Sleep(50 * time.Millisecond)
	if p, _ := h.sink.part("alerts", "alerts"); p.Coverage != Checking || evidence(p) > 0 {
		t.Fatalf("A's late read reached the part: %+v", p)
	}
	close(holdB)
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Live && evidence(p) == 1 }), "B's read")
}

// Re-keying to the key a generation already carries changes nothing.
func TestRekeyToTheSameKeyIsANoOp(t *testing.T) {
	src := newKeyed()
	src.set("A", SnapshotRead{Items: []any{"a1"}})
	h := keyedHarness(t, src)
	<-src.entered
	h.o.RekeySnapshot("alerts", "A")
	h.eventually(h.partIs("alerts", "alerts", func(p Part) bool { return p.Coverage == Live && evidence(p) == 1 }), "A live")
	src.mu.Lock()
	n := len(src.keysSeen)
	src.mu.Unlock()
	h.o.RekeySnapshot("alerts", "A")
	time.Sleep(50 * time.Millisecond)
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.keysSeen) != n {
		t.Fatalf("a same-key re-key read again: %v", src.keysSeen)
	}
	if p, _ := h.sink.part("alerts", "alerts"); p.Coverage != Live {
		t.Fatalf("a same-key re-key reset the part: %+v", p)
	}
}
