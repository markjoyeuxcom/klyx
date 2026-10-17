package observe

import (
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// rankedKind is budgetKind ranked by its "until" value when that starts
// with "r", else by name (coverage S2a §3): the test sets ranks that run
// against the names, as a Backup's newest-first rank runs against its key.
func rankedKind(name string, src *fakeSource, budget int) KindSpec {
	k := budgetKind(name, src, budget)
	k.Rank = func(key string, v any) string {
		if u := v.(cmView).Until; strings.HasPrefix(u, "r") {
			return u
		}
		return v.(cmView).Name
	}
	return k
}

// windowRec keeps the last capture's retained names and window.
type windowRec struct {
	mu      sync.Mutex
	names   string
	window  string
	hasWin  bool
	capture int
}

func (w *windowRec) state() (string, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.names, w.window, w.hasWin
}

func windowPart(kind string, rec *windowRec) PartSpec {
	inner := evalCMs(kind)
	return PartSpec{ID: kind, Entity: kind, Scope: "all namespaces", Classifier: "verdict", Counted: []string{kind},
		Evaluate: func(c Capture) PartResult {
			var names []string
			for _, o := range c.Objects[kind] {
				names = append(names, o.(cmView).Name)
			}
			sort.Strings(names)
			w, ok := c.Windows[kind]
			rec.mu.Lock()
			rec.names, rec.window, rec.hasWin = strings.Join(names, ","), w, ok
			rec.capture++
			rec.mu.Unlock()
			return inner(c)
		}}
}

func rank(r string) map[string]string { return map[string]string{"until": r} }

// The budget admits, evicts and blocks in rank order, the capture carries
// the window, and a rank that changes is re-placed as a list would place
// it (coverage S2a §3, revisions 1 and 2).
func TestBudgetRankedAdmissionAndWindow(t *testing.T) {
	src := newSource()
	// ranks run against the names: e first, a last
	for n, r := range map[string]string{"a": "r5", "b": "r4", "c": "r3", "d": "r2", "e": "r1"} {
		src.put(n, rank(r))
	}
	rec := &windowRec{}
	h := start(t, Config{Kinds: []KindSpec{rankedKind("cms", src, 300)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{windowPart("cms", rec)}}}})
	is := func(names, window string, has bool) func() bool {
		return func() bool { n, w, ok := rec.state(); return n == names && w == window && ok == has }
	}
	h.eventually(is("c,d,e", "r4", true), "the three first in rank order, b blocking")

	// a new object ranking first is admitted, and the run's last leaves
	src.put("f", rank("r0"))
	h.eventually(is("d,e,f", "r3", true), "f in, c out and blocking")

	// a retained object moving past the run's end leaves, and its room
	// refills by a list in rank order
	lists, _ := src.counts()
	src.put("d", rank("r9"))
	h.eventually(is("c,e,f", "r4", true), "d out, c refilled")
	if l, _ := src.counts(); l <= lists {
		t.Fatalf("no refill relist: %d -> %d", lists, l)
	}

	// a left-out object moving into the run is admitted at once, evicting
	// the run's last (no list needed)
	lists, _ = src.counts()
	src.put("a", rank("r-"))
	h.eventually(is("a,e,f", "r3", true), "a in, c out")
	if l, _ := src.counts(); l != lists {
		t.Fatalf("an admission by rank relisted: %d -> %d", lists, l)
	}

	// an oversized object ranking first is left out on its own, and the
	// window covers it
	src.put("0", map[string]string{"until": "big"}) // ranks by name "0", before every "r"
	h.eventually(is("a,e,f", "0", true), "the window starts at the oversized object")

	// shrunk, it is admitted and the run's last leaves
	src.put("0", nil)
	h.eventually(is("0,a,f", "r1", true), "0 admitted, e (last in rank order) out and blocking")

	// the blocker moving in the order relists
	h.clk.Step(RefillEvery)
	lists, _ = src.counts()
	src.put("e", rank("r8"))
	h.eventually(func() bool { l, _ := src.counts(); return l > lists }, "a moved blocker relists")
	h.eventually(is("0,a,f", "r3", true), "c now blocks")

	// nothing left out: no window
	for _, n := range []string{"b", "c", "d", "e"} {
		src.remove(n)
	}
	h.clk.Step(RefillEvery)
	h.eventually(is("0,a,f", "", false), "complete: no window")
}

// An unranked kind carries no window, capped or not: key order is not a
// question the evaluators ask.
func TestBudgetUnrankedHasNoWindow(t *testing.T) {
	src := newSource()
	for _, n := range []string{"a", "b", "c", "d"} {
		src.put(n, nil)
	}
	rec := &windowRec{}
	h := start(t, Config{Kinds: []KindSpec{budgetKind("cms", src, 200)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{windowPart("cms", rec)}}}})
	h.eventually(func() bool { n, _, ok := rec.state(); return n == "a,b" && !ok }, "capped, no window")
	h.settle(100 * time.Millisecond)
}
