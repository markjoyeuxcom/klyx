package fleet

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListEvents_WarningFirst(t *testing.T) {
	now := time.Unix(300_000, 0)

	warn := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "team", Name: "ev-warn"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api"},
		Type:           "Warning",
		Reason:         "BackOff",
		Message:        "back-off",
		Count:          3,
		LastTimestamp:  metav1.NewTime(now),
		FirstTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
	}
	normal := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "team", Name: "ev-norm"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
		Type:           "Normal",
		Reason:         "Pulled",
		Message:        "pulled image",
		Count:          1,
		LastTimestamp:  metav1.NewTime(now.Add(time.Second)), // newer timestamp but Normal
		FirstTimestamp: metav1.NewTime(now),
	}

	cs := fake.NewSimpleClientset(warn, normal)
	c := &ClusterConn{typed: cs}

	out, _, err := c.ListEvents(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 events, got %d", len(out))
	}
	// Warning must be first regardless of timestamp.
	if out[0].Type != "Warning" {
		t.Errorf("out[0].Type = %q, want Warning", out[0].Type)
	}
	if out[1].Type != "Normal" {
		t.Errorf("out[1].Type = %q, want Normal", out[1].Type)
	}
}

func TestListEvents_NamespaceScope(t *testing.T) {
	now := time.Unix(300_000, 0)

	evA := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "ns-a", Name: "ev-a"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "pod-a"},
		Type:           "Normal", Reason: "Pulled", Message: "ok",
		Count:         1,
		LastTimestamp: metav1.NewTime(now),
	}
	evB := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "ns-b", Name: "ev-b"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "pod-b"},
		Type:           "Warning", Reason: "BackOff", Message: "crash",
		Count:         2,
		LastTimestamp: metav1.NewTime(now),
	}

	cs := fake.NewSimpleClientset(evA, evB)
	c := &ClusterConn{typed: cs}

	out, _, err := c.ListEvents(context.Background(), "ns-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 event in ns-a, got %d", len(out))
	}
	if out[0].Namespace != "ns-a" {
		t.Errorf("namespace scope failed: got %q", out[0].Namespace)
	}
	if out[0].Reason != "Pulled" {
		t.Errorf("reason: got %q, want Pulled", out[0].Reason)
	}
}

func TestListEvents_FieldMapping(t *testing.T) {
	now := time.Unix(300_000, 0)
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ev-1"},
		InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "web"},
		Type:           "Warning",
		Reason:         "ReplicaSetFailed",
		Message:        "pod quota exceeded",
		Count:          7,
		LastTimestamp:  metav1.NewTime(now),
		FirstTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
	}

	cs := fake.NewSimpleClientset(ev)
	c := &ClusterConn{typed: cs}

	out, _, err := c.ListEvents(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	s := out[0]
	if s.Kind != "Deployment" {
		t.Errorf("Kind: got %q, want Deployment", s.Kind)
	}
	if s.Name != "web" {
		t.Errorf("Name: got %q, want web", s.Name)
	}
	if s.Count != 7 {
		t.Errorf("Count: got %d, want 7", s.Count)
	}
	if s.LastSeenUnix != now.Unix() {
		t.Errorf("LastSeenUnix: got %d, want %d", s.LastSeenUnix, now.Unix())
	}
	if s.Type != "Warning" {
		t.Errorf("Type: got %q, want Warning", s.Type)
	}
	if s.Message != "pod quota exceeded" {
		t.Errorf("Message: got %q", s.Message)
	}
}
