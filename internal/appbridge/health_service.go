package appbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// HealthSummaryEvent carries one cluster's complete summary (1c addendum §4).
// The frontend subscribes before bootstrapping with Summaries and keeps a
// summary only when its (Incarnation, Seq) is newer than the one held.
const HealthSummaryEvent = "health:summary"

// HealthSink publishes Observer output as Wails events.
type HealthSink struct {
	em      Emitter
	lineage *rowsLineage
	trace   *rowsTrace  // nil unless KLYX_ROWS_TRACE names a file (the 1c §6a lab gate)
	log     *obslog.Log // the observation log every Observer's transition stream feeds (The Line slice 2b)
}

func NewHealthSink(em Emitter) *HealthSink {
	h := &HealthSink{em: em, lineage: newRowsLineage(), trace: openRowsTrace(os.Getenv("KLYX_ROWS_TRACE")), log: obslog.New()}
	if h.trace != nil {
		startLabNoise(em) // lab-only, and only when asked for
	}
	return h
}

func (h *HealthSink) Summary(s observe.Summary) {
	h.lineage.summary(s)
	h.em.Emit(HealthSummaryEvent, s)
}

// Transitions feed the observation log (The Line slice 2b §6): each batch
// is applied whole, at its commit, on the Observer's loop goroutine. Nothing
// is emitted to the webview yet.
func (h *HealthSink) Transitions(b observe.TransitionBatch) { h.log.Apply(b) }

// ObservationLog is the log the sink feeds.
func (h *HealthSink) ObservationLog() *obslog.Log { return h.log }

// Rows go to one event per part, as the lens's DTOs (slice 3 §2): a part
// no lens reads yet has no converter and is not emitted. A delta part with
// a subscribed lineage is sent as a delta against it (addendum §3.3).
func (h *HealthSink) Rows(r observe.Rows) {
	dto, ok := toRowsDTO(r)
	if !ok {
		return
	}
	msg, emit := h.lineage.publish(dto)
	if !emit {
		return
	}
	if h.trace != nil {
		msg.EmittedAtMs = time.Now().UnixMilli()
		h.trace.record(msg)
	}
	h.em.Emit(HealthRowsEventPrefix+r.Cluster+":"+r.Task+"/"+r.Part.ID, msg)
}

// LinkRowsLineage makes the service's bootstraps and subscriptions share the
// sink's lineage (addendum §3.3). Unlinked, no lineage is retained, so the
// sink sends every part in full and bootstraps capture afresh.
func LinkRowsLineage(svc *HealthService, sink *HealthSink) { svc.lineage = sink.lineage }

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
	rows := dto.Rows
	if !dto.Full {
		rows = dto.Upserts
	}
	if v := reflect.ValueOf(rows); v.Kind() == reflect.Slice {
		n = v.Len()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = fmt.Fprintf(t.f, "%d %s/%s bytes=%d rows=%d full=%t removed=%d\n", time.Now().UnixMilli(), dto.Task, dto.Part.ID, len(b), n, dto.Full, len(dto.Removed))
}

// HealthService is the bootstrap side of the health summary.
type HealthService struct {
	observers func() []*observe.Observer
	rows      *rowsRegistry
	lineage   *rowsLineage // the sink's, once linked (LinkRowsLineage)
}

func NewHealthService(observers func() []*observe.Observer) *HealthService {
	return &HealthService{observers: observers, rows: newRowsRegistry()}
}

// LabReport appends the §6a webview probe's report beside the rows trace
// (KLYX_ROWS_TRACE + ".probe"), so a lab run reads the webview's numbers
// without the screen. A no-op unless the lab trace is on.
func (s *HealthService) LabReport(text string) {
	path := os.Getenv("KLYX_ROWS_TRACE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path+".probe", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "--- %s\n%s\n", time.Now().Format(time.RFC3339), text)
	_ = f.Close()
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
