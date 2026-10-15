package fleetindex

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/observe"
)

// Observed is the part of a cluster's Observer the index reads (§2.4).
type Observed interface {
	Incarnation() uint64
	Kinds() []observe.KindInfo
	WatchKind(kind string, f func(observe.KindState)) (cancel func())
	VisitKind(kind string, visit func(obj any) bool) (st observe.KindState, complete, ok bool)
	Done() <-chan struct{}
}

// Acquire is how an index-acquired kind is read (§2.5).
type Acquire struct {
	// Served resolves a group/resource's served version, the preferred one
	// when served; served is false when discovery lacks it.
	Served func(ctx context.Context, group, resource, prefer string) (version string, served bool, err error)
	Reader *Reader
}

// Gap is what an Observer projection keeps no copy of (§2.1).
type Gap struct{ NoLabels, NoCreated bool }

// Settings configure the index.
type Settings struct {
	Kinds  []KindRef // nil: DefaultKinds; empty: none
	Limits Limits    // zero: DefaultLimits
	// Gaps names, by Observer kind, a projection that keeps no labels or
	// no creation time.
	Gaps         map[string]Gap
	Clock        func() time.Time
	VisitEvery   time.Duration // the visit throttle per kind (1s)
	BackoffMin   time.Duration // 1s
	BackoffMax   time.Duration // 30s
	ReprobeEvery time.Duration // a kind not served is re-probed (5m)
	WatchTimeout time.Duration // the server-side watch timeout (5m)
}

// Index is the fleet metadata index: one coordinator per cluster, one
// budget.
type Index struct {
	s      Settings
	budget *Budget

	mu       sync.Mutex
	order    []string
	clusters map[string]*coord
	genSeq   uint64
	wg       sync.WaitGroup
	stopped  bool
	quit     chan struct{} // closed by Stop
}

// New builds an index. The search's working set is charged to the fleet
// once (§2.6).
func New(s Settings) *Index {
	if s.Kinds == nil {
		s.Kinds = DefaultKinds
	}
	if s.Limits == (Limits{}) {
		s.Limits = DefaultLimits
	}
	if s.Clock == nil {
		s.Clock = time.Now
	}
	if s.VisitEvery == 0 {
		s.VisitEvery = time.Second
	}
	if s.BackoffMin == 0 {
		s.BackoffMin = time.Second
	}
	if s.BackoffMax == 0 {
		s.BackoffMax = 30 * time.Second
	}
	if s.ReprobeEvery == 0 {
		s.ReprobeEvery = 5 * time.Minute
	}
	if s.WatchTimeout == 0 {
		s.WatchTimeout = 5 * time.Minute
	}
	ix := &Index{s: s, budget: NewBudget(s.Limits), clusters: map[string]*coord{}, quit: make(chan struct{})}
	ix.budget.Admit("", ClassOther, 0, SearchReserve)
	return ix
}

// SetClusters records the fleet's clusters in fleet order; coverage names
// every one (§2.7).
func (ix *Index) SetClusters(names []string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.order = append([]string(nil), names...)
}

func (ix *Index) coordFor(cluster string) *coord {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.stopped {
		return nil
	}
	c := ix.clusters[cluster]
	if c == nil {
		c = &coord{ix: ix, name: cluster, store: newClusterStore(cluster, ix.budget, ix.s.Clock)}
		ix.clusters[cluster] = c
	}
	return c
}

func (ix *Index) nextGen() uint64 {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.genSeq++
	return ix.genSeq
}

// Attach hands a cluster's Observer (a new one is a new incarnation) and
// its read path to the index.
func (ix *Index) Attach(cluster string, obs Observed, acq Acquire) {
	c := ix.coordFor(cluster)
	if c == nil || obs == nil {
		return
	}
	c.mu.Lock()
	if c.obs == obs {
		c.mu.Unlock()
		return
	}
	c.stopLocked()
	c.obs, c.acq, c.ended = obs, acq, false
	if c.usable {
		c.startLocked()
	}
	c.mu.Unlock()
	ix.wg.Add(1)
	go func() {
		defer ix.wg.Done()
		select {
		case <-obs.Done():
			c.end(obs)
		case <-ix.quit:
		}
	}()
}

// SetConnection hands a cluster's connection word to the index, in the
// same serialized handoff that gives it to the Observer (§2.7).
func (ix *Index) SetConnection(cluster, word string) {
	c := ix.coordFor(cluster)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.word = word
	usable := connectionUsable(word)
	if usable == c.usable {
		return
	}
	c.usable = usable
	if !usable {
		c.stopLocked()
		c.store.staleAll(ix.s.Clock())
		return
	}
	if c.obs != nil && !c.ended {
		c.startLocked()
	}
}

// connectionUsable: the connection allows observation (the Observer's
// rule: "connected", or "connected · <reason>").
func connectionUsable(word string) bool {
	return word == "connected" || strings.HasPrefix(word, "connected · ")
}

// Stop ends every cluster's acquisition and waits for it.
func (ix *Index) Stop() {
	ix.mu.Lock()
	if !ix.stopped {
		close(ix.quit)
	}
	ix.stopped = true
	cs := make([]*coord, 0, len(ix.clusters))
	for _, c := range ix.clusters {
		cs = append(cs, c)
	}
	ix.mu.Unlock()
	for _, c := range cs {
		c.mu.Lock()
		c.stopLocked()
		c.mu.Unlock()
	}
	ix.wg.Wait()
}

// coord is one cluster's coordinator: it runs acquisition generations.
type coord struct {
	ix    *Index
	name  string
	store *clusterStore

	mu     sync.Mutex
	obs    Observed
	acq    Acquire
	word   string
	usable bool
	ran    bool // a generation has run: the cluster has been indexed
	ended  bool // its Observer ended (Final)
	gen    uint64
	cancel context.CancelFunc
}

func (c *coord) startLocked() {
	c.stopLocked()
	gen := c.ix.nextGen()
	c.gen, c.ran = gen, true
	c.store.begin(gen)
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	obs, acq := c.obs, c.acq
	c.ix.wg.Add(1)
	go func() {
		defer c.ix.wg.Done()
		c.run(ctx, gen, obs, acq)
	}()
}

func (c *coord) stopLocked() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.gen = 0
}

// end is Final: the Observer ended, so the cluster's entries are released
// and it reads "not indexed".
func (c *coord) end(obs Observed) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.obs != obs {
		return // an earlier incarnation
	}
	c.stopLocked()
	c.store.release()
	c.ended, c.ran = true, false
}

// replan starts a new generation when generation gen is still current: an
// Observer kind became served or stopped being served (§2.1).
func (c *coord) replan(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || gen == 0 {
		return
	}
	c.startLocked()
}

// planned is one kind's acquisition in a generation.
type planned struct {
	ref      KindRef
	class    Class
	observer string // the Observer kind visited; "" when the index acquires it
}

// plan orders the configured kinds (§2.3): Observer kinds but Pods, then
// the index-acquired kinds in configuration order, then Pods. served
// records, for every Observer kind matched, whether it was served when
// planned.
func (c *coord) plan(obs Observed) (out []planned, served map[string]bool) {
	byGR := map[string]string{}
	for _, k := range obs.Kinds() {
		g, r := groupResource(k.GVR)
		byGR[g+"/"+r] = k.Name
	}
	served = map[string]bool{}
	var first, acquired, pods []planned
	for _, ref := range c.ix.s.Kinds {
		p := planned{ref: ref, class: ClassOther}
		if ref.Group == "" && ref.Resource == "pods" {
			p.class = ClassPods
		}
		if name, ok := byGR[ref.Key()]; ok {
			st, _, _ := obs.VisitKind(name, func(any) bool { return false })
			served[name] = st.Coverage != observe.Absent
			if served[name] {
				p.observer = name
			}
		}
		switch {
		case p.class == ClassPods:
			pods = append(pods, p)
		case p.observer != "":
			first = append(first, p)
		default:
			acquired = append(acquired, p)
		}
	}
	return append(append(first, acquired...), pods...), served
}

var errStop = errors.New("fleetindex: stopped")

// run is one acquisition generation.
func (c *coord) run(ctx context.Context, gen uint64, obs Observed, acq Acquire) {
	plan, served := c.plan(obs)
	var mu sync.Mutex
	dirty := map[string]bool{}
	wake := make(chan struct{}, 1)
	byObserver := map[string]planned{}
	for _, p := range plan {
		if p.observer != "" {
			byObserver[p.observer] = p
		}
	}
	for name, was := range served {
		cancel := obs.WatchKind(name, func(st observe.KindState) {
			if (st.Coverage != observe.Absent) != was {
				c.replan(gen)
				return
			}
			mu.Lock()
			dirty[name] = true
			mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		})
		defer cancel()
	}
	sem := make(chan struct{}, 1) // one list at a time on this cluster
	for _, p := range plan {
		if ctx.Err() != nil {
			return
		}
		if p.observer != "" {
			c.visit(gen, obs, p)
			continue
		}
		first := make(chan struct{})
		c.ix.wg.Add(1)
		go func() {
			defer c.ix.wg.Done()
			c.acquire(ctx, gen, acq, p, sem, first)
		}()
		select {
		case <-first:
		case <-ctx.Done():
			return
		}
	}
	// then each Observer kind is visited on change, at most once per
	// VisitEvery
	last := map[string]time.Time{}
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer:
			timer = nil
		}
		mu.Lock()
		names := make([]string, 0, len(dirty))
		for n := range dirty {
			names = append(names, n)
		}
		mu.Unlock()
		now := time.Now()
		var wait time.Duration
		for _, n := range names {
			if d := last[n].Add(c.ix.s.VisitEvery).Sub(now); d > 0 {
				if wait == 0 || d < wait {
					wait = d
				}
				continue
			}
			mu.Lock()
			delete(dirty, n)
			mu.Unlock()
			last[n] = now
			if p, ok := byObserver[n]; ok {
				c.visit(gen, obs, p)
			}
		}
		if wait > 0 && timer == nil {
			timer = time.After(wait)
		}
	}
}

// visit reads an Observer kind in place (§2.4).
func (c *coord) visit(gen uint64, obs Observed, p planned) {
	key := p.ref.Key()
	mark, ok := c.store.beginRead(gen, key, p.class)
	if !ok {
		return
	}
	gap := c.ix.s.Gaps[p.observer]
	stopped := false
	st, complete, ok := obs.VisitKind(p.observer, func(obj any) bool {
		a, err := meta.Accessor(obj)
		if err != nil {
			return true
		}
		src := Source{UID: string(a.GetUID()), Namespace: a.GetNamespace(), Name: a.GetName(), NoLabels: gap.NoLabels, NoCreated: gap.NoCreated}
		if !gap.NoLabels {
			src.Labels = a.GetLabels()
		}
		if !gap.NoCreated {
			src.Created = a.GetCreationTimestamp().Time
		}
		if c.store.put(gen, key, mark, src) != putApplied {
			stopped = true
			return false
		}
		return true
	})
	switch {
	case !ok || stopped:
	case !st.CountKnown:
		c.store.setStatus(gen, key, p.class, statusOf(st))
	case complete:
		c.store.endRead(gen, key, mark, statusOf(st))
	}
}

// statusOf is an Observer kind's state as the index's.
func statusOf(st observe.KindState) KindStatus {
	switch st.Coverage {
	case observe.Live:
		return KindStatus{State: StateLive}
	case observe.Stale:
		return KindStatus{State: StateStale, AsOf: st.LastGoodAt, Err: st.Err}
	case observe.Absent:
		return KindStatus{State: StateNotServed}
	case observe.Unreadable:
		return KindStatus{State: StateStale, Err: st.Err}
	default:
		return KindStatus{State: StateIndexing}
	}
}

// acquire reads an index-acquired kind for the generation: discovery, a
// list, then a watch, relisting in place and retrying with backoff (§2.5).
// first is closed once its first list has finished, however it ended.
func (c *coord) acquire(ctx context.Context, gen uint64, acq Acquire, p planned, sem chan struct{}, first chan struct{}) {
	once := sync.OnceFunc(func() { close(first) })
	defer once()
	key := p.ref.Key()
	s := c.ix.s
	backoff := s.BackoffMin
	var lastGood *time.Time
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	fail := func(st KindStatus) {
		st.AsOf = lastGood
		c.store.setStatus(gen, key, p.class, st)
	}
	for ctx.Err() == nil {
		version, served, err := acq.Served(ctx, p.ref.Group, p.ref.Resource, p.ref.Version)
		if err != nil {
			fail(KindStatus{State: StateStale, Err: "discovery: " + err.Error()})
			once()
			if !sleep(backoff) {
				return
			}
			backoff = min(2*backoff, s.BackoffMax)
			continue
		}
		if !served {
			fail(KindStatus{State: StateNotServed})
			once()
			if !sleep(s.ReprobeEvery) {
				return
			}
			continue
		}
		gvr := schema.GroupVersionResource{Group: p.ref.Group, Version: version, Resource: p.ref.Resource}
		rv, err := c.list(ctx, gen, acq.Reader, p, gvr, sem)
		once()
		if done := c.settle(gen, p, err, fail); done {
			return
		}
		if err != nil {
			if !sleep(c.retryAfter(err, &backoff)) {
				return
			}
			continue
		}
		now := s.Clock()
		lastGood, backoff = &now, s.BackoffMin
		// follow from the list until the watch must relist
		for ctx.Err() == nil {
			if !c.ix.budget.Admit(c.name, p.class, 0, StreamReserve) {
				c.store.capKind(gen, key)
				return
			}
			next, werr := acq.Reader.Watch(ctx, gvr, rv, s.WatchTimeout, func(ev string, src Source) error {
				if ev == EvDeleted {
					if !c.store.remove(gen, key, src.UID) {
						return errStop
					}
					return nil
				}
				if c.store.put(gen, key, 0, src) != putApplied {
					return errStop
				}
				return nil
			})
			c.ix.budget.Release(c.name, 0, StreamReserve)
			rv = next
			if werr == nil {
				now := s.Clock()
				lastGood, backoff = &now, s.BackoffMin
				continue // the server closed it: resume
			}
			if codeOf(werr) == http.StatusGone {
				break // relist in place
			}
			if done := c.settle(gen, p, werr, fail); done {
				return
			}
			if codeOf(werr) == http.StatusForbidden {
				fail(KindStatus{State: StateWatchDenied, Err: werr.Error()})
			} else {
				fail(KindStatus{State: StateStale, Err: werr.Error()})
			}
			if !sleep(c.retryAfter(werr, &backoff)) {
				return
			}
		}
	}
}

// settle handles an error that ends the kind's acquisition: a stop, a
// cancelled generation, an oversize object, a refused or unsupported
// read. It reports whether acquisition is over.
func (c *coord) settle(gen uint64, p planned, err error, fail func(KindStatus)) bool {
	key := p.ref.Key()
	switch {
	case err == nil:
		return false
	case errors.Is(err, errStop), errors.Is(err, context.Canceled):
		return true
	case errors.Is(err, ErrOversize):
		c.store.freezeOversize(gen, key, err)
		return true
	case codeOf(err) == http.StatusNotAcceptable:
		fail(KindStatus{State: StateUnsupported, Err: err.Error()})
		return true
	}
	return false
}

// retryAfter is the wait before retrying err: a refusal is retried at the
// longest backoff (permissions change rarely), anything else doubles.
func (c *coord) retryAfter(err error, backoff *time.Duration) time.Duration {
	if codeOf(err) == http.StatusForbidden {
		return c.ix.s.BackoffMax
	}
	d := *backoff
	*backoff = min(2**backoff, c.ix.s.BackoffMax)
	return d
}

// list reads a kind completely, page by page, in place; an expired
// continue restarts it under the same mark. It returns the list's
// resourceVersion.
func (c *coord) list(ctx context.Context, gen uint64, rd *Reader, p planned, gvr schema.GroupVersionResource, sem chan struct{}) (string, error) {
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-sem }()
	key := p.ref.Key()
	mark, ok := c.store.beginRead(gen, key, p.class)
	if !ok {
		return "", errStop
	}
	cont := ""
	for {
		if !c.ix.budget.Admit(c.name, p.class, 0, StreamReserve) {
			c.store.capKind(gen, key)
			return "", errStop
		}
		rv, next, err := rd.ListPage(ctx, gvr, cont, func(s Source) error {
			if c.store.put(gen, key, mark, s) != putApplied {
				return errStop
			}
			return nil
		})
		c.ix.budget.Release(c.name, 0, StreamReserve)
		if err != nil {
			if codeOf(err) == http.StatusGone && cont != "" {
				cont = "" // the continue token expired: restart, same mark
				continue
			}
			if codeOf(err) == http.StatusForbidden {
				c.store.setStatus(gen, key, p.class, KindStatus{State: StateDenied, Err: err.Error()})
			} else if codeOf(err) == http.StatusNotFound {
				c.store.setStatus(gen, key, p.class, KindStatus{State: StateNotServed})
			} else if !errors.Is(err, errStop) && !errors.Is(err, ErrOversize) && codeOf(err) != http.StatusNotAcceptable && ctx.Err() == nil {
				c.store.setStatus(gen, key, p.class, KindStatus{State: StateStale, Err: err.Error()})
			}
			return "", err
		}
		if next == "" {
			c.store.endRead(gen, key, mark, KindStatus{State: StateLive})
			return rv, nil
		}
		cont = next
	}
}

// ClusterCoverage is one cluster's index coverage (§3).
type ClusterCoverage struct {
	Cluster     string
	Indexed     bool   // a generation has run: entries may be held
	Connection  string // the connection word
	Incarnation uint64
	Kinds       []KindCoverage // in configuration order
}

// KindCoverage is one kind's state on one cluster.
type KindCoverage struct {
	Key, Kind string
	KindStatus
}

// Coverage lists every cluster in fleet order, then any attached cluster
// the fleet order lacks.
func (ix *Index) Coverage() []ClusterCoverage {
	ix.mu.Lock()
	order := append([]string(nil), ix.order...)
	cs := map[string]*coord{}
	for n, c := range ix.clusters {
		cs[n] = c
	}
	ix.mu.Unlock()
	seen := map[string]bool{}
	var out []ClusterCoverage
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		cc := ClusterCoverage{Cluster: name}
		c := cs[name]
		if c == nil {
			out = append(out, cc)
			return
		}
		c.mu.Lock()
		cc.Connection, cc.Indexed = c.word, c.ran
		if c.obs != nil {
			cc.Incarnation = c.obs.Incarnation()
		}
		c.mu.Unlock()
		if cc.Indexed {
			st := c.store.status()
			for _, ref := range ix.s.Kinds {
				ks, ok := st[ref.Key()]
				if !ok {
					ks = KindStatus{State: StateIndexing}
				}
				cc.Kinds = append(cc.Kinds, KindCoverage{Key: ref.Key(), Kind: ref.Kind, KindStatus: ks})
			}
		}
		out = append(out, cc)
	}
	for _, n := range order {
		add(n)
	}
	var rest []string
	for n := range cs {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	for _, n := range rest {
		add(n)
	}
	return out
}

// Used reports a cluster's and the fleet's admitted entries and bytes.
func (ix *Index) Used(cluster string) (clusterEntries int, clusterBytes int64, fleetEntries int, fleetBytes int64) {
	return ix.budget.Used(cluster)
}
