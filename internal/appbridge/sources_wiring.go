package appbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/fleet"
)

// AlertHostLeaser admits a bucket's read on its host through S2's
// admission (Settings S3a §13): a lease on the host's current connection,
// listed under the host's plans, whose context ends when that connection
// is retired. A host that is reserved, held or retiring admits nothing.
func AlertHostLeaser(l *Lifecycle) fleet.HostLeaser {
	return func(host, what string) (fleet.Conn, context.Context, func(), error) {
		le, err := l.Acquire(host, Work{Kind: "alerts", What: what})
		if err != nil {
			return nil, nil, nil, err
		}
		c, _ := le.Conn.(fleet.Conn)
		return c, le.Ctx, le.Release, nil
	}
}

// alertLineageCheck is why alerts read under a lineage may no longer be
// sent, "" while its terms hold (Settings S3a §4.3). Stored once at
// startup by main, as the guard is: a plain function, never a bound
// service's method the webview could call.
var alertLineageCheck atomic.Pointer[func(cluster string, l alertmanager.Lineage) string]

// SetAlertLineageCheck stores the check (main: the registry's
// LineageProblem).
func SetAlertLineageCheck(f func(cluster string, l alertmanager.Lineage) string) {
	alertLineageCheck.Store(&f)
}

func lineageProblem(cluster string, l alertmanager.Lineage) string {
	if f := alertLineageCheck.Load(); f != nil {
		return (*f)(cluster, l)
	}
	return ""
}

// alertTerms closes the admission of alert dependencies across an
// Observability Apply (Settings S3a §5; Codex's review of S3a-4a, R6): a
// lineage is checked and recorded under its read lock, and an Apply holds
// its write lock from its final work list until its publication. A read
// made meanwhile records its lineage after the publication, and is then
// refused when the terms it was read under moved: no work holding the old
// terms starts unnamed.
var alertTerms sync.RWMutex

// lineageSet is the alert lineages one call's evidence was read under: its
// fence checks them before every later read and at the send (S3a §4.3).
// Each is named, while its call runs, in an Observability plan's work.
type lineageSet struct {
	id      uint64
	cluster string
	started time.Time
	mu      sync.Mutex
	list    []alertmanager.Lineage
}

// add keeps a read's lineage, or refuses rows whose terms already moved:
// they are never used, so the call is not ended over them.
func (s *lineageSet) add(l alertmanager.Lineage) error {
	if l == (alertmanager.Lineage{}) {
		return nil
	}
	alertTerms.RLock()
	defer alertTerms.RUnlock()
	if p := lineageProblem(s.cluster, l); p != "" {
		return errors.New(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.list, l) {
		s.list = append(s.list, l)
	}
	return nil
}

// explainCalls are the calls fenced by their lineages, while they run.
var explainCalls = struct {
	mu   sync.Mutex
	seq  uint64
	sets map[uint64]*lineageSet
}{sets: map[uint64]*lineageSet{}}

// explainAlertWork is the running calls holding alerts read on clusters,
// by identity (R6): their next send is refused once the terms move.
func explainAlertWork(in map[string]bool) []PlanWorkDTO {
	explainCalls.mu.Lock()
	defer explainCalls.mu.Unlock()
	var out []PlanWorkDTO
	for _, set := range explainCalls.sets {
		set.mu.Lock()
		held := len(set.list) > 0
		set.mu.Unlock()
		if !held || !in[set.cluster] {
			continue
		}
		out = append(out, PlanWorkDTO{ID: fmt.Sprintf("explain:%d", set.id), Kind: "explain", Cluster: set.cluster, Started: set.started.UTC().Format(time.RFC3339),
			What: "an Explain on " + set.cluster + " holding alerts read under the old terms: its send is refused"})
	}
	return out
}

func (s *lineageSet) problem(cluster string) error {
	s.mu.Lock()
	list := append([]alertmanager.Lineage(nil), s.list...)
	s.mu.Unlock()
	for _, l := range list {
		if p := lineageProblem(cluster, l); p != "" {
			return errors.New(p)
		}
	}
	return nil
}

// withLineages fences a call's evidence (Explain): the fence, then every
// lineage its reads recorded through the context. done ends the call's
// place in Observability plans' work.
func withLineages(ctx context.Context, cluster string, fence func() error) (context.Context, func() error, func()) {
	set := &lineageSet{cluster: cluster, started: time.Now()}
	explainCalls.mu.Lock()
	explainCalls.seq++
	set.id = explainCalls.seq
	explainCalls.sets[set.id] = set
	explainCalls.mu.Unlock()
	done := func() {
		explainCalls.mu.Lock()
		delete(explainCalls.sets, set.id)
		explainCalls.mu.Unlock()
	}
	return context.WithValue(ctx, lineageRecorderKey{}, set.add), func() error {
		if err := fence(); err != nil {
			return err
		}
		return set.problem(cluster)
	}, done
}

type lineageRecorderKey struct{}

// recordLineage records an alerts read's lineage with the call whose
// context carries a recorder, nothing otherwise. An error is that the
// rows were read under terms that have since moved: they are not used.
func recordLineage(ctx context.Context, l alertmanager.Lineage) error {
	if rec, ok := ctx.Value(lineageRecorderKey{}).(func(alertmanager.Lineage) error); ok {
		return rec(l)
	}
	return nil
}
