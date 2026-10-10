package gwapi

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func gatewayWith(conds ...interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "eg", "namespace": "infra", "generation": int64(3)},
		"spec":     map[string]interface{}{"gatewayClassName": "c"},
	}}
	if len(conds) > 0 {
		u.Object["status"] = map[string]interface{}{"conditions": conds}
	}
	return u
}

// Codex review (step 6): absent, Unknown and False are different evidence.
func TestGatewayConditionEvidence(t *testing.T) {
	absent := ParseGatewayRef(gatewayWith())
	if absent.AcceptedCond.Status != "" || absent.Accepted {
		t.Fatalf("absent: %+v", absent.AcceptedCond)
	}
	unknown := ParseGatewayRef(gatewayWith(map[string]interface{}{"type": "Accepted", "status": "Unknown", "reason": "Pending", "observedGeneration": int64(3)}))
	if unknown.AcceptedCond.Status != "Unknown" || unknown.AcceptedCond.Reason != "Pending" {
		t.Fatalf("unknown: %+v", unknown.AcceptedCond)
	}
	stale := ParseGateway(gatewayWith(map[string]interface{}{"type": "Accepted", "status": "False", "reason": "InvalidParameters", "observedGeneration": int64(2)}))
	if stale.AcceptedCond.Status != "False" || stale.AcceptedCond.ObservedGeneration != 2 || stale.Generation != 3 {
		t.Fatalf("stale False must keep its generation: %+v gen=%d", stale.AcceptedCond, stale.Generation)
	}
}

func TestRouteParentEvidence(t *testing.T) {
	route := func(status map[string]interface{}) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "r", "namespace": "apps", "generation": int64(5)},
			"spec":     map[string]interface{}{"parentRefs": []interface{}{map[string]interface{}{"name": "eg", "namespace": "infra"}}},
		}}
		if status != nil {
			u.Object["status"] = status
		}
		return u
	}
	rn, ok := RouteForGateway(route(nil), "infra", "eg")
	if !ok || rn.ParentFound || rn.AcceptedCond.Status != "" {
		t.Fatalf("no status for this parent is absent evidence: %+v", rn)
	}
	rn, _ = RouteForGateway(route(map[string]interface{}{"parents": []interface{}{map[string]interface{}{
		"parentRef":  map[string]interface{}{"name": "eg", "namespace": "infra"},
		"conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "False", "reason": "NotAllowedByListeners", "observedGeneration": int64(5)}, map[string]interface{}{"type": "ResolvedRefs", "status": "True"}},
	}}}), "infra", "eg")
	if !rn.ParentFound || rn.AcceptedCond.Status != "False" || rn.AcceptedCond.Reason != "NotAllowedByListeners" || rn.Accepted || !rn.ResolvedRefs || rn.Generation != 5 {
		t.Fatalf("explicit False: %+v", rn)
	}
}
