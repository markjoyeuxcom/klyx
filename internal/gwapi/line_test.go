package gwapi

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// The Line §8.1: condOf keeps lastTransitionTime.
func TestCondOfKeepsTransitionTime(t *testing.T) {
	conds := []interface{}{map[string]interface{}{"type": "Accepted", "status": "True", "lastTransitionTime": "2026-10-14T09:00:00Z"}}
	if c := condOf(conds, "Accepted"); !c.LastTransitionTime.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v", c.LastTransitionTime)
	}
}

func TestGatewayRuleSinceAndReason(t *testing.T) {
	t1 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	cond := func(status string, observed int64, at time.Time) Cond {
		return Cond{Status: status, Reason: "R" + status, Message: "m", ObservedGeneration: observed, LastTransitionTime: at}
	}
	cases := []struct {
		name               string
		accepted, programd Cond
		role               health.Kind
		since              *time.Time
		reason             string
	}{
		{"not accepted: Accepted's time", cond("False", 2, t1), Cond{}, health.Fault, &t1, "RFalse: m"},
		{"not accepted, last observed: reason, no Since", cond("False", 1, t1), Cond{}, health.Degraded, nil, "RFalse: m"},
		{"not programmed after a later acceptance: the later time", cond("True", 2, t2), cond("False", 2, t1), health.Degraded, &t2, "RFalse: m"},
		{"not programmed after an earlier acceptance: Programmed's time", cond("True", 2, t1), cond("False", 2, t2), health.Degraded, &t2, "RFalse: m"},
		{"not programmed, last observed", cond("True", 2, t1), cond("False", 1, t2), health.Degraded, nil, "RFalse: m"},
		{"programmed: the later of both, no reason", cond("True", 2, t1), cond("True", 2, t2), health.OK, &t2, ""},
		{"programmed without Accepted's time: undated", cond("True", 2, time.Time{}), cond("True", 2, t2), health.OK, nil, ""},
		{"syncing: undated", cond("True", 1, t1), cond("True", 1, t2), health.Progressing, nil, ""},
		{"pending: undated", cond("Unknown", 2, t1), Cond{}, health.Progressing, nil, ""},
		{"no status: undated", Cond{}, Cond{}, health.Unknown, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := GatewayRule(c.accepted, c.programd, 2)
			if e.Role != c.role || e.Reason != c.reason {
				t.Fatalf("role %s reason %q, want %s %q", e.Role, e.Reason, c.role, c.reason)
			}
			switch {
			case c.since == nil && e.Since != nil:
				t.Fatalf("want no Since, got %v", *e.Since)
			case c.since != nil && (e.Since == nil || !e.Since.Equal(*c.since)):
				t.Fatalf("want Since %v, got %v", *c.since, e.Since)
			}
		})
	}
}

// A route's role is the worst of several issues, some observed only, so it
// is never dated; a condition-raised worst issue names its reason.
func TestRouteRuleReasonWithoutSince(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	rn := acceptedRoute(svc("a", 0, 0))
	rn.AcceptedCond = Cond{Status: "False", Reason: "NotAllowedByListeners", Message: "no listener allows this kind", LastTransitionTime: at}
	if e := RouteRule(rn, nil); e.Role != health.Fault || e.Since != nil || e.Reason != "NotAllowedByListeners: no listener allows this kind" {
		t.Errorf("not accepted: got %+v", e)
	}
	if e := RouteRule(acceptedRoute(svc("a", 0, 0)), nil); e.Role != health.Fault || e.Since != nil || e.Reason != "" {
		t.Errorf("an observed-only issue has no reason: got %+v", e)
	}
}
