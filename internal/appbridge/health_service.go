package appbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// HealthSummaryEvent carries one cluster's complete summary (1c addendum §4).
// The frontend subscribes before bootstrapping with Summaries and keeps a
// summary only when its (Incarnation, Seq) is newer than the one held.
const HealthSummaryEvent = "health:summary"

// HealthSink publishes Observer output as Wails events.
type HealthSink struct {
	em    Emitter
	trace *rowsTrace // nil unless KLYX_ROWS_TRACE names a file (the 1c §6a lab gate)
}

func NewHealthSink(em Emitter) *HealthSink {
	return &HealthSink{em: em, trace: openRowsTrace(os.Getenv("KLYX_ROWS_TRACE"))}
}

func (h *HealthSink) Summary(s observe.Summary) { h.em.Emit(HealthSummaryEvent, s) }

// Rows go to one event per part, as the lens's DTOs (slice 3 §2): a part
// no lens reads yet has no converter and is not emitted.
func (h *HealthSink) Rows(r observe.Rows) {
	if dto, ok := toRowsDTO(r); ok {
		h.trace.record(dto)
		h.em.Emit(HealthRowsEventPrefix+r.Cluster+":"+r.Task+"/"+r.Part.ID, dto)
	}
}

// rowsTrace records each rows push's JSON size for the slice 3 §6a webview
// gate (payload: at most one push per part per second, each pods push at
// most 4 MB). Lab-only: off unless KLYX_ROWS_TRACE names a file; it costs a
// marshal per push when on.
type rowsTrace struct {
	mu sync.Mutex
	f  *os.File
}

func openRowsTrace(path string) *rowsTrace {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return &rowsTrace{f: f}
}

func (t *rowsTrace) record(dto RowsDTO) {
	if t == nil {
		return
	}
	b, err := json.Marshal(dto)
	if err != nil {
		return
	}
	n := 0
	if v := reflect.ValueOf(dto.Rows); v.Kind() == reflect.Slice {
		n = v.Len()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = fmt.Fprintf(t.f, "%d %s/%s bytes=%d rows=%d\n", time.Now().UnixMilli(), dto.Task, dto.Part.ID, len(b), n)
}

// HealthService is the bootstrap side of the health summary.
type HealthService struct {
	observers func() []*observe.Observer
	rows      *rowsRegistry
}

func NewHealthService(observers func() []*observe.Observer) *HealthService {
	return &HealthService{observers: observers, rows: newRowsRegistry()}
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
