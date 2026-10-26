package appbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/envread"
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

	lmu       sync.RWMutex
	listeners []HealthListener

	// The identity gate (Settings S2 §2.4): current says whether a
	// publication's (cluster, incarnation) is the cluster's current
	// connection; nil passes every one (tools, tests). Each cluster's
	// publications are checked and delivered under its own handoff lock, so
	// none passes between "current" and its delivery, and a trusted end
	// waits for a delivery already under way.
	gmu     sync.Mutex
	current func(cluster string, inc uint64) bool
	idents  map[string]*sinkIdent
}

// sinkIdent is one cluster's handoff: its lock, the incarnation it last
// delivered and that one's last sequences, and the incarnations ended.
type sinkIdent struct {
	mu         sync.Mutex
	inc        uint64
	batchSeq   uint64
	summarySeq uint64
	ended      map[uint64]bool
}

// SetIdentityCheck installs the identity gate.
func (h *HealthSink) SetIdentityCheck(current func(cluster string, inc uint64) bool) {
	h.gmu.Lock()
	defer h.gmu.Unlock()
	h.current = current
}

func (h *HealthSink) ident(cluster string) (*sinkIdent, func(string, uint64) bool) {
	h.gmu.Lock()
	defer h.gmu.Unlock()
	if h.idents == nil {
		h.idents = map[string]*sinkIdent{}
	}
	id := h.idents[cluster]
	if id == nil {
		id = &sinkIdent{ended: map[uint64]bool{}}
		h.idents[cluster] = id
	}
	return id, h.current
}

// admit runs deliver when (cluster, inc) may publish, under the cluster's
// handoff lock: never for an ended or retired incarnation.
func (h *HealthSink) admit(cluster string, inc uint64, deliver func(*sinkIdent)) {
	id, current := h.ident(cluster)
	id.mu.Lock()
	defer id.mu.Unlock()
	if id.ended[inc] || (current != nil && !current(cluster, inc)) {
		return
	}
	if inc != id.inc {
		id.inc, id.batchSeq, id.summarySeq = inc, 0, 0
	}
	deliver(id)
}

// EndIdentity is the trusted end of a retired incarnation (Settings S2
// §2.4): the lifecycle's, never left to the retired Observer. It delivers
// what an Observer's own end does - its end batch, then its Final summary -
// to the log, the listeners and the webview, after any delivery already
// under way, and only once; whatever that incarnation publishes later is
// dropped. Listeners act on their generation of inc only.
func (h *HealthSink) EndIdentity(cluster string, inc uint64, connection string) {
	id, _ := h.ident(cluster)
	id.mu.Lock()
	defer id.mu.Unlock()
	if id.ended[inc] {
		return
	}
	id.ended[inc] = true
	bseq, sseq := uint64(0), uint64(0)
	if id.inc == inc {
		bseq, sseq = id.batchSeq, id.summarySeq
	}
	now := time.Now()
	b := observe.TransitionBatch{Cluster: cluster, Incarnation: inc, Seq: bseq + 1, Kind: observe.BatchEnd, At: now}
	h.log.Apply(b)
	h.each(func(l HealthListener) { l.OnTransitions(b) })
	sum := observe.Summary{Cluster: cluster, Incarnation: inc, Seq: sseq + 1, EvaluatedAt: now, Connection: connection, Final: true}
	h.lineage.summary(sum)
	h.em.Emit(HealthSummaryEvent, sum)
	h.each(func(l HealthListener) { l.OnSummary(sum) })
}

// clusterForgetter is a listener that keeps per-cluster state a removal
// drops (Settings S2 §2.5).
type clusterForgetter interface{ ForgetCluster(cluster string) }

// ForgetCluster drops a removed cluster from the log, the lineage and every
// listener that keeps it. Its ended incarnations stay ended, so a late
// publication from them is still dropped and recreates nothing.
func (h *HealthSink) ForgetCluster(cluster string) {
	h.log.Forget(cluster)
	h.lineage.forget(cluster)
	h.each(func(l HealthListener) {
		if f, ok := l.(clusterForgetter); ok {
			f.ForgetCluster(cluster)
		}
	})
}

// HealthListener receives every Observer publication in Go, before the
// webview conversion (The Line slice 3 note §2.1). Its methods run on the
// publishing Observer's loop goroutine: they must only record and signal -
// never subscribe, bootstrap (RowsNow waits on that same loop), compute or
// block.
type HealthListener interface {
	OnSummary(observe.Summary)
	OnRows(observe.Rows)
	OnTransitions(observe.TransitionBatch)
}

// AddListener registers a Go listener for every later publication.
func (h *HealthSink) AddListener(l HealthListener) {
	h.lmu.Lock()
	defer h.lmu.Unlock()
	h.listeners = append(h.listeners, l)
}

func (h *HealthSink) each(f func(HealthListener)) {
	h.lmu.RLock()
	ls := h.listeners
	h.lmu.RUnlock()
	for _, l := range ls {
		f(l)
	}
}

func NewHealthSink(em Emitter) *HealthSink {
	h := &HealthSink{em: em, lineage: newRowsLineage(), trace: openRowsTrace(envread.Get(envread.KlyxRowsTrace)), log: obslog.New()}
	if h.trace != nil {
		startLabNoise(em) // lab-only, and only when asked for
	}
	return h
}

func (h *HealthSink) Summary(s observe.Summary) {
	h.admit(s.Cluster, s.Incarnation, func(id *sinkIdent) {
		id.summarySeq = max(id.summarySeq, s.Seq)
		h.lineage.summary(s)
		h.em.Emit(HealthSummaryEvent, s)
		h.each(func(l HealthListener) { l.OnSummary(s) })
	})
}

// Transitions feed the observation log (The Line slice 2b §6): each batch
// is applied whole, at its commit, on the Observer's loop goroutine. Nothing
// is emitted to the webview yet.
func (h *HealthSink) Transitions(b observe.TransitionBatch) {
	h.admit(b.Cluster, b.Incarnation, func(id *sinkIdent) {
		id.batchSeq = max(id.batchSeq, b.Seq)
		h.log.Apply(b)
		h.each(func(l HealthListener) { l.OnTransitions(b) })
	})
}

// ObservationLog is the log the sink feeds.
func (h *HealthSink) ObservationLog() *obslog.Log { return h.log }

// Rows go to one event per part, as the lens's DTOs (slice 3 §2): a part
// no lens reads yet has no converter and is not emitted. A delta part with
// a subscribed lineage is sent as a delta against it (addendum §3.3).
func (h *HealthSink) Rows(r observe.Rows) {
	h.admit(r.Cluster, r.Incarnation, func(*sinkIdent) { h.rows(r) })
}

func (h *HealthSink) rows(r observe.Rows) {
	h.each(func(l HealthListener) { l.OnRows(r) }) // raw rows, before any webview conversion or delta
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
	path := envread.Get(envread.KlyxRowsTrace)
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
