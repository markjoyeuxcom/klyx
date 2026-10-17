package velero

import "testing"

// Every listed Backup says how it relates to the current schedules (S2b
// §2), by Attribute's precedence.
func TestAttributions(t *testing.T) {
	s := sched("0 2 * * *", lb)
	n := run(nName, lb, lb, "Completed")
	named := run("daily-x", day(3, 0, 0), day(3, 0, 0), "Completed")
	disputed := run("daily-d", day(4, 0, 30), day(4, 0, 0), "Completed")
	synced := run("daily-s", day(9, 0, 0), day(1, 0, 0), "Completed")
	reimported := owned(run("daily-r", day(9, 0, 0), day(1, 0, 0), "Completed"))
	manual := &Backup{Meta: Meta{Namespace: "velero", Name: "pre-upgrade", Created: day(5, 0, 0)}, Start: day(5, 0, 0), Phase: "Completed"}
	foreign := run("daily-f", day(9, 0, 0), day(1, 0, 0), "Completed")
	foreign.Owners = []Owner{{UID: "99999999-2222-3333-4444-555555555555", Name: "daily"}}
	got := Attributions([]*Backup{n, named, disputed, synced, reimported, manual, foreign}, []*Schedule{s}, srvUTC)
	for b, want := range map[*Backup]Attr{
		n: {Schedule: "daily", How: "proven"}, named: {Schedule: "daily", How: "named"},
		disputed: {Schedule: "daily", How: "named", Disputed: true}, synced: {Schedule: "daily", How: "synced"},
		reimported: {Schedule: "daily", How: "proven"}, manual: {}, foreign: {},
	} {
		if got[b] != want {
			t.Errorf("%s: %+v, want %+v", b.Name, got[b], want)
		}
	}
}
