package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
)

// The one gate AI Explain and the chat verdict map share (S2b §3): a
// listed Backup that is not counted is never Klyx's verdict, even though a
// zero evaluation would read as a fault.
func TestFactRowsAreNeverVerdicts(t *testing.T) {
	b := &velero.Backup{Meta: velero.Meta{Namespace: "velero", Name: "daily-1", UID: "u1"}, Phase: "Failed"}
	set := healthsrc.RowSet[healthsrc.VeleroBackupRow]{Rows: []healthsrc.Row[healthsrc.VeleroBackupRow]{
		{Ref: healthsrc.RowRef{UID: "u1", GVR: "velero.io/v1/backups"}, Data: healthsrc.VeleroBackupRow{Backup: b}},
		{Ref: healthsrc.RowRef{UID: "u2", GVR: "velero.io/v1/backups"}, Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "Failed"},
			Data: healthsrc.VeleroBackupRow{Backup: b, Counted: true}},
	}}
	rows := observe.Rows{Task: healthsrc.TaskVelero, Part: observe.Part{ID: "backups", Classifier: "native"}, EvaluatedAt: time.Now(), Rows: set}
	fact, ok := findRow(set, "u1")
	if !ok {
		t.Fatal("no row")
	}
	if _, err := anchorVerdict(rows, fact); !errors.Is(err, errFactRow) {
		t.Fatalf("a fact row anchored a verdict: %v", err)
	}
	if _, ok := verdictOf(rows, fact, new(int)); ok {
		t.Fatal("chat read a fact row as a verdict")
	}
	counted, _ := findRow(set, "u2")
	if v, err := anchorVerdict(rows, counted); err != nil || v.Label != "Failed" {
		t.Fatalf("a counted row: %+v %v", v, err)
	}
	n := 0
	forEachRow(set, func(r capturedRow) {
		if _, ok := verdictOf(rows, r, new(int)); ok {
			n++
		}
	})
	if n != 1 {
		t.Fatalf("%d verdicts, want the counted row's only", n)
	}
}

type fakeVeleroConn struct {
	res      fleet.VeleroRestores
	err      error
	rows     map[string]observe.Rows
	rowsDown bool
}

func (f *fakeVeleroConn) ListVeleroRestores(context.Context) (fleet.VeleroRestores, error) {
	return f.res, f.err
}
func (f *fakeVeleroConn) HealthRowsNow(_ context.Context, task, part string) (observe.Rows, bool) {
	if f.rowsDown {
		return observe.Rows{}, false
	}
	r, ok := f.rows[task+"/"+part]
	return r, ok
}

func backupsRows(cov observe.KindCoverage, names ...string) observe.Rows {
	var rs []healthsrc.Row[healthsrc.VeleroBackupRow]
	for _, n := range names {
		b := &velero.Backup{Meta: velero.Meta{Namespace: "velero", Name: n, UID: "u-" + n}, Phase: "Completed", ScheduleLabel: "daily"}
		rs = append(rs, healthsrc.Row[healthsrc.VeleroBackupRow]{Ref: healthsrc.RowRef{UID: b.UID}, Data: healthsrc.VeleroBackupRow{Backup: b,
			Attribution: velero.Attr{Schedule: "daily", How: "proven"}}})
	}
	cov.Kind = healthsrc.KindVeleroBackups
	return observe.Rows{Task: healthsrc.TaskVelero, Part: observe.Part{ID: "backups", Kinds: []observe.KindCoverage{cov}},
		Rows: healthsrc.RowSet[healthsrc.VeleroBackupRow]{Rows: rs}}
}

func restoreU(name, backup string, extra map[string]any) unstructured.Unstructured {
	spec := map[string]any{"backupName": backup}
	for k, v := range extra {
		spec[k] = v
	}
	return unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "Restore",
		"metadata": map[string]any{"namespace": "velero", "name": name, "uid": "r-" + name}, "spec": spec,
		"status": map[string]any{"phase": "Failed", "failureReason": "restore execution failed: Authorization: Bearer " + s2aNeedle}}}
}

// The source join reads the Backups kind's own coverage (S2b §3): a capped
// or stale absence is never "not in the cluster", and a failure to read the
// Backups is told apart from a failure to list Restores.
func TestVeleroRestoresSourceJoin(t *testing.T) {
	good := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	items := []unstructured.Unstructured{restoreU("r1", "daily-1", nil), restoreU("r2", "gone", nil), restoreU("r3", "", nil),
		restoreU("r4", "Bad_Name", map[string]any{"hooks": map[string]any{"resources": []any{map[string]any{"name": s2aNeedle}}}})}
	res := fleet.VeleroRestores{Served: true, Restores: velero.ProjectRestores(items), More: true, ReadAt: good}
	cases := []struct {
		cov     observe.KindCoverage
		down    bool
		backups string
		r1, r2  string
	}{
		{observe.KindCoverage{Coverage: observe.Live}, false, "live", "found", "not-found"},
		{observe.KindCoverage{Coverage: observe.Live, Truncated: true}, false, "capped", "found", "not-retained"},
		{observe.KindCoverage{Coverage: observe.Stale, LastGoodAt: &good}, false, "stale", "found", "not-in-last-read"},
		{observe.KindCoverage{Coverage: observe.Checking}, false, "unreadable", "unreadable", "unreadable"},
		{observe.KindCoverage{Coverage: observe.Unreadable, Class: "forbidden"}, false, "unreadable", "unreadable", "unreadable"},
		{observe.KindCoverage{}, true, "unreadable", "unreadable", "unreadable"},
	}
	for _, c := range cases {
		conn := &fakeVeleroConn{res: res, rowsDown: c.down, rows: map[string]observe.Rows{"velero/backups": backupsRows(c.cov, "daily-1")}}
		svc := NewVeleroService(func(string) (VeleroConn, bool) { return conn, true })
		out := svc.GetVeleroRestores("c1", "read-7")
		if out.Cluster != "c1" || out.ReadID != "read-7" || out.State != "read" || out.Backups != c.backups || !out.More || out.Remaining != nil {
			t.Fatalf("%v: %+v", c.cov, out)
		}
		by := map[string]VeleroRestoreDTO{}
		for _, r := range out.Restores {
			by[r.Name] = r
		}
		if by["r1"].Source.State != c.r1 || by["r2"].Source.State != c.r2 || by["r3"].Source.State != "none" || by["r4"].Source.State != "invalid" {
			t.Fatalf("%v: %+v / %+v", c.cov, by["r1"].Source, by["r2"].Source)
		}
		if c.r1 == "found" && (by["r1"].Source.Attribution != "proven" || by["r1"].Source.Backup == nil) {
			t.Fatalf("found: %+v", by["r1"].Source)
		}
		if c.backups == "stale" && (by["r1"].Source.AsOf == nil || by["r2"].Source.AsOf == nil) {
			t.Fatal("stale reads name their time")
		}
		if b := rawJSONOf(t, out); strings.Contains(b, s2aNeedle) {
			t.Fatalf("disclosed: %s", b)
		}
	}
	// a failure to list Restores is the read's error, never a join state
	conn := &fakeVeleroConn{err: errors.New("boom"), rows: map[string]observe.Rows{"velero/backups": backupsRows(observe.KindCoverage{Coverage: observe.Live}, "daily-1")}}
	svc := NewVeleroService(func(string) (VeleroConn, bool) { return conn, true })
	if out := svc.GetVeleroRestores("c1", "x"); out.State != "error" || len(out.Restores) != 0 || out.Backups != "" {
		t.Fatalf("%+v", out)
	}
	// absent and forbidden are said, never empty
	for _, r := range []fleet.VeleroRestores{{}, {Served: true, Forbidden: true}} {
		conn.err, conn.res = nil, r
		out := svc.GetVeleroRestores("c1", "x")
		if out.State != map[bool]string{false: "not-served", true: "forbidden"}[r.Served] {
			t.Fatalf("%+v", out)
		}
	}
	// the server's count is carried only when supplied
	n := int64(42)
	conn.res = fleet.VeleroRestores{Served: true, More: true, Remaining: &n}
	if out := svc.GetVeleroRestores("c1", "x"); out.Remaining == nil || *out.Remaining != 42 {
		t.Fatalf("%+v", out)
	}
}

// The fleet read names a cluster without an Observer or without Velero.
func TestVeleroBackupHorizon(t *testing.T) {
	none := NewVeleroService(func(string) (VeleroConn, bool) { return nil, false })
	if out := none.GetBackupHorizon("gone"); out.State != "no-observer" || out.Schedules == nil {
		t.Fatalf("%+v", out)
	}
	conn := &fakeVeleroConn{rows: map[string]observe.Rows{"velero/schedules": {Part: observe.Part{ID: "schedules", Coverage: observe.Absent}}}}
	svc := NewVeleroService(func(string) (VeleroConn, bool) { return conn, true })
	if out := svc.GetBackupHorizon("c1"); out.State != "no-velero" {
		t.Fatalf("%+v", out)
	}
	at := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	s := &velero.Schedule{Meta: velero.Meta{Namespace: "velero", Name: "daily", UID: "s1"}}
	conn.rows["velero/schedules"] = observe.Rows{EvaluatedAt: at, Part: observe.Part{ID: "schedules", Coverage: observe.Live},
		Rows: healthsrc.RowSet[healthsrc.VeleroScheduleRow]{Rows: []healthsrc.Row[healthsrc.VeleroScheduleRow]{{Ref: healthsrc.RowRef{UID: "s1"},
			Data: healthsrc.VeleroScheduleRow{Schedule: s, Verdict: velero.ScheduleVerdict{Horizon: velero.Horizon{State: velero.HorizonNone}}}}}}}
	out := svc.GetBackupHorizon("c1")
	if out.State != "read" || out.AsOf == nil || !out.AsOf.Equal(at) || len(out.Schedules) != 1 || out.Schedules[0].Horizon.Text != "no successful backup retained" {
		t.Fatalf("%+v", out)
	}
}

// The serialised Restore, source join included, stays within the bound at
// the worst case the CRD allows; every count stays whole (Codex's S2b
// implementation review, finding 5).
func TestVeleroRestoreDTOWorstCase(t *testing.T) {
	name := func(c string, i int) string { return fmt.Sprintf("%s%02d", strings.Repeat(c, 61), i) }
	mapping := map[string]any{}
	var inc, exc, ve []any
	for i := 0; i < 100; i++ {
		mapping[name("n", i)] = name("m", i)
		inc, exc = append(inc, name("i", i)), append(exc, name("e", i))
	}
	for i := 0; i < 10; i++ {
		ve = append(ve, "Validation error in resource modifiers provided in configmap velero/x: bad")
	}
	long := strings.Repeat("b", 253)
	u := restoreU("r1", long, map[string]any{"scheduleName": strings.Repeat("s", 253), "includedNamespaces": inc, "excludedNamespaces": exc,
		"namespaceMapping": mapping, "existingResourcePolicy": strings.Repeat("x", 300), "itemOperationTimeout": strings.Repeat("y", 300),
		"resourceModifier": map[string]any{"kind": "ConfigMap", "name": strings.Repeat("m", 253)},
		"resourcePolicy":   map[string]any{"kind": "ConfigMap", "name": strings.Repeat("p", 253)}})
	u.Object["metadata"].(map[string]any)["name"] = strings.Repeat("r", 253)
	u.Object["status"].(map[string]any)["validationErrors"] = ve
	r := velero.ProjectRestore(&u)
	now := time.Now()
	b := &velero.Backup{Meta: velero.Meta{Namespace: "velero", Name: long, UID: "11111111-2222-3333-4444-555555555555", Created: now}, Phase: "Completed",
		Start: now, Completion: now, Errors: 9, Warnings: 9}
	bk := veleroBackups{state: "stale", asOf: &now, byName: map[string]healthsrc.VeleroBackupRow{"velero/" + long: {Backup: b, Expired: true,
		Attribution: velero.Attr{Schedule: strings.Repeat("s", 253), How: "named", Coverage: velero.SchedulesIncomplete}}}}
	d := veleroRestoreDTO(r, bk)
	raw, _ := json.Marshal(d)
	if len(raw) > velero.MaxObject {
		t.Fatalf("final DTO %d > %d", len(raw), velero.MaxObject)
	}
	if !d.Cut || d.MappingCount != 100 || d.IncludedCount != 100 || d.ExcludedCount != 100 || d.ValidationCount != 10 || d.Source.State != "found" {
		t.Fatalf("cut=%v counts %d %d %d %d source %s", d.Cut, d.MappingCount, d.IncludedCount, d.ExcludedCount, d.ValidationCount, d.Source.State)
	}
	// a small Restore is never cut
	small := restoreU("r2", "daily-1", nil)
	if s := veleroRestoreDTO(velero.ProjectRestore(&small), bk); s.Cut {
		t.Fatal("cut a small restore")
	}
}
