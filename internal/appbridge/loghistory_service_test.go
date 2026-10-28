package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/moomora/klyx/internal/loki"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/sources"
)

// fakeHistory is a Loki source: the labels it lists (by query), and the
// entries each LogQL selects, newest first.
type fakeHistory struct {
	mu      sync.Mutex
	lineage string
	rev     uint64
	match   map[string]string
	tenant  string
	secret  string
	labels  map[string][]string // query -> names; absent: no data
	entries map[string][]loki.Entry
	queries []string
	err     error
}

func (f *fakeHistory) LineageKey() string { return f.lineage }
func (f *fakeHistory) Revision() uint64   { return f.rev }
func (f *fakeHistory) Source() string     { return "Service logs/loki:http" }
func (f *fakeHistory) Tenant() string     { return f.tenant }
func (f *fakeHistory) Sentence() string   { return "from the Service loki/loki:3100 on this cluster" }
func (f *fakeHistory) Lineage() sources.Lineage {
	return sources.Lineage{Kind: "loki", Mode: "service", Identity: "service|" + f.lineage, CredRev: f.rev}
}
func (f *fakeHistory) Match() map[string]string             { return f.match }
func (f *fakeHistory) Current() bool                        { return true }
func (f *fakeHistory) Slot(context.Context) (func(), error) { return func() {}, nil }
func (f *fakeHistory) Redactor() safehttp.Redactor {
	if f.secret == "" {
		return safehttp.NewRedactor()
	}
	return safehttp.NewRedactor(safehttp.Bearer(f.secret))
}
func (f *fakeHistory) Labels(_ context.Context, _, _ time.Time, q string) ([]string, bool, error) {
	l, ok := f.labels[q]
	return l, ok, nil
}
func (f *fakeHistory) Query(_ context.Context, q string, start, end int64, limit int) ([]loki.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	var out []loki.Entry
	for _, e := range f.entries[q] {
		if e.TS >= start && e.TS < end {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

var tNow = time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC)

func histSvc(f *fakeHistory) *LogHistoryService {
	s := NewLogHistoryService(func(string) (HistoryReader, error) { return f, nil })
	s.now = func() time.Time { return tNow }
	return s
}

func lines(n int, from time.Time, uid string, prefix string) []loki.Entry {
	var out []loki.Entry
	for i := 0; i < n; i++ {
		meta := map[string]string{}
		if uid != "" {
			meta["k8s_pod_uid"] = uid
		}
		out = append(out, loki.Entry{Stream: map[string]string{"k8s_namespace_name": "team", "k8s_pod_name": "api-1", "k8s_container_name": "web"},
			Meta: meta, TS: from.Add(-time.Duration(i) * time.Second).UnixNano(), Line: fmt.Sprintf("%s %d", prefix, i)})
	}
	return out
}

const otelLabels = "k8s_cluster_name k8s_namespace_name k8s_pod_name k8s_container_name"

func otelFake() *fakeHistory {
	return &fakeHistory{lineage: "L1", rev: 1, match: map[string]string{"k8s_cluster_name": "prd"}, tenant: "lab",
		labels: map[string][]string{`{k8s_cluster_name="prd"}`: strings.Fields(otelLabels)}, entries: map[string][]loki.Entry{}}
}

func q(branch loki.Branch, mode loki.Mode, uid string) string {
	return loki.Query(branch, loki.Target{Match: map[string]string{"k8s_cluster_name": "prd"}, Namespace: "team", Pod: "api-1", UID: uid}, mode)
}

var podBranch = loki.Branch{NSLabel: "k8s_namespace_name", Pod: loki.K8sPodIndexed}

// after is the next-page request of a view that took page p.
func after(req PodHistoryRequestDTO, p LogHistoryPageDTO) PodHistoryRequestDTO {
	req.Restart, req.Set, req.After = false, p.Set, p.Page
	return req
}

// Coverage S8b §4: a first page whose every entry carries a UID puts the
// page set in UID mode, read again with the filter; Load older keeps it.
func TestHistoryUIDModeAndPaging(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "u-1")] = lines(1500, tNow.Add(-time.Minute), "u-1", "name")
	f.entries[q(podBranch, loki.ModeUID, "u-1")] = lines(1500, tNow.Add(-time.Minute), "u-1", "uid")
	s := histSvc(f)
	req := PodHistoryRequestDTO{Cluster: "c", View: "v1", Namespace: "team", Pod: "api-1", UID: "u-1", CreatedUnix: tNow.Add(-2 * time.Hour).Unix()}
	p := s.ReadPodHistory(req)
	if p.Error != "" || p.Mode != "uid" || len(p.Lines) != 1000 || !strings.HasPrefix(p.Lines[0].Line, "uid 0") || p.Done || !p.Copyable {
		t.Fatalf("first page: %+v", p.Error+" "+p.Mode+" "+fmt.Sprint(len(p.Lines), p.Done, p.Copyable))
	}
	if !strings.Contains(p.Strength, "selected by this pod's UID") || p.Lines[0].Container != "web" || len(p.Selectors) != 2 {
		t.Fatalf("strength %q container %q selectors %v", p.Strength, p.Lines[0].Container, p.Selectors)
	}
	p2 := s.ReadPodHistory(after(req, p))
	if p2.Error != "" || p2.Mode != "uid" || len(p2.Lines) != 500 || p2.Lines[0].Line != "uid 1000" || !p2.Done {
		t.Fatalf("load older: %s %d %v %q", p2.Error, len(p2.Lines), p2.Done, p2.Lines[0].Line)
	}
	// an explicit name mode starts a new page set; the UID is offered back
	req.Restart, req.Mode = true, "name"
	p3 := s.ReadPodHistory(req)
	if p3.Mode != "name" || !p3.UIDOffer || len(p3.Lines) != 1000 || !strings.HasPrefix(p3.Lines[0].Line, "name 0") ||
		!strings.Contains(p3.Strength, "lines timestamped from this pod's creation") {
		t.Fatalf("explicit name mode: %s %v %d %q", p3.Mode, p3.UIDOffer, len(p3.Lines), p3.Strength)
	}
	for _, sel := range p3.Selectors {
		if strings.Contains(sel, "k8s_pod_uid") {
			t.Fatalf("name mode filtered the UID: %s", sel)
		}
	}
}

// Mixed evidence and none: the name selector, said with its bound.
func TestHistoryStrength(t *testing.T) {
	f := otelFake()
	mixed := append(lines(3, tNow.Add(-time.Minute), "u-1", "with"), lines(3, tNow.Add(-2*time.Minute), "", "without")...)
	f.entries[q(podBranch, loki.ModeName, "u-1")] = mixed
	s := histSvc(f)
	p := s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "v2", Namespace: "team", Pod: "api-1", UID: "u-1"})
	if p.Mode != "name" || p.Strength != "some lines carry a pod UID, some do not; selected by namespace and name" {
		t.Fatalf("mixed: %s %q", p.Mode, p.Strength)
	}
	f.entries[q(podBranch, loki.ModeName, "u-1")] = lines(3, tNow.Add(-time.Minute), "", "none")
	for _, tc := range []struct {
		created int64
		gone    bool
		want    string
	}{
		{0, false, "selected by namespace and name only: every pod with this name in the range"},
		{tNow.Add(-time.Hour).Unix(), false, "selected by namespace and name, lines timestamped from this pod's creation; another pod with this name in that time would match too"},
		{tNow.Add(-time.Hour).Unix(), true, "selected by namespace and name, lines timestamped from this pod's creation; another pod with this name in that time would match too; a later pod with this name would match after this one was removed"},
	} {
		p := s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "v3", Restart: true, Namespace: "team", Pod: "api-1", UID: "u-1", CreatedUnix: tc.created, Gone: tc.gone})
		if p.Strength != tc.want {
			t.Errorf("created %d gone %v: %q", tc.created, tc.gone, p.Strength)
		}
	}
	// a preset range reaching before the creation is "name only"
	p = s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "v3", Restart: true, Namespace: "team", Pod: "api-1", UID: "u-1",
		CreatedUnix: tNow.Add(-time.Hour).Unix(), RangeSeconds: 6 * 3600})
	if !strings.HasPrefix(p.Strength, "selected by namespace and name only") {
		t.Fatalf("preset before creation: %q", p.Strength)
	}
}

// §2: nothing selectable is said, never "no logs": a match label no stream
// indexes, a match nothing matches, no namespace label.
func TestHistoryNoStreams(t *testing.T) {
	f := otelFake()
	f.labels = map[string][]string{"": {"cluster", "namespace", "pod"}} // the tenant has no k8s_cluster_name
	s := histSvc(f)
	req := PodHistoryRequestDTO{Cluster: "c", View: "v4", Namespace: "team", Pod: "api-1", UID: "u-1"}
	if p := s.ReadPodHistory(req); p.NoStreams != "match names k8s_cluster_name, which is not an index label on tenant lab in the range" || len(p.Lines) != 0 {
		t.Fatalf("not indexed: %q", p.NoStreams)
	}
	f.labels = map[string][]string{"": {"k8s_cluster_name", "k8s_namespace_name"}}
	req.Restart = true
	if p := s.ReadPodHistory(req); p.NoStreams != `no streams match {k8s_cluster_name="prd"} on tenant lab in the range` {
		t.Fatalf("no match: %q", p.NoStreams)
	}
	f.labels = map[string][]string{`{k8s_cluster_name="prd"}`: {"k8s_cluster_name", "service_name"}}
	if p := s.ReadPodHistory(req); p.NoStreams != loki.ErrNoNamespaceLabel.Error() {
		t.Fatalf("no namespace label: %q", p.NoStreams)
	}
}

// §5, §6: the credential boundary - lines through the exact-only redactor,
// selectors as display values, Copy LogQL unavailable when one differs; a
// line over 64 KiB clipped on a rune boundary.
func TestHistoryCredentialBoundary(t *testing.T) {
	f := otelFake()
	f.secret, f.match = "prd", map[string]string{"k8s_cluster_name": "prd"} // a credential equal to the match value
	es := lines(2, tNow.Add(-time.Minute), "", "token=prd and an email a@b.example and password=hunter2")
	es[1].Line = strings.Repeat("€", 30_000) // 3 bytes each: 65,536 falls mid-rune
	f.entries[q(podBranch, loki.ModeName, "")] = es
	s := histSvc(f)
	p := s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "v5", Namespace: "team", Pod: "api-1"})
	if p.Error != "" || p.Copyable {
		t.Fatalf("copyable with the credential in the query: %+v", p.Error)
	}
	for _, sel := range p.Selectors {
		if strings.Contains(sel, `"prd"`) {
			t.Fatalf("the credential in a selector: %s", sel)
		}
	}
	// the endpoint's own credential only: a general credential shape in an
	// honest log line (password=hunter2) stays as the application wrote it
	if !strings.Contains(p.Lines[0].Line, "token=[redacted]") || !strings.Contains(p.Lines[0].Line, "a@b.example") || !strings.Contains(p.Lines[0].Line, "password=hunter2") {
		t.Fatalf("log content: exact redaction only: %q", p.Lines[0].Line)
	}
	if !p.Lines[1].Clipped || len(p.Lines[1].Line) > historyLineMax || !utf8.ValidString(p.Lines[1].Line) || !strings.HasSuffix(p.Lines[1].Line, "€") {
		t.Fatalf("clip: %v %d", p.Lines[1].Clipped, len(p.Lines[1].Line))
	}
}

// §6: a revision change between pages ends the page set: the next request
// starts again from the newest, said as Reset.
func TestHistorySourceMoved(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(1500, tNow.Add(-time.Minute), "", "x")
	s := histSvc(f)
	req := PodHistoryRequestDTO{Cluster: "c", View: "v6", Namespace: "team", Pod: "api-1"}
	first := s.ReadPodHistory(req)
	if len(first.Lines) != 1000 {
		t.Fatalf("first: %d", len(first.Lines))
	}
	f.rev = 2
	p := s.ReadPodHistory(after(req, first))
	if !p.Reset || len(p.Lines) != 1000 || p.Lines[0].Line != "x 0" {
		t.Fatalf("moved: reset %v, %d lines, first %q", p.Reset, len(p.Lines), p.Lines[0].Line)
	}
	if bad := s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "bad view!", Namespace: "team", Pod: "api-1"}); bad.Error == "" {
		t.Fatal("an invalid view id was accepted")
	}
}

// §8: the range - since created, clipped to 7 days; a preset; else a day.
func TestHistoryRange(t *testing.T) {
	for _, tc := range []struct {
		req  PodHistoryRequestDTO
		want time.Duration
	}{
		{PodHistoryRequestDTO{CreatedUnix: tNow.Add(-3 * time.Hour).Unix()}, 3 * time.Hour},
		{PodHistoryRequestDTO{CreatedUnix: tNow.Add(-30 * 24 * time.Hour).Unix()}, 7 * 24 * time.Hour},
		{PodHistoryRequestDTO{RangeSeconds: 3600}, time.Hour},
		{PodHistoryRequestDTO{RangeSeconds: 30 * 24 * 3600}, 7 * 24 * time.Hour},
		{PodHistoryRequestDTO{}, 24 * time.Hour},
	} {
		start, end := historyRange(tc.req, tNow)
		if end.Sub(start) != tc.want {
			t.Errorf("%+v: %v", tc.req, end.Sub(start))
		}
	}
}

// ---- Codex's review of b2f945c ----

// A next page with no page set to continue (expired, evicted, failed)
// starts from the newest, said as Reset, so the view replaces its lines.
func TestHistoryExpiredCursorResets(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(1500, tNow.Add(-time.Minute), "", "x")
	s := histSvc(f)
	now := tNow
	s.now = func() time.Time { return now }
	req := PodHistoryRequestDTO{Cluster: "c", View: "exp", Restart: true, Namespace: "team", Pod: "api-1"}
	first := s.ReadPodHistory(req)
	if first.Reset || len(first.Lines) != 1000 {
		t.Fatalf("first: reset %v, %d", first.Reset, len(first.Lines))
	}
	now = now.Add(historyIdle + time.Minute)
	if p := s.ReadPodHistory(after(req, first)); !p.Reset || p.Lines[0].Line != "x 0" {
		t.Fatalf("an expired cursor: reset %v, first %q", p.Reset, p.Lines[0].Line)
	}
}

// A stopped boundary's marker, with the server's refusal, stays on every
// page until skipped; Load older has nothing to do (More false).
func TestHistoryMarkersPersist(t *testing.T) {
	f := otelFake()
	es := lines(6000, tNow.Add(-time.Minute), "", "burst")
	for i := range es {
		es[i].TS = es[0].TS
	}
	f.entries[q(podBranch, loki.ModeName, "")] = es
	s := histSvc(f)
	req := PodHistoryRequestDTO{Cluster: "c", View: "stop", Restart: true, Namespace: "team", Pod: "api-1"}
	p := s.ReadPodHistory(req)
	for i := 0; i < 4; i++ {
		p = s.ReadPodHistory(after(req, p))
	}
	if len(p.Markers) != 1 || p.More || p.Done || p.Markers[0].Count < 5000 {
		t.Fatalf("after several pages: markers %+v more %v done %v", p.Markers, p.More, p.Done)
	}
	req = after(req, p)
	req.Skip = []int{p.Markers[0].Branch}
	if p = s.ReadPodHistory(req); len(p.Markers) != 0 || p.Reset {
		t.Fatalf("skip: %+v", p.Markers)
	}
}

// slowHistory holds every Slot until released, so views stay in flight.
type slowHistory struct {
	*fakeHistory
	in      chan struct{}
	release chan struct{}
}

func (f *slowHistory) Slot(ctx context.Context) (func(), error) {
	f.in <- struct{}{}
	select {
	case <-f.release:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// The service holds at most 16 views; a new one while all are reading is
// refused, never added.
func TestHistoryViewLimitWhileBusy(t *testing.T) {
	f := &slowHistory{fakeHistory: otelFake(), in: make(chan struct{}, 32), release: make(chan struct{})}
	s := NewLogHistoryService(func(string) (HistoryReader, error) { return f, nil })
	var wg sync.WaitGroup
	for i := 0; i < historyViewsMax; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: fmt.Sprint("v", i), Restart: true, Namespace: "team", Pod: "api-1"})
		}(i)
	}
	for i := 0; i < historyViewsMax; i++ {
		<-f.in
	}
	p := s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "one-more", Restart: true, Namespace: "team", Pod: "api-1"})
	close(f.release)
	wg.Wait()
	if p.Error != errHistoryViewsFull.Error() {
		t.Fatalf("a seventeenth view: %q", p.Error)
	}
	s.mu.Lock()
	n := len(s.views)
	s.mu.Unlock()
	if n > historyViewsMax {
		t.Fatalf("%d views held", n)
	}
}

// Requests for one view, concurrent: one owner at a time (run under -race).
func TestHistoryConcurrentSameView(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(1500, tNow.Add(-time.Minute), "", "x")
	s := histSvc(f)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "same", Restart: i%2 == 0, Namespace: "team", Pod: "api-1"})
		}(i)
	}
	close(start)
	wg.Wait()
}

// movingHistory moves its source after the last branch of a page answers.
type movingHistory struct {
	*fakeHistory
	moved atomic.Bool
	after string
}

func (f *movingHistory) Current() bool { return !f.moved.Load() }
func (f *movingHistory) Query(ctx context.Context, q string, start, end int64, limit int) ([]loki.Entry, error) {
	es, err := f.fakeHistory.Query(ctx, q, start, end, limit)
	if strings.Contains(q, f.after) {
		f.moved.Store(true)
	}
	return es, err
}

// The completion fence: a source that moves during a page - first or next -
// refuses the page, and the next request starts again (Reset).
func TestHistoryCompletionFence(t *testing.T) {
	f := &movingHistory{fakeHistory: otelFake(), after: `pod="", k8s_pod_name=""`}
	f.entries[q(podBranch, loki.ModeName, "")] = lines(1500, tNow.Add(-time.Minute), "", "OLD")
	s := NewLogHistoryService(func(string) (HistoryReader, error) { return f, nil })
	s.now = func() time.Time { return tNow }
	req := PodHistoryRequestDTO{Cluster: "c", View: "fence", Restart: true, Namespace: "team", Pod: "api-1"}
	if p := s.ReadPodHistory(req); len(p.Lines) != 0 || p.Error == "" || !p.Reset {
		t.Fatalf("first page over a moved source: %d lines, %q", len(p.Lines), p.Error)
	}
	f.moved.Store(false)
	f.after = "never"
	first := s.ReadPodHistory(req)
	if len(first.Lines) != 1000 || first.Error != "" {
		t.Fatalf("a current source: %d %q", len(first.Lines), first.Error)
	}
	f.after = `k8s_pod_name="api-1"` // the branch a next page still reads
	if p := s.ReadPodHistory(after(req, first)); len(p.Lines) != 0 || p.Error == "" || !p.Reset {
		t.Fatalf("a next page over a moved source: %d lines, %q", len(p.Lines), p.Error)
	}
}

// An empty label list in the match is nothing selectable: the fallback read
// says why, never "no namespace label".
func TestHistoryEmptyLabels(t *testing.T) {
	f := otelFake()
	f.labels = map[string][]string{`{k8s_cluster_name="prd"}`: {}, "": {"namespace", "pod"}}
	p := histSvc(f).ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "empty", Restart: true, Namespace: "team", Pod: "api-1"})
	if p.NoStreams != "match names k8s_cluster_name, which is not an index label on tenant lab in the range" {
		t.Fatalf("empty labels: %q", p.NoStreams)
	}
}

// §8, measured on the admitted worst shape: six branches each answering
// about 8 MiB of 60 KiB lines, and one 128 KiB label shared by every line.
// The page carries at most PageBytes of lines; the label is clipped.
//
//	go test ./internal/appbridge -run TestHistoryBudget -v
func TestHistoryBudget(t *testing.T) {
	f := otelFake()
	f.labels = map[string][]string{`{k8s_cluster_name="prd"}`: {"namespace", "k8s_namespace_name", "pod", "k8s_pod_name"}}
	bs, _ := loki.Branches(f.labels[`{k8s_cluster_name="prd"}`])
	shared := map[string]string{"k8s_container_name": strings.Repeat("c", 128<<10)}
	for bi, b := range bs {
		var es []loki.Entry
		for i := 0; i < 136; i++ { // 136 x 60 KiB ~ 8 MiB per branch
			es = append(es, loki.Entry{Stream: shared, TS: tNow.Add(-time.Duration(bi*1000+i) * time.Millisecond).UnixNano(),
				Line: fmt.Sprintf("b%d-%03d %s", bi, i, strings.Repeat("x", 60<<10))})
		}
		f.entries[loki.Query(b, loki.Target{Match: f.match, Namespace: "team", Pod: "api-1"}, loki.ModeName)] = es
	}
	if len(bs) != 6 {
		t.Fatalf("branches: %d", len(bs))
	}
	s := histSvc(f)
	before := heapNow()
	p := s.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "budget", Restart: true, Namespace: "team", Pod: "api-1"})
	enc, _ := json.Marshal(p)
	retained := int64(heapNow()) - int64(before)
	t.Logf("page: %d lines, DTO %.1f MiB, heap after the page %+.1f MiB", len(p.Lines), float64(len(enc))/(1<<20), float64(retained)/(1<<20))
	if p.Error != "" || len(p.Lines) == 0 || len(enc) > loki.PageBytes+(1<<20) {
		t.Fatalf("the page is not bounded: %d lines, %d bytes, %q", len(p.Lines), len(enc), p.Error)
	}
	if len(p.Lines[0].Container) > 260 {
		t.Fatalf("the shared label repeats unclipped: %d bytes", len(p.Lines[0].Container))
	}
	runtime.KeepAlive(p)
}

// Codex's review of f4cce2f: the budget is what the bridge carries - the
// line as shown and serialized. A redaction that lengthens every line (a
// 4-byte credential shown as [redacted]) and lines of control characters
// (each escaped to six bytes) stay within PageBytes, and the lines a page
// leaves out are the next page's: every line once, in order.
func TestHistoryBudgetShown(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		n            int
		line         func(i int) string
	}{
		{"redaction", "pass", 1000, func(i int) string { return strings.Repeat("pass", 1750) + fmt.Sprintf(" %04d", i) }}, // 7 MB read, 17.5 MB shown
		{"escaping", "", 400, func(i int) string { return strings.Repeat("\x00<", 30<<10) + fmt.Sprintf(" %04d", i) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := otelFake()
			f.secret = tc.secret
			es := lines(tc.n, tNow.Add(-time.Minute), "", "")
			for i := range es {
				es[i].Line = tc.line(i)
			}
			f.entries[q(podBranch, loki.ModeName, "")] = es
			s := histSvc(f)
			req := PodHistoryRequestDTO{Cluster: "c", View: "shown-" + tc.name, Restart: true, Namespace: "team", Pod: "api-1"}
			var got []string
			p := s.ReadPodHistory(req)
			for pages := 1; ; pages++ {
				enc, _ := json.Marshal(p)
				t.Logf("page %d: %d lines, DTO %.2f MiB", pages, len(p.Lines), float64(len(enc))/(1<<20))
				if p.Error != "" || len(p.Lines) == 0 || len(enc) > loki.PageBytes+(64<<10) {
					t.Fatalf("page %d is not bounded by what it carries: %d lines, %d bytes, %q", pages, len(p.Lines), len(enc), p.Error)
				}
				for _, l := range p.Lines {
					got = append(got, l.Line[len(l.Line)-4:])
				}
				if p.Done || pages > 20 {
					break
				}
				p = s.ReadPodHistory(after(req, p))
			}
			if len(got) != tc.n {
				t.Fatalf("delivered %d of %d", len(got), tc.n)
			}
			for i, g := range got {
				if g != fmt.Sprintf("%04d", i) {
					t.Fatalf("line %d is %s: a line was lost or repeated", i, g)
				}
			}
		})
	}
}

// A line clipped for display owns its bytes (Codex's review of 1230f36):
// one 6 MiB line of a 4-byte credential, which redaction lengthens to 15
// MiB, leaves the 64 KiB shown and nothing of the 15 MiB behind it.
func TestHistoryClippedLineOwnsItsBytes(t *testing.T) {
	s := strings.Repeat("€", 30_000)
	if c := clipUTF8(s, historyLineMax); unsafe.StringData(c) == unsafe.StringData(s) {
		t.Fatal("the clip shares the line's backing array")
	}
	f := otelFake()
	f.secret = "pass"
	es := lines(1, tNow.Add(-time.Minute), "", "")
	es[0].Line = strings.Repeat("pass", (6<<20)/4)
	f.entries[q(podBranch, loki.ModeName, "")] = es
	svc := histSvc(f)
	before := heapNow()
	p := svc.ReadPodHistory(PodHistoryRequestDTO{Cluster: "c", View: "own", Restart: true, Namespace: "team", Pod: "api-1"})
	retained := int64(heapNow()) - int64(before)
	t.Logf("shown %d bytes, heap after the page %+.2f MiB", len(p.Lines[0].Line), float64(retained)/(1<<20))
	if p.Error != "" || !p.Lines[0].Clipped || len(p.Lines[0].Line) != historyLineMax || retained > 2<<20 {
		t.Fatalf("the clipped line keeps its redaction alive: %d bytes shown, %d retained, %q", len(p.Lines[0].Line), retained, p.Error)
	}
	runtime.KeepAlive(p)
	runtime.KeepAlive(f)
}

func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Nothing selectable leaves no page set: a next page starts again from the
// newest, said as Reset. A failed page - first or next - keeps nothing: the
// page set the view holds is where it was, so its cursor still continues it
// (Codex's review of f4cce2f), and a request with no cursor starts again.
func TestHistoryFailedPageKeepsNothing(t *testing.T) {
	f := otelFake()
	f.labels = map[string][]string{}
	s := histSvc(f)
	req := PodHistoryRequestDTO{Cluster: "c", View: "failed", Restart: true, Namespace: "team", Pod: "api-1"}
	p := s.ReadPodHistory(req)
	if p.NoStreams == "" || p.Set != 0 {
		t.Fatalf("no streams: %+v", p)
	}
	f.labels = map[string][]string{`{k8s_cluster_name="prd"}`: strings.Fields(otelLabels)}
	f.entries[q(podBranch, loki.ModeName, "")] = lines(1500, tNow.Add(-time.Minute), "", "x")
	if p = s.ReadPodHistory(after(req, p)); !p.Reset || len(p.Lines) != 1000 || p.Page != 1 {
		t.Fatalf("after nothing selectable: reset %v, %d lines, %q", p.Reset, len(p.Lines), p.Error)
	}
	held := p
	f.err = errors.New("Loki answered HTTP 502")
	if r := s.ReadPodHistory(req); r.Error == "" || r.Set != 0 {
		t.Fatalf("a failed Refresh: %q, set %d", r.Error, r.Set)
	}
	if r := s.ReadPodHistory(after(req, held)); r.Error == "" || r.Set != 0 {
		t.Fatalf("a failed next page: %q, set %d", r.Error, r.Set)
	}
	f.err = nil
	if r := s.ReadPodHistory(after(req, held)); r.Reset || len(r.Lines) != 500 || r.Lines[0].Line != "x 1000" || r.Page != 2 {
		t.Fatalf("the cursor held across failures: reset %v, %d lines, page %d", r.Reset, len(r.Lines), r.Page)
	}
	if r := s.ReadPodHistory(after(req, LogHistoryPageDTO{})); !r.Reset || r.Lines[0].Line != "x 0" {
		t.Fatalf("no cursor: reset %v", r.Reset)
	}
}

// A next page names the page it continues (Codex's review of f4cce2f): the
// view's last page continues; the one before it reads the last page again
// (the view discarded its reply); any other starts again, said as Reset.
func TestHistoryCursorNamesThePage(t *testing.T) {
	f := otelFake()
	f.entries[q(podBranch, loki.ModeName, "")] = lines(2500, tNow.Add(-time.Minute), "", "x")
	s := histSvc(f)
	req := PodHistoryRequestDTO{Cluster: "c", View: "cursor", Restart: true, Namespace: "team", Pod: "api-1"}
	p1 := s.ReadPodHistory(req)
	p2 := s.ReadPodHistory(after(req, p1)) // a reply the view discards
	if p2.Page != 2 || p2.Lines[0].Line != "x 1000" {
		t.Fatalf("page 2: %d %q", p2.Page, p2.Lines[0].Line)
	}
	again := s.ReadPodHistory(after(req, p1))
	if again.Reset || again.Page != 2 || fmt.Sprint(again.Lines) != fmt.Sprint(p2.Lines) {
		t.Fatalf("page 2 read again: reset %v, page %d, %d lines", again.Reset, again.Page, len(again.Lines))
	}
	// a second discard reads it again too: the page before stays the one
	// before the last page, never the discarded read
	if twice := s.ReadPodHistory(after(req, p1)); fmt.Sprint(twice.Lines) != fmt.Sprint(p2.Lines) || twice.Page != 2 {
		t.Fatalf("page 2 read a third time: page %d, %d lines", twice.Page, len(twice.Lines))
	}
	last := again.Lines[len(again.Lines)-1].Line
	p3 := s.ReadPodHistory(after(req, again))
	if p3.Page != 3 || p3.Lines[0].Line == last || p3.Lines[len(p3.Lines)-1].Line != "x 2499" || !p3.Done {
		t.Fatalf("page 3: %d %q", p3.Page, p3.Lines[0].Line)
	}
	if n := len(p1.Lines) + len(again.Lines) + len(p3.Lines); n != 2500 {
		t.Fatalf("delivered %d of 2500", n)
	}
	// another page set's cursor, though its page number is the view's last;
	// then a page this view took long ago
	for _, stale := range []LogHistoryPageDTO{{Set: p1.Set + 99, Page: 3}, p1} {
		if r := s.ReadPodHistory(after(req, stale)); !r.Reset || r.Lines[0].Line != "x 0" || r.Set == p1.Set {
			t.Fatalf("a cursor the view does not hold (%d/%d): reset %v", stale.Set, stale.Page, r.Reset)
		}
	}
}

// blockingHistory holds a next page's read until released, telling the
// test its context.
type blockingHistory struct {
	*fakeHistory
	block   atomic.Bool
	entered chan context.Context
	release chan struct{}
}

func (f *blockingHistory) Query(ctx context.Context, q string, start, end int64, limit int) ([]loki.Entry, error) {
	es, err := f.fakeHistory.Query(ctx, q, start, end, limit)
	if f.block.CompareAndSwap(true, false) {
		f.entered <- ctx
		<-f.release
	}
	return es, err
}

// The owner fence (Codex's review of f4cce2f): a request a newer one
// replaces mid-page delivers nothing and keeps nothing; the newer request,
// from the same cursor, reads the page the replaced one never delivered.
func TestHistoryReplacedOwnerKeepsNothing(t *testing.T) {
	f := &blockingHistory{fakeHistory: otelFake(), entered: make(chan context.Context, 1), release: make(chan struct{})}
	f.entries[q(podBranch, loki.ModeName, "")] = lines(1500, tNow.Add(-time.Minute), "", "x")
	s := NewLogHistoryService(func(string) (HistoryReader, error) { return f, nil })
	s.now = func() time.Time { return tNow }
	req := PodHistoryRequestDTO{Cluster: "c", View: "owner", Restart: true, Namespace: "team", Pod: "api-1"}
	p := s.ReadPodHistory(req)
	f.block.Store(true)
	old, next := make(chan LogHistoryPageDTO, 1), make(chan LogHistoryPageDTO, 1)
	go func() { old <- s.ReadPodHistory(after(req, p)) }()
	ctx := <-f.entered
	go func() { next <- s.ReadPodHistory(after(req, p)) }()
	<-ctx.Done() // the newer request replaced the read in flight
	close(f.release)
	a, b := <-old, <-next
	if a.Error != "a newer request for this view replaced this one" || len(a.Lines) != 0 || a.Page != 0 {
		t.Fatalf("the replaced owner: %q, %d lines, page %d", a.Error, len(a.Lines), a.Page)
	}
	if b.Error != "" || b.Reset || len(b.Lines) != 500 || b.Lines[0].Line != "x 1000" || b.Page != 2 || !b.Done {
		t.Fatalf("the newer owner: %q reset %v, %d lines, page %d", b.Error, b.Reset, len(b.Lines), b.Page)
	}
}

// jsonStringLen is what encoding/json writes, on every byte shape.
func TestJSONStringLen(t *testing.T) {
	cases := []string{"", "plain", "<a href=\"x\">&amp;</a>", "\x00\x01\x1f\x7f\b\f\n\r\t\\", "€ \u2028 \u2029 ✓", "\xff\xfe bad \xe2\x82", strings.Repeat("\x00", 1000)}
	for i := 0; i < 256; i++ {
		cases = append(cases, string([]byte{byte(i)}), "a"+string([]byte{byte(i), 0x80})+"z")
	}
	for _, c := range cases {
		enc, _ := json.Marshal(c)
		// exact on valid UTF-8; an invalid byte counts the longer of its two
		// encodings (\ufffd escaped, or the replacement rune)
		if got := jsonStringLen(c); got < len(enc) || utf8.ValidString(c) && got != len(enc) {
			t.Fatalf("%q: %d, json.Marshal %d", c, got, len(enc))
		}
	}
}
