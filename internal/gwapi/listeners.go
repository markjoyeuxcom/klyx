package gwapi

import (
	"fmt"

	"github.com/moomora/klyx/internal/health"
)

// ListenerIssue is one listener's failure as its controller reported it.
type ListenerIssue struct {
	Listener string
	Cond     TypedCond
}

// listenerChecks are the failing states of a listener's conditions, in the
// order that explains it best (S7 §3): not accepted (an unsupported
// protocol, say), then conflicted - it lost the merge's precedence and
// serves nothing - then unresolved references, then not programmed.
var listenerChecks = []struct {
	condType, failing string
}{
	{"Accepted", "False"},
	{"Conflicted", "True"},
	{"ResolvedRefs", "False"},
	{"Programmed", "False"},
}

// ListenerIssues returns, in listener order, each listener's first failing
// condition (S7 §3). Only a condition observed at generation is judged: an
// older one describes a spec that has since changed, so it is shown, never
// judged.
func ListenerIssues(listeners []Listener, generation int64) []ListenerIssue {
	var out []ListenerIssue
	for _, l := range listeners {
		if l.Status == nil {
			continue
		}
		for _, chk := range listenerChecks {
			c := l.Status.Of(chk.condType)
			if c.Status != chk.failing || condState(c, generation).stale {
				continue
			}
			out = append(out, ListenerIssue{Listener: l.Name, Cond: TypedCond{Type: chk.condType, Cond: c}})
			break
		}
	}
	return out
}

// GatewayVerdict is a Gateway's verdict: GatewayRule's, from its own
// Accepted and Programmed conditions, and with the Gateway itself healthy,
// its listeners' evidence (S7 §3) - a listener its controller reports
// failing makes the Gateway Degraded, named with the listener and reason.
func GatewayVerdict(accepted, programmed Cond, generation int64, listeners []Listener) health.NativeEval {
	e := GatewayRule(accepted, programmed, generation)
	if e.Role != health.OK {
		return e
	}
	return withListenerIssues(e, ListenerIssues(listeners, generation))
}

// withListenerIssues degrades a healthy verdict by its listeners' issues.
// No condition dates the result: it is the worst of several listeners.
func withListenerIssues(e health.NativeEval, issues []ListenerIssue) health.NativeEval {
	if len(issues) == 0 {
		return e
	}
	first := issues[0]
	label := "listener " + clipLabel(first.Listener) + ": " + clipLabel(issueReason(first.Cond))
	if n := len(issues) - 1; n > 0 {
		label += fmt.Sprintf(" (+%d more)", n)
	}
	return health.NativeEval{Included: true, Role: health.Degraded, Label: label,
		Reason: health.CapReason(first.Cond.Reason, first.Cond.Message),
		Cond:   health.CondAt(first.Cond.Type, first.Cond.Status, first.Cond.LastTransitionTime)}
}

// issueReason names a failing condition by its reason, or by its type and
// status when the controller gave none.
func issueReason(c TypedCond) string {
	if c.Reason != "" {
		return c.Reason
	}
	if c.Type == "Conflicted" {
		return "conflicted"
	}
	return c.Type + " " + c.Status
}

// labelPartMax bounds one name or reason inside a verdict label: the full
// reason stays in the verdict's Reason.
const labelPartMax = 64

func clipLabel(s string) string {
	if r := []rune(s); len(r) > labelPartMax {
		return string(r[:labelPartMax-1]) + "…"
	}
	return s
}
