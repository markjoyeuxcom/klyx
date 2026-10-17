package velero

import (
	"fmt"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// Membership is why a Backup is, or is not, in the unscheduled backups part
// (S2a §4.3).
type Membership struct {
	In bool
	// Why names the case: "unattributed", "named, newer than N", "named, no
	// N", "proven, order unknown" for members; "synced", "not recent",
	// "proven", "superseded by N" or "superseded" otherwise.
	Why string
	// Schedule is the current schedule it relates to, if any.
	Schedule string
	// Disputed: its timing is within plausible clock skew of a synced one.
	Disputed bool
}

// scheduleView is what membership needs of one schedule.
type scheduleView struct {
	s      *Schedule
	nName  string
	loc    *time.Location
	nAt    time.Time
	hasN   bool
	proven []timedRun // proven runs with a known execution time
}

// Unscheduled decides membership for every retained Backup against the
// current schedules (S2a §4.3, revisions 3 to 5).
func Unscheduled(backups []*Backup, schedules []*Schedule, servers Servers, now time.Time) map[*Backup]Membership {
	views := map[string][]*scheduleView{} // namespace -> schedules
	for _, s := range schedules {
		srv, _ := servers.For(s.Namespace)
		sv := &scheduleView{s: s, loc: zoneOf(srv.Server)}
		if !s.LastBackup.IsZero() && sv.loc != nil {
			sv.nName = RunName(s.Name, s.LastBackup, sv.loc)
		}
		for _, b := range backups {
			if Attribute(s, b, sv.nName, sv.loc) != Proven {
				continue
			}
			if at, ok := execTime(s, b, sv.nName, sv.loc); ok {
				sv.proven = append(sv.proven, timedRun{b, at})
				if b.Name == sv.nName {
					sv.nAt, sv.hasN = at, true
				}
			}
		}
		views[s.Namespace] = append(views[s.Namespace], sv)
	}
	out := make(map[*Backup]Membership, len(backups))
	for _, b := range backups {
		out[b] = membership(b, views[b.Namespace], now)
	}
	return out
}

func membership(b *Backup, views []*scheduleView, now time.Time) Membership {
	timing := b.Timing()
	if timing == TimingSynced {
		return Membership{Why: "synced"}
	}
	rt := b.RunTime()
	if !now.Before(rt.Add(Recent)) {
		return Membership{Why: "not recent"}
	}
	m := Membership{In: true, Why: "unattributed", Disputed: timing == TimingDisputed}
	for _, sv := range views {
		switch Attribute(sv.s, b, sv.nName, sv.loc) {
		case Proven:
			if _, ok := execTime(sv.s, b, sv.nName, sv.loc); ok {
				return Membership{Why: "proven", Schedule: sv.s.Name}
			}
			// order unknown: counted until a finished proven run with a
			// known, later execution supersedes it (revision 5)
			for _, r := range sv.proven {
				if Terminal(r.b.Phase) && r.at.After(b.Created.Add(windowSkew)) {
					return Membership{Why: "superseded", Schedule: sv.s.Name}
				}
			}
			m = Membership{In: true, Why: "proven, order unknown", Schedule: sv.s.Name, Disputed: m.Disputed}
		case Named:
			at, _ := execTime(sv.s, b, sv.nName, sv.loc)
			if sv.hasN && !at.After(sv.nAt) {
				return Membership{Why: "superseded by N", Schedule: sv.s.Name}
			}
			why := "named, no N"
			if sv.hasN {
				why = "named, newer than N"
			}
			m = Membership{In: true, Why: why, Schedule: sv.s.Name, Disputed: m.Disputed}
		}
	}
	return m
}

// BackupRule is a member's role (S2a §4.3); Next is when it leaves the
// part, its run time plus 24 hours, the same basis as its membership.
func BackupRule(b *Backup, m Membership, servers Servers, now time.Time) health.NativeEval {
	var ev health.NativeEval
	set := func(role health.Kind, label, reason string) {
		ev.Role, ev.Label, ev.Included = role, label, role.Attention()
		if m.Disputed {
			if reason == "" {
				reason = "may have been synced from object storage"
			} else {
				reason += "; may have been synced from object storage"
			}
		}
		if reason != "" {
			ev.Reason = health.CapReason(label, reason)
		}
	}
	switch {
	case servers.unprocessed(b.Namespace):
		set(health.Degraded, "Not processed", "not in a Velero server's namespace: Velero processes only its own")
	case b.Phase == "Completed":
		set(health.OK, "Completed", "")
	case b.Phase == "Failed" || b.Phase == "FailedValidation":
		set(health.Fault, b.Phase, failureText(b))
	case b.Phase == "PartiallyFailed":
		set(health.Degraded, "PartiallyFailed", fmt.Sprintf("%d errors", b.Errors))
	case b.Phase == "WaitingForPluginOperationsPartiallyFailed" || b.Phase == "FinalizingPartiallyFailed":
		set(health.Progressing, b.Phase, "partially failed so far")
	case b.Phase == "Deleting":
		set(health.Progressing, "Deleting", "")
	default:
		label := b.Phase
		if label == "" {
			label = "New"
		}
		set(health.Progressing, label, "")
	}
	if leave := b.RunTime().Add(Recent); leave.After(now) {
		ev.Next = health.At(leave)
	}
	return ev
}

// Attr is how a listed Backup relates to the current schedules (S2b §2).
// How is "proven", "named", "synced" (it carries a current schedule's name
// and looks synced from object storage, unproven) or "" (not a run of a
// current schedule). Attribute's precedence decides: a proof wins over the
// looks-synced heuristic.
type Attr struct {
	Schedule string
	How      string
	// Disputed: named, with timing within plausible skew of a synced one.
	Disputed bool
}

// Attributions relates every listed Backup to the current schedules.
func Attributions(backups []*Backup, schedules []*Schedule, servers Servers) map[*Backup]Attr {
	type view struct {
		s     *Schedule
		nName string
		loc   *time.Location
	}
	byName, byUID := map[string]*view{}, map[string]*view{}
	for _, s := range schedules {
		srv, _ := servers.For(s.Namespace)
		v := &view{s: s, loc: zoneOf(srv.Server)}
		if !s.LastBackup.IsZero() && v.loc != nil {
			v.nName = RunName(s.Name, s.LastBackup, v.loc)
		}
		byName[s.Namespace+"/"+s.Name], byUID[s.UID] = v, v
	}
	out := make(map[*Backup]Attr, len(backups))
	for _, b := range backups {
		var cands []*view
		if v := byName[b.Namespace+"/"+b.ScheduleLabel]; v != nil && b.ScheduleLabel != "" {
			cands = append(cands, v)
		}
		for _, o := range b.Owners {
			if v := byUID[o.UID]; v != nil && v.s.Namespace == b.Namespace {
				cands = append(cands, v)
			}
		}
		var a Attr
		for _, v := range cands {
			switch Attribute(v.s, b, v.nName, v.loc) {
			case Proven:
				a = Attr{Schedule: v.s.Name, How: "proven"}
			case Named:
				if a.How == "" {
					a = Attr{Schedule: v.s.Name, How: "named", Disputed: b.Timing() == TimingDisputed}
				}
			}
			if a.How == "proven" {
				break
			}
		}
		// as S2a's rail counts them: unowned, carrying the name, looking synced
		if a.How == "" && b.Timing() == TimingSynced && len(b.Owners) == 0 && len(cands) > 0 && cands[0].s.Name == b.ScheduleLabel {
			a = Attr{Schedule: b.ScheduleLabel, How: "synced"}
		}
		out[b] = a
	}
	return out
}
