package karpenter

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

// The Line §8.1: the parsers keep the Ready condition's lastTransitionTime.
func TestParseKeepsReadySince(t *testing.T) {
	u := nodePoolU("default", "False", "", "")
	u.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["lastTransitionTime"] = "2026-10-14T09:00:00Z"
	if p := ParseNodePool(u); !p.ReadySince.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("pool ready since %v", p.ReadySince)
	}
	claim := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "NodeClaim", "metadata": map[string]interface{}{"name": "c1"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True", "lastTransitionTime": "2026-10-14T10:00:00Z"},
		}},
	}}
	if c := ParseNodeClaim(claim, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)); !c.ReadySince.Equal(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("claim ready since %v", c.ReadySince)
	}
}

func TestClaimRuleSinceAndReason(t *testing.T) {
	created := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	at := created.Add(2 * time.Minute)
	deadline := created.Add(LaunchDeadline + time.Second)

	if e := ClaimRule(NodeClaim{Ready: "ready", ReadySince: at, Created: created}); e.Since == nil || !e.Since.Equal(at) || e.Reason != "" {
		t.Errorf("ready: dated by Ready, got %+v", e)
	}
	failed := NodeClaim{Ready: "not-ready", ReadySince: at, Created: created, FailingType: "Launched", FailingStatus: "False", FailingReason: "InsufficientCapacity", FailingMessage: "no offering"}
	if e := ClaimRule(failed); e.Role != health.Fault || e.Since == nil || !e.Since.Equal(at) ||
		e.Reason != "Launched False: InsufficientCapacity: no capacity was available for the requested instance types" {
		t.Errorf("not ready: dated by Ready, the failing link in Klyx's wording (S10 §8), got %+v", e)
	}

	stuck := NodeClaim{Ready: "unknown", Created: created, AgeSeconds: 1000}
	e := ClaimRule(stuck)
	if e.Role != health.Degraded || e.Since == nil || !e.Since.Equal(deadline) || e.Reason != "not ready 15m0s after creation" {
		t.Errorf("appears stuck is derived from the launch deadline, got %+v", e)
	}
	stuck.ReadySince = deadline.Add(time.Minute) // Ready went Unknown after the deadline
	stuck.FailingType, stuck.FailingStatus, stuck.FailingReason, stuck.FailingMessage = "Registered", "Unknown", "NodeNotFound", "node not registered"
	if e := ClaimRule(stuck); e.Since == nil || !e.Since.Equal(stuck.ReadySince) || e.Reason != "Registered Unknown: NodeNotFound: no node has registered for the claim" {
		t.Errorf("appears stuck, later Ready change and a named link, got %+v", e)
	}

	if e := ClaimRule(NodeClaim{Ready: "unknown", Created: created, AgeSeconds: 60, ReadySince: at}); e.Role != health.Progressing || e.Since != nil || e.Reason != "" {
		t.Errorf("launching: undated, got %+v", e)
	}
}

func TestPoolRuleSinceAndReason(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	ready := &Cond{Type: "Ready", Status: "False", Reason: "NodeClassNotReady", Message: "nodeclass missing", At: at}
	p := NodePool{Ready: "not-ready", CondReady: ready, Conds: Conds{Total: 1}}
	// S10: the role is the worst of several inputs (the class, its link, the
	// controllers), so its start is not provable and Since stays nil; the
	// deciding condition is the dated evidence.
	e := PoolRule(p, PoolInputs{}).Eval
	if e.Since != nil || e.Cond == nil || !e.Cond.At.Equal(at) ||
		e.Reason != "not ready: Ready False: NodeClassNotReady: see the condition in Klyx" {
		t.Errorf("not ready pool, got %+v", e)
	}
	if e := PoolRule(NodePool{Ready: "unknown"}, PoolInputs{}).Eval; e.Since != nil || e.Role != health.Unknown || e.Label != "not reported" {
		t.Errorf("no conditions: not reported, undated, got %+v", e)
	}
}
