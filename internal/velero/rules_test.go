package velero

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// The fixture: an hourly-ish daily schedule on a v1.18.4 server in UTC.
var (
	day     = func(h, m, s int) time.Time { return time.Date(2026, 10, 17, h, m, s, 0, time.UTC) }
	created = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	srvUTC  = Servers{Read: true, ByNamespace: map[string]ServerFacts{"velero": {Ready: true, Server: v1184.withZone("UTC")}}}
)

func (s Server) withZone(z string) Server { s.Zone, s.ZoneResolved = z, true; return s }

func sched(cron string, lastBackup time.Time) *Schedule {
	return &Schedule{Meta: Meta{Namespace: "velero", Name: "daily", UID: sUID, Created: created}, Cron: cron, Phase: "Enabled", LastBackup: lastBackup}
}

// run is a local Backup of the schedule, created at c, started at st (zero:
// never started), in phase.
func run(name string, c, st time.Time, phase string) *Backup {
	b := &Backup{Meta: Meta{Namespace: "velero", Name: name, Created: c}, ScheduleLabel: "daily", Start: st, Phase: phase}
	if phase == "Failed" {
		cl, _ := Classify(FieldBackupFailure, "backup execution failed: AccessDenied")
		b.Failure = &cl
	}
	return b
}

func owned(b *Backup) *Backup { b.Owners = []Owner{{UID: sUID, Name: "daily"}}; return b }

func complete(bs ...*Backup) History { return History{Backups: bs, Readable: true, Complete: true} }

func capped(w time.Time, tier0 bool, bs ...*Backup) History {
	return History{Backups: bs, Readable: true, Window: &Window{W: w, Tier0: tier0}}
}

func judge(t *testing.T, s *Schedule, h History, now time.Time) ScheduleVerdict {
	t.Helper()
	return ScheduleRule(ScheduleInput{S: s, History: h, Servers: srvUTC, Now: now})
}

func want(t *testing.T, v ScheduleVerdict, role health.Kind, label string, reason ...string) {
	t.Helper()
	if v.Eval.Role != role || v.Eval.Label != label {
		t.Fatalf("got %v %q (%s), want %v %q", v.Eval.Role, v.Eval.Label, v.Eval.Reason, role, label)
	}
	for _, r := range reason {
		if !strings.Contains(v.Eval.Reason, r) {
			t.Fatalf("reason %q lacks %q", v.Eval.Reason, r)
		}
	}
	if v.Eval.Included != role.Attention() {
		t.Fatalf("included %v for %v", v.Eval.Included, role)
	}
}

// N for lastBackup 2026-10-16 02:00:05 is daily-20261016020005; the next
// run is due 2026-10-17 02:00, missed from 02:10.
var lb = time.Date(2026, 10, 16, 2, 0, 5, 0, time.UTC)

const nName = "daily-20261016020005"

func TestScheduleRowsOneToEight(t *testing.T) {
	now := day(1, 0, 0)
	s := sched("0 2 * * *", lb)

	v := ScheduleRule(ScheduleInput{S: s, History: complete(), Servers: Servers{Read: true}, Now: now})
	want(t, v, health.Degraded, "Not processed", "Velero processes only its own")
	if v := ScheduleRule(ScheduleInput{S: s, History: complete(), Servers: Servers{}, Now: now}); v.Eval.Label == "Not processed" {
		t.Fatal("an unread server list proves nothing")
	}

	p := sched("0 2 * *", lb)
	p.Paused = true
	want(t, judge(t, p, complete(), now), health.Paused, "Paused", "will fail validation when resumed")

	fv := sched("", lb)
	fv.Phase = "FailedValidation"
	want(t, judge(t, fv, complete(), now), health.Fault, "FailedValidation", "the cron is empty")
	fv.Cron = "0 2 * *"
	want(t, judge(t, fv, complete(), now), health.Fault, "FailedValidation", LibraryGoCron)

	nw := sched("0 2 * * *", time.Time{})
	nw.Phase, nw.Created = "", now.Add(-time.Minute)
	v = judge(t, nw, complete(), now)
	want(t, v, health.Progressing, "Awaiting validation")
	if v.Eval.Next == nil || !v.Eval.Next.Equal(nw.Created.Add(processWait)) {
		t.Fatalf("next %v", v.Eval.Next)
	}
	nw.Created = now.Add(-6 * time.Minute)
	want(t, judge(t, nw, complete(), now), health.Degraded, "Not processed", "not processed by Velero")

	noZone := Servers{Read: true, ByNamespace: map[string]ServerFacts{"velero": {Ready: true}}}
	v = ScheduleRule(ScheduleInput{S: s, History: complete(), Servers: noZone, Now: now})
	want(t, v, health.Unknown, "Unknown time", "time zone is unresolved")

	want(t, judge(t, sched("0 0 31 2 *", lb), complete(), now), health.Degraded, "Never matches")

	// resumed: held for the grace from ResumedAt, then the rules apply
	late := day(3, 0, 0)
	in := ScheduleInput{S: s, History: complete(), Servers: srvUTC, Now: late, Resume: Resume{Awaiting: true, ResumedAt: late.Add(-time.Minute)}}
	want(t, ScheduleRule(in), health.Progressing, "Resumed")
	in.Resume.ResumedAt = late.Add(-Grace)
	want(t, ScheduleRule(in), health.Fault, "Missed")

	want(t, judge(t, s, History{}, now), health.Unknown, "Runs unknown")
}

func TestScheduleRowNineMissedDeferredOverdue(t *testing.T) {
	s := sched("0 2 * * *", lb)
	n := run(nName, lb, lb.Add(time.Second), "Completed")
	before := day(2, 9, 59)
	v := judge(t, s, complete(n), before)
	want(t, v, health.OK, "Completed")
	if v.Eval.Next == nil || !v.Eval.Next.Equal(day(2, 10, 0)) {
		t.Fatalf("next %v, want the missed boundary", v.Eval.Next)
	}
	at := day(2, 10, 0)
	v = judge(t, s, complete(n), at)
	want(t, v, health.Fault, "Missed", "the run due at 2026-10-17 02:00:00Z")
	if v.Eval.Boundary == nil || !v.Eval.Boundary.Equal(at) {
		t.Fatalf("boundary %v", v.Eval.Boundary)
	}

	disabled := Servers{Read: true, ByNamespace: map[string]ServerFacts{"velero": {Ready: true, Server: v1184.withZone("UTC"), ScheduleDisabled: true}}}
	v = ScheduleRule(ScheduleInput{S: s, History: complete(n), Servers: disabled, Now: at})
	want(t, v, health.Fault, "Missed", "schedule controller is disabled")

	// Velero's guard: InProgress defers, Queued does not
	blocker := run("daily-20261016030000", day(-21, 0, 0), time.Time{}, "InProgress")
	blocker.ScheduleLabel, blocker.Owners = "daily", nil
	v = judge(t, s, complete(n, blocker), at)
	want(t, v, health.Degraded, "Deferred", "waits for daily-20261016030000")
	blocker.Phase = "Queued"
	want(t, judge(t, s, complete(n, blocker), at), health.Fault, "Missed")

	// capped: a tier-1 window keeps the guard complete; a tier-0 one does not
	want(t, judge(t, s, capped(day(-48, 0, 0), false, n), at), health.Fault, "Missed")
	want(t, judge(t, s, capped(day(-48, 0, 0), true, n), at), health.Degraded, "Overdue", "more runs are in progress than Klyx holds")

	// a manual run never ends a missed state
	manual := run("adhoc", day(2, 5, 0), day(2, 5, 1), "Completed")
	want(t, judge(t, s, complete(n, manual), at), health.Fault, "Missed")

	// skipImmediately moves the base: nothing owed until the next tick
	s2 := sched("0 2 * * *", lb)
	s2.LastSkipped = day(2, 5, 0) // unpaused with --skip-immediately after the 02:00 tick
	want(t, judge(t, s2, complete(n), at), health.OK, "Completed")
}

func TestScheduleOutcomeAnchoredOnN(t *testing.T) {
	s := sched("0 2 * * *", lb)
	now := day(1, 0, 0)
	older := owned(run("daily-20261015020000", lb.Add(-24*time.Hour), lb.Add(-24*time.Hour), "Completed"))

	want(t, judge(t, s, complete(run(nName, lb, lb.Add(time.Second), "Failed")), now), health.Fault, "Last run failed", "the backup failed while running", "AccessDenied")
	pf := run(nName, lb, lb.Add(time.Second), "PartiallyFailed")
	pf.Errors = 4
	want(t, judge(t, s, complete(pf), now), health.Degraded, "Partially failed", "4 errors")

	// N unseen, then gone; an older success is a fact, never the outcome
	v := judge(t, s, complete(older), lb.Add(30*time.Second))
	want(t, v, health.Progressing, "Run created", "not yet seen")
	v = judge(t, s, complete(older), now)
	want(t, v, health.Unknown, "Last run unknown", "not in the cluster")
	if v.Fact != older {
		t.Fatalf("fact %v", v.Fact)
	}
	failedOlder := owned(run("daily-20261015020000", lb.Add(-24*time.Hour), lb.Add(-24*time.Hour), "Failed"))
	want(t, judge(t, s, complete(failedOlder), now), health.Unknown, "Last run unknown")
	// past W, or a tier-0 window: not retained
	want(t, judge(t, s, capped(lb.Add(time.Minute), false), now), health.Unknown, "Last run unknown", "is not retained")
	want(t, judge(t, s, capped(day(-48, 0, 0), true), now), health.Unknown, "Last run unknown", "is not retained")
	// the zone unresolved: N cannot be identified
	noZone := Servers{Read: true, ByNamespace: map[string]ServerFacts{"velero": {Ready: true, Server: Server{Version: Version{1, 18, 4}, VersionResolved: true}}}}
	pref := sched("CRON_TZ=UTC 0 2 * * *", lb)
	v = ScheduleRule(ScheduleInput{S: pref, History: complete(older), Servers: noZone, Now: now})
	want(t, v, health.Unknown, "Last run unknown", "cannot be identified")

	// a named Completed run newer than a failed N: a fact, still lit
	failedN := run(nName, lb, lb.Add(time.Second), "Failed")
	newer := run("adhoc", lb.Add(time.Hour), lb.Add(time.Hour), "Completed")
	v = judge(t, s, complete(failedN, newer), now)
	want(t, v, health.Fault, "Last run failed")
	if len(v.Newer) != 1 || v.Newer[0] != newer {
		t.Fatalf("newer facts %v", v.Newer)
	}
	// a proven run newer than N decides
	newerProven := owned(run("adhoc", lb.Add(time.Hour), lb.Add(time.Hour), "Completed"))
	want(t, judge(t, s, complete(failedN, newerProven), now), health.OK, "Completed")
}

// Codex's re-review cases: the previous outcome while N runs (revisions 3
// to 6) and re-imported records (revision 3).
func TestSchedulePreviousOutcome(t *testing.T) {
	ten, half, eleven := day(10, 0, 0), day(10, 30, 0), day(11, 0, 0)
	s := sched("0 * * * *", eleven) // hourly; N created 11:00
	n := run("daily-20261017110000", eleven, eleven.Add(time.Second), "InProgress")
	provenFail := owned(run("daily-20261017100000", ten, ten, "Failed"))
	namedOK := run("adhoc", half, half, "Completed")
	now := day(11, 5, 0)

	// uncapped: the proven failure decides, never the named success
	v := judge(t, s, complete(n, provenFail, namedOK), now)
	want(t, v, health.Fault, "Last run failed", "a new run is in progress")
	if v.Previous != provenFail {
		t.Fatalf("previous %v", v.Previous)
	}
	// no proven terminal run: the named one, qualified
	v = judge(t, s, complete(n, namedOK), now)
	want(t, v, health.OK, "Completed", "matched by name")
	// capped with W at 10:20, the failure left out: unknown, never the named success
	v = judge(t, s, capped(day(10, 20, 0), false, n, namedOK), now)
	want(t, v, health.Unknown, "Previous run unknown", "the run before")

	// mixed clocks around W: A ran at 10:20:00, B (FailedValidation, the
	// controller's name 10:20:30) was created at 10:19:30 and is W
	a := owned(run("daily-20261017102000", day(10, 20, 0), day(10, 20, 0), "Completed"))
	w := day(10, 19, 30)
	v = judge(t, s, capped(w, false, n, a), now)
	want(t, v, health.Unknown, "Previous run unknown")
	if v.Fact != a {
		t.Fatalf("A is a fact: %v", v.Fact)
	}
	a.Phase = "Failed"
	want(t, judge(t, s, capped(w, false, n, a), now), health.Unknown, "Previous run unknown")
	// past the margin it decides
	want(t, judge(t, s, capped(day(10, 18, 0), false, n, a), now), health.Fault, "Last run failed")

	// a re-imported older proven success (new creation, old start) after a
	// failed N: N still decides; and the reverse
	failedN := run("daily-20261017110000", eleven, eleven.Add(time.Second), "Failed")
	reimported := owned(run("daily-20261017100000", day(11, 30, 0), ten, "Completed"))
	want(t, judge(t, s, complete(failedN, reimported), day(11, 40, 0)), health.Fault, "Last run failed")
	okN := run("daily-20261017110000", eleven, eleven.Add(time.Second), "Completed")
	oldFail := owned(run("daily-20261017100000", day(11, 30, 0), ten, "Failed"))
	want(t, judge(t, s, complete(okN, oldFail), day(11, 40, 0)), health.OK, "Completed")
}

func TestScheduleCompletenessUnderCap(t *testing.T) {
	s := sched("0 2 * * *", lb)
	n := run(nName, lb, lb.Add(time.Second), "Completed")
	now := day(1, 0, 0)
	want(t, judge(t, s, capped(lb.Add(-2*time.Minute), false, n), now), health.OK, "Completed")
	want(t, judge(t, s, capped(lb.Add(-30*time.Second), false, n), now), health.Unknown, "Last run unknown", "may not be retained")
}

// An older-created, later-started run (the 1.18 queue): retained by its run
// time, it decides.
func TestScheduleQueueOrder(t *testing.T) {
	s := sched("0 2 * * *", lb)
	n := run(nName, lb, lb.Add(time.Second), "Completed")
	late := owned(run("adhoc", lb.Add(-time.Hour), lb.Add(time.Hour), "Failed"))
	want(t, judge(t, s, complete(n, late), day(1, 0, 0)), health.Fault, "Last run failed")
	n.Phase, late.Phase = "Failed", "Completed"
	want(t, judge(t, s, complete(n, late), day(1, 0, 0)), health.OK, "Completed")
}

// Without lastBackup, proven runs decide alone (revision 3).
func TestScheduleProvenBeforeFirstRun(t *testing.T) {
	s := sched("0 3 * * 0", time.Time{}) // weekly, first due Sunday 18 Oct
	s.UseOwnerReferences, s.Created = true, day(9, 0, 0)
	now := day(12, 0, 0)
	want(t, judge(t, s, complete(), now), health.Progressing, "Awaiting first run")
	m := owned(run("maintenance", day(11, 0, 0), day(11, 0, 1), "Failed"))
	want(t, judge(t, s, complete(m), now), health.Fault, "Last run failed")
	m.Phase = "PartiallyFailed"
	want(t, judge(t, s, complete(m), now), health.Degraded, "Partially failed")
}

func TestUnscheduled(t *testing.T) {
	lb8 := day(8, 0, 5)
	s := sched("0 2 * * *", lb8)
	n := run("daily-20261017080005", lb8, lb8.Add(time.Second), "Completed")
	now := day(12, 0, 0)
	manual := run("adhoc", day(9, 0, 0), day(9, 0, 1), "Failed")
	manual.ScheduleLabel = ""
	old := run("old", day(-30, 0, 0), day(-30, 0, 0), "Failed")
	old.ScheduleLabel = ""
	synced := run("daily-20261017040000", day(9, 0, 0), day(4, 0, 0), "Failed")
	disputed := run("other", day(9, 0, 0), day(8, 59, 30), "Failed")
	disputed.ScheduleLabel = ""
	namedNewer := run("from-sched", day(9, 0, 0), day(9, 0, 1), "Failed")
	namedOlder := run("daily-20261017070000", day(7, 0, 0), day(7, 0, 0), "Failed")
	ms := Unscheduled([]*Backup{n, manual, old, synced, disputed, namedNewer, namedOlder}, []*Schedule{s}, srvUTC, now)
	for b, w := range map[*Backup]struct {
		in  bool
		why string
	}{
		n: {false, "proven"}, manual: {true, "unattributed"}, old: {false, "not recent"}, synced: {false, "synced"},
		disputed: {true, "unattributed"}, namedNewer: {true, "named, newer than N"}, namedOlder: {false, "superseded by N"},
	} {
		if m := ms[b]; m.In != w.in || m.Why != w.why {
			t.Errorf("%s: %+v, want %v %q", b.Name, m, w.in, w.why)
		}
	}
	if !ms[disputed].Disputed || !strings.Contains(BackupRule(disputed, ms[disputed], srvUTC, now).Reason, "may have been synced") {
		t.Fatal("disputed timing is counted with its qualifier")
	}
	ev := BackupRule(manual, ms[manual], srvUTC, now)
	if ev.Role != health.Fault || !ev.Included {
		t.Fatalf("%+v", ev)
	}
}

// A proven run of unknown order is counted until a finished proven run of
// known, later execution supersedes it (revisions 4 and 5).
func TestUnscheduledOrderUnknownProven(t *testing.T) {
	s := sched("0 3 * * 0", time.Time{})
	fv := owned(run("maintenance-copy", day(9, 0, 0), time.Time{}, "FailedValidation"))
	now := day(10, 0, 0)
	if m := Unscheduled([]*Backup{fv}, []*Schedule{s}, srvUTC, now)[fv]; !m.In || m.Why != "proven, order unknown" {
		t.Fatalf("%+v", m)
	}
	repl := owned(run("maintenance-2", day(9, 30, 0), day(9, 30, 1), "InProgress"))
	if m := Unscheduled([]*Backup{fv, repl}, []*Schedule{s}, srvUTC, now)[fv]; !m.In {
		t.Fatalf("a replacement that only started supersedes nothing: %+v", m)
	}
	for _, phase := range []string{"Completed", "Failed"} {
		repl.Phase = phase
		if m := Unscheduled([]*Backup{fv, repl}, []*Schedule{s}, srvUTC, now)[fv]; m.In || m.Why != "superseded" {
			t.Fatalf("%s replacement: %+v", phase, m)
		}
	}
}

// The 24-hour window and its clock share the run time (revision 4).
func TestUnscheduledWindowOneBasis(t *testing.T) {
	b := run("adhoc", day(10, 0, 0), day(12, 0, 0), "Failed")
	b.ScheduleLabel = ""
	next := day(12, 0, 0).Add(Recent)
	ev := BackupRule(b, Membership{In: true}, srvUTC, day(13, 0, 0))
	if ev.Next == nil || !ev.Next.Equal(next) {
		t.Fatalf("next %v, want %v", ev.Next, next)
	}
	if m := Unscheduled([]*Backup{b}, nil, srvUTC, day(10, 0, 0).Add(Recent))[b]; !m.In {
		t.Fatalf("still counted at creation + 24h: %+v", m)
	}
	if m := Unscheduled([]*Backup{b}, nil, srvUTC, next)[b]; m.In {
		t.Fatalf("leaves at run time + 24h: %+v", m)
	}
}

func TestLocationRule(t *testing.T) {
	now := day(12, 0, 0)
	loc := func(phase string) *Location {
		return &Location{Meta: Meta{Namespace: "velero", Name: "default", Created: day(0, 0, 0)}, Phase: phase, LastValidation: now.Add(-30 * time.Second)}
	}
	u := loc("Unavailable")
	c, _ := Classify(FieldLocationMessage, `BackupStorageLocation "default" is unavailable: unable to get credentials: AccessDenied `+needle)
	u.Message = &c
	ev := LocationRule(u, srvUTC, now)
	if ev.Role != health.Fault || !strings.Contains(ev.Reason, "the credentials could not be read") || strings.Contains(ev.Reason, needle) {
		t.Fatalf("%+v", ev)
	}
	a := loc("Available")
	a.ValidationFrequency, a.ValidationSet = time.Minute, true
	if ev := LocationRule(a, srvUTC, now); ev.Role != health.OK || ev.Next == nil {
		t.Fatalf("%+v", ev)
	}
	a.LastValidation = now.Add(-8 * time.Minute)
	if ev := LocationRule(a, srvUTC, now); ev.Role != health.Degraded || ev.Label != "Validation overdue" {
		t.Fatalf("overdue precedes Available: %+v", ev)
	}
	a.ValidationSet = false
	if ev := LocationRule(a, srvUTC, now); ev.Role != health.OK {
		t.Fatalf("no declared frequency, no clock: %+v", ev)
	}
	e := loc("")
	e.Created = now.Add(-time.Minute)
	if ev := LocationRule(e, srvUTC, now); ev.Role != health.Progressing {
		t.Fatalf("%+v", ev)
	}
	e.Created = now.Add(-time.Hour)
	if ev := LocationRule(e, srvUTC, now); ev.Role != health.Degraded || ev.Label != "Not validated" {
		t.Fatalf("%+v", ev)
	}
	if ev := LocationRule(a, Servers{Read: true}, now); ev.Label != "Not processed" {
		t.Fatalf("%+v", ev)
	}
}
