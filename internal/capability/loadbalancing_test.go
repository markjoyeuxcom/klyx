package capability

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func metallbDep(ns, name string, labels map[string]string, ready bool) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: &one}}
	if ready {
		d.Status.AvailableReplicas = 1
	}
	return d
}

// Coverage S4 §2: MetalLB's controller by the native or the chart labels;
// not ready only for one read not ready; unknown when unreadable or absent.
func TestS4DetectLoadBalancing(t *testing.T) {
	served := map[string]bool{metallbGroup: true, awsLBCGroup: true}
	native := metallbDep("metallb-system", "controller", map[string]string{"app": "metallb", "component": "controller"}, true)
	chart := metallbDep("lb", "metallb-controller", map[string]string{"app.kubernetes.io/name": "metallb", "app.kubernetes.io/component": "controller"}, false)
	got := (&Detector{cs: fake.NewSimpleClientset(native, chart)}).detectLoadBalancing(context.Background(), served)
	if !got.MetalLB || !got.AWSLBC || !got.ControllersRead || len(got.Controllers) != 2 || !got.Verified || got.Healthy || got.Tier != Degraded {
		t.Fatalf("both labels, one not ready: %+v", got)
	}
	got = (&Detector{cs: fake.NewSimpleClientset(native)}).detectLoadBalancing(context.Background(), served)
	if !got.Healthy || got.Tier != Healthy {
		t.Fatalf("ready: %+v", got)
	}
	got = (&Detector{cs: fake.NewSimpleClientset()}).detectLoadBalancing(context.Background(), served)
	if got.Verified || got.Healthy || got.Reason != metallbNoCtl || !got.ControllersRead {
		t.Fatalf("no controller found is unknown: %+v", got)
	}
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	got = (&Detector{cs: cs}).detectLoadBalancing(context.Background(), served)
	if got.ControllersRead || got.Verified || len(got.Controllers) != 0 {
		t.Fatalf("unreadable: %+v", got)
	}
	if got := (&Detector{cs: fake.NewSimpleClientset()}).detectLoadBalancing(context.Background(), map[string]bool{}); got.MetalLB || got.Tier != Absent {
		t.Fatalf("absent: %+v", got)
	}
}
