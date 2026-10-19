package observe

import (
	"sync/atomic"
	"testing"
	"time"
)

// r0 ranks model blocking backups; r1 ranks model terminal backups.
func TestReviewS2bTier0WindowCanRetainTerminal(t *testing.T) {
	src := newSource()
	for n, r := range map[string]string{"a": "r01", "b": "r02", "c": "r03", "d": "r04", "e": "r10"} {
		src.put(n, rank(r))
	}
	rec := &windowRec{}
	var terminalSeen atomic.Bool
	part := windowPart("cms", rec)
	inner := part.Evaluate
	part.Evaluate = func(c Capture) PartResult {
		for _, o := range c.Objects["cms"] {
			v := o.(cmView)
			if v.Name == "c" && v.Until == "r11" && c.Windows["cms"] == "r04" {
				terminalSeen.Store(true)
			}
		}
		return inner(c)
	}
	h := start(t, Config{Kinds: []KindSpec{rankedKind("cms", src, 200)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{part}}}})
	is := func(names, window string) func() bool {
		return func() bool { n, w, ok := rec.state(); return n == names && w == window && ok }
	}
	h.eventually(is("a,b", "r03"), "initial blocking prefix")
	// Trigger a refill, starting its one-minute rate limit.
	src.remove("b")
	h.eventually(is("a,c", "r04"), "refill admits c; d blocks")
	lists, _ := src.counts()
	// Removing d clears blockKey, but its blocking rank stays the floor.
	src.remove("d")
	h.settle(100 * time.Millisecond)
	// Retained c finishes while the refill is delayed.
	src.put("c", rank("r11"))
	h.eventually(func() bool { return terminalSeen.Load() && is("a,c", "r04")() }, "terminal c retained with blocking floor")
	if n, _ := src.counts(); n != lists {
		t.Fatalf("unexpected refill %d -> %d", lists, n)
	}
	t.Log("retained terminal c with tier-0 floor r04; no complete refill yet")
}
