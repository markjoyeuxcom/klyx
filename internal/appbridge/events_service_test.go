package appbridge

import (
	"context"
	"testing"

	"github.com/moomora/klyx/internal/workloads"
)

// fakeEventsConn satisfies EventsConn without fleet dependency.
type fakeEventsConn struct {
	events    []workloads.EventSummary
	truncated bool
	err       error
}

func (f *fakeEventsConn) ListEvents(_ context.Context, _ string) ([]workloads.EventSummary, bool, error) {
	return f.events, f.truncated, f.err
}

func (f *fakeEventsConn) WatchDirty(context.Context, string, []string, func(), func(bool)) (func(), error) {
	return func() {}, nil
}

// --- cluster miss ---

func TestEventsService_ClusterMiss_NonNilEmpties(t *testing.T) {
	svc := NewEventsService(func(string) (EventsConn, bool) { return nil, false }, nil)
	dto := svc.ListEvents("nope", "")
	if dto.Events == nil || dto.Namespaces == nil {
		t.Fatal("slices must be non-nil on cluster miss")
	}
	if len(dto.Events) != 0 || len(dto.Namespaces) != 0 {
		t.Fatalf("want empty slices, got events=%d ns=%d", len(dto.Events), len(dto.Namespaces))
	}
}

// --- truncation ---

func TestEventsService_ListEvents_TruncationSurfaced(t *testing.T) {
	conn := &fakeEventsConn{events: []workloads.EventSummary{{Type: "Normal", Reason: "x"}}, truncated: true}
	svc := NewEventsService(func(string) (EventsConn, bool) { return conn, true }, nil)
	dto := svc.ListEvents("c", "")
	if !dto.Truncated {
		t.Fatal("a capped list must report Truncated - the subset is not the full event set")
	}
}

// --- mapping ---

func TestEventsService_ListEvents_Mapping(t *testing.T) {
	events := []workloads.EventSummary{
		{
			Type: "Warning", Reason: "BackOff", Message: "crash loop",
			Count: 5, Namespace: "team", Kind: "Pod", Name: "api",
			LastSeenUnix: 300, FirstSeenUnix: 100,
		},
		{
			Type: "Normal", Reason: "Pulled", Message: "pulled image",
			Count: 1, Namespace: "team", Kind: "Pod", Name: "web",
			LastSeenUnix: 200, FirstSeenUnix: 150,
		},
	}
	conn := &fakeEventsConn{events: events}
	svc := NewEventsService(func(string) (EventsConn, bool) { return conn, true }, nil)

	all := svc.ListEvents("c", "")
	if len(all.Events) != 2 {
		t.Fatalf("want 2 events, got %d", len(all.Events))
	}

	// Verify field mapping on first row.
	r := all.Events[0]
	if r.Type != "Warning" {
		t.Errorf("Type: got %q, want Warning", r.Type)
	}
	if r.Reason != "BackOff" {
		t.Errorf("Reason: got %q, want BackOff", r.Reason)
	}
	if r.Message != "crash loop" {
		t.Errorf("Message: got %q", r.Message)
	}
	if r.Count != 5 {
		t.Errorf("Count: got %d, want 5", r.Count)
	}
	if r.Namespace != "team" {
		t.Errorf("Namespace: got %q", r.Namespace)
	}
	if r.Kind != "Pod" {
		t.Errorf("Kind: got %q", r.Kind)
	}
	if r.Name != "api" {
		t.Errorf("Name: got %q", r.Name)
	}
	if r.LastSeenUnix != 300 {
		t.Errorf("LastSeenUnix: got %d", r.LastSeenUnix)
	}
	if r.FirstSeenUnix != 100 {
		t.Errorf("FirstSeenUnix: got %d", r.FirstSeenUnix)
	}
}

// --- namespaces only on all-ns load ---

func TestEventsService_Namespaces_OnlyOnAllNs(t *testing.T) {
	events := []workloads.EventSummary{
		{Type: "Warning", Reason: "X", Namespace: "b", Kind: "Pod", Name: "p1", Count: 1},
		{Type: "Normal", Reason: "Y", Namespace: "a", Kind: "Pod", Name: "p2", Count: 1},
	}
	conn := &fakeEventsConn{events: events}
	svc := NewEventsService(func(string) (EventsConn, bool) { return conn, true }, nil)

	all := svc.ListEvents("c", "")
	if len(all.Namespaces) != 2 {
		t.Errorf("all-ns: want 2 namespaces, got %d: %v", len(all.Namespaces), all.Namespaces)
	}
	if len(all.Namespaces) >= 2 && (all.Namespaces[0] != "a" || all.Namespaces[1] != "b") {
		t.Errorf("namespaces not sorted: %v", all.Namespaces)
	}

	scoped := svc.ListEvents("c", "b")
	if len(scoped.Namespaces) != 0 {
		t.Errorf("scoped: namespaces must be empty, got %v", scoped.Namespaces)
	}
}

// --- error returns empties ---

func TestEventsService_Error_ReturnsEmpties(t *testing.T) {
	conn := &fakeEventsConn{err: context.DeadlineExceeded}
	svc := NewEventsService(func(string) (EventsConn, bool) { return conn, true }, nil)
	dto := svc.ListEvents("c", "")
	if dto.Events == nil || dto.Namespaces == nil {
		t.Fatal("slices must be non-nil on error")
	}
	if len(dto.Events) != 0 {
		t.Errorf("want 0 events on error, got %d", len(dto.Events))
	}
	// Review 2026-10-03 #10: a failed read must say so, never pass as "no events".
	if dto.Error == "" {
		t.Error("a failed list must carry Error - empty must not read as quiet")
	}
}
