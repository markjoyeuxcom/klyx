package velero

import (
	"fmt"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// Rule timings (S2a §4).
const (
	// Grace is how late Velero may create the run it owes before it is
	// missed: the controller's one-minute cadence and a server restart.
	Grace = 10 * time.Minute
	// processWait is how long an object may wait for Velero's first
	// validation before it reads "not processed".
	processWait = 5 * time.Minute
	// unseenWait is how long the run at lastBackup may trail the
	// schedule's watch before it is missing.
	unseenWait = time.Minute
	// windowSkew is the margin between an execution time (the server's
	// clock) and the run time a rank was built from (S2a §4.2 row 10).
	windowSkew = time.Minute
	// Recent is the unscheduled backups' window (S2a §4.3).
	Recent = 24 * time.Hour
)

// ServerFacts is the detector's view of the Velero server of one namespace
// (S2a §3 controllers). Server's version and zone carry their own resolved
// flags; ScheduleDisabled is set only when resolved.
type ServerFacts struct {
	Ready bool
	Server
	ScheduleDisabled bool
}

// Servers is the detector's facts across namespaces. Read is false when
// the server list could not be read: then no object reads "not processed".
type Servers struct {
	Read        bool
	ByNamespace map[string]ServerFacts
}

// For is a namespace's server, and whether one was found.
func (s Servers) For(ns string) (ServerFacts, bool) {
	f, ok := s.ByNamespace[ns]
	return f, ok
}

// unprocessed reports a namespace no Velero server watches, proven by a
// read list.
func (s Servers) unprocessed(ns string) bool {
	_, ok := s.For(ns)
	return s.Read && !ok
}

// History is the Backups capture: what is retained, and what the cap can
// hide (S2a §3).
type History struct {
	Backups  []*Backup
	Readable bool
	// Complete: live with nothing left out.
	Complete bool
	// Window is the capped kind's boundary; nil while capped means it is
	// unknown, which proves nothing.
	Window *Window
}

// covers reports that every Backup the cap left out ran before t: the
// history is complete, or t is later than W plus the skew margin.
func (h History) covers(t time.Time) bool {
	if h.Complete {
		return true
	}
	if h.Window == nil || h.Window.Tier0 {
		return false
	}
	return t.After(h.Window.W.Add(windowSkew))
}

// guardComplete reports that every Backup in a guard phase is retained.
func (h History) guardComplete() bool {
	return h.Complete || (h.Window != nil && !h.Window.Tier0)
}

// Resume is a schedule's resume state from the tracker (S2a §4.2 row 7):
// Awaiting covers the tracker's Awaiting and Unproven, from ResumedAt.
type Resume struct {
	Awaiting  bool
	ResumedAt time.Time
}

// ScheduleInput is everything one schedule's verdict reads.
type ScheduleInput struct {
	S       *Schedule
	History History
	Servers Servers
	Resume  Resume
	Now     time.Time
}

// Run is one run on a schedule's rail.
type Run struct {
	Backup  *Backup
	Proven  bool
	At      time.Time // execution time; zero when unknown
	Timing  Timing
	Retired bool // an older run of the schedule superseded by the deciding run
}

// ScheduleVerdict is a schedule's verdict and what decided it.
type ScheduleVerdict struct {
	Eval health.NativeEval
	Due  Due
	// N is the run Velero last created and NState how it was found:
	// "found", "unseen", "not-retained", "unidentified", "gone" or "".
	N      *Backup
	NName  string
	NState string
	// Deciding is the run whose outcome is the verdict; Previous the
	// previous outcome while newer runs are in progress (PreviousNamed when
	// it was matched by name only).
	Deciding      *Backup
	Previous      *Backup
	PreviousNamed bool
	// Blocker is the run Velero defers the owed run behind.
	Blocker *Backup
	// Fact is a retained run shown beside an Unknown outcome.
	Fact *Backup
	// Runs is the rail, newest first; Newer lists named runs newer than N
	// (facts, never deciding); Synced counts Backups carrying the schedule's
	// name that look synced.
	Runs   []Run
	Newer  []*Backup
	Synced int
	// Horizon is the latest retained success (S2b §4), a fact beside the
	// verdict: it never changes the role.
	Horizon Horizon
}

func (v *ScheduleVerdict) set(role health.Kind, label, reason string) {
	v.Eval.Role, v.Eval.Label, v.Eval.Included = role, label, role.Attention()
	v.Eval.Reason = ""
	if reason != "" {
		v.Eval.Reason = health.CapReason(label, reason)
	}
}

func (v *ScheduleVerdict) next(t time.Time, now time.Time) {
	if t.IsZero() || !t.After(now) {
		return
	}
	if v.Eval.Next == nil || t.Before(*v.Eval.Next) {
		v.Eval.Next = health.At(t)
	}
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05Z") }

// zoneOf is the server's zone, nil when unresolved or unknown to Klyx.
func zoneOf(srv Server) *time.Location {
	if !srv.ZoneResolved {
		return nil
	}
	name := srv.Zone
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}

// ScheduleRule is S2a §4.2's classifier, with S2b's horizon beside it. The
// earliest candidate expiration joins Next, so the horizon is re-read when
// it changes; the role is the classifier's alone.
func ScheduleRule(in ScheduleInput) ScheduleVerdict {
	v := scheduleRule(in)
	srv, _ := in.Servers.For(in.S.Namespace)
	v.Horizon = HorizonOf(in.S, in.History, v.NName, zoneOf(srv.Server), in.Now)
	v.next(v.Horizon.NextExpiry, in.Now)
	return v
}

// scheduleRule is S2a §4.2's classifier: first match wins.
func scheduleRule(in ScheduleInput) ScheduleVerdict {
	s, now := in.S, in.Now
	var v ScheduleVerdict
	srv, _ := in.Servers.For(s.Namespace)
	loc := zoneOf(srv.Server)
	if s.LastBackup.IsZero() || loc == nil {
		v.NName = ""
	} else {
		v.NName = RunName(s.Name, s.LastBackup, loc)
	}
	v.Runs, v.Synced = rail(s, in.History.Backups, v.NName, loc)

	// 1. not processed
	if in.Servers.unprocessed(s.Namespace) {
		v.set(health.Degraded, "Not processed", "not in a Velero server's namespace: Velero processes only its own")
		return v
	}
	// 2. paused
	if s.Paused {
		v.set(health.Paused, "Paused", "")
		if s.Cron != "" && !s.CronTooLong {
			if e := ParseError(s.Cron, srv.Server); e != "" {
				v.Eval.Reason = health.CapReason("Paused", "will fail validation when resumed: "+e)
			}
		}
		return v
	}
	// 3. FailedValidation
	if s.Phase == "FailedValidation" {
		why := "Velero rejected the cron"
		switch {
		case s.CronTooLong:
			why = "the cron is too long to read"
		case s.Cron == "":
			why = "the cron is empty"
		default:
			if e := ParseError(s.Cron, srv.Server); e != "" {
				why = e
			}
		}
		v.set(health.Fault, "FailedValidation", why)
		return v
	}
	// 4. awaiting validation / not processed by Velero
	if s.Phase == "" || s.Phase == "New" {
		if now.Before(s.Created.Add(processWait)) {
			v.set(health.Progressing, "Awaiting validation", "awaiting Velero's first validation")
			v.next(s.Created.Add(processWait), now)
		} else {
			v.set(health.Degraded, "Not processed", "not processed by Velero")
		}
		return v
	}
	if s.Phase != "Enabled" {
		v.set(health.Unknown, "Unknown phase", "Velero reports a phase Klyx does not know")
		return v
	}
	// 5. the due time
	if s.CronTooLong {
		v.set(health.Unknown, "Unknown time", "the cron is too long to read")
		return v
	}
	v.Due = NextRun(s.Cron, Base(s.LastBackup, s.Created, s.LastSkipped), srv.Server)
	if v.Due.Unknown != "" {
		v.set(health.Unknown, "Unknown time", v.Due.Unknown)
		return v
	}
	// 6. never matches
	if v.Due.Zero {
		v.set(health.Degraded, "Never matches", "this cron never matches a time: Velero starts a run on every check")
		return v
	}
	missedAt := v.Due.D.Add(Grace)
	// 7. resumed
	if in.Resume.Awaiting && now.Before(in.Resume.ResumedAt.Add(Grace)) {
		v.set(health.Progressing, "Resumed", "resumed: awaiting Velero's first run")
		v.next(in.Resume.ResumedAt.Add(Grace), now)
		v.next(missedAt, now)
		return v
	}
	// 8. runs unreadable
	if !in.History.Readable {
		v.set(health.Unknown, "Runs unknown", "runs not readable")
		return v
	}
	// 9. missed or deferred
	if !now.Before(missedAt) {
		v.Eval.Boundary = health.At(missedAt)
		owed := "the run due at " + stamp(v.Due.D)
		if b := guardBlocker(s, in.History.Backups); b != nil {
			v.Blocker = b
			v.set(health.Degraded, "Deferred", owed+" waits for "+b.Name+" (Velero defers while one runs)")
			return v
		}
		if in.History.guardComplete() {
			why := "Velero created no backup for " + owed
			if srv.ScheduleDisabled {
				why += "; the schedule controller is disabled on this server"
			}
			v.set(health.Fault, "Missed", why)
			return v
		}
		v.set(health.Degraded, "Overdue", "Velero created no backup for "+owed+", and whether it is deferring it behind a run in progress cannot be told: more runs are in progress than Klyx holds")
		return v
	}
	v.next(missedAt, now)
	// 10. the outcome of the last run
	outcome(&v, s, in, loc)
	return v
}

// guardBlocker is a retained Backup Velero's overlap guard would wait for:
// in the schedule's namespace, labelled with its name, in a guard phase.
func guardBlocker(s *Schedule, backups []*Backup) *Backup {
	var out *Backup
	for _, b := range backups {
		if b.Namespace == s.Namespace && b.ScheduleLabel == s.Name && Blocking(b.Phase) {
			if out == nil || b.RunTime().After(out.RunTime()) {
				out = b
			}
		}
	}
	return out
}

// rail is the schedule's recent runs (proven or named), newest first, at
// most railLen, and the count of Backups with its name that look synced.
func rail(s *Schedule, backups []*Backup, nName string, loc *time.Location) ([]Run, int) {
	var runs []Run
	synced := 0
	for _, b := range backups {
		if b.Namespace != s.Namespace {
			continue
		}
		if b.ScheduleLabel == s.Name && b.Timing() == TimingSynced && len(b.Owners) == 0 && b.Name != nName {
			synced++
		}
		a := Attribute(s, b, nName, loc)
		if a == NotRun {
			continue
		}
		at, _ := execTime(s, b, nName, loc)
		runs = append(runs, Run{Backup: b, Proven: a == Proven, At: at, Timing: b.Timing()})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].At.Equal(runs[j].At) {
			return runs[i].Backup.Name > runs[j].Backup.Name
		}
		return runs[i].At.After(runs[j].At)
	})
	if len(runs) > railLen {
		runs = runs[:railLen]
	}
	return runs, synced
}

const railLen = 8

// execTime is a run's execution time; N's falls back to lastBackup.
func execTime(s *Schedule, b *Backup, nName string, loc *time.Location) (time.Time, bool) {
	if at, ok := b.ExecutionTime(s.Name, loc); ok {
		return at, true
	}
	if nName != "" && b.Name == nName && !s.LastBackup.IsZero() {
		return s.LastBackup, true
	}
	return time.Time{}, false
}

type timedRun struct {
	b  *Backup
	at time.Time
}

// outcome is row 10: anchored on N, decided by proven runs only.
func outcome(v *ScheduleVerdict, s *Schedule, in ScheduleInput, loc *time.Location) {
	now, h := in.Now, in.History
	var proven, named []timedRun
	for _, b := range h.Backups {
		switch Attribute(s, b, v.NName, loc) {
		case Proven:
			if at, ok := execTime(s, b, v.NName, loc); ok {
				proven = append(proven, timedRun{b, at})
			}
		case Named:
			if at, ok := execTime(s, b, v.NName, loc); ok {
				named = append(named, timedRun{b, at})
			}
		}
	}
	var anchor time.Time // N's execution time; the schedule's creation without N
	switch {
	case s.LastBackup.IsZero():
		anchor = s.Created.Add(-incarnationSkew)
	case loc == nil:
		v.NState = "unidentified"
		v.set(health.Unknown, "Last run unknown", "the run Velero created at "+stamp(s.LastBackup)+" cannot be identified (the server's zone is unresolved)")
		return
	default:
		for _, r := range proven {
			if r.b.Name == v.NName {
				v.N, anchor = r.b, r.at
			}
		}
		if v.N == nil {
			switch {
			case now.Before(s.LastBackup.Add(unseenWait)):
				v.NState = "unseen"
				v.set(health.Progressing, "Run created", "run created at "+stamp(s.LastBackup)+", not yet seen")
				v.next(s.LastBackup.Add(unseenWait), now)
			case !h.covers(s.LastBackup):
				v.NState = "not-retained"
				v.set(health.Unknown, "Last run unknown", "the run Velero created at "+stamp(s.LastBackup)+" is not retained")
			default:
				v.NState = "gone"
				v.set(health.Unknown, "Last run unknown", "the run Velero created at "+stamp(s.LastBackup)+" is not in the cluster (deleted or expired)")
			}
			v.Fact = newestTerminal(proven, time.Time{}, false)
			return
		}
		v.NState = "found"
	}
	// the deciding runs: N and proven runs after it (all proven without N)
	var deciding []timedRun
	for _, r := range proven {
		if (v.N != nil && r.b == v.N) || r.at.After(anchor) || (v.N == nil && !r.at.Before(anchor)) {
			deciding = append(deciding, r)
		}
	}
	for _, r := range named {
		if v.N != nil && r.at.After(anchor) {
			v.Newer = append(v.Newer, r.b)
		}
	}
	if !h.covers(anchor) {
		v.set(health.Unknown, "Last run unknown", "a run after "+stamp(anchor)+" may not be retained")
		v.Fact = newestTerminal(deciding, time.Time{}, false)
		return
	}
	if d := newestTerminal(deciding, time.Time{}, false); d != nil {
		v.Deciding = d
		decide(v, d, "", inProgressAfter(deciding, d, s, loc, v.NName))
		return
	}
	if len(deciding) == 0 {
		v.set(health.Progressing, "Awaiting first run", "awaiting its first run, due "+stamp(v.Due.D))
		return
	}
	// every deciding run is in progress: the previous outcome
	before := anchor
	if v.N == nil {
		before = oldest(deciding)
	}
	if p := newestTerminal(proven, before, true); p != nil {
		at, _ := execTime(s, p, v.NName, loc)
		if !h.covers(at) {
			v.set(health.Unknown, "Previous run unknown", "the run before "+stamp(before)+" may not be retained")
			v.Fact = p
			return
		}
		v.Previous = p
		decide(v, p, "", true)
		return
	}
	if !h.covers(s.Created.Add(-incarnationSkew)) {
		v.set(health.Unknown, "Previous run unknown", "the run before "+stamp(before)+" may not be retained")
		return
	}
	if q := newestTerminal(named, before, true); q != nil {
		v.Previous, v.PreviousNamed = q, true
		qual := "matched by name"
		if q.Timing() == TimingDisputed {
			qual += "; may have been synced from object storage"
		}
		decide(v, q, qual, true)
		return
	}
	if v.N != nil && len(proven) == 1 {
		v.set(health.Progressing, "First run in progress", "first run in progress")
		return
	}
	v.set(health.Progressing, "Run in progress", "run in progress")
}

// newestTerminal is the newest terminal run, before `before` when bounded.
func newestTerminal(runs []timedRun, before time.Time, bounded bool) *Backup {
	var best *timedRun
	for i := range runs {
		r := &runs[i]
		if !Terminal(r.b.Phase) || (bounded && !r.at.Before(before)) {
			continue
		}
		if best == nil || r.at.After(best.at) || (r.at.Equal(best.at) && r.b.Name > best.b.Name) {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	return best.b
}

func oldest(runs []timedRun) time.Time {
	t := runs[0].at
	for _, r := range runs[1:] {
		if r.at.Before(t) {
			t = r.at
		}
	}
	return t
}

// inProgressAfter reports a deciding run in progress newer than d.
func inProgressAfter(runs []timedRun, d *Backup, s *Schedule, loc *time.Location, nName string) bool {
	dAt, _ := execTime(s, d, nName, loc)
	for _, r := range runs {
		if !Terminal(r.b.Phase) && r.at.After(dAt) {
			return true
		}
	}
	return false
}

// decide sets the verdict from one run's terminal phase.
func decide(v *ScheduleVerdict, b *Backup, qualifier string, newer bool) {
	add := func(s string) string {
		if newer {
			s += "; a new run is in progress"
		}
		if qualifier != "" {
			s += " (" + qualifier + ")"
		}
		return s
	}
	switch b.Phase {
	case "Completed":
		v.set(health.OK, "Completed", add("last run completed "+stamp(completedAt(b))))
	case "PartiallyFailed":
		v.set(health.Degraded, "Partially failed", add(fmt.Sprintf("the last run partially failed: %d errors", b.Errors)))
	default: // Failed, FailedValidation
		v.set(health.Fault, "Last run failed", add("the last run failed: "+failureText(b)))
	}
}

func completedAt(b *Backup) time.Time {
	if !b.Completion.IsZero() {
		return b.Completion
	}
	return b.RunTime()
}

// failureText is a failed run's class (S2a §6): its failure reason, else
// its first validation error, else its phase.
func failureText(b *Backup) string {
	if b.Failure != nil {
		return TextOf(*b.Failure)
	}
	if len(b.Validation) > 0 {
		return TextOf(b.Validation[0])
	}
	return b.Phase
}

// LocationRule is S2a §4.1: first match wins.
func LocationRule(l *Location, servers Servers, now time.Time) health.NativeEval {
	var ev health.NativeEval
	set := func(role health.Kind, label, reason string) {
		ev.Role, ev.Label, ev.Included = role, label, role.Attention()
		if reason != "" {
			ev.Reason = health.CapReason(label, reason)
		}
	}
	switch {
	case servers.unprocessed(l.Namespace):
		set(health.Degraded, "Not processed", "not in a Velero server's namespace: Velero processes only its own")
	case l.Phase == "Unavailable":
		why := "Unavailable"
		if l.Message != nil {
			why = TextOf(*l.Message)
		}
		set(health.Fault, "Unavailable", why)
	case l.Phase == "Available" && l.ValidationSet && l.ValidationFrequency > 0 && !now.Before(l.overdueAt()):
		ev.Boundary = health.At(l.overdueAt())
		set(health.Degraded, "Validation overdue", "last validated "+stamp(l.LastValidation))
	case l.Phase == "Available":
		set(health.OK, "Available", "")
		if l.ValidationSet && l.ValidationFrequency > 0 && !l.LastValidation.IsZero() {
			ev.Next = health.At(l.overdueAt())
		}
	case l.Phase == "" && now.Before(l.Created.Add(processWait)):
		set(health.Progressing, "Awaiting validation", "awaiting Velero's first validation")
		ev.Next = health.At(l.Created.Add(processWait))
	case l.Phase == "":
		set(health.Degraded, "Not validated", "not validated by Velero")
	default:
		set(health.Unknown, "Unknown phase", "Velero reports a phase Klyx does not know")
	}
	return ev
}

// overdueAt is when an Available location's declared validation is
// overdue: last + 2 x frequency + 5m.
func (l *Location) overdueAt() time.Time {
	return l.LastValidation.Add(2*l.ValidationFrequency + 5*time.Minute)
}

// TextOf is a class's fixed wording, with its vocabulary.
func TextOf(c Class) string {
	t := c.Text
	if t == "" {
		t = textOfCode[c.Code]
	}
	for _, v := range c.Vocabulary {
		t += "; " + v
	}
	return t
}
