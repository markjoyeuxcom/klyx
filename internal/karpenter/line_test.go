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
	if e := ClaimRule(failed); e.Role != health.Fault || e.Since == nil || !e.Since.Equal(at) || e.Reason != "InsufficientCapacity: no offering" {
		t.Errorf("not ready: dated by Ready, the failing link's reason, got %+v", e)
	}

	stuck := NodeClaim{Ready: "unknown", Created: created, AgeSeconds: 1000}
	e := ClaimRule(stuck)
	if e.Role != health.Degraded || e.Since == nil || !e.Since.Equal(deadline) || e.Reason != "not ready 15m0s after creation" {
		t.Errorf("appears stuck is derived from the launch deadline, got %+v", e)
	}
	stuck.ReadySince = deadline.Add(time.Minute) // Ready went Unknown after the deadline
	stuck.FailingType, stuck.FailingReason, stuck.FailingMessage = "Registered", "NodeNotFound", "node not registered"
	if e := ClaimRule(stuck); e.Since == nil || !e.Since.Equal(stuck.ReadySince) || e.Reason != "NodeNotFound: node not registered" {
		t.Errorf("appears stuck, later Ready change and a named link, got %+v", e)
	}

	if e := ClaimRule(NodeClaim{Ready: "unknown", Created: created, AgeSeconds: 60, ReadySince: at}); e.Role != health.Progressing || e.Since != nil || e.Reason != "" {
		t.Errorf("launching: undated, got %+v", e)
	}
}

func TestPoolRuleSinceAndReason(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	if e := PoolRule(NodePool{Ready: "not-ready", Reason: "NodeClassNotReady", Message: "nodeclass missing", ReadySince: at}); e.Since == nil || !e.Since.Equal(at) || e.Reason != "NodeClassNotReady: nodeclass missing" {
		t.Errorf("not ready pool, got %+v", e)
	}
	if e := PoolRule(NodePool{Ready: "unknown"}); e.Since != nil || e.Reason != "" {
		t.Errorf("no Ready condition: undated, got %+v", e)
	}
}
