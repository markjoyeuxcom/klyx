package workloads

import (
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTemplateImages_InitFirst(t *testing.T) {
	spec := corev1.PodSpec{
		InitContainers: []corev1.Container{{Image: "busybox:1.36"}},
		Containers:     []corev1.Container{{Image: "shop/cart:v2"}, {Image: "envoy:v1.30"}},
	}
	want := []string{"busybox:1.36", "shop/cart:v2", "envoy:v1.30"}
	if got := TemplateImages(spec); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestRevisionDataImages(t *testing.T) {
	raw := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"web","image":"nginx:1.27"}],"initContainers":[{"name":"init","image":"busybox:1.36"}]}}}}`)
	want := []string{"busybox:1.36", "nginx:1.27"}
	if got := RevisionDataImages(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := RevisionDataImages([]byte(`{"status":{}}`)); got != nil {
		t.Fatalf("template-less data must yield nil, got %v", got)
	}
	if got := RevisionDataImages([]byte(`not json`)); got != nil {
		t.Fatalf("garbage must yield nil, got %v", got)
	}
}

func TestAssembleCarriesDeploymentPaused(t *testing.T) {
	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "held"},
		Spec:       appsv1.DeploymentSpec{Paused: true, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "held"}}},
	}
	ws := Assemble([]appsv1.Deployment{d}, nil, nil, nil, false, time.Unix(0, 0))
	if len(ws) != 1 || !ws[0].Paused {
		t.Fatalf("paused must carry: %+v", ws)
	}
}
