package velero

import "time"

// Horizon states.
const (
	HorizonFound      = "found"
	HorizonNone       = "none"
	HorizonUnreadable = "unreadable"
)

// Horizon is a schedule's latest retained successful backup (coverage S2b
// §4): a fact beside the verdict, never part of it. It reads "latest
// retained successful backup", never "restorable".
type Horizon struct {
	// State is HorizonFound, HorizonNone (no candidate retained) or
	// HorizonUnreadable.
	State   string
	Success *Backup
	// Proven: the success is a proven run of the schedule; else it was
	// matched by name only, and is never shown as a confirmed success.
	Proven bool
	// Untimed: its completion time is not recorded; it is shown by its
	// start, as a start.
	Untimed bool
	// OrderUnknown: another success without a recorded completion time may
	// be newer.
	OrderUnknown bool
	// Incomplete: the retained history may be missing a newer success. The
	// Backups kind is capped and does not hold the schedule's whole
	// history, or its window is unknown or in tier 0 (S2b revision 2).
	Incomplete bool
	// W is the window when it is known and in tier 1; Tier0 when the window
	// is in tier 0, which certifies no terminal coverage.
	W     time.Time
	Tier0 bool
	// Partial is a PartiallyFailed run after the success (the newest one
	// when there is no success), PartialProven how it was attributed, and
	// PartialOrderUnknown when its order against the success is unknown.
	Partial             *Backup
	PartialProven       bool
	PartialOrderUnknown bool
	// Expired counts the schedule's Completed runs past their expiration
	// that are still in the API: Velero's GC deletes a Backup only while
	// its storage location is available and writable.
	Expired int
	// NextExpiry is the earliest expiration among the candidates: the
	// horizon changes then without an API event (S2b §4, Clock).
	NextExpiry time.Time
}

// certainlyAfter is S2b §4's order rule: a finished after b only when both
// completion times are recorded and a's is later, or a started after b's
// recorded completion. Any other pair's order is unknown.
func certainlyAfter(a, b *Backup) bool {
	if b.Completion.IsZero() {
		return false
	}
	if !a.Completion.IsZero() && a.Completion.After(b.Completion) {
		return true
	}
	return !a.Start.IsZero() && a.Start.After(b.Completion)
}

// newest picks the newest of runs by the order rule. untimed reports that
// it has no recorded completion time, unknown that another run's order
// against it is unknown.
func newest(runs []*Backup) (best *Backup, untimed, unknown bool) {
	var timed *Backup
	var bare []*Backup
	for _, b := range runs {
		if b.Completion.IsZero() {
			bare = append(bare, b)
			continue
		}
		if timed == nil || b.Completion.After(timed.Completion) || (b.Completion.Equal(timed.Completion) && b.Name > timed.Name) {
			timed = b
		}
	}
	var later *Backup // the latest-started run certainly after timed
	for _, b := range bare {
		if timed != nil && !certainlyAfter(b, timed) {
			continue
		}
		if later == nil || b.Start.After(later.Start) || (b.Start.Equal(later.Start) && b.Name > later.Name) {
			later = b
		}
	}
	switch {
	case later != nil:
		// every other run without a completion time is unordered against it
		return later, true, len(bare) > 1
	case timed != nil:
		// each run without one started before timed completed
		return timed, false, len(bare) > 0
	}
	return nil, false, false
}

// HorizonOf computes s's latest retained success from the history the
// verdict read, at now. nName and loc are the verdict's N and zone.
func HorizonOf(s *Schedule, h History, nName string, loc *time.Location, now time.Time) Horizon {
	if !h.Readable {
		return Horizon{State: HorizonUnreadable}
	}
	hz := Horizon{State: HorizonNone}
	var success, partial [2][]*Backup // [0] proven, [1] named
	for _, b := range h.Backups {
		a := Attribute(s, b, nName, loc)
		if a == NotRun || (b.Phase != "Completed" && b.Phase != "PartiallyFailed") {
			continue
		}
		if !b.Expiration.IsZero() && !b.Expiration.After(now) {
			if b.Phase == "Completed" {
				hz.Expired++
			}
			continue
		}
		if !b.Expiration.IsZero() && (hz.NextExpiry.IsZero() || b.Expiration.Before(hz.NextExpiry)) {
			hz.NextExpiry = b.Expiration
		}
		tier := 1
		if a == Proven {
			tier = 0
		}
		if b.Phase == "Completed" {
			success[tier] = append(success[tier], b)
		} else {
			partial[tier] = append(partial[tier], b)
		}
	}
	for tier := range success {
		if b, untimed, unknown := newest(success[tier]); b != nil {
			hz.State, hz.Success, hz.Proven, hz.Untimed, hz.OrderUnknown = HorizonFound, b, tier == 0, untimed, unknown
			break
		}
	}
	for tier := range partial {
		if p, unknown := newerPartial(partial[tier], hz.Success); p != nil {
			hz.Partial, hz.PartialProven, hz.PartialOrderUnknown = p, tier == 0, unknown
			break
		}
	}
	if !h.covers(s.Created.Add(-incarnationSkew)) {
		hz.Incomplete = true
		if h.Window != nil {
			hz.Tier0 = h.Window.Tier0
			if !hz.Tier0 {
				hz.W = h.Window.W
			}
		}
	}
	return hz
}

// newerPartial is the newest PartiallyFailed run certainly after success,
// else the newest whose order against it is unknown (unknown true). With
// no success, it is the newest one.
func newerPartial(runs []*Backup, success *Backup) (*Backup, bool) {
	if success == nil {
		p, _, _ := newest(runs)
		return p, false
	}
	var after, unordered []*Backup
	for _, p := range runs {
		switch {
		case certainlyAfter(p, success):
			after = append(after, p)
		case !certainlyAfter(success, p):
			unordered = append(unordered, p)
		}
	}
	if p, _, _ := newest(after); p != nil {
		return p, false
	}
	p, _, _ := newest(unordered)
	return p, p != nil
}

// At is the success's time as the horizon shows it: its completion, or its
// start when its completion time is not recorded (Untimed).
func (h Horizon) At() time.Time {
	if h.Success == nil {
		return time.Time{}
	}
	if h.Untimed {
		return h.Success.Start
	}
	return h.Success.Completion
}

// Text is Klyx's wording of the horizon, the same in every cell, fleet row
// and AI field (S2b §4): its confidence is never dropped, and it never says
// "restorable".
func (h Horizon) Text() string {
	var s string
	switch {
	case h.State == HorizonUnreadable:
		return "runs not readable"
	case h.Success == nil:
		s = "no successful backup retained"
	case h.Proven:
		s = "latest proven retained success"
	default:
		s = "a Completed record matched by name (not proven to be this schedule's)"
	}
	if h.Success != nil {
		switch {
		case h.Untimed && h.Success.Start.IsZero():
			// a success with no time at all is still a success, never "none"
			// (Codex's S2b implementation review, finding 3)
			s += "; its start and completion times are not recorded"
		case h.Untimed:
			s += ", started " + stamp(h.Success.Start) + "; its completion time is not recorded"
		default:
			s += ", completed " + stamp(h.Success.Completion)
		}
		if h.OrderUnknown {
			s += "; a success without a recorded completion time may be newer"
		}
	}
	if h.Incomplete {
		switch {
		case h.Tier0:
			s += "; the retained history may be missing finished runs"
		case !h.W.IsZero():
			if h.Success == nil {
				s += " since " + stamp(h.W)
			} else {
				s += "; a run that started before " + stamp(h.W) + " and completed later may not be retained"
			}
		default:
			s += "; the retained history may be missing runs"
		}
	}
	return s
}

// PartialText words the PartiallyFailed run beside the success, "" when
// there is none.
func (h Horizon) PartialText() string {
	switch {
	case h.Partial == nil:
		return ""
	case h.Success == nil && h.PartialProven:
		return "a run partially failed"
	case h.Success == nil:
		return "a record matched by name partially failed"
	case h.PartialOrderUnknown && h.PartialProven:
		return "a run of unknown order partially failed"
	case h.PartialOrderUnknown:
		return "a record matched by name, of unknown order, partially failed"
	case h.PartialProven:
		return "a newer run partially failed"
	}
	return "a newer record matched by name partially failed"
}
