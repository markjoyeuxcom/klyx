package fleetindex

import (
	"sync"
	"time"
)

// Kind states (slice 9 note §2.8).
const (
	StateIndexing      = "indexing"    // the first read is in progress
	StateLive          = "live"        // read, and following changes
	StateCapped        = "capped"      // frozen at a cap
	StateOversize      = "oversize"    // frozen at an object over the scanner's bounds
	StateDenied        = "denied"      // the list was refused (403)
	StateWatchDenied   = "watchDenied" // the watch was refused after a good list
	StateUnsupported   = "unsupported" // metadata unsupported (406)
	StateStale         = "stale"       // errors, retried with backoff; or the connection is down
	StateNotServed     = "notServed"
	StateConfiguredOut = "configuredOut"
)

// KindStatus is one kind's state on one cluster.
type KindStatus struct {
	State   string
	Entries int
	AsOf    *time.Time // capped: when it froze; stale and watch denied: the last good read
	Err     string
}

// kindDesc says what a kind is on its cluster: its display name, its
// served API and, when the Observer holds it, the Observer's kind name.
type kindDesc struct {
	ref      KindRef
	gvr      string // group/version/resource, as served
	observer string
}

// kindStore is one kind's entries on one cluster.
type kindStore struct {
	desc    kindDesc
	class   Class
	entries map[string]*Entry // by UID
	bytes   int64             // charged, the kind's overhead included
	status  KindStatus
	frozen  bool
	mark    uint64 // the read in progress
}

// clusterStore is one cluster's index. Every write names the acquisition
// generation it belongs to and applies only while that generation is
// current (§2.7): a cancelled generation's late results are dropped.
// Generation 0 is none: the connection is down, or the cluster ended.
type clusterStore struct {
	cluster string
	budget  *Budget
	clk     func() time.Time

	mu    sync.RWMutex
	gen   uint64
	kinds map[string]*kindStore
}

func newClusterStore(cluster string, b *Budget, clk func() time.Time) *clusterStore {
	return &clusterStore{cluster: cluster, budget: b, clk: clk, kinds: map[string]*kindStore{}}
}

// begin starts acquisition generation gen (> 0): a frozen kind is thawed for its
// in-place re-read, keeping what it holds.
func (c *clusterStore) begin(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen = gen
	for _, k := range c.kinds {
		k.frozen = false
	}
}

// kindLocked returns the kind's store, created (and its overhead admitted)
// on first use; nil when even the overhead does not fit.
func (c *clusterStore) kindLocked(kind string, class Class) *kindStore {
	if k := c.kinds[kind]; k != nil {
		return k
	}
	if !c.budget.Admit(c.cluster, class, 0, kindOverhead) {
		return nil
	}
	k := &kindStore{class: class, entries: map[string]*Entry{}, bytes: kindOverhead, status: KindStatus{State: StateIndexing}}
	c.kinds[kind] = k
	return k
}

// describe records what kind is on this cluster for generation gen.
func (c *clusterStore) describe(gen uint64, kind string, class Class, d kindDesc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == 0 || gen != c.gen {
		return
	}
	if k := c.kindLocked(kind, class); k != nil {
		k.desc = d
	}
}

// beginRead starts a complete read of kind (a list or a visit) and returns
// its mark; ok is false for a stale generation or a frozen kind.
func (c *clusterStore) beginRead(gen uint64, kind string, class Class) (mark uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == 0 || gen != c.gen {
		return 0, false
	}
	k := c.kindLocked(kind, class)
	if k == nil {
		c.freezeMissingLocked(kind, class)
		return 0, false
	}
	if k.frozen {
		return 0, false
	}
	k.mark++
	return k.mark, true
}

// freezeMissingLocked records a kind that could not even start: capped
// with nothing held.
func (c *clusterStore) freezeMissingLocked(kind string, class Class) {
	now := c.clk()
	c.kinds[kind] = &kindStore{class: class, entries: map[string]*Entry{}, frozen: true, status: KindStatus{State: StateCapped, AsOf: &now}}
}

// Put result.
const (
	putApplied = iota
	putStale   // not this generation, or the kind is frozen: dropped
	putCapped  // refused at a cap: the kind froze
)

// put applies one object seen by read mark (0 for a watch event between
// reads). An unchanged entry is only marked; a new or changed one is
// admitted first and built after.
func (c *clusterStore) put(gen uint64, kind string, mark uint64, s Source) int {
	hash := s.labelsHash()
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.kinds[kind]
	if gen == 0 || gen != c.gen || k == nil || k.frozen {
		return putStale
	}
	if e := k.entries[s.UID]; e != nil {
		if mark > e.mark {
			e.mark = mark
		}
		if e.same(s, hash) {
			return putApplied
		}
		size := estimate(s)
		if d := size - e.size; d > 0 && !c.budget.Admit(c.cluster, k.class, 0, d) {
			c.freezeLocked(k)
			return putCapped
		} else if d < 0 {
			c.budget.Release(c.cluster, 0, -d)
		}
		ne := build(s, hash, size)
		ne.mark = e.mark
		k.entries[s.UID] = ne
		k.bytes += size - e.size
		return putApplied
	}
	size := estimate(s)
	if !c.budget.Admit(c.cluster, k.class, 1, size) {
		c.freezeLocked(k)
		return putCapped
	}
	e := build(s, hash, size)
	e.mark = max(mark, k.mark)
	k.entries[s.UID] = e
	k.bytes += size
	return putApplied
}

// remove applies a watch deletion.
func (c *clusterStore) remove(gen uint64, kind, uid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.kinds[kind]
	if gen == 0 || gen != c.gen || k == nil || k.frozen {
		return false
	}
	if e := k.entries[uid]; e != nil {
		delete(k.entries, uid)
		k.bytes -= e.size
		c.budget.Release(c.cluster, 1, e.size)
	}
	return true
}

// endRead completes read mark: what it did not see is released, and the
// kind takes st (live, or stale for an Observer kind read from last-good
// evidence). A frozen kind or a stale generation keeps what it holds.
func (c *clusterStore) endRead(gen uint64, kind string, mark uint64, st KindStatus) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.kinds[kind]
	if gen == 0 || gen != c.gen || k == nil || k.frozen || k.mark != mark {
		return false
	}
	for uid, e := range k.entries {
		if e.mark < mark {
			delete(k.entries, uid)
			k.bytes -= e.size
			c.budget.Release(c.cluster, 1, e.size)
		}
	}
	st.Entries = len(k.entries)
	k.status = st
	return true
}

func (c *clusterStore) freezeLocked(k *kindStore) {
	now := c.clk()
	k.frozen = true
	k.status = KindStatus{State: StateCapped, Entries: len(k.entries), AsOf: &now}
}

// capKind freezes a kind whose stream could not be admitted: it reads
// capped, keeping what it holds.
func (c *clusterStore) capKind(gen uint64, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.kinds[kind]
	if gen == 0 || gen != c.gen || k == nil || k.frozen {
		return
	}
	c.freezeLocked(k)
}

// freezeOversize stops a kind at an object over the scanner's bounds,
// keeping what it admitted.
func (c *clusterStore) freezeOversize(gen uint64, kind string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.kinds[kind]
	if gen == 0 || gen != c.gen || k == nil || k.frozen {
		return
	}
	now := c.clk()
	k.frozen = true
	k.status = KindStatus{State: StateOversize, Entries: len(k.entries), AsOf: &now, Err: err.Error()}
}

// setStatus records a kind's state for generation gen (a failure, a
// stale connection); a frozen kind stays capped.
func (c *clusterStore) setStatus(gen uint64, kind string, class Class, st KindStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == 0 || gen != c.gen {
		return
	}
	k := c.kindLocked(kind, class)
	if k == nil || k.frozen {
		return
	}
	st.Entries = len(k.entries)
	k.status = st
}

// dropLocked releases a kind entirely. Caller holds c.mu.
func (c *clusterStore) dropLocked(kind string) {
	k := c.kinds[kind]
	if k == nil {
		return
	}
	c.budget.Release(c.cluster, len(k.entries), k.bytes)
	delete(c.kinds, kind)
}

// release drops every kind: the cluster is no longer indexed.
func (c *clusterStore) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for kind := range c.kinds {
		c.dropLocked(kind)
	}
	c.gen = 0 // nothing in flight applies any more
}

// staleAll marks every kind stale at once, keeping its entries (the
// connection went down).
func (c *clusterStore) staleAll(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen = 0 // the cancelled generation's late results are dropped
	for _, k := range c.kinds {
		if k.frozen {
			continue
		}
		at := at
		k.status = KindStatus{State: StateStale, Entries: len(k.entries), AsOf: &at, Err: "connection down"}
	}
}

// status returns every kind's state.
func (c *clusterStore) status() map[string]KindStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]KindStatus, len(c.kinds))
	for name, k := range c.kinds {
		out[name] = k.status
	}
	return out
}

// scan calls f with each kind under the read lock, with the generation its
// entries belong to; f returns false to stop. f must not retain the kind.
func (c *clusterStore) scan(f func(kind string, k *kindStore, gen uint64) bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for name, k := range c.kinds {
		if !f(name, k, c.gen) {
			return
		}
	}
}
