package observe

import (
	"strings"
	"testing"
	"time"
)

func TestReviewS2aR2PendingFloorClearsOnlyOnList(t *testing.T) {
	src := newSource()
	for n, r := range map[string]string{"a": "r1", "b": "r2", "c": "r3", "d": "r4", "e": "r5"} {
		src.put(n, rank(r))
	}
	k := rankedKind("cms", src, 200)
	k.Size = func(v any) int {
		if strings.HasSuffix(v.(cmView).Until, "big") {
			return 1000
		}
		return 100
	}
	k.Rank = func(_ string, v any) string { return strings.TrimSuffix(v.(cmView).Until, "big") }
	rec := &windowRec{}
	h := start(t, Config{Kinds: []KindSpec{k}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{windowPart("cms", rec)}}}})
	is := func(names, want string) func() bool {
		return func() bool { n, w, ok := rec.state(); return n == names && w == want && ok }
	}
	h.eventually(is("a,b", "r3"), "initial list")
	src.remove("b")
	h.eventually(is("a,c", "r4"), "first refill")
	src.put("d", rank("r9big"))
	h.settle(100 * time.Millisecond)
	src.put("d", rank("r8big"))
	h.settle(100 * time.Millisecond)
	src.put("d", rank("r9"))
	h.settle(100 * time.Millisecond)
	if !is("a,c", "r4")() {
		n, w, ok := rec.state()
		t.Fatalf("pending floor advanced: %s %s %v", n, w, ok)
	}
	h.clk.Step(RefillEvery)
	h.eventually(is("a,c", "r5"), "complete list releases old r4 floor and establishes e/r5")
}
