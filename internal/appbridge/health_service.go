package appbridge

import "github.com/moomora/klyx/internal/observe"

// HealthSummaryEvent carries one cluster's complete summary (1c addendum §4).
// The frontend subscribes before bootstrapping with Summaries and keeps a
// summary only when its (Incarnation, Seq) is newer than the one held.
const HealthSummaryEvent = "health:summary"

// HealthSink publishes Observer output as Wails events.
type HealthSink struct{ em Emitter }

func NewHealthSink(em Emitter) *HealthSink { return &HealthSink{em: em} }

func (h *HealthSink) Summary(s observe.Summary) { h.em.Emit(HealthSummaryEvent, s) }

// Rows go to one event per part. Nothing subscribes until the lenses move
// onto the shared observation (slice 1c-3).
func (h *HealthSink) Rows(r observe.Rows) {
	h.em.Emit("health:rows:"+r.Cluster+":"+r.Task+"/"+r.Part.ID, r)
}

// HealthService is the bootstrap side of the health summary.
type HealthService struct {
	observers func() []*observe.Observer
}

func NewHealthService(observers func() []*observe.Observer) *HealthService {
	return &HealthService{observers: observers}
}

// Summaries returns the latest summary of every running cluster Observer.
// A finished Observer (its Final published) is omitted.
func (s *HealthService) Summaries() []observe.Summary {
	var out []observe.Summary
	for _, o := range s.observers() {
		if sum, ok := o.Latest(); ok && !sum.Final {
			out = append(out, sum)
		}
	}
	return out
}
