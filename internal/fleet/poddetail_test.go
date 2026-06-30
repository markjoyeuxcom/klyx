package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
)

func makeFakePod(ns, name string) *corev1.Pod {
	ctrl := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         ns,
			Name:              name,
			UID:               "uid-test-1",
			Labels:            map[string]string{"app": "web"},
			Annotations:       map[string]string{"owner": "platform"},
			CreationTimestamp: metav1.NewTime(time.Unix(100_000, 0)),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "web-rs", Controller: &ctrl},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "web-sa",
			Containers:         []corev1.Container{{Name: "web", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{
			Phase:    corev1.PodRunning,
			PodIP:    "10.0.0.5",
			QOSClass: corev1.PodQOSBurstable,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, Reason: ""},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "web",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}
}

func TestPodDetail_BasicFields(t *testing.T) {
	now := time.Unix(200_000, 0)
	pod := makeFakePod("team-a", "web-abc")
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "web-abc.e1", Namespace: "team-a"},
		InvolvedObject: corev1.ObjectReference{UID: "uid-test-1"},
		Type:           "Warning",
		Reason:         "Backoff",
		Message:        "back off",
		Count:          2,
		LastTimestamp:  metav1.NewTime(now),
	}

	cs := typedfake.NewSimpleClientset(pod, ev)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	d, err := c.PodDetail(context.Background(), "team-a", "web-abc")
	if err != nil {
		t.Fatalf("PodDetail: %v", err)
	}

	// Summary populated.
	if d.Summary.Name != "web-abc" || d.Summary.Namespace != "team-a" {
		t.Errorf("summary name/ns: %+v", d.Summary)
	}
	if d.Summary.Rank != workloads.Healthy {
		t.Errorf("rank: got %v, want Healthy", d.Summary.Rank)
	}
	if d.Summary.Node != "node-1" || d.Summary.IP != "10.0.0.5" {
		t.Errorf("node/ip: %+v", d.Summary)
	}
	if d.Summary.OwnerKind != "ReplicaSet" || d.Summary.OwnerName != "web-rs" {
		t.Errorf("owner: %+v", d.Summary)
	}

	// Detail-specific fields.
	if d.Labels["app"] != "web" {
		t.Errorf("labels: %+v", d.Labels)
	}
	if d.Annotations["owner"] != "platform" {
		t.Errorf("annotations: %+v", d.Annotations)
	}
	if d.ServiceAccount != "web-sa" {
		t.Errorf("serviceAccount: %q", d.ServiceAccount)
	}
	if d.QoSClass != "Burstable" {
		t.Errorf("qosClass: %q", d.QoSClass)
	}

	// Conditions mapped.
	if len(d.Conditions) != 1 || d.Conditions[0].Type != "Ready" || d.Conditions[0].Status != "True" {
		t.Errorf("conditions: %+v", d.Conditions)
	}

	// Events via instanceEvents.
	if len(d.Events) != 1 || d.Events[0].Reason != "Backoff" || d.Events[0].Count != 2 {
		t.Errorf("events: %+v", d.Events)
	}

	// YAML contains pod kind.
	if !strings.Contains(d.YAML, "kind: Pod") {
		t.Errorf("yaml missing 'kind: Pod': %s", d.YAML)
	}
	// managedFields stripped.
	if strings.Contains(d.YAML, "managedFields") {
		t.Errorf("yaml must not contain managedFields")
	}
}

func TestPodDetail_NotFound(t *testing.T) {
	cs := typedfake.NewSimpleClientset()
	c := &ClusterConn{typed: cs, clk: clock.Real{}}
	_, err := c.PodDetail(context.Background(), "ns", "missing")
	if err == nil {
		t.Fatal("want error for missing pod, got nil")
	}
}

func TestPodDetail_EmptyLabelsNonNil(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "bare",
			UID:       "uid-bare",
			// No labels set.
		},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img:1"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	cs := typedfake.NewSimpleClientset(pod)
	c := &ClusterConn{typed: cs, clk: clock.Real{}}

	d, err := c.PodDetail(context.Background(), "ns", "bare")
	if err != nil {
		t.Fatalf("PodDetail: %v", err)
	}
	if d.Labels == nil {
		t.Error("Labels must be non-nil even when pod has no labels")
	}
	if d.Annotations == nil {
		t.Error("Annotations must be non-nil even when pod has no annotations")
	}
}
