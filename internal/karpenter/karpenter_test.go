package karpenter

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func nodePoolU(name string, ready string, cpuLimit, cpuUsed string) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]interface{}{"name": name},
		"spec":       map[string]interface{}{"weight": int64(10), "limits": map[string]interface{}{}},
		"status": map[string]interface{}{
			"resources": map[string]interface{}{},
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": ready, "reason": "NodeClassReady", "message": "ok"},
			},
		},
	}
	if cpuLimit != "" {
		obj["spec"].(map[string]interface{})["limits"].(map[string]interface{})["cpu"] = cpuLimit
	}
	if cpuUsed != "" {
		obj["status"].(map[string]interface{})["resources"].(map[string]interface{})["cpu"] = cpuUsed
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestParseNodePool(t *testing.T) {
	p := ParseNodePool(nodePoolU("workers", "True", "100", "42500m"))
	if p.Name != "workers" || p.Ready != "ready" || p.Weight != 10 {
		t.Fatalf("pool: %+v", p)
	}
	if p.CPULimit == nil || *p.CPULimit != 100 || p.CPUUsed == nil || *p.CPUUsed != 42.5 {
		t.Fatalf("cpu quantities: %+v", p)
	}
	// Unset limit stays nil - never a fabricated zero.
	if p.MemLimit != nil || p.MemUsed != nil {
		t.Fatalf("unset mem must stay nil: %+v", p)
	}
	if bad := ParseNodePool(nodePoolU("broken", "False", "", "")); bad.Ready != "not-ready" || bad.Reason != "NodeClassReady" {
		t.Fatalf("broken pool: %+v", bad)
	}
}

func nodeClaimU(name, pool string, created time.Time, conds ...map[string]interface{}) *unstructured.Unstructured {
	cs := make([]interface{}, 0, len(conds))
	for _, c := range conds {
		cs = append(cs, c)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodeClaim",
		"metadata": map[string]interface{}{
			"name":              name,
			"creationTimestamp": created.UTC().Format("2006-01-02T15:04:05Z"),
			"labels": map[string]interface{}{
				LabelNodePool:     pool,
				LabelCapacityType: "spot",
				LabelInstanceType: "m7g.large",
			},
		},
		"status": map[string]interface{}{"nodeName": name + "-node", "conditions": cs},
	}}
}

func cond(t, status, reason, message string) map[string]interface{} {
	return map[string]interface{}{"type": t, "status": status, "reason": reason, "message": message}
}

func TestParseNodeClaim_HealthyAndIdentity(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	c := ParseNodeClaim(nodeClaimU("wk-abc", "workers", now.Add(-time.Hour),
		cond("Launched", "True", "", ""), cond("Registered", "True", "", ""),
		cond("Initialized", "True", "", ""), cond("Ready", "True", "", "")), now)
	if c.Ready != "ready" || c.FailingType != "" {
		t.Fatalf("claim: %+v", c)
	}
	if c.Pool != "workers" || c.CapacityType != "spot" || c.InstanceType != "m7g.large" || c.NodeName != "wk-abc-node" {
		t.Fatalf("identity: %+v", c)
	}
	if c.AgeSeconds != 3600 {
		t.Fatalf("age: %d", c.AgeSeconds)
	}
}

func TestParseNodeClaim_NamesTheFirstBrokenLink(t *testing.T) {
	now := time.Unix(10000, 0)
	// Launched but never registered: the diagnosis is Registered, not Ready.
	c := ParseNodeClaim(nodeClaimU("stuck", "workers", now,
		cond("Launched", "True", "", ""),
		cond("Registered", "False", "NodeNotFound", "node did not join"),
		cond("Initialized", "False", "", ""),
		cond("Ready", "False", "NotRegistered", "")), now)
	if c.Ready != "not-ready" || c.FailingType != "Registered" || c.FailingReason != "NodeNotFound" {
		t.Fatalf("stuck claim: %+v", c)
	}
	// No chain conditions at all: falls back to Ready's own reason.
	c2 := ParseNodeClaim(nodeClaimU("odd", "workers", now,
		cond("Ready", "False", "SomethingElse", "msg")), now)
	if c2.FailingType != "Ready" || c2.FailingReason != "SomethingElse" {
		t.Fatalf("fallback: %+v", c2)
	}
}

func TestParseNodeClaim_InformationalStates(t *testing.T) {
	now := time.Unix(0, 0)
	c := ParseNodeClaim(nodeClaimU("drifty", "workers", now,
		cond("Ready", "True", "", ""), cond("Drifted", "True", "RequirementsDrifted", ""),
		cond("Consolidatable", "True", "", "")), now)
	if c.Ready != "ready" || !c.Drifted || !c.Consolidatable {
		t.Fatalf("informational states are not errors: %+v", c)
	}
}

func TestSorting(t *testing.T) {
	pools := []NodePool{{Name: "b", Ready: "ready"}, {Name: "a", Ready: "not-ready"}}
	SortPoolsBrokenFirst(pools)
	if pools[0].Name != "a" {
		t.Fatalf("pools: %+v", pools)
	}
	claims := []NodeClaim{
		{Name: "old-stuck", Ready: "not-ready", AgeSeconds: 9000},
		{Name: "ok", Ready: "ready", AgeSeconds: 50},
		{Name: "new-stuck", Ready: "not-ready", AgeSeconds: 300},
	}
	SortClaimsStuckFirst(claims)
	if claims[0].Name != "new-stuck" || claims[2].Name != "ok" {
		t.Fatalf("claims: %+v", claims)
	}
}

func TestParseNodeClaim_OnlyAConfirmedFalseLinkIsTheFailure(t *testing.T) {
	now := time.Unix(10000, 0)
	// Ready=False, Launched Unknown, Registered False: the proven failure is
	// Registered, never the merely-unverified Launched.
	c := ParseNodeClaim(nodeClaimU("x", "workers", now,
		cond("Launched", "Unknown", "", ""),
		cond("Registered", "False", "NodeNotFound", "node did not join"),
		cond("Ready", "False", "NotRegistered", "")), now)
	if c.FailingType != "Registered" || c.FailingStatus != "False" {
		t.Fatalf("want the confirmed False link: %+v", c)
	}
	// No False link: the first non-True link is named with its own status.
	c2 := ParseNodeClaim(nodeClaimU("y", "workers", now,
		cond("Launched", "Unknown", "", ""),
		cond("Ready", "False", "", "")), now)
	if c2.FailingType != "Launched" || c2.FailingStatus != "Unknown" {
		t.Fatalf("want an unverified link with its status: %+v", c2)
	}
	// An unsupported Ready status is unknown, never a fault.
	c3 := ParseNodeClaim(nodeClaimU("z", "workers", now, cond("Ready", "Bogus", "", "")), now)
	if c3.Ready != "unknown" {
		t.Fatalf("unsupported status must be unknown: %+v", c3)
	}
	// An absent Ready condition is unknown.
	c4 := ParseNodeClaim(nodeClaimU("w", "workers", now), now)
	if c4.Ready != "unknown" {
		t.Fatalf("absent Ready must be unknown: %+v", c4)
	}
}
