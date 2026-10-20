package observe

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// budgetKind is a ConfigMap kind under a byte budget: each projection costs
// 100 bytes, or 1000 when its "until" is "big".
func budgetKind(name string, src *fakeSource, budget int) KindSpec {
	k := cmKind(name, src)
	k.Budget, k.MaxObject = budget, 500
	k.Size = func(v any) int {
		if v.(cmView).Until == "big" {
			return 1000
		}
		return 100
	}
	return k
}

func retainedNames(p Part, h *harness) string {
	r, ok := h.o.RowsNow("things", "cms")
	if !ok {
		return ""
	}
	rows := append([]string(nil), r.Rows.([]string)...)
	for i := range rows {
		rows[i] = strings.Split(rows[i], ":")[0]
	}
	sort.Strings(rows)
	return strings.Join(rows, ",")
}

// Coverage S1a §3: admission is the longest key-ordered run that fits,
// whatever order objects arrive in; the kind then reads truncated with a
// lower-bound count, and an oversized object is left out on its own.
func TestBudgetAdmitsInKeyOrder(t *testing.T) {
	src := newSource()
	for _, n := range []string{"e", "c", "a", "d", "b"} {
		src.put(n, nil)
	}
	src.put("ab", map[string]string{"until": "big"}) // over MaxObject: left out alone
	h := start(t, Config{Kinds: []KindSpec{budgetKind("cms", src, 300)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.o.SubscribeRows("things", "cms", Owner{Kind: "lens", ID: "t"})
	h.eventually(h.partIs("things", "cms", live), "live")
	h.eventually(func() bool { p, _ := h.sink.part("things", "cms"); return retainedNames(p, h) == "a,b,c" }, "a, b and c admitted")
	p, _ := h.sink.part("things", "cms")
	if k := kindOf(p); !k.Truncated || k.NotRetained != 3 || k.Objects != 3 {
		t.Fatalf("coverage: %+v", k)
	}

	// a key sorting first is admitted, and the run's last key leaves
	src.put("0", nil)
	h.eventually(func() bool { p, _ := h.sink.part("things", "cms"); return retainedNames(p, h) == "0,a,b" }, "0 in, c out")
	// past a capped run's end nothing is admitted, even when it would fit
	src.put("z", nil)
	h.settle(300 * time.Millisecond)
	if p, _ := h.sink.part("things", "cms"); retainedNames(p, h) != "0,a,b" {
		t.Fatalf("z admitted past the run: %s", retainedNames(p, h))
	}

	// a deletion while capped refills by relisting, in key order
	lists, _ := src.counts()
	src.remove("a")
	h.eventually(func() bool { p, _ := h.sink.part("things", "cms"); return retainedNames(p, h) == "0,b,c" }, "refilled with c")
	if l, _ := src.counts(); l <= lists {
		t.Fatalf("no refill relist: %d -> %d", lists, l)
	}
	if p, _ := h.sink.part("things", "cms"); !live(p) {
		t.Fatalf("a refill is not a freshness event: %+v", p)
	}

	// a second deletion within RefillEvery waits for its window, then refills
	lists, _ = src.counts()
	src.remove("0")
	h.settle(300 * time.Millisecond)
	if l, _ := src.counts(); l != lists {
		t.Fatalf("a refill inside its window: %d -> %d", lists, l)
	}
	h.clk.Step(RefillEvery)
	h.eventually(func() bool { p, _ := h.sink.part("things", "cms"); return retainedNames(p, h) == "b,c,d" }, "refilled after its window")

	// within budget, an oversized object alone still truncates the kind
	for _, n := range []string{"d", "e", "z"} {
		src.remove(n)
	}
	h.clk.Step(RefillEvery)
	h.eventually(func() bool {
		p, _ := h.sink.part("things", "cms")
		k := kindOf(p)
		return retainedNames(p, h) == "b,c" && k.Truncated && k.NotRetained == 1
	}, "only the oversized object left out")
	// and its deletion, never retained, relists and clears it
	src.remove("ab")
	h.clk.Step(RefillEvery)
	h.eventually(func() bool {
		p, _ := h.sink.part("things", "cms")
		k := kindOf(p)
		return retainedNames(p, h) == "b,c" && !k.Truncated && k.NotRetained == 0
	}, "nothing left out")
}

// An object left out by the budget is never a deletion: the ledger reads
// the kind as capped, one loss, and no recovery for what it no longer holds.
func TestBudgetLeftOutIsNotDeleted(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	src.put("b", map[string]string{"bad": "1"})
	k := ledgerKind("cms", src)
	k.Budget, k.MaxObject, k.Size = 250, 500, func(any) int { return 100 }
	h := ledgerHarness(t, src, Config{Kinds: []KindSpec{k}})
	next := h.baseline("a:absent>ok b:absent>fault")
	src.put("0", nil) // evicts b
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "capped")
	if loss.Loss.Reason != LossCapped {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	h.settle(300 * time.Millisecond)
	for _, x := range h.sink.allBatches()[i+1:] {
		for _, it := range x.Items {
			if it.Name == "b" {
				t.Fatalf("an evicted object moved in the ledger: %+v", x)
			}
		}
	}
}

// sizedKind sizes each projection by its "until" value as a number of
// bytes (100 when unset).
func sizedKind(name string, src *fakeSource, budget, maxObject int) KindSpec {
	k := cmKind(name, src)
	k.Budget, k.MaxObject = budget, maxObject
	k.Size = func(v any) int {
		if n, err := strconv.Atoi(v.(cmView).Until); err == nil {
			return n
		}
		return 100
	}
	return k
}

func sz(n int) map[string]string { return map[string]string{"until": strconv.Itoa(n)} }

func budgetHarness(t *testing.T, k KindSpec) *harness {
	h := start(t, Config{Kinds: []KindSpec{k}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.o.SubscribeRows("things", "cms", Owner{Kind: "lens", ID: "t"})
	h.eventually(h.partIs("things", "cms", live), "live")
	return h
}

func coverageIs(h *harness, names string, truncated bool, notRetained int) func() bool {
	return func() bool {
		p, _ := h.sink.part("things", "cms")
		k := kindOf(p)
		return retainedNames(p, h) == names && k.Truncated == truncated && k.NotRetained == notRetained
	}
}

// Codex's S1a implementation review, finding 3: NotRetained counts omitted
// objects, never watch events. Updates of an object left out do not count
// again; a new one does, and a deleted one is no longer counted.
func TestBudgetCountsObjectsNotEvents(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	src.put("b", nil)
	h := budgetHarness(t, sizedKind("cms", src, 100, 500))
	h.eventually(coverageIs(h, "a", true, 1), "b left out")
	for i := 0; i < 3; i++ {
		src.put("b", map[string]string{"bad": strconv.Itoa(i)})
	}
	h.settle(300 * time.Millisecond)
	if !coverageIs(h, "a", true, 1)() {
		p, _ := h.sink.part("things", "cms")
		t.Fatalf("three updates of b: %+v", kindOf(p))
	}
	src.put("c", nil) // a new object past the run's end
	h.eventually(coverageIs(h, "a", true, 2), "c counted once")
	src.put("c", map[string]string{"bad": "1"})
	h.settle(200 * time.Millisecond)
	if !coverageIs(h, "a", true, 2)() {
		t.Fatal("an update of c counted again")
	}
	src.remove("c")
	h.eventually(coverageIs(h, "a", true, 1), "c gone")
}

// Codex's S1a implementation review, finding 4: a cap heals on an update
// that a list would admit. An oversized object shrinking to fit is
// admitted; the count reaches zero and a refill clears the cap.
func TestBudgetHealsOnAFittingUpdate(t *testing.T) {
	src := newSource()
	src.put("a", sz(1000))
	h := budgetHarness(t, sizedKind("cms", src, 300, 500))
	h.eventually(coverageIs(h, "", true, 1), "a left out for its size")
	src.put("a", sz(100))
	h.eventually(coverageIs(h, "a", false, 0), "a admitted, cap cleared by a refill")
}

// A retained object shrinking frees room for the blocker; the blocker
// shrinking to fit lets the run continue: both refill, as a list would
// admit them.
func TestBudgetRefillsWhenTheBlockerCanFit(t *testing.T) {
	src := newSource()
	src.put("a", sz(200))
	src.put("b", sz(200)) // the blocker: 200 + 200 > 300
	src.put("c", sz(50))
	h := budgetHarness(t, sizedKind("cms", src, 300, 500))
	h.eventually(coverageIs(h, "a", true, 2), "b blocks the run, c after it")
	// c fits the room but sorts after the blocker: no list would admit it
	lists, _ := src.counts()
	src.put("c", sz(40))
	h.settle(300 * time.Millisecond)
	if l, _ := src.counts(); l != lists || !coverageIs(h, "a", true, 2)() {
		t.Fatalf("a futile refill or admission past the blocker: lists %d -> %d", lists, l)
	}
	// a shrinks: the blocker fits now, so the kind refills
	src.put("a", sz(100))
	h.eventually(coverageIs(h, "a,b", true, 1), "a and b retained after the refill, c still out")
	// the new blocker c shrinks to fit: the run continues
	h.clk.Step(RefillEvery)
	src.put("c", sz(0))
	h.eventually(coverageIs(h, "a,b,c", false, 0), "c admitted by a refill")
}

// Finding 2: the retained model's allocation follows what is admitted,
// never the whole list.
func TestBudgetAdmitAllocatesByAdmitted(t *testing.T) {
	all := make(map[string]any, 100000)
	for i := 0; i < 100000; i++ {
		all[fmt.Sprintf("ns/o%06d", i)] = cmView{Name: strconv.Itoa(i)}
	}
	spec := KindSpec{Budget: 1, MaxObject: 1, Size: func(any) int { return 1 }}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out, adm := admit(all, spec) // all is dead from here, before the GC below
	runtime.GC()
	runtime.ReadMemStats(&after)
	if len(out) != 1 || adm.notRetained != 99999 {
		t.Fatalf("admission: %d retained, %d left out", len(out), adm.notRetained)
	}
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 512<<10 {
		t.Fatalf("one admitted value holds %d heap bytes", grew)
	}
	runtime.KeepAlive(out)
}

// Coverage S7 §8: the namespaces kind keeps labels within a budget, and the
// Summary's namespace count stays exact past it - the objects the budget
// left out are counted.
func TestNamespaceCountIsExactPastTheBudget(t *testing.T) {
	src := newSource()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		src.put(n, nil)
	}
	h := start(t, Config{Kinds: []KindSpec{budgetKind("cms", src, 300)}, NamespacesKind: "cms",
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(func() bool {
		s, ok := h.sink.last()
		p, _ := h.sink.part("things", "cms")
		return ok && s.Namespaces != nil && *s.Namespaces == 5 && kindOf(p).Truncated && kindOf(p).NotRetained == 2
	}, "five namespaces counted with two left out")
	src.remove("e")
	h.eventually(func() bool { s, ok := h.sink.last(); return ok && s.Namespaces != nil && *s.Namespaces == 4 }, "a left-out namespace's deletion is counted")
}

// Coverage S11b (Codex's implementation review, finding 1): the capture
// names a capped kind whatever its freshness, so an evaluator never reads
// a cut read as whole.
func TestCaptureNamesACappedKind(t *testing.T) {
	src := newSource()
	for _, n := range []string{"a", "b", "c", "d"} {
		src.put(n, nil)
	}
	var mu sync.Mutex
	var capped, complete []bool
	part := cmPart("cms", "cms")
	inner := part.Evaluate
	part.Evaluate = func(c Capture) PartResult {
		mu.Lock()
		capped, complete = append(capped, c.Capped["cms"]), append(complete, c.Complete["cms"])
		mu.Unlock()
		return inner(c)
	}
	h := start(t, Config{Kinds: []KindSpec{budgetKind("cms", src, 300)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{part}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	h.eventually(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(capped) > 0 && capped[len(capped)-1] && !complete[len(complete)-1]
	}, "a capped live read")
	src.remove("d")
	h.clk.Step(RefillEvery)
	h.eventually(func() bool { mu.Lock(); defer mu.Unlock(); return !capped[len(capped)-1] && complete[len(complete)-1] }, "whole once it fits")
}
