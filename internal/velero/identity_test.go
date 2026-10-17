package velero

import (
	"testing"
	"time"
)

var (
	t0     = time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	sUID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	daily  = &Schedule{Meta: Meta{Namespace: "velero", Name: "daily", UID: sUID, Created: t0.Add(-48 * time.Hour)}}
	amster = mustZone("Europe/Amsterdam")
)

func mustZone(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func bk(name string, created, start time.Time) *Backup {
	return &Backup{Meta: Meta{Namespace: "velero", Name: name, Created: created}, ScheduleLabel: "daily", Start: start}
}

func TestRunName(t *testing.T) {
	if n := RunName("daily", t0, time.UTC); n != "daily-20261017020000" {
		t.Fatal(n)
	}
	// the controller names runs in the server's zone
	if n := RunName("daily", t0, amster); n != "daily-20261017040000" {
		t.Fatal(n)
	}
}

func TestTiming(t *testing.T) {
	cases := map[Timing]*Backup{
		TimingLocal:    bk("a", t0, t0.Add(3*time.Second)),
		TimingDisputed: bk("b", t0, t0.Add(-30*time.Second)), // a server 30 s behind
		TimingSynced:   bk("c", t0, t0.Add(-3*time.Hour)),
	}
	for want, b := range cases {
		if got := b.Timing(); got != want {
			t.Errorf("%s: %v, want %v", b.Name, got, want)
		}
	}
	if (&Backup{Meta: Meta{Created: t0}}).Timing() != TimingLocal {
		t.Fatal("never started reads local")
	}
}

func TestExecutionTime(t *testing.T) {
	if at, ok := bk("x", t0, t0.Add(time.Minute)).ExecutionTime("daily", time.UTC); !ok || !at.Equal(t0.Add(time.Minute)) {
		t.Fatalf("start: %v %v", at, ok)
	}
	// a FailedValidation run: the controller's name, in the server's zone
	fv := bk("daily-20261017040000", t0, time.Time{})
	if at, ok := fv.ExecutionTime("daily", amster); !ok || !at.Equal(t0) {
		t.Fatalf("name in Amsterdam: %v %v", at, ok)
	}
	if _, ok := fv.ExecutionTime("daily", nil); ok {
		t.Fatal("no zone, no name time")
	}
	for _, name := range []string{"maintenance-copy", "daily-2026101704000", "daily-x0261017040000", "weekly-20261017040000"} {
		if _, ok := bk(name, t0, time.Time{}).ExecutionTime("daily", time.UTC); ok {
			t.Errorf("%s: order should be unknown", name)
		}
	}
}

func TestAttribute(t *testing.T) {
	n := RunName("daily", t0, time.UTC)
	nRun := bk(n, t0.Add(-time.Hour), t0.Add(-time.Hour)) // re-imported N: still N
	nRun.Start = t0.Add(-time.Hour)
	owned := bk("manual", t0, t0.Add(time.Second))
	owned.ScheduleLabel = ""
	owned.Owners = []Owner{{UID: sUID, Name: "daily"}}
	other := bk("daily-20261016020000", t0, t0.Add(time.Second))
	other.Owners = []Owner{{UID: "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "daily"}}
	incomplete := bk("daily-20261015020000", t0, t0.Add(time.Second))
	incomplete.OwnersIncomplete = true
	named := bk("daily-20261016020000", t0.Add(-24*time.Hour), t0.Add(-24*time.Hour+time.Second))
	synced := bk("daily-20261014020000", t0, t0.Add(-72*time.Hour))
	earlier := bk("daily-20261010020000", t0.Add(-7*24*time.Hour), t0.Add(-7*24*time.Hour))
	wrongNS := bk("daily-20261016030000", t0, t0.Add(time.Second))
	wrongNS.Namespace = "other"
	unknownOrder := bk("maintenance-copy", t0, time.Time{})
	cases := []struct {
		b    *Backup
		want Attribution
	}{
		{nRun, Proven}, {owned, Proven}, {other, NotRun}, {incomplete, NotRun},
		{named, Named}, {synced, NotRun}, {earlier, NotRun}, {wrongNS, NotRun}, {unknownOrder, NotRun},
	}
	for _, c := range cases {
		if got := Attribute(daily, c.b, n, time.UTC); got != c.want {
			t.Errorf("%s: %v, want %v", c.b.Name, got, c.want)
		}
	}
	// N needs the schedule's label too
	impostor := bk(n, t0, t0)
	impostor.ScheduleLabel = ""
	if Attribute(daily, impostor, n, time.UTC) != NotRun {
		t.Fatal("an unlabelled object with N's name is not N")
	}
}
