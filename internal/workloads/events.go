package workloads

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// EventSummary is one cluster event for the events lens.
type EventSummary struct {
	Type      string // "Normal" | "Warning"
	Reason    string
	Message   string
	Count     int32
	Namespace string
	// Involved object identity for cross-linking:
	Kind, Name string
	// UID is the involved object's UID: a same-name replacement's events
	// are told apart from the current object's by it.
	UID string
	// LastSeenUnix for sorting/display (unix seconds; 0 if unknown)
	LastSeenUnix  int64
	FirstSeenUnix int64
}

// SummarizeEvents converts core v1 events to the lens rows, warning-first then
// most-recent-first. Handles both legacy count/lastTimestamp and the newer
// series/eventTime fields honestly (prefer series.lastObservedTime/count when
// set, else lastTimestamp/count, else eventTime; never invent a timestamp - 0
// means unknown).
func SummarizeEvents(events []corev1.Event) []EventSummary {
	out := make([]EventSummary, 0, len(events))
	for i := range events {
		e := &events[i]
		out = append(out, EventSummary{
			Type:          e.Type,
			Reason:        e.Reason,
			Message:       e.Message,
			Count:         eventCount(e),
			Namespace:     e.Namespace,
			Kind:          e.InvolvedObject.Kind,
			Name:          e.InvolvedObject.Name,
			UID:           string(e.InvolvedObject.UID),
			LastSeenUnix:  eventLastSeen(e),
			FirstSeenUnix: eventFirstSeen(e),
		})
	}
	// Warning before Normal; within same type most-recent first;
	// then Namespace/Name asc for a stable ordering.
	sort.SliceStable(out, func(a, b int) bool {
		ta, tb := typeOrder(out[a].Type), typeOrder(out[b].Type)
		if ta != tb {
			return ta < tb
		}
		if out[a].LastSeenUnix != out[b].LastSeenUnix {
			return out[a].LastSeenUnix > out[b].LastSeenUnix
		}
		if out[a].Namespace != out[b].Namespace {
			return out[a].Namespace < out[b].Namespace
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// typeOrder maps event type to a sort key (Warning=0 sorts before Normal=1).
func typeOrder(t string) int {
	if t == "Warning" {
		return 0
	}
	return 1
}

// eventCount returns the best-effort count for an event.
// Prefer series.count (newer API) when set, else e.Count when >0, else 1.
func eventCount(e *corev1.Event) int32 {
	if e.Series != nil && e.Series.Count > 0 {
		return e.Series.Count
	}
	if e.Count > 0 {
		return e.Count
	}
	return 1
}

// eventLastSeen returns the best unix-second timestamp for when the event was
// last observed. Preference: series.lastObservedTime > lastTimestamp > eventTime.
// Returns 0 when none is set (unknown).
func eventLastSeen(e *corev1.Event) int64 {
	if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		return e.Series.LastObservedTime.Unix()
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Unix()
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Unix()
	}
	return 0
}

// eventFirstSeen returns the best unix-second timestamp for when the event was
// first observed. Uses firstTimestamp when set, else eventTime, else 0.
func eventFirstSeen(e *corev1.Event) int64 {
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Unix()
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Unix()
	}
	return 0
}
