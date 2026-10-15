// Package fleetindex is the fleet metadata index (The Line slice 9, parent
// §8.4): per cluster, what each indexed kind holds - identity, namespace,
// name, capped labels and creation time, never contents - within one
// shared budget, so the console can search every cluster at once.
package fleetindex

import "sync"

// Limits are the index's bounds across the store, the buffers and the
// search index (parent §14).
type Limits struct {
	ClusterEntries int
	ClusterBytes   int64
	FleetEntries   int
	FleetBytes     int64
	// PodPercent is the share of every limit Pods may fill (§14.11: Pods
	// are capped first): the rest is always left to the other kinds.
	PodPercent int
}

// DefaultLimits: 50,000 entries and 32 MiB per cluster, 200,000 and 128 MiB
// across the fleet; Pods up to 80% of each.
var DefaultLimits = Limits{ClusterEntries: 50_000, ClusterBytes: 32 << 20, FleetEntries: 200_000, FleetBytes: 128 << 20, PodPercent: 80}

// Class is an admission's priority class.
type Class int

const (
	ClassOther Class = iota // every kind but Pods: the full limits
	ClassPods               // Pods: PodPercent of each limit
)

// ceiling is a limit for class c.
func (l Limits) ceiling(limit int64, c Class) int64 {
	if c != ClassPods {
		return limit
	}
	p := int64(l.PodPercent)
	if p <= 0 || p > 100 {
		p = 100
	}
	return limit * p / 100
}

type usage struct {
	entries int
	bytes   int64
}

// Budget is the shared accounting: atomic across kinds and clusters. Every
// list page and every watch event that adds an entry or grows one is
// admitted against both the cluster's and the fleet's limits before it is
// retained.
type Budget struct {
	mu      sync.Mutex
	limits  Limits
	cluster map[string]*usage
	fleet   usage
}

func NewBudget(l Limits) *Budget { return &Budget{limits: l, cluster: map[string]*usage{}} }

// Admit reserves entries and bytes for a cluster when both its limits and
// the fleet's allow it at class c's ceiling, all or nothing. A delta may be
// negative in bytes (a shrinking entry), which always fits.
func (b *Budget) Admit(cluster string, c Class, entries int, bytes int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.cluster[cluster]
	if u == nil {
		u = &usage{}
		b.cluster[cluster] = u
	}
	if entries > 0 || bytes > 0 {
		l := b.limits
		if int64(u.entries+entries) > l.ceiling(int64(l.ClusterEntries), c) || u.bytes+bytes > l.ceiling(l.ClusterBytes, c) ||
			int64(b.fleet.entries+entries) > l.ceiling(int64(l.FleetEntries), c) || b.fleet.bytes+bytes > l.ceiling(l.FleetBytes, c) {
			return false
		}
	}
	u.entries += entries
	u.bytes += bytes
	b.fleet.entries += entries
	b.fleet.bytes += bytes
	return true
}

// Release returns what a cluster held (deleted entries, a stopped kind).
func (b *Budget) Release(cluster string, entries int, bytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.cluster[cluster]
	if u == nil {
		return
	}
	u.entries -= entries
	u.bytes -= bytes
	b.fleet.entries -= entries
	b.fleet.bytes -= bytes
	if u.entries < 0 || u.bytes < 0 {
		u.entries, u.bytes = max(u.entries, 0), max(u.bytes, 0)
	}
}

// Used reports a cluster's and the fleet's usage.
func (b *Budget) Used(cluster string) (clusterEntries int, clusterBytes int64, fleetEntries int, fleetBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u := b.cluster[cluster]; u != nil {
		clusterEntries, clusterBytes = u.entries, u.bytes
	}
	return clusterEntries, clusterBytes, b.fleet.entries, b.fleet.bytes
}
