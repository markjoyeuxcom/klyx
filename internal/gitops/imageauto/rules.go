package imageauto

import (
	"fmt"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// Labels the rules use.
const (
	LabelSuspended  = "Suspended"
	LabelSyncing    = "Syncing…"
	LabelScanDue    = "Scan overdue"
	LabelRunDue     = "Run overdue"
	ReasonDepNotRdy = "DependencyNotReady"
)

// minSlack is the least slack past an interval before a clock is overdue.
const minSlack = 5 * time.Minute

// Pending is S1b §4.1's rule: the spec has no verdict yet. Ready describes
// an older generation, or it is not a failure and status.observedGeneration
// lags. A Ready=False written for the current generation is never pending:
// the controllers advance status.observedGeneration only on success. A
// Ready without its own observedGeneration keeps the status rule.
func Pending(c Common) bool {
	lag := c.ObservedSet && c.Generation > 0 && c.Generation > c.ObservedGeneration
	switch {
	case c.ReadyGeneration == 0 || c.Generation == 0:
		return lag
	case c.Generation > c.ReadyGeneration:
		return true
	case c.Ready == Failed:
		return false
	}
	return lag
}

// Rule is the shared verdict for the three kinds (S1b §4.2): suspension is
// an intended pause; a pending spec is Syncing; Stalled and Ready=False
// are faults; Reconciling, or Ready Unknown with a progressing reason, is
// progressing; Ready is ok.
func Rule(c Common) health.NativeEval {
	switch {
	case c.Suspended:
		return health.NativeEval{Role: health.Paused, Label: LabelSuspended}
	case Pending(c):
		return health.NativeEval{Included: true, Role: health.Progressing, Label: LabelSyncing}
	}
	e := health.NativeEval{Included: c.Ready != Ready}
	switch c.Ready {
	case Ready:
		e.Role, e.Label = health.OK, "Ready"
		e.Cond = health.CondAt("Ready", "True", c.ReadySince)
	case Failed:
		e.Role, e.Label = health.Fault, "Failed"
		if c.Stalled {
			e.Label = "Stalled"
		}
		e.Cond = health.CondAt("Ready", "False", c.ReadySince)
		e.Reason = health.CapReason(c.Reason, c.Message)
	case Reconciling:
		e.Role, e.Label = health.Progressing, "Reconciling"
	default:
		switch c.Reason {
		case "Progressing", "ProgressingWithRetry", "Reconciling":
			e.Role, e.Label = health.Progressing, "Progressing"
		default:
			e.Role, e.Label = health.Unknown, "Unknown"
			if c.Stalled {
				e.Role, e.Label = health.Fault, "Stalled"
				e.Reason = health.CapReason(c.StalledReason, c.Message)
			}
		}
	}
	return e
}

// current: Ready=True for the current generation, not suspended - the
// only state a clock can make overdue.
func current(c Common) bool {
	return !c.Suspended && !Pending(c) && c.Ready == Ready
}

// deadline is t + interval + max(interval, 5m) (S1b §4.3).
func deadline(t time.Time, interval time.Duration) time.Time {
	slack := interval
	if slack < minSlack {
		slack = minSlack
	}
	return t.Add(interval + slack)
}

// clock overlays an overdue state on a current Ready: Degraded and counted
// once now passes the deadline, else the deadline is Next.
func clock(e health.NativeEval, c Common, at time.Time, interval time.Duration, now time.Time, label, reason string) health.NativeEval {
	if !current(c) || at.IsZero() || interval <= 0 {
		return e
	}
	due := deadline(at, interval)
	if now.Before(due) {
		e.Next = health.At(due)
		return e
	}
	return health.NativeEval{Included: true, Role: health.Degraded, Label: label, Boundary: health.At(due),
		Cond: e.Cond, Reason: health.CapReason("", reason)}
}

// RepositoryRule is Rule with the scan clock: Ready for the current
// generation while no scan has completed for over twice the interval is
// "Scan overdue".
func RepositoryRule(r Repository, now time.Time) health.NativeEval {
	e := Rule(r.Common)
	return clock(e, r.Common, r.ScanTime, r.Interval, now, LabelScanDue,
		fmt.Sprintf("no completed scan since %s (interval %s), while Ready", r.ScanTime.UTC().Format(time.RFC3339), r.IntervalText))
}

// PolicyRule is Rule; a policy has no clock of its own (S1b §4.3).
func PolicyRule(p Policy) health.NativeEval { return Rule(p.Common) }

// BlockedBy names the repository a policy waits for when its reason is
// DependencyNotReady, "" otherwise.
func (p Policy) BlockedBy() string {
	if p.Suspended || p.Ready == Ready || p.Reason != ReasonDepNotRdy {
		return ""
	}
	return p.RepositoryNamespace + "/" + p.RepositoryName
}

// AutomationRule is Rule with the run clock: no newer run recorded for over
// twice the interval while Ready is "Run overdue". lastAutomationRunTime
// is published only with a run's final patch, so a long run may be in
// flight: the role stays Degraded, never a fault.
func AutomationRule(a Automation, now time.Time) health.NativeEval {
	e := Rule(a.Common)
	return clock(e, a.Common, a.LastRun, a.Interval, now, LabelRunDue,
		fmt.Sprintf("no newer run recorded since %s (interval %s); a run may be in progress", a.LastRun.UTC().Format(time.RFC3339), a.IntervalText))
}

// ShortSHA is a revision's SHA, from a Flux artifact revision
// ("main@sha1:<sha>", "sha1:<sha>") or a bare one; "" when there is none.
func ShortSHA(rev string) string {
	if i := strings.LastIndex(rev, ":"); i >= 0 && (strings.HasSuffix(rev[:i], "sha1") || strings.HasSuffix(rev[:i], "sha256")) {
		return rev[i+1:]
	}
	if strings.Contains(rev, "@") || strings.Contains(rev, ":") {
		return ""
	}
	return rev
}
