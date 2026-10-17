package velero

import (
	"regexp"
	"time"
)

// nameStamp is the controller's timestamp format in a run's name
// (schedule_types.go 138-139).
const nameStamp = "20060102150405"

var stampTail = regexp.MustCompile(`^[0-9]{14}$`)

// RunName is the name Velero's schedule controller gives the run it creates
// at lastBackup: <schedule>-<lastBackup in the server's zone> (S2a §3, N).
func RunName(schedule string, lastBackup time.Time, loc *time.Location) string {
	return schedule + "-" + lastBackup.In(loc).Format(nameStamp)
}

// Timing is what a Backup's own timestamps say about its origin (S2a §3,
// revision 2): processing that began before the object existed here was
// another object's, re-created by backup sync.
type Timing int

const (
	TimingLocal Timing = iota
	// TimingDisputed: processing began 5 s to 2 minutes before creation,
	// within plausible clock skew; counted with a qualifier.
	TimingDisputed
	// TimingSynced: processing began more than 2 minutes before creation;
	// never counted, always listed.
	TimingSynced
)

// Sync allowances (S2a §3).
const (
	syncSkew     = 2 * time.Minute
	syncDisputed = 5 * time.Second
)

// Timing reads a Backup's origin from its start and creation. A Backup
// that never started has nothing to compare and reads local.
func (b *Backup) Timing() Timing {
	if b.Start.IsZero() || b.Created.IsZero() {
		return TimingLocal
	}
	switch gap := b.Created.Sub(b.Start); {
	case gap > syncSkew:
		return TimingSynced
	case gap > syncDisputed:
		return TimingDisputed
	}
	return TimingLocal
}

// ExecutionTime orders runs (S2a §3, revision 3): the start, else, for a
// run that never started, the timestamp in its name when the name is the
// controller's form for schedule, read in the server's zone. ok is false
// when neither holds: such a run never decides.
func (b *Backup) ExecutionTime(schedule string, loc *time.Location) (time.Time, bool) {
	if !b.Start.IsZero() {
		return b.Start, true
	}
	if loc == nil || schedule == "" || len(b.Name) != len(schedule)+1+len(nameStamp) || b.Name[:len(schedule)+1] != schedule+"-" {
		return time.Time{}, false
	}
	tail := b.Name[len(schedule)+1:]
	if !stampTail.MatchString(tail) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(nameStamp, tail, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Attribution is how a Backup relates to a schedule (S2a §3).
type Attribution int

const (
	NotRun Attribution = iota
	// Named: matched by label since the schedule was created, not looking
	// synced; history and the uncapped previous outcome, never deciding.
	Named
	// Proven: N, or an owner reference carrying the schedule's UID; only
	// proven runs decide a verdict.
	Proven
)

// incarnationSkew lets a named run's execution time precede its schedule's
// creation by the clocks' skew (S2a §3).
const incarnationSkew = time.Minute

// Attribute relates b to schedule s. nName is s's N (RunName), "" when it
// cannot be identified; loc is the server's zone (nil when unresolved).
func Attribute(s *Schedule, b *Backup, nName string, loc *time.Location) Attribution {
	if b.Namespace != s.Namespace {
		return NotRun
	}
	if nName != "" && b.Name == nName && b.ScheduleLabel == s.Name {
		return Proven
	}
	for _, o := range b.Owners {
		if o.UID == s.UID {
			return Proven
		}
	}
	if b.OwnersIncomplete || len(b.Owners) > 0 {
		// an incomplete owner set proves nothing either way; a Schedule
		// owner to another UID is another schedule's run
		return NotRun
	}
	if b.ScheduleLabel != s.Name || b.Timing() == TimingSynced {
		return NotRun
	}
	at, ok := b.ExecutionTime(s.Name, loc)
	if !ok || at.Before(s.Created.Add(-incarnationSkew)) {
		return NotRun
	}
	return Named
}
