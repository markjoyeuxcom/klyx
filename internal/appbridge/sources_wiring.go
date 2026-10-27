package appbridge

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

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

// lineageSet is the alert lineages one call's evidence was read under: its
// fence checks them before every later read and at the send (S3a §4.3).
type lineageSet struct {
	cluster string
	mu      sync.Mutex
	list    []alertmanager.Lineage
}

// add keeps a read's lineage, or refuses rows whose terms already moved:
// they are never used, so the call is not ended over them.
func (s *lineageSet) add(l alertmanager.Lineage) error {
	if l == (alertmanager.Lineage{}) {
		return nil
	}
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

// withLineages fences a call's evidence (Explain, Suggest-fix): the fence,
// then every lineage its reads recorded through the context.
func withLineages(ctx context.Context, cluster string, fence func() error) (context.Context, func() error) {
	set := &lineageSet{cluster: cluster}
	return context.WithValue(ctx, lineageRecorderKey{}, set.add), func() error {
		if err := fence(); err != nil {
			return err
		}
		return set.problem(cluster)
	}
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
