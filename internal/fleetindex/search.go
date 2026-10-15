package fleetindex

import (
	"container/heap"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// The search (slice 9 note §3): over every cluster's index, by name, then
// namespace/name, then kind; ranked deterministically; bounded by a top-K
// of at most MaxHits; each hit's aspect from its cluster's Observer by UID,
// fenced by incarnation.

const (
	// MaxHits bounds one search's hits.
	MaxHits = 200
	// MinQuery is the shortest query searched; the empty query is the
	// console's own.
	MinQuery = 2
)

// Hit is one indexed object that matched.
type Hit struct {
	Cluster              string
	Kind                 string // display: "ConfigMap"
	Key                  string // the index's kind: group/resource
	GVR                  string // as served: group/version/resource
	Namespace, Name, UID string
	// Incarnation and Gen: the Observer and acquisition generation the
	// entry was read under.
	Incarnation, Gen uint64
	// KindState is the kind's state on its cluster (live, stale, capped,
	// ...); AsOf its as-of time when not live.
	KindState string
	AsOf      *time.Time
	// Role is the Observer's word on the object; a kind the Observer does
	// not evaluate, or an unverifiable read, is unverified.
	Role observe.RoleView

	score    int
	order    int // the cluster's place in the fleet
	observer string
}

// Result is a search's hits, the number of matches before the bound, and
// the coverage they were searched under.
type Result struct {
	Hits     []Hit
	Total    int
	Coverage []ClusterCoverage
}

// Search runs one search; a newer search waits for an older to return, and
// the caller cancels the older through its context.
func (ix *Index) Search(ctx context.Context, query string, limit int) (Result, error) {
	ix.searchMu.Lock()
	defer ix.searchMu.Unlock()
	res := Result{Coverage: ix.Coverage()}
	if limit <= 0 || limit > MaxHits {
		limit = MaxHits
	}
	m := newMatcher(query)
	if m == nil {
		return res, nil
	}
	ix.mu.Lock()
	order := append([]string(nil), ix.order...)
	cs := map[string]*coord{}
	for n, c := range ix.clusters {
		cs[n] = c
	}
	ix.mu.Unlock()
	rank := map[string]int{}
	for i, n := range order {
		rank[n] = i
	}
	var names []string
	for n := range cs {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, iok := rank[names[i]]
		rj, jok := rank[names[j]]
		if iok != jok {
			return iok
		}
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	top := &hitHeap{}
	scanned := 0
	for i, name := range names {
		c := cs[name]
		c.mu.Lock()
		var inc uint64
		if c.obs != nil {
			inc = c.obs.Incarnation()
		}
		c.mu.Unlock()
		c.store.scan(func(kind string, k *kindStore, gen uint64) bool {
			if !m.kindOK(k.desc.ref) {
				return true
			}
			for _, e := range k.entries {
				if scanned++; scanned%4096 == 0 && ctx.Err() != nil {
					return false
				}
				score := m.score(e, k.desc.ref)
				if score == 0 {
					continue
				}
				res.Total++
				h := Hit{Cluster: name, Kind: k.desc.ref.Kind, Key: kind, GVR: k.desc.gvr, Namespace: e.Namespace, Name: e.Name, UID: e.UID,
					Incarnation: inc, Gen: gen, KindState: k.status.State, score: score, order: i, observer: k.desc.observer}
				if k.status.State != StateLive {
					h.AsOf = k.status.AsOf
				}
				if top.Len() < limit {
					heap.Push(top, h)
				} else if before(h, (*top)[0]) {
					(*top)[0] = h
					heap.Fix(top, 0)
				}
			}
			return true
		})
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	hits := make([]Hit, top.Len())
	for i := len(hits) - 1; i >= 0; i-- {
		hits[i] = heap.Pop(top).(Hit)
	}
	if err := ix.roles(ctx, cs, hits); err != nil {
		return Result{}, err
	}
	res.Hits = hits
	return res, nil
}

// roles sets each hit's aspect from its cluster's Observer, one call per
// cluster, fenced by the incarnation the hit was indexed under.
func (ix *Index) roles(ctx context.Context, cs map[string]*coord, hits []Hit) error {
	unverified := func(reason string) observe.RoleView {
		return observe.RoleView{State: observe.RoleUnverified, Reason: reason}
	}
	byCluster := map[string][]int{}
	for i := range hits {
		if hits[i].observer == "" {
			hits[i].Role = unverified(observe.ReasonNoLedger)
			continue
		}
		byCluster[hits[i].Cluster] = append(byCluster[hits[i].Cluster], i)
	}
	for name, idx := range byCluster {
		c := cs[name]
		c.mu.Lock()
		obs := c.obs
		c.mu.Unlock()
		var qs []observe.RoleQuery
		var asked []int
		for _, i := range idx {
			if obs == nil || obs.Incarnation() != hits[i].Incarnation {
				hits[i].Role = unverified("the cluster's Observer changed since it was indexed")
				continue
			}
			qs = append(qs, observe.RoleQuery{UID: hits[i].UID, Source: hits[i].observer})
			asked = append(asked, i)
		}
		if len(qs) == 0 {
			continue
		}
		vs, ok := obs.RolesNow(ctx, qs)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for j, i := range asked {
			if !ok {
				hits[i].Role = unverified("the Observer did not answer")
				continue
			}
			hits[i].Role = vs[j]
		}
	}
	return nil
}

// before orders hits: score, then fleet order, kind, namespace, name, UID.
func before(a, b Hit) bool {
	switch {
	case a.score != b.score:
		return a.score > b.score
	case a.order != b.order:
		return a.order < b.order
	case a.Key != b.Key:
		return a.Key < b.Key
	case a.Namespace != b.Namespace:
		return a.Namespace < b.Namespace
	case a.Name != b.Name:
		return a.Name < b.Name
	}
	return a.UID < b.UID
}

// hitHeap keeps the best `limit` hits with the worst at the root.
type hitHeap []Hit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return before(h[j], h[i]) }
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(Hit)) }
func (h *hitHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// matcher is one query: a name (and namespace/name) needle, and an
// optional kind restriction ("pod web": the first word names a kind).
type matcher struct {
	needle string
	kind   string // a kind restriction, lower-cased
	nsName bool   // the needle has a slash: match namespace/name
}

func newMatcher(query string) *matcher {
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if len(q) < MinQuery {
		return nil
	}
	m := &matcher{needle: q}
	if first, rest, ok := strings.Cut(q, " "); ok && len(rest) > 0 {
		m.kind, m.needle = first, rest
	}
	m.nsName = strings.Contains(m.needle, "/")
	return m
}

// kindMatches: the word names the kind, its plural resource, or a prefix of
// either.
func kindMatches(word string, ref KindRef) bool {
	k := strings.ToLower(ref.Kind)
	return strings.HasPrefix(k, word) || strings.HasPrefix(ref.Resource, word)
}

func (m *matcher) kindOK(ref KindRef) bool {
	return m.kind == "" || kindMatches(m.kind, ref)
}

// score is 0 for no match; higher is better.
func (m *matcher) score(e *Entry, ref KindRef) int {
	name := lower(e.Name)
	switch {
	case name == m.needle:
		return 1000
	case strings.HasPrefix(name, m.needle):
		return 800 - min(len(name)-len(m.needle), 99)
	}
	if i := strings.Index(name, m.needle); i >= 0 {
		return 600 - min(i, 99)
	}
	if m.nsName {
		full := lower(e.Namespace) + "/" + name
		if full == m.needle {
			return 1000
		}
		if strings.HasPrefix(full, m.needle) {
			return 700
		}
		if strings.Contains(full, m.needle) {
			return 500
		}
	}
	if m.kind == "" && kindMatches(m.needle, ref) {
		return 100 // a kind alone lists its objects, after every name match
	}
	return 0
}

// lower is s lower-cased, allocating only when s has an upper-case letter
// (Kubernetes names rarely do).
func lower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}
