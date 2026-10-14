package gwapi

import (
	"fmt"
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
		gen                int64
		accepted, programd Cond
		role               health.Kind
		since              *time.Time
		cond               string // "Type=Status@hour", "" for none
		reason             string
	}{
		{"not accepted: Accepted's time", 1, cond("False", 1, t1), Cond{}, health.Fault, &t1, "Accepted=False@9", "RFalse: m"},
		{"not accepted on an edited Gateway: evidence only", 2, cond("False", 2, t1), Cond{}, health.Fault, nil, "Accepted=False@9", "RFalse: m"},
		{"not accepted, last observed: reason and evidence, no Since", 2, cond("False", 1, t1), Cond{}, health.Degraded, nil, "Accepted=False@9", "RFalse: m"},
		{"not programmed after a later acceptance: the later time", 1, cond("True", 1, t2), cond("False", 1, t1), health.Degraded, &t2, "Programmed=False@9", "RFalse: m"},
		{"not programmed after an earlier acceptance: Programmed's time", 1, cond("True", 1, t1), cond("False", 1, t2), health.Degraded, &t2, "Programmed=False@10", "RFalse: m"},
		{"not programmed, last observed", 2, cond("True", 2, t1), cond("False", 1, t2), health.Degraded, nil, "Programmed=False@10", "RFalse: m"},
		{"programmed: the later of both, no reason", 1, cond("True", 1, t1), cond("True", 1, t2), health.OK, &t2, "Programmed=True@10", ""},
		{"programmed without Accepted's time: undated", 1, cond("True", 1, time.Time{}), cond("True", 1, t2), health.OK, nil, "Programmed=True@10", ""},
		{"syncing: undated", 2, cond("True", 1, t1), cond("True", 1, t2), health.Progressing, nil, "", ""},
		{"pending: undated", 1, cond("Unknown", 1, t1), Cond{}, health.Progressing, nil, "", ""},
		{"no status: undated", 1, Cond{}, Cond{}, health.Unknown, nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := GatewayRule(c.accepted, c.programd, c.gen)
			if e.Role != c.role || e.Reason != c.reason {
				t.Fatalf("role %s reason %q, want %s %q", e.Role, e.Reason, c.role, c.reason)
			}
			switch {
			case c.since == nil && e.Since != nil:
				t.Fatalf("want no Since, got %v", *e.Since)
			case c.since != nil && (e.Since == nil || !e.Since.Equal(*c.since)):
				t.Fatalf("want Since %v, got %v", *c.since, e.Since)
			}
			got := ""
			if e.Cond != nil {
				got = fmt.Sprintf("%s=%s@%d", e.Cond.Type, e.Cond.Status, e.Cond.At.Hour())
			}
			if got != c.cond {
				t.Fatalf("condition evidence %q, want %q", got, c.cond)
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

// Codex review of 75b3990: Accepted=False at 09:00 (fault), the spec edited at
// 10:00 (last observed, degraded), re-observed at 11:00 with the same status
// and time. The fault that follows is not dated 09:00.
func TestGatewaySinceAcrossALastObservedOverlay(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	c := Cond{Status: "False", Reason: "Invalid", ObservedGeneration: 1, LastTransitionTime: at}
	if e := GatewayRule(c, Cond{}, 1); e.Role != health.Fault || e.Since == nil || !e.Since.Equal(at) {
		t.Fatalf("never edited: dated, got %+v", e)
	}
	if e := GatewayRule(c, Cond{}, 2); e.Role != health.Degraded || e.Since != nil {
		t.Fatalf("the overlay: last observed, undated, got %+v", e)
	}
	c.ObservedGeneration = 2
	if e := GatewayRule(c, Cond{}, 2); e.Role != health.Fault || e.Since != nil || e.Reason == "" || e.Cond == nil || !e.Cond.At.Equal(at) {
		t.Fatalf("after the overlay: fault, undated, Accepted's age kept as evidence, got %+v", e)
	}
}
