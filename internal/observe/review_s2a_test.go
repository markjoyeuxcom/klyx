package observe

import (
	"strings"
	"testing"
	"time"
)

func TestReviewS2aOversizedMovedBlocker(t *testing.T) {
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
	is := func(names, window string) func() bool {
		return func() bool { n, w, ok := rec.state(); return n == names && w == window && ok }
	}
	h.eventually(is("a,b", "r3"), "initial list")
	src.remove("b")
	h.eventually(is("a,c", "r4"), "refill sets rate limit")
	lists, _ := src.counts()
	src.put("d", rank("r9big"))
	h.settle(300 * time.Millisecond)
	if l, _ := src.counts(); l != lists {
		t.Fatalf("relisted too soon: %d -> %d", lists, l)
	}
	if n, w, ok := rec.state(); n != "a,c" || !ok || w != "r4" {
		t.Fatalf("window must cover omitted e at r5 until full list: retained=%s window=%q known=%v", n, w, ok)
	}
}
