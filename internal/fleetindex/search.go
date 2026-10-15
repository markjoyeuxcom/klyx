package fleetindex

import (
	"container/heap"
	"context"
	"errors"
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
// the coverage they were searched under. Release returns the search's
// working reservation once the caller is done with the result (its DTO
// built); it is idempotent.
type Result struct {
	Hits     []Hit
	Total    int
	Coverage []ClusterCoverage
	release  func()
}

// Release returns the search's reservation.
func (r *Result) Release() {
	if r.release != nil {
		r.release()
		r.release = nil
	}
}

// The search's working set (review of 9b), admitted against the fleet
// budget for each search and calibrated by TestSearchWorkingSetFitsItsReservation:
// a fixed part (the query, the matcher, the call), per possible hit (its
// candidate, its Hit, its role query and answer, its DTO), per cluster and
// per kind of coverage.
const (
	MaxQueryBytes   = 512 // a longer query can match no name: it searches nothing
	searchPerHit    = 1 << 10
	searchPerCover  = 512
	searchPerKind   = 256
	searchFixedPart = SearchReserve
)

// beforeScan runs before each cluster's scan: a test seam, nil otherwise.
var beforeScan func(cluster string)

// ErrSearchBudget: the index's budget cannot hold a search's working set now.
var ErrSearchBudget = errors.New("fleetindex: the index's memory budget cannot hold a search now")

func searchReserve(limit int, cov []ClusterCoverage) int64 {
	n := int64(searchFixedPart) + int64(limit)*searchPerHit + int64(len(cov))*searchPerCover
	for _, c := range cov {
		n += int64(len(c.Kinds)) * searchPerKind
	}
	return n
}

// candidate is a scanned match, compact: the Hit is built only for the
// best limit of them. It keeps the entry's identity strings, never the
// entry - its labels would outlive a concurrent deletion uncharged (review
// of 3436289) - and those strings' bytes are admitted while it is held.
type candidate struct {
	ns, name, uid string
	bytes         int64
	desc          int32 // into the search's kind descriptions
	order         int32
	score         int32
	gen, inc      uint64
}

// scannedKind is one kind's description as the scan read it.
type scannedKind struct {
	key, kind, gvr, observer, state string
	asOf                            *time.Time
	cluster                         string
}

// Search runs one search. Searches run one at a time; waiting for the slot
// honours ctx, so a cancelled search never waits behind another.
func (ix *Index) Search(ctx context.Context, query string, limit int) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err // decided before the slot, so it never races a free one
	}
	select {
	case ix.searchSlot <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	defer func() { <-ix.searchSlot }()
	if limit <= 0 || limit > MaxHits {
		limit = MaxHits
	}
	cov := ix.Coverage()
	reserve := searchReserve(limit, cov)
	if !ix.budget.Admit("", ClassOther, 0, reserve) {
		return Result{}, ErrSearchBudget
	}
	released := false
	var held int64 // the held candidates' identity bytes, admitted as they enter
	res := Result{Coverage: cov, release: func() {
		if !released {
			released = true
			ix.budget.Release("", 0, reserve+held)
		}
	}}
	fail := func(err error) (Result, error) {
		res.Release()
		return Result{}, err
	}
	var m *matcher
	if len(query) <= MaxQueryBytes {
		m = newMatcher(query)
	}
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
	var kinds []scannedKind
	top := &candHeap{c: make([]candidate, 0, limit), kinds: &kinds}
	overBudget := false
	scanned := 0
	for i, name := range names {
		if beforeScan != nil {
			beforeScan(name)
		}
		cs[name].store.scan(func(kind string, k *kindStore, _ uint64) bool {
			if !m.kindOK(k.desc.ref) {
				return true
			}
			d := int32(-1)
			for _, e := range k.entries {
				if scanned++; scanned%4096 == 0 && ctx.Err() != nil {
					return false
				}
				score := m.score(e, k.desc.ref)
				if score == 0 {
					continue
				}
				res.Total++
				if d < 0 {
					sk := scannedKind{key: kind, kind: k.desc.ref.Kind, gvr: k.desc.gvr, observer: k.desc.observer, state: k.status.State, cluster: name}
					if k.status.State != StateLive {
						sk.asOf = k.status.AsOf
					}
					kinds = append(kinds, sk)
					d = int32(len(kinds) - 1)
				}
				c := candidate{ns: e.Namespace, name: e.Name, uid: e.UID, desc: d, order: int32(i), score: int32(score), gen: e.gen, inc: e.inc}
				c.bytes = int64(len(c.ns) + len(c.name) + len(c.uid))
				full := top.Len() >= limit
				if full && !top.better(c, top.c[0]) {
					continue
				}
				if !ix.budget.Admit("", ClassOther, 0, c.bytes) {
					overBudget = true
					return false
				}
				held += c.bytes
				if !full {
					heap.Push(top, c)
				} else {
					ix.budget.Release("", 0, top.c[0].bytes)
					held -= top.c[0].bytes
					top.c[0] = c
					heap.Fix(top, 0)
				}
			}
			return true
		})
		if overBudget {
			return fail(ErrSearchBudget)
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
	}
	hits := make([]Hit, top.Len())
	for i := len(hits) - 1; i >= 0; i-- {
		c := heap.Pop(top).(candidate)
		k := kinds[c.desc]
		hits[i] = Hit{Cluster: k.cluster, Kind: k.kind, Key: k.key, GVR: k.gvr, Namespace: c.ns, Name: c.name, UID: c.uid,
			Incarnation: c.inc, Gen: c.gen, KindState: k.state, AsOf: k.asOf, score: int(c.score), order: int(c.order), observer: k.observer}
	}
	if err := ix.roles(ctx, cs, hits); err != nil {
		return fail(err)
	}
	res.Hits = hits
	return res, nil
}

// roles sets each hit's aspect from its cluster's Observer, one call per
// cluster, fenced by the incarnation the hit was indexed under - checked
// before the call and again after it, so an Observer replaced during the
// read never lends its answer to the new one's hits (review of 9b).
func (ix *Index) roles(ctx context.Context, cs map[string]*coord, hits []Hit) error {
	unverified := func(reason string) observe.RoleView {
		return observe.RoleView{State: observe.RoleUnverified, Reason: reason}
	}
	const changed = "the cluster's Observer changed since it was indexed"
	current := func(c *coord) Observed {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.obs
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
		obs := current(c)
		var qs []observe.RoleQuery
		var asked []int
		for _, i := range idx {
			if obs == nil || obs.Incarnation() != hits[i].Incarnation {
				hits[i].Role = unverified(changed)
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
		same := current(c) == obs
		for j, i := range asked {
			switch {
			case !ok:
				hits[i].Role = unverified("the Observer did not answer")
			case !same || obs.Incarnation() != hits[i].Incarnation:
				hits[i].Role = unverified(changed)
			default:
				hits[i].Role = vs[j]
			}
		}
	}
	return nil
}

// candHeap keeps the best candidates, at most its capacity, with the worst
// at the root; ties read the scanned kinds' keys.
type candHeap struct {
	c     []candidate
	kinds *[]scannedKind
}

func (h *candHeap) Len() int           { return len(h.c) }
func (h *candHeap) Less(i, j int) bool { return h.better(h.c[j], h.c[i]) }
func (h *candHeap) Swap(i, j int)      { h.c[i], h.c[j] = h.c[j], h.c[i] }
func (h *candHeap) Push(x any)         { h.c = append(h.c, x.(candidate)) }
func (h *candHeap) Pop() any {
	x := h.c[len(h.c)-1]
	h.c = h.c[:len(h.c)-1]
	return x
}

// better orders matches: score, then fleet order, kind, namespace, name,
// UID.
func (h *candHeap) better(a, b candidate) bool {
	switch {
	case a.score != b.score:
		return a.score > b.score
	case a.order != b.order:
		return a.order < b.order
	}
	if ka, kb := (*h.kinds)[a.desc].key, (*h.kinds)[b.desc].key; ka != kb {
		return ka < kb
	}
	switch {
	case a.ns != b.ns:
		return a.ns < b.ns
	case a.name != b.name:
		return a.name < b.name
	}
	return a.uid < b.uid
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
