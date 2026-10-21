package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Set and After name the page a next page continues from: the page set
	// and the last of its pages the view took (Codex's review of f4cce2f).
	// The view's cursor moves only with what the view took, so a reply it
	// discarded is read again, never skipped.
	Set   uint64 `json:"set,omitempty"`
	After int    `json:"after,omitempty"`
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
	More      bool                  `json:"more"` // a branch can still be read: Load older has something to do
	Markers   []LogHistoryMarkerDTO `json:"markers,omitempty"`
	NoStreams string                `json:"noStreams,omitempty"`
	Reset     bool                  `json:"reset,omitempty"` // the page set ended: the source changed or expired
	Error     string                `json:"error,omitempty"`
	// Set and Page name this page (0: none - nothing to continue); a next
	// page sends them back as Set and After.
	Set  uint64 `json:"set,omitempty"`
	Page int    `json:"page,omitempty"`
}

// historySlot is a view's place in the service. It outlives the view's page
// sets, so one mutex guards the view's page state across restarts (Codex's
// review of b2f945c): a new request cancels the slot's read in flight, then
// waits on mu for it to let go. s.mu guards cancel, inFlight and used; mu
// guards state.
type historySlot struct {
	mu       sync.Mutex
	state    *historyView // nil: no page set (none yet, failed, or ended)
	cancel   context.CancelFunc
	inFlight int
	used     time.Time
}

// historyView is one page set: what was locked when it started (§4) and
// where its branches are - after its last page (ps), and before it (prev),
// so a page whose reply the view discarded is read again.
type historyView struct {
	set      uint64
	seq      int // the pages delivered
	prev     *loki.PageSet
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
}

// LogHistoryService serves History views.
type LogHistoryService struct {
	readerOf func(cluster string) (HistoryReader, error)
	now      func() time.Time
	mu       sync.Mutex
	views    map[string]*historySlot
	sets     atomic.Uint64
}

// errHistoryViewsFull: every view the service holds is reading; a new one
// waits for one to finish rather than growing the set.
var errHistoryViewsFull = fmt.Errorf("%d log history views are reading; close one or try again", historyViewsMax)

// NewLogHistoryService wires the cluster's Loki reader.
func NewLogHistoryService(readerOf func(cluster string) (HistoryReader, error)) *LogHistoryService {
	return &LogHistoryService{readerOf: readerOf, now: time.Now, views: map[string]*historySlot{}}
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
	if sl := s.views[view]; sl != nil && sl.cancel != nil {
		sl.cancel()
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
	slot, err := s.enter(req.View, cancel)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer s.leave(slot)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if ctx.Err() != nil {
		out.Error = "a newer request for this view replaced this one"
		return out
	}
	release, err := r.Slot(ctx)
	if err != nil {
		out.Error = r.Redactor().Text(err.Error())
		return out
	}
	defer release()
	v := slot.state
	moved := v != nil && (v.cluster != req.Cluster || v.lineage != r.LineageKey() || v.rev != r.Revision() ||
		v.target.UID != req.UID || v.target.Namespace != req.Namespace || v.target.Pod != req.Pod || v.target.Container != req.Container)
	// the page a next page continues: the view's last, or the one before it
	// when the view never took the last (its reply was discarded)
	var from *loki.PageSet
	if !req.Restart && v != nil && !moved && req.Set == v.set {
		switch {
		case req.After == v.seq:
			from = v.ps
		case req.After == v.seq-1 && v.prev != nil:
			from = v.prev
		}
	}
	var next *historyView // staged: kept only when its page is delivered
	if from == nil {
		// a new page set: Restart, or a next page with nothing to continue
		// (expired, evicted, its source moved, or a cursor the view does not
		// hold) starts from the newest, said as Reset, so the view replaces
		// what it holds instead of appending the newest again
		next = &historyView{set: s.sets.Add(1), cluster: req.Cluster, lineage: r.LineageKey(), rev: r.Revision()}
		out = s.firstPage(ctx, r, next, req, out)
		out.Reset = out.Reset || !req.Restart
	} else {
		staged := *v
		next = &staged
		next.ps = from.Clone()
		for _, b := range req.Skip {
			if b >= 0 && b < len(next.ps.Branches) && next.ps.Branches[b].Stopped {
				next.ps.Skip(b)
			}
		}
		out = s.page(ctx, r, next, out)
	}
	// the owner fence (Codex's review of f4cce2f): a request a newer one
	// replaced, or out of time, delivers nothing and keeps nothing - its page
	// set is where the view left it
	if err := ctx.Err(); err != nil {
		e := "a newer request for this view replaced this one"
		if errors.Is(err, context.DeadlineExceeded) {
			e = fmt.Sprintf("the page took longer than %s; nothing was kept", historyPageLimit)
		}
		return LogHistoryPageDTO{View: req.View, Lines: []LogHistoryLineDTO{}, Selectors: []string{}, Error: e}
	}
	// the completion fence: a source that moved during the page (its
	// configuration or credential) ends the page set, and the page read
	// over it is refused, never returned as current
	if !r.Current() {
		slot.state = nil
		return LogHistoryPageDTO{View: req.View, Lines: []LogHistoryLineDTO{}, Selectors: []string{}, Reset: true, Error: fleet.ErrLokiSourceChanged.Error()}
	}
	switch {
	case out.Error != "":
		// a failed page keeps nothing: the view's page set is where it was,
		// so the cursor the view holds still continues it
	case next.ps == nil:
		slot.state = nil // nothing selectable: no page set
	default:
		if from != nil {
			next.prev, next.seq = from, req.After+1
		} else {
			next.prev, next.seq = nil, 1
		}
		slot.state = next
		out.Set, out.Page = next.set, next.seq
	}
	return out
}

// enter finds the view's slot, or makes one (refused when the service holds
// its maximum and none is idle), and cancels the slot's read in flight.
func (s *LogHistoryService) enter(view string, cancel context.CancelFunc) (*historySlot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, sl := range s.views {
		if sl.inFlight == 0 && now.Sub(sl.used) > historyIdle {
			delete(s.views, id)
		}
	}
	sl := s.views[view]
	if sl == nil {
		if len(s.views) >= historyViewsMax && !s.evictLocked() {
			return nil, errHistoryViewsFull
		}
		sl = &historySlot{}
		s.views[view] = sl
	}
	if sl.cancel != nil {
		sl.cancel()
	}
	sl.cancel, sl.used = cancel, now
	sl.inFlight++
	return sl, nil
}

// evictLocked drops the least recently used idle slot; false when every
// slot is reading.
func (s *LogHistoryService) evictLocked() bool {
	var oldest string
	var at time.Time
	for id, sl := range s.views {
		if sl.inFlight == 0 && (oldest == "" || sl.used.Before(at)) {
			oldest, at = id, sl.used
		}
	}
	if oldest == "" {
		return false
	}
	delete(s.views, oldest)
	return true
}

func (s *LogHistoryService) leave(sl *historySlot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl.inFlight--
	sl.used = s.now()
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
	if !data || len(labels) == 0 {
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
	return loki.ReadPage(ctx, s.reader(r, v), v.ps, shownBytes(red))
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
	kept, markers, err := loki.ReadPage(ctx, s.reader(r, v), v.ps, shownBytes(r.Redactor()))
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
	out.More = v.ps.More()
	for _, k := range kept {
		line, clipped := shownLine(red, k.Entry)
		out.Lines = append(out.Lines, LogHistoryLineDTO{TS: time.Unix(0, k.TS).UTC().Format(time.RFC3339Nano), Line: line,
			Container: shownContainer(red, k.Entry), Branch: k.Branch, Clipped: clipped})
	}
	for _, m := range markers {
		out.Markers = append(out.Markers, LogHistoryMarkerDTO{Branch: m.Branch, TS: time.Unix(0, m.TS).UTC().Format(time.RFC3339Nano), Count: m.Count,
			Refused: m.Refused})
	}
	return out
}

// shownLine is a line as the page shows it: log content through the
// endpoint's own credential only (§5), then clipped on a rune boundary.
func shownLine(red safehttp.Redactor, e loki.Entry) (string, bool) {
	line := red.Exact(e.Line)
	if len(line) > historyLineMax {
		return clipUTF8(line, historyLineMax), true
	}
	return line, false
}

// shownContainer is the container a line is shown with. A label repeats on
// every line of its stream: bounded like a label value, so one large label
// cannot multiply into the reply.
func shownContainer(red safehttp.Redactor, e loki.Entry) string {
	c := e.Stream[loki.LabelContainer]
	if c == "" {
		c = e.Stream[loki.LabelK8sContainer]
	}
	if c == "" {
		c = e.Meta[loki.LabelContainer]
	}
	if c == "" {
		c = e.Meta[loki.LabelK8sContainer]
	}
	return alertmanager.Clip(red.Exact(c), alertmanager.MaxLabelValue)
}

// lineFieldBytes bounds a serialized line's fields besides its line and
// container: {"ts":"<30>","line":,"container":,"branch":<n>,"clipped":true},
const lineFieldBytes = 128

// shownBytes is an entry's share of a page's budget as the bridge carries it
// (Codex's review of f4cce2f): the line and container as shown - a
// redaction can lengthen them - and JSON-escaped, as the bridge serializes
// them, plus the line's other fields.
func shownBytes(red safehttp.Redactor) loki.SizeFunc {
	return func(e loki.Entry) int {
		line, _ := shownLine(red, e)
		return jsonStringLen(line) + jsonStringLen(shownContainer(red, e)) + lineFieldBytes
	}
}

// jsonStringLen is the length of s as encoding/json writes it (HTML-safe,
// as the bridge's json.Marshal does): quoted, a control character, <, >
// and & as \u00XX, U+2028 and U+2029 escaped; an invalid byte at most six
// (\ufffd escaped, or the replacement rune, by the encoder's version).
func jsonStringLen(s string) int {
	n := 2
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\' || b == '\b' || b == '\f' || b == '\n' || b == '\r' || b == '\t':
				n += 2
			case b < 0x20 || b == '<' || b == '>' || b == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case c == utf8.RuneError && size == 1, c == '\u2028', c == '\u2029':
			n += 6
		default:
			n += size
		}
		i += size
	}
	return n
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

// clipUTF8 cuts s to at most n bytes on a rune boundary. The cut owns its
// bytes (Codex's review of 1230f36): a slice of s would keep all of s - a
// line a redaction lengthened to many MiB - alive behind 64 KiB shown.
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.Clone(s[:n])
}
