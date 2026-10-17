package observe

import (
	"strings"
	"testing"
	"time"
)

func TestReviewS2aR1BoundarySurvivesAnotherUpdate(t *testing.T) {
	for _, next := range []string{"r8big", "r9", "deleted-then-added"} {
		t.Run(next, func(t *testing.T) {
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
			h.eventually(is("a,c", "r4"), "first refill")
			lists, _ := src.counts()
			if next == "deleted-then-added" {
				src.remove("d")
			} else {
				src.put("d", rank("r9big"))
			}
			h.settle(100 * time.Millisecond)
			if next != "deleted-then-added" {
				if n, w, ok := rec.state(); n != "a,c" || w != "r4" || !ok {
					t.Fatalf("first update not covered: %s %s %v", n, w, ok)
				}
			}
			if next == "deleted-then-added" {
				src.put("f", rank("r9"))
			} else {
				src.put("d", rank(next))
			}
			h.settle(100 * time.Millisecond)
			if n, _ := src.counts(); n != lists {
				t.Fatalf("a complete refill occurred: %d -> %d", lists, n)
			}
			if n, w, ok := rec.state(); n != "a,c" || (ok && w > "r4") {
				t.Fatalf("pending boundary lost after second update; e/r5 remains omitted: retained=%s window=%q known=%v", n, w, ok)
			}
		})
	}
}
