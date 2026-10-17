package healthsrc

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
)

func veleroObj(kind, name string, created time.Time, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	md := map[string]any{"name": name, "namespace": "velero", "uid": "uid-" + kind + "-" + name, "creationTimestamp": created.UTC().Format(time.RFC3339)}
	if labels != nil {
		md["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": kind, "metadata": md, "spec": spec, "status": status}}
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func veleroFixture(now time.Time) []runtime.Object {
	lb := now.Add(-time.Hour).Truncate(time.Second)
	week := now.Add(-7 * 24 * time.Hour)
	return []runtime.Object{
		veleroObj("Schedule", "daily", week, nil, map[string]any{"schedule": "@every 2h", "template": map[string]any{}},
			map[string]any{"phase": "Enabled", "lastBackup": rfc(lb)}),
		veleroObj("Schedule", "broken", week, nil, map[string]any{"schedule": "0 2 * *", "template": map[string]any{"storageLocation": "dr"}},
			map[string]any{"phase": "FailedValidation", "validationErrors": []any{"invalid schedule: expected exactly 5 fields"}}),
		veleroObj("Schedule", "paused", week, nil, map[string]any{"schedule": "0 3 * * *", "paused": true, "template": map[string]any{}},
			map[string]any{"phase": "Enabled"}),
		veleroObj("BackupStorageLocation", "default", week, nil, map[string]any{"provider": "aws", "default": true, "objectStorage": map[string]any{"bucket": "b"}},
			map[string]any{"phase": "Available", "lastValidationTime": rfc(now.Add(-30 * time.Second))}),
		veleroObj("BackupStorageLocation", "dr", week, nil, map[string]any{"provider": "aws", "objectStorage": map[string]any{"bucket": "dr"}},
			map[string]any{"phase": "Unavailable", "message": `BackupStorageLocation "dr" is unavailable: AccessDenied S2aLABneedle`}),
		// N, the run Velero created at lastBackup
		veleroObj("Backup", "daily-"+lb.UTC().Format("20060102150405"), lb, map[string]any{velero.LabelSchedule: "daily"}, map[string]any{"storageLocation": "default"},
			map[string]any{"phase": "Completed", "startTimestamp": rfc(lb.Add(time.Second)), "completionTimestamp": rfc(lb.Add(time.Minute)),
				"expiration": rfc(lb.Add(720 * time.Hour))}),
		// a manual backup that failed: unscheduled, counted
		veleroObj("Backup", "pre-upgrade", now.Add(-2*time.Hour), nil, map[string]any{"storageLocation": "dr"},
			map[string]any{"phase": "FailedValidation", "validationErrors": []any{"backup can't be created because BackupStorageLocation dr is in Unavailable status."},
				"expiration": rfc(now.Add(-time.Hour))}),
		// synced from object storage: never counted
		veleroObj("Backup", "daily-20260101020000", now.Add(-10*time.Minute), map[string]any{velero.LabelSchedule: "daily"}, map[string]any{"storageLocation": "default"},
			map[string]any{"phase": "Failed", "startTimestamp": rfc(now.Add(-30 * 24 * time.Hour))}),
	}
}

func veleroServed(_ context.Context, group, _, _ string) (observe.Probe, error) {
	if group == velero.Group {
		return observe.Probe{Served: true, Version: velero.APIVersion}, nil
	}
	return observe.Probe{}, nil
}

func startVelero(t *testing.T, objs ...runtime.Object) (*sink, *observe.Observer) {
	t.Helper()
	lists := map[schema.GroupVersionResource]string{
		{Group: velero.Group, Version: velero.APIVersion, Resource: KindVeleroSchedules}: "ScheduleList",
		{Group: velero.Group, Version: velero.APIVersion, Resource: KindVeleroLocations}: "BackupStorageLocationList",
		{Group: velero.Group, Version: velero.APIVersion, Resource: KindVeleroBackups}:   "BackupList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
	kinds, domains := Velero(NativeDeps{Cluster: "c1", Dyn: dyn, Served: veleroServed})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: 10 * time.Millisecond})
	o.SetFact(FactVelero, VeleroServers(capability.VeleroCapability{Present: true, ServersRead: true,
		Servers: []capability.VeleroServer{{Namespace: "velero", Name: "velero", Ready: true, RolledOut: true, Image: "velero/velero:v1.18.4"}}}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	for _, p := range []string{"schedules", "locations", "backups"} {
		o.SubscribeRows(TaskVelero, p, observe.Owner{Kind: "lens", ID: "t"})
	}
	return s, o
}

// Coverage S2a §3 and §4 through the observer.
func TestVeleroParts(t *testing.T) {
	s, o := startVelero(t, veleroFixture(time.Now())...)
	waitFor(t, "three parts counted", func() bool {
		sc, ok1 := nativePart(s, TaskVelero, "schedules")
		lo, ok2 := nativePart(s, TaskVelero, "locations")
		bk, ok3 := nativePart(s, TaskVelero, "backups")
		return ok1 && ok2 && ok3 && sc.Native != nil && lo.Native != nil && bk.Native != nil &&
			sc.Native.Total == 3 && sc.Native.Included == 1 && lo.Native.Total == 2 && lo.Native.Included == 1 &&
			bk.Native.Total == 1 && bk.Native.Included == 1
	})
	rows, ok := o.RowsNow(TaskVelero, "schedules")
	if !ok {
		t.Fatal("no schedule rows")
	}
	by := map[string]Row[VeleroScheduleRow]{}
	for _, r := range rows.Rows.(RowSet[VeleroScheduleRow]).Rows {
		by[r.Data.Schedule.Name] = r
	}
	daily := by["daily"]
	if daily.Eval.Label != "Completed" || daily.Data.Verdict.NState != "found" || daily.Data.Verdict.Synced != 1 {
		t.Fatalf("daily: %+v / %+v", daily.Eval, daily.Data.Verdict)
	}
	if l := daily.Data.Location; l.State != "found" || l.Name != "default" || !l.ByDefault || l.Label != "Available" {
		t.Fatalf("daily's location block: %+v", l)
	}
	if b := by["broken"]; b.Eval.Label != "FailedValidation" || b.Data.Location.Label != "Unavailable" {
		t.Fatalf("broken: %+v %+v", b.Eval, b.Data.Location)
	}
	if p := by["paused"]; p.Eval.Label != "Paused" {
		t.Fatalf("paused: %+v", p.Eval)
	}

	lrows, _ := o.RowsNow(TaskVelero, "locations")
	for _, r := range lrows.Rows.(RowSet[VeleroLocationRow]).Rows {
		switch r.Data.Location.Name {
		case "default":
			if len(r.Data.Schedules) != 2 || r.Data.Expired != 0 {
				t.Fatalf("default: %+v", r.Data)
			}
		case "dr":
			if len(r.Data.Schedules) != 1 || r.Data.Expired != 1 || r.Eval.Label != "Unavailable" {
				t.Fatalf("dr: %+v %+v", r.Eval, r.Data)
			}
		}
	}
	brows, _ := o.RowsNow(TaskVelero, "backups")
	if bs := brows.Rows.(RowSet[VeleroBackupRow]).Rows; len(bs) != 1 || bs[0].Data.Backup.Name != "pre-upgrade" || bs[0].Data.Membership.Why != "unattributed" {
		t.Fatalf("backups rows: %+v", bs)
	}
}

// The detector's servers onto the rules' facts (S2a §3): configuration
// only from a rolled-out server, a valueFrom TZ unresolved, and servers in
// one namespace that disagree unresolved.
func TestVeleroServers(t *testing.T) {
	v := VeleroServers(capability.VeleroCapability{ServersRead: true, Servers: []capability.VeleroServer{
		{Namespace: "a", Name: "velero", Ready: true, RolledOut: true, Image: "velero/velero:v1.18.4", TZ: "Europe/Amsterdam", TZSet: true, ScheduleDisabled: true},
		{Namespace: "b", Name: "velero", Ready: true, RolledOut: false, Image: "velero/velero:v1.18.4", ScheduleDisabled: true},
		{Namespace: "c", Name: "velero", Ready: true, RolledOut: true, Image: "velero/velero:v1.17.2", TZFromRef: true, TZSet: true},
		{Namespace: "d", Name: "v1", Ready: true, RolledOut: true, Image: "velero/velero:v1.18.4"},
		{Namespace: "d", Name: "v2", Ready: false, RolledOut: true, Image: "velero/velero:v1.17.2"},
	}})
	a := v.ByNamespace["a"]
	if !a.VersionResolved || a.Version.String() != "1.18.4" || a.Zone != "Europe/Amsterdam" || !a.ZoneResolved || !a.ScheduleDisabled {
		t.Fatalf("a: %+v", a)
	}
	if b := v.ByNamespace["b"]; b.VersionResolved || b.ZoneResolved || b.ScheduleDisabled {
		t.Fatalf("a rollout in progress resolves nothing: %+v", b)
	}
	if c := v.ByNamespace["c"]; !c.VersionResolved || c.ZoneResolved {
		t.Fatalf("a valueFrom TZ: %+v", c)
	}
	if d := v.ByNamespace["d"]; d.VersionResolved || !d.ZoneResolved || d.Ready {
		t.Fatalf("two servers disagreeing: %+v", d)
	}
	if !v.Read {
		t.Fatal("read")
	}
}
