package loki

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func ent(ts int64, line string, meta map[string]string) Entry {
	return Entry{Stream: map[string]string{"namespace": "team"}, Meta: meta, TS: ts, Line: line}
}

// a fake Loki branch: entries newest first, answering [start, end) limit
func (f fakeBranch) read(branch int, start, end int64, limit int) Read {
	var out []Entry
	for _, e := range f {
		if e.TS >= start && e.TS < end {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return Read{Branch: branch, Limit: limit, Entries: out}
}

type fakeBranch []Entry // newest first

// page drives one page through ReadPage against the fake branches.
func page(ps *PageSet, fs []fakeBranch) []Kept {
	kept, _, err := ReadPage(context.Background(), func(_ context.Context, i int, start, end int64, limit int) ([]Entry, error) {
		return fs[i].read(i, start, end, limit).Entries, nil
	}, ps)
	if err != nil {
		panic(err)
	}
	return kept
}

func burst(ts int64, n int, prefix string) fakeBranch {
	var out fakeBranch
	for i := 0; i < n; i++ {
		out = append(out, ent(ts, fmt.Sprintf("%s %d", prefix, i), nil))
	}
	return out
}

func older(from int64, n int, prefix string) fakeBranch {
	var out fakeBranch
	for i := 0; i < n; i++ {
		out = append(out, ent(from-int64(i), fmt.Sprintf("%s %d", prefix, i), nil))
	}
	return out
}

// S8b §5: branches merge newest first, each moves its own cursor, and the
// range is done only when every branch is exhausted.
func TestPageMergesBranchesAndPagesToTheStart(t *testing.T) {
	a := older(10_000, 1500, "a") // 10000 .. 8501
	b := older(9_000, 700, "b")   // 9000 .. 8301
	ps := NewPageSet(1, 20_000, 2)
	seen := map[string]bool{}
	var pages []int
	for i := 0; i < 10 && !ps.Done(); i++ {
		kept := page(ps, []fakeBranch{a, b})
		last := int64(1 << 62)
		for _, k := range kept {
			if k.TS > last {
				t.Fatalf("page %d not newest first", i)
			}
			last = k.TS
			if seen[k.Line] {
				t.Fatalf("delivered twice: %s", k.Line)
			}
			seen[k.Line] = true
		}
		pages = append(pages, len(kept))
	}
	if !ps.Done() || len(seen) != 2200 {
		t.Fatalf("pages %v: done %v, delivered %d of 2200", pages, ps.Done(), len(seen))
	}
}

// Codex's re-review of c2b4e26: the overlap is removed before the cut -
// 1,000 held at a boundary plus 200 older recovered add those 200 - and
// exhaustion counts what was delivered earlier.
func TestOverlapRemovedBeforeTheCut(t *testing.T) {
	f := append(burst(5000, 1000, "t"), older(4999, 200, "o")...)
	ps := NewPageSet(1, 10_000, 1)
	first := page(ps, []fakeBranch{f})
	if len(first) != 1000 || ps.Branches[0].Boundary != 5000 || ps.Done() {
		t.Fatalf("first: %d boundary %d", len(first), ps.Branches[0].Boundary)
	}
	// the next read is saturated at 1,000 (all at 5000); the re-read at
	// 5,000 returns the 1,000 held plus the 200 older: those 200 are kept
	second := page(ps, []fakeBranch{f})
	if len(second) != 200 || second[0].Line != "o 0" || !ps.Done() {
		t.Fatalf("second: %d kept, done %v", len(second), ps.Done())
	}
}

// Lab finding 11: 1,200 entries at one nanosecond across two streams, with
// older entries behind: the re-read recovers them all; more than the
// re-read can hold stops the branch with "at least N", never a loop, a
// silent skip or a false start-of-range; Skip past T is explicit.
func TestSaturation(t *testing.T) {
	two := append(burst(5000, 600, "stdout"), burst(5000, 600, "stderr")...)
	f := append(two, older(4999, 50, "older")...)
	ps := NewPageSet(1, 10_000, 1)
	total := 0
	for i := 0; i < 6 && !ps.Done(); i++ {
		total += len(page(ps, []fakeBranch{f}))
	}
	if total != 1250 || !ps.Done() {
		t.Fatalf("recovered %d of 1250, done %v", total, ps.Done())
	}
	// 6,000 at one timestamp: past the re-read
	g := append(burst(5000, 6000, "x"), older(4999, 10, "older")...)
	ps = NewPageSet(1, 10_000, 1)
	got := len(page(ps, []fakeBranch{g}))
	for i := 0; i < 8 && ps.Branches[0].Active(); i++ {
		got += len(page(ps, []fakeBranch{g}))
	}
	b := ps.Branches[0]
	if !b.Stopped || ps.Done() || b.StoppedCount < 5000 {
		t.Fatalf("not stopped explicitly: %+v (delivered %d)", b, got)
	}
	// an empty page while stopped: nothing read, nothing claimed done
	if k := page(ps, []fakeBranch{g}); len(k) != 0 || ps.Done() {
		t.Fatal("a stopped branch read again or claimed the start")
	}
	ps.Skip(0)
	rest := page(ps, []fakeBranch{g})
	if len(rest) != 10 || rest[0].Line != "older 0" || !ps.Done() {
		t.Fatalf("skip past T: %d", len(rest))
	}
}

// A read shorter than its limit whose entries were cut by another
// branch's newer ones is not exhausted.
func TestExhaustionReadsTheRawResponse(t *testing.T) {
	a := older(10_000, 999, "a")
	b := older(5_000, 10, "b") // short, but all older than a's
	ps := NewPageSet(1, 20_000, 2)
	page(ps, []fakeBranch{a, b})
	if ps.Branches[0].Exhausted != true || ps.Branches[1].Exhausted {
		t.Fatalf("after the first page: %+v", ps.Branches)
	}
	page(ps, []fakeBranch{a, b})
	if !ps.Done() {
		t.Fatalf("b's ten not delivered: %+v", ps.Branches)
	}
}

func TestUIDsOf(t *testing.T) {
	w := Kept{Entry: ent(1, "a", map[string]string{"k8s_pod_uid": "u"})}
	wo := Kept{Entry: ent(1, "b", map[string]string{})}
	idx := Kept{Entry: Entry{Stream: map[string]string{"k8s_pod_uid": "u"}, TS: 1}}
	for _, tc := range []struct {
		ks   []Kept
		want UIDEvidence
	}{{nil, UIDNone}, {[]Kept{wo}, UIDNone}, {[]Kept{w, idx}, UIDAll}, {[]Kept{w, wo}, UIDSome}} {
		if got := UIDsOf(tc.ks); got != tc.want {
			t.Errorf("%v: %v", tc.ks, got)
		}
	}
}

// A full read at the boundary and the timestamp just before it is not
// saturated: it can advance. Only a read holding nothing older than the
// boundary is.
func TestSaturationNeedsNothingOlderThanTheBoundary(t *testing.T) {
	// 700 newer, then 400 at T (300 delivered on the first page), then
	// 4,600 at T-1: the second read is full at T and T-1, the re-read too
	f := append(older(6000, 700, "new"), burst(5000, 400, "t")...)
	f = append(f, burst(4999, 4600, "t-1")...)
	ps := NewPageSet(1, 10_000, 1)
	if k := page(ps, []fakeBranch{f}); len(k) != 1000 || ps.Branches[0].Boundary != 5000 {
		t.Fatalf("first page: %d, boundary %d", len(k), ps.Branches[0].Boundary)
	}
	k := page(ps, []fakeBranch{f})
	if ps.Branches[0].Stopped || len(k) != 700 || ps.Branches[0].Boundary != 4999 { // 100 new at T, 600 at T-1
		t.Fatalf("second page: stopped %v, kept %d, boundary %d", ps.Branches[0].Stopped, len(k), ps.Branches[0].Boundary)
	}
}

// "At least N" counts every distinct entry known at the boundary: those
// delivered and those the re-read returned, which Loki may choose apart.
func TestStopCountsDeliveredAndRead(t *testing.T) {
	ps := NewPageSet(1, 10_000, 1)
	a, b, c, d := ent(5000, "a", nil), ent(5000, "b", nil), ent(5000, "c", nil), ent(5000, "d", nil)
	ps.Apply([]Read{{Branch: 0, Limit: 3, Entries: []Entry{a, b, c}}}, 3)
	ps.Stop(Read{Branch: 0, Limit: 2, Entries: []Entry{c, d}})
	if b := ps.Branches[0]; !b.Stopped || b.StoppedCount != 4 {
		t.Fatalf("at least: %+v", b)
	}
}

// A re-read the server refuses (its 4xx: a lower max_entries_limit) stops
// the branch with the server's words; any other failure fails the page
// whole and leaves the page set where it was.
func TestReadPageRefusalsAndFailures(t *testing.T) {
	f := append(burst(5000, 1500, "t"), older(4999, 10, "o")...)
	ps := NewPageSet(1, 10_000, 1)
	page(ps, []fakeBranch{f}) // 1,000 at T delivered
	refuse := func(_ context.Context, i int, start, end int64, limit int) ([]Entry, error) {
		if limit == RetryLimit {
			return nil, &StatusError{Code: 400, Text: "max entries limit per query exceeded (5000 > 2000)"}
		}
		return f.read(i, start, end, limit).Entries, nil
	}
	kept, markers, err := ReadPage(context.Background(), refuse, ps)
	if err != nil || len(kept) != 0 || len(markers) != 1 || markers[0].Count != 1000 || markers[0].TS != 5000 ||
		markers[0].Refused != "max entries limit per query exceeded (5000 > 2000)" || !ps.Branches[0].Stopped {
		t.Fatalf("refused: %v %+v %v", kept, markers, err)
	}
	ps = NewPageSet(1, 10_000, 2)
	before := fmt.Sprintf("%+v", ps.Branches)
	boom := errors.New("connection reset")
	fail := func(_ context.Context, i int, start, end int64, limit int) ([]Entry, error) {
		if i == 1 {
			return nil, boom
		}
		return f.read(i, start, end, limit).Entries, nil
	}
	if _, _, err := ReadPage(context.Background(), fail, ps); !errors.Is(err, boom) || fmt.Sprintf("%+v", ps.Branches) != before {
		t.Fatalf("a failed read moved the page set: %v %+v", err, ps.Branches)
	}
}
