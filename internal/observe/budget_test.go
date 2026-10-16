package observe

import (
	"sort"
	"strings"
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
