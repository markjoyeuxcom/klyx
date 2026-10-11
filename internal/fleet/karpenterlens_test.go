package fleet

import (
	"context"
	"strconv"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

var (
	npGVRTest = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	ncGVRTest = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
)

func karpPool(name, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]interface{}{"name": name},
		"spec":       map[string]interface{}{"limits": map[string]interface{}{"cpu": "100"}},
		"status": map[string]interface{}{
			"resources":  map[string]interface{}{"cpu": "10"},
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": ready}},
		},
	}}
}

func karpClaim(name, pool, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodeClaim",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": map[string]interface{}{"karpenter.sh/nodepool": pool},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": ready}},
		},
	}}
}

func newKarpConn(present bool, typedObjs []runtime.Object, dynObjs ...runtime.Object) *ClusterConn {
	listKinds := map[schema.GroupVersionResource]string{
		npGVRTest: "NodePoolList",
		ncGVRTest: "NodeClaimList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, dynObjs...)
	typed := typedfake.NewSimpleClientset(typedObjs...)
	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.NewFake(time.Unix(1000, 0)), config.MetricsConfig{})
	c.mu.Lock()
	c.caps.Karpenter.Present = present
	c.mu.Unlock()
	return c
}

func TestKarpenterState_NotDetectedIsHonest(t *testing.T) {
	c := newKarpConn(false, nil)
	snap, err := c.KarpenterState(context.Background())
	if err != nil || snap.Detected {
		t.Fatalf("absent karpenter: %+v %v", snap, err)
	}
}

func TestKarpenterState_TriageAndControllerHealth(t *testing.T) {
	karpDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "karpenter"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2},
	}
	disruptEv := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ev1"},
		InvolvedObject: corev1.ObjectReference{Kind: "NodeClaim", Name: "wk-old"},
		Type:           "Normal", Reason: "DisruptionTerminating",
		Message:       "disrupting via consolidation delete",
		LastTimestamp: metav1.Time{Time: time.Unix(900, 0)},
	}
	c := newKarpConn(true, []runtime.Object{karpDep, disruptEv},
		karpPool("workers", "True"),
		karpPool("gpu", "False"),
		karpClaim("wk-1", "workers", "True"),
		karpClaim("wk-2", "workers", "False"),
	)

	snap, err := c.KarpenterState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Detected || !snap.ControllerHealthy || !snap.AsOf.Equal(time.Unix(1000, 0)) {
		t.Fatalf("detection/health/as-of: %+v", snap)
	}
	// The NodePools and NodeClaims are the Observer's rows now (slice 3
	// §5.5): the snapshot never lists them.
	// The disruption feed carries the event.
	if len(snap.Events) != 1 || snap.Events[0].Reason != "DisruptionTerminating" || snap.Events[0].Kind != "NodeClaim" {
		t.Fatalf("events: %+v", snap.Events)
	}
}

func TestKarpenterState_PartialInputsStayUnverified(t *testing.T) {
	c := newKarpConn(true, nil, karpPool("workers", "True"), karpClaim("wk-1", "workers", "True"))
	typed := c.typed.(*typedfake.Clientset)
	typed.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "karpenter", nil)
	})
	typed.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.ListAction).GetListRestrictions().Fields.String() == "involvedObject.kind=NodePool" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)
		}
		return false, nil, nil
	})
	snap, err := c.KarpenterState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ControllerVerified || snap.ControllerHealthy || snap.ControllerError == "" {
		t.Fatalf("a forbidden controller read must be unverified with its reason: %+v", snap)
	}
	if snap.EventsError == "" {
		t.Fatalf("a failed event read is reported: %+v", snap)
	}
}

func TestKarpenterState_MissingControllerIsUnverifiedAndFeedCapIsDisclosed(t *testing.T) {
	var objs []runtime.Object
	for i := 0; i < karpenterEventCap; i++ {
		objs = append(objs, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ev" + strconv.Itoa(i)},
			InvolvedObject: corev1.ObjectReference{Kind: "NodeClaim", Name: "c"},
			LastTimestamp:  metav1.Time{Time: time.Unix(int64(500+i), 0)},
		})
	}
	c := newKarpConn(true, objs, karpPool("workers", "True"))
	snap, err := c.KarpenterState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ControllerVerified || snap.ControllerError == "" {
		t.Fatalf("no karpenter Deployment under the expected name is unverified, not an outage: %+v", snap)
	}
	if !snap.EventsTruncated {
		t.Fatalf("a full page of events must disclose partial coverage: %+v", snap)
	}
}
