package appbridge

import (
	"context"
	"github.com/moomora/klyx/internal/urlscrub"
	"sort"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/workloads"
)

const eventsTimeout = 30 * time.Second

// EventsConn is the per-cluster read surface EventsService needs.
type EventsConn interface {
	ListEvents(ctx context.Context, namespace string) (events []workloads.EventSummary, truncated bool, err error)
	WatchDirty(ctx context.Context, namespace string, kinds []string, onDirty func(), onLive func(bool)) (stop func(), err error)
}

// EventsService is bound to JS. ListEvents returns warning-first event rows for
// a cluster; OpenLiveEvents/CloseLiveEvents drive a watch-backed live
// subscription that emits liveEvents:<cluster>:<ns> on change.
type EventsService struct {
	lookup func(string) (EventsConn, bool) // one-shot reads
	lease  leaser                          // a live subscription's lease (Settings S2 §2.3)
	em     Emitter
	live   *liveRegistry

	lmu    sync.Mutex
	leases map[string]*liveEntry // by subscription key
}

// NewEventsService creates an EventsService with the given cluster-lookup
// function and event emitter (for the live-list subscription).
func NewEventsService(lookup func(string) (EventsConn, bool), em Emitter) *EventsService {
	return &EventsService{lookup: lookup, lease: plainLeaser(lookup), em: em, live: newLiveRegistry(), leases: map[string]*liveEntry{}}
}

func (s *EventsService) setLeaser(l leaser) { s.lease = l }

// keep records key's lease, releasing the one it replaces.
// liveEntry is one live subscription and the lease it runs on, made before
// the lease is taken: its End finds the subscription by this identity,
// never by key, so an old connection's end never touches a replacement
// opened on the same key (Codex's review of S2a, F1-F2). Guarded by lmu.
type liveEntry struct {
	le    *Lease
	sub   *liveSub
	ended string // set when the connection ended it, before or after it opened
}

func (s *EventsService) keep(key string, e *liveEntry) {
	s.lmu.Lock()
	old := s.leases[key]
	s.leases[key] = e
	s.lmu.Unlock()
	if old != nil && old != e {
		old.le.Release()
	}
}

// drop releases key's entry, when it is still e (nil: whichever it is).
func (s *EventsService) drop(key string, e *liveEntry) bool {
	s.lmu.Lock()
	cur := s.leases[key]
	if cur == nil || (e != nil && cur != e) {
		s.lmu.Unlock()
		return false
	}
	delete(s.leases, key)
	s.lmu.Unlock()
	cur.le.Release()
	return true
}

// bind records e's subscription, and says why it must not run: its
// connection ended it before the subscription was recorded. Then e is
// dropped, and the caller stops the subscription.
func (s *EventsService) bind(key string, e *liveEntry, sub *liveSub) string {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	e.sub = sub
	if e.ended != "" && s.leases[key] == e {
		delete(s.leases, key)
	}
	return e.ended
}

// endLive ends e because its connection was retired (S2 §2.3): its own
// subscription is detached and the page told why, under the registry's
// lock, so the notice never lands after a replacement opened; the watch
// stops off the lifecycle's path. Not yet subscribed, e is marked, and bind
// stops it.
func (s *EventsService) endLive(key string, e *liveEntry, liveEvent, why string) {
	s.lmu.Lock()
	e.ended = why
	sub := e.sub
	s.lmu.Unlock()
	s.drop(key, e)
	if s.live.detach(key, sub, func() { s.em.Emit(liveEvent, liveStatusDTO{Live: false, Ended: why}) }) {
		go s.live.stopSub(sub)
	}
}

// ListEvents returns warning-first event rows for a cluster, scoped to namespace
// ("" = all). Namespaces is the sorted distinct set of event namespaces,
// populated ONLY on the all-namespaces load (dropdown source). Cluster miss
// returns non-nil empties (never panics on null).
func (s *EventsService) ListEvents(cluster, namespace string) EventsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return EventsResultDTO{Namespaces: []string{}, Events: []EventRowDTO{}, Error: "cluster not connected: " + cluster}
	}
	out, _ := computeEvents(conn, namespace)
	return out
}

// computeEvents lists events on conn and builds the EventsResultDTO with the
// same namespace-set and mapping rules ListEvents uses. ok=false means the list
// failed (returns non-nil empties); the live runner uses ok to gate
// emit/liveness.
func computeEvents(conn EventsConn, namespace string) (EventsResultDTO, bool) {
	out := EventsResultDTO{Namespaces: []string{}, Events: []EventRowDTO{}}
	ctx, cancel := context.WithTimeout(context.Background(), eventsTimeout)
	defer cancel()

	events, truncated, err := conn.ListEvents(ctx, namespace)
	if err != nil {
		out.Error = err.Error()
		return out, false
	}
	out.Truncated = truncated

	nsSet := map[string]bool{}
	for _, e := range events {
		nsSet[e.Namespace] = true
		out.Events = append(out.Events, toEventRowDTO(e))
	}
	if namespace == "" {
		for ns := range nsSet {
			out.Namespaces = append(out.Namespaces, ns)
		}
		sort.Strings(out.Namespaces)
	}
	return out, true
}

// OpenLiveEvents starts (or replaces) a watch-backed live subscription for the
// cluster+namespace. It emits liveEvents:<cluster>:<ns> (EventsResultDTO) on
// each debounced change and liveEventsStatus:... ({live:bool}) on liveness
// edges. Cluster miss returns an error.
func (s *EventsService) OpenLiveEvents(cluster, namespace string) ActionResultDTO {
	key := "events:" + cluster + ":" + namespace
	dataEvent := "liveEvents:" + cluster + ":" + namespace
	liveEvent := "liveEventsStatus:" + cluster + ":" + namespace
	where := "every namespace"
	if namespace != "" {
		where = namespace
	}
	e := &liveEntry{}
	le, err := s.lease(cluster, Work{Kind: "events", What: "live events in " + where + ", opening",
		End: func(why string) { s.endLive(key, e, liveEvent, why) }})
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := le.Conn.(EventsConn)
	if !ok {
		le.Release()
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	e.le = le
	s.keep(key, e)

	sub := s.live.open(key,
		func(onDirty func(), onLive func(bool)) (func(), error) {
			return conn.WatchDirty(le.Ctx, namespace, []string{"events"}, onDirty, onLive)
		},
		func() (any, bool) { return computeEvents(conn, namespace) },
		func(payload any) { s.em.Emit(dataEvent, payload) },
		func(live bool) { s.em.Emit(liveEvent, liveStatusDTO{Live: live}) },
	)
	if why := s.bind(key, e, sub); why != "" {
		le.Release()
		if s.live.detach(key, sub, nil) {
			s.live.stopSub(sub)
		}
		return ActionResultDTO{Error: "the live events ended: " + why}
	}
	if err := le.Settle("live events in " + where); err != nil {
		// retired meanwhile: this subscription, never a replacement
		if s.drop(key, e) && s.live.detach(key, sub, nil) {
			s.live.stopSub(sub)
		}
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// CloseLiveEvents stops the live subscription. Idempotent.
func (s *EventsService) CloseLiveEvents(cluster, namespace string) {
	key := "events:" + cluster + ":" + namespace
	s.live.close(key)
	s.drop(key, nil)
}

// CloseAll stops every live events subscription. Called on app shutdown.
func (s *EventsService) CloseAll() {
	s.live.closeAll()
	s.lmu.Lock()
	ls := s.leases
	s.leases = map[string]*liveEntry{}
	s.lmu.Unlock()
	for _, e := range ls {
		e.le.Release()
	}
}

func toEventRowDTO(e workloads.EventSummary) EventRowDTO {
	return EventRowDTO{
		Type:          e.Type,
		Reason:        e.Reason,
		Message:       urlscrub.Text(e.Message).Text,
		Count:         e.Count,
		Namespace:     e.Namespace,
		Kind:          e.Kind,
		Name:          e.Name,
		LastSeenUnix:  e.LastSeenUnix,
		FirstSeenUnix: e.FirstSeenUnix,
	}
}
