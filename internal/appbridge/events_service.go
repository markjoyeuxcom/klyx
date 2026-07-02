package appbridge

import (
	"context"
	"sort"
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
	lookup func(string) (EventsConn, bool)
	em     Emitter
	live   *liveRegistry
}

// NewEventsService creates an EventsService with the given cluster-lookup
// function and event emitter (for the live-list subscription).
func NewEventsService(lookup func(string) (EventsConn, bool), em Emitter) *EventsService {
	return &EventsService{lookup: lookup, em: em, live: newLiveRegistry()}
}

// ListEvents returns warning-first event rows for a cluster, scoped to namespace
// ("" = all). Namespaces is the sorted distinct set of event namespaces,
// populated ONLY on the all-namespaces load (dropdown source). Cluster miss
// returns non-nil empties (never panics on null).
func (s *EventsService) ListEvents(cluster, namespace string) EventsResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return EventsResultDTO{Namespaces: []string{}, Events: []EventRowDTO{}}
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
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	key := "events:" + cluster + ":" + namespace
	dataEvent := "liveEvents:" + cluster + ":" + namespace
	liveEvent := "liveEventsStatus:" + cluster + ":" + namespace

	s.live.open(key,
		func(onDirty func(), onLive func(bool)) (func(), error) {
			return conn.WatchDirty(context.Background(), namespace, []string{"events"}, onDirty, onLive)
		},
		func() (any, bool) { return computeEvents(conn, namespace) },
		func(payload any) { s.em.Emit(dataEvent, payload) },
		func(live bool) { s.em.Emit(liveEvent, liveStatusDTO{Live: live}) },
	)
	return ActionResultDTO{OK: true}
}

// CloseLiveEvents stops the live subscription. Idempotent.
func (s *EventsService) CloseLiveEvents(cluster, namespace string) {
	s.live.close("events:" + cluster + ":" + namespace)
}

// CloseAll stops every live events subscription. Called on app shutdown.
func (s *EventsService) CloseAll() { s.live.closeAll() }

func toEventRowDTO(e workloads.EventSummary) EventRowDTO {
	return EventRowDTO{
		Type:          e.Type,
		Reason:        e.Reason,
		Message:       e.Message,
		Count:         e.Count,
		Namespace:     e.Namespace,
		Kind:          e.Kind,
		Name:          e.Name,
		LastSeenUnix:  e.LastSeenUnix,
		FirstSeenUnix: e.FirstSeenUnix,
	}
}
