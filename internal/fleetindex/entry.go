package fleetindex

import (
	"sort"
	"time"
)

// LabelBytes caps an entry's labels (keys plus values, parent §8.4).
const LabelBytes = 2048

// The estimate's constants (slice 9 note §2.6): conservative, calibrated by
// TestEstimateCoversRetainedHeap against the heap an entry actually keeps.
const (
	entryOverhead  = 256  // the Entry, its pointer and its share of the kind's map
	labelsOverhead = 32   // the label slice's allocation
	perLabel       = 64   // a label's two string headers and their allocations' rounding
	kindOverhead   = 4096 // a kind's store: its map header and state
	// StreamReserve is charged while a list or a watch is open: its decode
	// holds one object at a time (§2.6).
	StreamReserve = 256 << 10
	// SearchReserve is the search's working set, charged once (§3).
	SearchReserve = 64 << 10
)

// Label is one kept label.
type Label struct{ Key, Value string }

// Entry is one indexed object: identity, place, capped labels and creation
// time - never its contents.
type Entry struct {
	UID, Namespace, Name string
	Labels               []Label // sorted by key, at most LabelBytes
	LabelsTruncated      bool
	NoLabels             bool      // the source keeps none: "labels not indexed"
	Created              time.Time // zero with NoCreated
	NoCreated            bool      // the source keeps no creation time

	labelHash uint64 // of the source's labels, before the cap
	mark      uint64 // the last read that saw it (mark-and-sweep)
	size      int64  // what the budget holds for it
}

// Source is what an entry is built from: a projected object's metadata.
type Source struct {
	UID, Namespace, Name string
	Labels               map[string]string
	Created              time.Time
	NoLabels, NoCreated  bool

	scanned   bool
	kept      []Label // in the order sent
	truncated bool
	hash      uint64
}

// labelsHash is the hash of every label the source has.
func (s *Source) labelsHash() uint64 {
	if s.scanned {
		return s.hash
	}
	return labelHash(s.Labels)
}

// labelHash is order-independent, so a visit compares labels without
// sorting or allocating: the sum of each pair's FNV-1a.
func labelHash(l map[string]string) uint64 {
	var sum uint64
	for k, v := range l {
		sum += pairHash(k, v)
	}
	return sum
}

func pairHash(k, v string) uint64 {
	h := fnvString(fnvOffset, k)
	h *= fnvPrime // the separator: a zero byte
	return fnvString(h, v)
}

const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

func fnvString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime
	}
	return h
}

// same reports whether e already holds s, without allocating.
func (e *Entry) same(s Source, hash uint64) bool {
	return e.UID == s.UID && e.Namespace == s.Namespace && e.Name == s.Name && e.NoLabels == s.NoLabels &&
		e.NoCreated == s.NoCreated && e.Created.Equal(s.Created) && e.labelHash == hash
}

// keptLabels returns the label keys an entry keeps, sorted, and whether the
// cap truncated them. Under the cap (the common case) every key is kept.
func keptLabels(l map[string]string) ([]string, bool) {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for i, k := range keys {
		n += len(k) + len(l[k])
		if n > LabelBytes {
			return keys[:i], true
		}
	}
	return keys, false
}

// estimate is the budget's charge for an entry built from s (§2.6), computed
// before anything is built.
func estimate(s Source) int64 {
	n := int64(entryOverhead + len(s.UID) + len(s.Namespace) + len(s.Name))
	if s.scanned {
		if len(s.kept) == 0 {
			return n
		}
		n += labelsOverhead
		for _, l := range s.kept {
			n += int64(perLabel + len(l.Key) + len(l.Value))
		}
		return n
	}
	if len(s.Labels) == 0 || s.NoLabels {
		return n
	}
	total := 0
	for k, v := range s.Labels {
		total += len(k) + len(v)
	}
	if total <= LabelBytes {
		return n + labelsOverhead + int64(perLabel*len(s.Labels)+total)
	}
	keys, _ := keptLabels(s.Labels)
	n += labelsOverhead
	for _, k := range keys {
		n += int64(perLabel + len(k) + len(s.Labels[k]))
	}
	return n
}

// build makes the entry for s; size is its estimate.
func build(s Source, hash uint64, size int64) *Entry {
	e := &Entry{UID: s.UID, Namespace: s.Namespace, Name: s.Name, NoLabels: s.NoLabels, Created: s.Created, NoCreated: s.NoCreated, labelHash: hash, size: size}
	if s.scanned {
		if len(s.kept) > 0 {
			e.Labels = append([]Label(nil), s.kept...)
			sort.Slice(e.Labels, func(i, j int) bool { return e.Labels[i].Key < e.Labels[j].Key })
		}
		e.LabelsTruncated = s.truncated
		return e
	}
	if len(s.Labels) > 0 && !s.NoLabels {
		keys, truncated := keptLabels(s.Labels)
		e.Labels = make([]Label, len(keys))
		for i, k := range keys {
			e.Labels[i] = Label{Key: k, Value: s.Labels[k]}
		}
		e.LabelsTruncated = truncated
	}
	return e
}
