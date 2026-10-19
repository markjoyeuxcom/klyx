package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/loki"
	"github.com/moomora/klyx/internal/safehttp"
)

// Coverage S8b: pod log history from the cluster's Loki, on demand. A view
// (the logs pane in History mode) reads page sets: the first page decides
// the mode, which is then locked with the branches, the absolute range, the
// source's lineage key and revision, the pod's UID and the container (§4);
// Load older keeps them; Restart (a new range, container, mode, or Refresh)
// starts a new page set from the newest.

const (
	historyViewsMax  = 16
	historyIdle      = 15 * time.Minute
	historyPageLimit = 30 * time.Second
	historyLineMax   = 64 << 10 // bytes of a line shown (§5)
	historyMaxRange  = 7 * 24 * time.Hour
)

// HistoryReader is one resolved Loki source (fleet.LokiReader).
type HistoryReader interface {
	LineageKey() string
	Revision() uint64
	Source() string
	Tenant() string
	Match() map[string]string
	Redactor() safehttp.Redactor
	Current() bool
	Slot(ctx context.Context) (func(), error)
	Labels(ctx context.Context, start, end time.Time, query string) ([]string, bool, error)
	Query(ctx context.Context, query string, start, end int64, limit int) ([]loki.Entry, error)
}

// PodHistoryRequestDTO asks for one page of a pod's history.
type PodHistoryRequestDTO struct {
	Cluster   string `json:"cluster"`
	View      string `json:"view"` // the view's id, chosen by the frontend
	Restart   bool   `json:"restart"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	UID       string `json:"uid"`
	Container string `json:"container,omitempty"`
	// CreatedUnix is the pod's created time (0: unknown); Gone, that it is
	// removed (the name bound's end is then open).
	CreatedUnix  int64  `json:"createdUnix,omitempty"`
	Gone         bool   `json:"gone,omitempty"`
	RangeSeconds int64  `json:"rangeSeconds,omitempty"` // 0: since created (clipped to 7 days), else 24 hours
	Mode         string `json:"mode,omitempty"`         // auto | name | uid
	Skip         []int  `json:"skip,omitempty"`         // stopped branches to move past (explicit)
}

// LogHistoryLineDTO is one line, a display value.
type LogHistoryLineDTO struct {
	TS        string `json:"ts"` // RFC 3339, nanoseconds
	Line      string `json:"line"`
	Container string `json:"container,omitempty"`
	Branch    int    `json:"branch"`
	Clipped   bool   `json:"clipped,omitempty"`
}

// LogHistoryMarkerDTO is a branch stopped at a saturated boundary.
type LogHistoryMarkerDTO struct {
	Branch  int    `json:"branch"`
	TS      string `json:"ts"`
	Count   int    `json:"count"`
	Refused string `json:"refused,omitempty"`
}

// LogHistoryPageDTO is one page and what Klyx says about it.
type LogHistoryPageDTO struct {
	View      string                `json:"view"`
	Lines     []LogHistoryLineDTO   `json:"lines"` // newest first
	Selectors []string              `json:"selectors"`
	Copyable  bool                  `json:"copyable"` // every selector shown as sent
	Mode      string                `json:"mode"`     // uid | name
	UIDOffer  bool                  `json:"uidOffer"` // name mode, and every first-page entry carried a UID
	Strength  string                `json:"strength"`
	Source    string                `json:"source"`
	Tenant    string                `json:"tenant,omitempty"`
	Start     string                `json:"start"`
	End       string                `json:"end"`
	Done      bool                  `json:"done"` // the start of the range reached
	Markers   []LogHistoryMarkerDTO `json:"markers,omitempty"`
	NoStreams string                `json:"noStreams,omitempty"`
	Reset     bool                  `json:"reset,omitempty"` // the page set ended: the source changed or expired
	Error     string                `json:"error,omitempty"`
}

type historyView struct {
	// mu serializes the view's reads: a new request cancels the old one's
	// context, then waits for it to let go of the page set
	mu       sync.Mutex
	cluster  string
	lineage  string
	rev      uint64
	target   loki.Target
	created  int64
	gone     bool
	queries  []string
	display  []string
	copyable bool
	mode     loki.Mode
	evidence loki.UIDEvidence
	start    time.Time
	end      time.Time
	ps       *loki.PageSet
	used     time.Time
	cancel   context.CancelFunc
	inFlight int
}

// LogHistoryService serves History views.
type LogHistoryService struct {
	readerOf func(cluster string) (HistoryReader, error)
	now      func() time.Time
	mu       sync.Mutex
	views    map[string]*historyView
}

// NewLogHistoryService wires the cluster's Loki reader.
func NewLogHistoryService(readerOf func(cluster string) (HistoryReader, error)) *LogHistoryService {
	return &LogHistoryService{readerOf: readerOf, now: time.Now, views: map[string]*historyView{}}
}

// LokiConn is a connection that can read its cluster's Loki.
type LokiConn interface {
	Loki() (*fleet.LokiReader, error)
}

// ReaderOf adapts a connection lookup to the service's reader (nil reader,
// never a typed nil, on an error).
func ReaderOf(lookup func(string) (LokiConn, bool)) func(string) (HistoryReader, error) {
	return func(cluster string) (HistoryReader, error) {
		c, ok := lookup(cluster)
		if !ok {
			return nil, errors.New("cluster not connected")
		}
		r, err := c.Loki()
		if err != nil {
			return nil, err
		}
		return r, nil
	}
}

// CloseView ends a view's page set (the pane closed or left History).
func (s *LogHistoryService) CloseView(view string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.views[view]; v != nil && v.cancel != nil {
		v.cancel()
	}
	delete(s.views, view)
}

// ReadPodHistory reads one page: the first of a new page set, or the next.
func (s *LogHistoryService) ReadPodHistory(req PodHistoryRequestDTO) LogHistoryPageDTO {
	out := LogHistoryPageDTO{View: req.View, Lines: []LogHistoryLineDTO{}, Selectors: []string{}}
	if err := validStreamID(req.View); err != nil {
		out.Error = err.Error()
		return out
	}
	if req.Namespace == "" || req.Pod == "" {
		out.Error = "a pod's namespace and name are required"
		return out
	}
	r, err := s.readerOf(req.Cluster)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyPageLimit)
	defer cancel()
	v, fresh, reset := s.claim(req, r, cancel)
	defer s.release(req.View, v)
	v.mu.Lock()
	defer v.mu.Unlock()
	release, err := r.Slot(ctx)
	if err != nil {
		out.Error = r.Redactor().Text(err.Error())
		return out
	}
	defer release()
	if reset {
		out.Reset = true
	}
	if fresh {
		return s.firstPage(ctx, r, v, req, out)
	}
	for _, b := range req.Skip {
		if b >= 0 && b < len(v.ps.Branches) && v.ps.Branches[b].Stopped {
			v.ps.Skip(b)
		}
	}
	return s.page(ctx, r, v, out)
}

// claim finds the view's page set, or starts one: on Restart, for a view
// it does not hold, or when the source moved (said: Reset). A new request
// cancels the view's read in flight.
func (s *LogHistoryService) claim(req PodHistoryRequestDTO, r HistoryReader, cancel context.CancelFunc) (*historyView, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, v := range s.views {
		if v.inFlight == 0 && now.Sub(v.used) > historyIdle {
			delete(s.views, id)
		}
	}
	v := s.views[req.View]
	if v != nil && v.cancel != nil {
		v.cancel()
	}
	moved := v != nil && v.ps != nil && (v.cluster != req.Cluster || v.lineage != r.LineageKey() || v.rev != r.Revision() ||
		v.target.UID != req.UID || v.target.Namespace != req.Namespace || v.target.Pod != req.Pod || v.target.Container != req.Container)
	if v == nil || v.ps == nil || req.Restart || moved {
		if len(s.views) >= historyViewsMax && v == nil {
			s.evictLocked()
		}
		v = &historyView{cluster: req.Cluster, lineage: r.LineageKey(), rev: r.Revision()}
		s.views[req.View] = v
		v.used, v.cancel = now, cancel
		v.inFlight++
		return v, true, moved && !req.Restart
	}
	v.used, v.cancel = now, cancel
	v.inFlight++
	return v, false, false
}

func (s *LogHistoryService) evictLocked() {
	var oldest string
	var at time.Time
	for id, v := range s.views {
		if v.inFlight == 0 && (oldest == "" || v.used.Before(at)) {
			oldest, at = id, v.used
		}
	}
	if oldest != "" {
		delete(s.views, oldest)
	}
}

func (s *LogHistoryService) release(view string, v *historyView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.inFlight--
	v.used = s.now()
}

// historyRange is the request's absolute range (§5): a preset back from
// now, else since the pod's creation clipped to 7 days, else 24 hours.
func historyRange(req PodHistoryRequestDTO, now time.Time) (time.Time, time.Time) {
	switch {
	case req.RangeSeconds > 0:
		d := time.Duration(req.RangeSeconds) * time.Second
		if d > historyMaxRange {
			d = historyMaxRange
		}
		return now.Add(-d), now
	case req.CreatedUnix > 0:
		start := time.Unix(req.CreatedUnix, 0)
		if now.Sub(start) > historyMaxRange {
			start = now.Add(-historyMaxRange)
		}
		return start, now
	}
	return now.Add(-24 * time.Hour), now
}

// matchSelector is the match alone as a stream selector ("" without one).
func matchSelector(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Quote(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func (s *LogHistoryService) firstPage(ctx context.Context, r HistoryReader, v *historyView, req PodHistoryRequestDTO, out LogHistoryPageDTO) LogHistoryPageDTO {
	red := r.Redactor()
	v.start, v.end = historyRange(req, s.now())
	v.created, v.gone = req.CreatedUnix, req.Gone
	v.target = loki.Target{Match: r.Match(), Namespace: req.Namespace, Pod: req.Pod, Container: req.Container, UID: req.UID}
	out.Source, out.Tenant = r.Source(), r.Tenant()
	out.Start, out.End = v.start.UTC().Format(time.RFC3339), v.end.UTC().Format(time.RFC3339)
	ms := matchSelector(v.target.Match)
	labels, data, err := r.Labels(ctx, v.start, v.end, ms)
	if err != nil {
		out.Error = red.Text(err.Error())
		return out
	}
	if !data {
		out.NoStreams = s.noStreams(ctx, r, v)
		return out
	}
	branches, err := loki.Branches(labels)
	if err != nil {
		out.NoStreams = err.Error()
		return out
	}
	mode := loki.ModeName
	if req.Mode == "uid" {
		mode = loki.ModeUID
	}
	kept, markers, err := s.start(ctx, r, v, branches, mode)
	if err != nil {
		out.Error = red.Text(err.Error())
		return out
	}
	v.evidence = loki.UIDsOf(kept)
	if req.Mode == "" || req.Mode == "auto" {
		if v.evidence == loki.UIDAll && req.UID != "" {
			// every first-page entry carries a UID: the page set is in UID
			// mode, its first page read again with the filter (§4)
			if kept, markers, err = s.start(ctx, r, v, branches, loki.ModeUID); err != nil {
				out.Error = red.Text(err.Error())
				return out
			}
		}
	}
	return s.fill(r, v, kept, markers, out)
}

// start begins a page set in a mode and reads its first page.
func (s *LogHistoryService) start(ctx context.Context, r HistoryReader, v *historyView, branches []loki.Branch, mode loki.Mode) ([]loki.Kept, []loki.Marker, error) {
	v.mode = mode
	v.queries, v.display = nil, nil
	v.copyable = true
	red := r.Redactor()
	for _, b := range branches {
		q := loki.Query(b, v.target, mode)
		d := alertmanager.Clip(red.Text(q), 4096)
		v.queries, v.display = append(v.queries, q), append(v.display, d)
		if d != q {
			v.copyable = false
		}
	}
	v.ps = loki.NewPageSet(v.start.UnixNano(), v.end.UnixNano()+1, len(branches))
	return loki.ReadPage(ctx, s.reader(r, v), v.ps)
}

func (s *LogHistoryService) reader(r HistoryReader, v *historyView) loki.QueryFunc {
	return func(ctx context.Context, branch int, start, end int64, limit int) ([]loki.Entry, error) {
		return r.Query(ctx, v.queries[branch], start, end, limit)
	}
}

func (s *LogHistoryService) page(ctx context.Context, r HistoryReader, v *historyView, out LogHistoryPageDTO) LogHistoryPageDTO {
	out.Source, out.Tenant = r.Source(), r.Tenant()
	out.Start, out.End = v.start.UTC().Format(time.RFC3339), v.end.UTC().Format(time.RFC3339)
	if !r.Current() {
		out.Reset, out.Error = true, fleet.ErrLokiSourceChanged.Error()
		return out
	}
	kept, markers, err := loki.ReadPage(ctx, s.reader(r, v), v.ps)
	if err != nil {
		out.Error = r.Redactor().Text(err.Error())
		return out
	}
	return s.fill(r, v, kept, markers, out)
}

// noStreams says why the labels read found nothing (§2): match names a
// label no stream indexes on the tenant, or no stream matches it.
func (s *LogHistoryService) noStreams(ctx context.Context, r HistoryReader, v *historyView) string {
	red := r.Redactor()
	tenant := "the default tenant"
	if t := r.Tenant(); t != "" {
		tenant = "tenant " + t
	}
	m := v.target.Match
	if len(m) == 0 {
		return "no streams on " + tenant + " in the range"
	}
	all, data, err := r.Labels(ctx, v.start, v.end, "")
	if err == nil && data {
		has := map[string]bool{}
		for _, l := range all {
			has[l] = true
		}
		var missing []string
		for k := range m {
			if !has[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			return alertmanager.Clip(red.Text(fmt.Sprintf("match names %s, which is not an index label on %s in the range", strings.Join(missing, ", "), tenant)), 512)
		}
	}
	return alertmanager.Clip(red.Text(fmt.Sprintf("no streams match %s on %s in the range", matchSelector(m), tenant)), 512)
}

func (s *LogHistoryService) fill(r HistoryReader, v *historyView, kept []loki.Kept, markers []loki.Marker, out LogHistoryPageDTO) LogHistoryPageDTO {
	red := r.Redactor()
	out.Selectors, out.Copyable = append([]string(nil), v.display...), v.copyable
	out.Mode = "name"
	if v.mode == loki.ModeUID {
		out.Mode = "uid"
	}
	out.UIDOffer = v.mode == loki.ModeName && v.evidence == loki.UIDAll && v.target.UID != ""
	out.Strength = strength(v)
	out.Done = v.ps.Done()
	for _, k := range kept {
		line := red.Exact(k.Line) // log content: the endpoint's own credential only (§5)
		clipped := false
		if len(line) > historyLineMax {
			line, clipped = clipUTF8(line, historyLineMax), true
		}
		c := k.Stream[loki.LabelContainer]
		if c == "" {
			c = k.Stream[loki.LabelK8sContainer]
		}
		if c == "" {
			c = k.Meta[loki.LabelContainer]
		}
		if c == "" {
			c = k.Meta[loki.LabelK8sContainer]
		}
		out.Lines = append(out.Lines, LogHistoryLineDTO{TS: time.Unix(0, k.TS).UTC().Format(time.RFC3339Nano), Line: line,
			Container: red.Exact(c), Branch: k.Branch, Clipped: clipped})
	}
	for _, m := range markers {
		out.Markers = append(out.Markers, LogHistoryMarkerDTO{Branch: m.Branch, TS: time.Unix(0, m.TS).UTC().Format(time.RFC3339Nano), Count: m.Count,
			Refused: m.Refused})
	}
	return out
}

// strength is the §4 sentence for a page set.
func strength(v *historyView) string {
	if v.mode == loki.ModeUID {
		return "selected by this pod's UID (k8s_pod_uid); lines without a pod UID are not shown"
	}
	if v.evidence == loki.UIDSome {
		return "some lines carry a pod UID, some do not; selected by namespace and name"
	}
	if v.created > 0 && !v.start.Before(time.Unix(v.created, 0)) {
		s := "selected by namespace and name, lines timestamped from this pod's creation; another pod with this name in that time would match too"
		if v.gone {
			s += "; a later pod with this name would match after this one was removed"
		}
		return s
	}
	return "selected by namespace and name only: every pod with this name in the range"
}

// clipUTF8 cuts s to at most n bytes on a rune boundary.
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
