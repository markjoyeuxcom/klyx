package appbridge

import (
	"regexp"

	"github.com/moomora/klyx/internal/lb"
)

// Coverage S4 §7, Codex's S4 implementation review finding 1: the policy for
// Explain on the lb task applies to the whole evidence document, not only
// the lens fields. A controller's message (an event, a condition, a
// configuration error) can carry addresses, address blocks, pool names or an
// annotation's value, so on this task the AI is sent reasons, states and
// counts, never a message; and of the object's facts only those that can
// hold no address.

// lbFactLabels are the instance facts that hold no address (crd's
// serviceFacts labels; the pool and state kinds have no facts).
var lbFactLabels = map[string]bool{"type": true, "traffic policy": true, "session affinity": true, "LB class": true}

// lbEvidence applies the policy to an assembled evidence document.
func lbEvidence(ev explainEvidence) explainEvidence {
	// a condition's reason in the lens fields (a Service's Cilium condition,
	// a pool's conflict) is an identifier or nothing: metav1.Condition
	// admits colons, so an IPv6 address can pass for a reason (Codex's S4
	// re-review)
	tokenReasons(ev.LensFields)
	ev.Target = lbObject(ev.Target)
	for i := range ev.Related {
		ev.Related[i] = lbObject(ev.Related[i])
	}
	for i := range ev.WarningEvents {
		// an event's reason is not validated by the API: kept only as an identifier
		ev.WarningEvents[i].Message, ev.WarningEvents[i].Reason = "", token(ev.WarningEvents[i].Reason, notIdentifier)
	}
	return ev
}

const notIdentifier = "(a reason that is not an identifier)"

func lbObject(o evidenceObject) evidenceObject {
	for i := range o.Conditions {
		o.Conditions[i].Message, o.Conditions[i].Reason = "", token(o.Conditions[i].Reason, notIdentifier)
	}
	var facts []evidenceFact
	for _, f := range o.Facts {
		if lbFactLabels[f.Label] {
			facts = append(facts, f)
		}
	}
	o.Facts = facts
	return o
}

// reasonToken keeps a controller's reason only when it is an identifier
// (an event's or condition's CamelCase or snake_case reason), never text.
var reasonToken = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func token(s, fallback string) string {
	if s == "" || reasonToken.MatchString(s) {
		return s
	}
	return fallback
}

// lbCause is a Service's cause as structure: which source speaks and its
// reason token, or the silent case (fixed wording and the class), never a
// message.
func lbCause(r lbServiceInput) string {
	if d := r.diag; d != nil {
		switch {
		case len(d.Events) > 0:
			return "event " + token(d.Events[0].Reason, notIdentifier)
		case d.Cilium != nil && d.Cilium.Current && d.Cilium.Status == "False":
			return "cilium " + token(d.Cilium.Reason, notIdentifier)
		case d.SvcLB != nil && d.SvcLB.HostPortConflict:
			return "servicelb host port conflict"
		case d.SvcLB != nil && d.SvcLB.DaemonSet != "" && d.SvcLB.Ready == 0:
			return "servicelb pods not ready"
		case d.Pool != nil && d.Pool.State == "missing":
			return "requested pool missing"
		}
		return d.Silent
	}
	switch {
	case r.alloc.PortError || r.alloc.PortsErrorCond:
		return "port error"
	case r.missingFamily != "":
		return "missing family " + r.missingFamily
	}
	return ""
}

type lbServiceInput struct {
	diag          *lb.Diagnosis
	alloc         lb.Allocation
	missingFamily string
}

// tokenReasons replaces every "reason" string in the lens fields, at any
// depth, by its identifier token.
func tokenReasons(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == "reason" {
				x[k] = token(s, notIdentifier)
				continue
			}
			tokenReasons(e)
		}
	case []any:
		for _, e := range x {
			tokenReasons(e)
		}
	}
}
