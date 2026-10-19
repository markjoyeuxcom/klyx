package appbridge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/moomora/klyx/internal/loki"
	"github.com/moomora/klyx/internal/safehttp"
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

func (f *fakeHistory) LineageKey() string                   { return f.lineage }
func (f *fakeHistory) Revision() uint64                     { return f.rev }
func (f *fakeHistory) Source() string                       { return "Service logs/loki:http" }
func (f *fakeHistory) Tenant() string                       { return f.tenant }
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
	p2 := s.ReadPodHistory(req)
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
	if p := s.ReadPodHistory(req); len(p.Lines) != 1000 {
		t.Fatalf("first: %d", len(p.Lines))
	}
	f.rev = 2
	p := s.ReadPodHistory(req)
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
