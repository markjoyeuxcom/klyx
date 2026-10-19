package loki

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// Page sizes (S8b §5, §8).
const (
	PageLimit  = 1000 // entries kept per page, across branches
	RetryLimit = 5000 // a saturated branch's one re-read (Loki's default maximum)
	// PageBytes bounds a page's line text, across branches (Codex's review
	// of b2f945c): six branches may each answer 8 MiB, and the page the
	// bridge carries stays one response's size. The cursor moves only past
	// what is kept, so the rest is read on the next page.
	PageBytes = 8 << 20
)

// entryBytes is an entry's share of a page's budget: what a page carries
// for it - its line, and a bounded allowance for its timestamp and the label
// shown beside it (clipped by the reader), never the stream's labels, which
// its entries share.
func entryBytes(e Entry) int { return len(e.Line) + 320 }

// BranchState is one branch's place in a page set.
type BranchState struct {
	End int64 // exclusive, Unix nanoseconds
	// Boundary is the timestamp of the oldest entry delivered (End-1 once a
	// page kept one); Delivered holds the keys delivered at it, for the
	// dedupe of the overlap the next read returns.
	Boundary  int64
	Delivered map[[32]byte]struct{}
	Exhausted bool
	// Stopped: saturated after the re-read - at least StoppedCount entries
	// share Boundary, and whether more remain cannot be established.
	Stopped      bool
	StoppedCount int
	Refused      string // the server's refusal of the re-read, when it refused
}

// Active: the branch still has entries to read.
func (b BranchState) Active() bool { return !b.Exhausted && !b.Stopped }

// PageSet is a history view's paging state across its branches. Mode,
// branches, range and identity are the caller's, locked for the page set
// (S8b §4); this is only where each branch is.
type PageSet struct {
	Start    int64
	Branches []BranchState
}

// NewPageSet starts n branches at end (exclusive).
func NewPageSet(start, end int64, n int) *PageSet {
	ps := &PageSet{Start: start, Branches: make([]BranchState, n)}
	for i := range ps.Branches {
		ps.Branches[i] = BranchState{End: end, Delivered: map[[32]byte]struct{}{}}
	}
	return ps
}

// More: a branch can still be read (Load older has something to do).
func (ps *PageSet) More() bool {
	for _, b := range ps.Branches {
		if b.Active() {
			return true
		}
	}
	return false
}

// Markers are every stopped branch's: an unresolved boundary stays said on
// every page until it is skipped or the page set ends.
func (ps *PageSet) Markers() []Marker {
	var out []Marker
	for i, b := range ps.Branches {
		if b.Stopped {
			out = append(out, Marker{Branch: i, TS: b.Boundary, Count: b.StoppedCount, Refused: b.Refused})
		}
	}
	return out
}

// Done: every branch is exhausted - the start of the range is reached. A
// stopped branch is not done.
func (ps *PageSet) Done() bool {
	for _, b := range ps.Branches {
		if !b.Exhausted {
			return false
		}
	}
	return true
}

// Read is one branch's raw response to a page request.
type Read struct {
	Branch  int
	Limit   int // the limit it was asked with
	Entries []Entry
}

// Saturated reports whether a branch's raw read cannot advance: it is full
// and holds nothing older than the branch's boundary (lab finding 11).
func (ps *PageSet) Saturated(r Read) bool {
	b := ps.Branches[r.Branch]
	if b.Boundary == 0 || len(r.Entries) < r.Limit {
		return false
	}
	for _, e := range r.Entries {
		if e.TS < b.Boundary {
			return false
		}
	}
	return true
}

// Stop ends a branch saturated after its re-read: at least the entries
// known at its boundary (delivered, and read) share that timestamp.
func (ps *PageSet) Stop(r Read) {
	b := &ps.Branches[r.Branch]
	known := map[[32]byte]struct{}{}
	for k := range b.Delivered {
		known[k] = struct{}{}
	}
	for _, e := range r.Entries {
		if e.TS == b.Boundary {
			known[key(r.Branch, e)] = struct{}{}
		}
	}
	b.Stopped, b.StoppedCount = true, len(known)
}

// Skip moves a stopped branch past its boundary - the person's explicit
// choice: the entries at that timestamp not delivered are left out.
func (ps *PageSet) Skip(branch int) {
	b := &ps.Branches[branch]
	b.End, b.Stopped, b.StoppedCount, b.Refused = b.Boundary, false, 0, ""
	b.Delivered = map[[32]byte]struct{}{}
}

// Kept is an entry a page keeps, with its branch.
type Kept struct {
	Branch int
	Entry
}

// Apply keeps the newest limit entries of the reads across branches and
// moves each branch (S8b §5): entries already delivered drop out first
// (before the cut, so recovered older entries are never lost behind a held
// boundary); the newest limit merge in a stable order; a branch's End moves
// to the oldest timestamp it contributed, plus one nanosecond. A branch is
// exhausted when its raw read was shorter than its limit and none of its
// entries was cut - exhaustion is read from the raw response, never from
// what remained after the dedupe.
func (ps *PageSet) Apply(reads []Read, limit int) []Kept {
	return ps.ApplyBounded(reads, limit, 0)
}

// ApplyBounded is Apply with a byte budget for the page's entries (0: none):
// the newest entries are kept while they fit, at least one.
func (ps *PageSet) ApplyBounded(reads []Read, limit, maxBytes int) []Kept {
	type cand struct {
		Kept
		k [32]byte
	}
	var all []cand
	fresh := make([]int, len(ps.Branches))
	for _, r := range reads {
		b := ps.Branches[r.Branch]
		for _, e := range r.Entries {
			k := key(r.Branch, e)
			if e.TS == b.Boundary {
				if _, done := b.Delivered[k]; done {
					continue
				}
			}
			all = append(all, cand{Kept{r.Branch, e}, k})
			fresh[r.Branch]++
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].TS != all[j].TS {
			return all[i].TS > all[j].TS
		}
		if all[i].Branch != all[j].Branch {
			return all[i].Branch < all[j].Branch
		}
		return string(all[i].k[:]) < string(all[j].k[:])
	})
	if len(all) > limit {
		all = all[:limit]
	}
	if maxBytes > 0 {
		n := 0
		for i, c := range all {
			if n += entryBytes(c.Entry); n > maxBytes && i > 0 {
				all = all[:i]
				break
			}
		}
	}
	kept := make([]Kept, 0, len(all))
	keptOf := make([]int, len(ps.Branches))
	oldest := make([]int64, len(ps.Branches))
	for _, c := range all {
		kept = append(kept, c.Kept)
		keptOf[c.Branch]++
		oldest[c.Branch] = c.TS // ascending through the newest-first order: the last is the oldest
	}
	for _, c := range all {
		b := &ps.Branches[c.Branch]
		if c.TS != oldest[c.Branch] {
			continue
		}
		if b.Boundary != c.TS {
			b.Boundary, b.Delivered = c.TS, map[[32]byte]struct{}{}
		}
		b.Delivered[c.k] = struct{}{}
		b.End = c.TS + 1
	}
	for _, r := range reads {
		if len(r.Entries) < r.Limit && keptOf[r.Branch] == fresh[r.Branch] {
			ps.Branches[r.Branch].Exhausted = true
		}
	}
	return kept
}

// key is an entry's dedupe key within its branch: the stream labels, the
// structured metadata, the timestamp and the line, raw.
func key(branch int, e Entry) [32]byte {
	h := sha256.New()
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(branch))
	h.Write(n[:])
	for _, m := range []map[string]string{e.Stream, e.Meta} {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			writeField(h, k)
			writeField(h, m[k])
		}
		h.Write([]byte{0xff}) // ends the map
	}
	binary.BigEndian.PutUint64(n[:], uint64(e.TS))
	h.Write(n[:])
	writeField(h, e.Line)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeField(h interface{ Write([]byte) (int, error) }, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(s))
}

// UIDEvidence is what a first page says about pod UIDs (S8b §4).
type UIDEvidence int

const (
	UIDNone UIDEvidence = iota // no entry, or no entry carries one
	UIDSome                    // some entries carry one, some do not
	UIDAll                     // every entry carries one
)

// UIDsOf classifies a first page's entries.
func UIDsOf(entries []Kept) UIDEvidence {
	with := 0
	for _, e := range entries {
		if e.UID() != "" {
			with++
		}
	}
	switch {
	case with == 0:
		return UIDNone
	case with == len(entries):
		return UIDAll
	}
	return UIDSome
}
