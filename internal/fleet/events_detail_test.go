package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/crd"
)

// Codex's Stage 0 review, finding 1: a generic Event's detail - YAML,
// fields, Summary - carries its text under its subject's rule, in either
// API, and is neither editable nor described raw.
func TestEventDetailUsesSubjectRule(t *testing.T) {
	const n = "PATHneedle1D"
	for _, c := range []struct{ group, subject, field string }{{"", "involvedObject", "message"}, {"events.k8s.io", "regarding", "note"}} {
		api := "v1"
		if c.group != "" {
			api = c.group + "/v1"
		}
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": api, "kind": "Event",
			"metadata": map[string]any{"name": "gh.1", "namespace": "flux-system", "uid": "e-1"},
			c.subject:  map[string]any{"apiVersion": "notification.toolkit.fluxcd.io/v1", "kind": "Receiver", "name": "gh", "uid": "r-1"},
			"reason":   "Succeeded",
			c.field:    "Receiver initialized for path: /hook/" + n,
			"type":     "Normal"}}
		gvr := schema.GroupVersionResource{Group: c.group, Version: "v1", Resource: "events"}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{gvr: "EventList"}, obj)
		// the inspector's typed events of the same object share the
		// classifier (Codex's Stage 0 review 2): a related Provider's rule
		typed := typedfake.NewSimpleClientset(&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "p.1", Namespace: "flux-system"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p", UID: "e-1"}, Related: &corev1.ObjectReference{Kind: "Provider", Name: "slack"},
			Reason: "NotificationDispatchFailed", Message: "failed to send to https://hooks.example/T/" + n})
		conn := NewClusterConn("c", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
		d, err := conn.GetInstanceDetail(context.Background(), c.group, "v1", "events", "flux-system", "gh.1")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), n) || d.EditEligible || d.DescribeMode != crd.DescribeWithheld || len(d.Events) != 1 {
			t.Fatalf("%s: %s", c.group, b)
		}
		var text string
		for _, cell := range d.Summary {
			if cell.Column.JSONPath == "."+c.field {
				text = cell.Value
			}
		}
		if text != "Receiver initialized for path: "+crd.WithheldValue {
			t.Fatalf("%s: the Summary shows the text under the rule: %+v", c.group, d.Summary)
		}
	}
}

// The guarded editor refuses an Event whose live text its subject's rule
// changes, in Klyx's words for an Event, and never patches it.
func TestApplyRefusesASanitisedEvent(t *testing.T) {
	live := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"name": "gh.1", "namespace": "flux-system", "uid": "e-1", "resourceVersion": "3"},
		"involvedObject": map[string]any{"kind": "Receiver", "name": "gh"}, "reason": "Succeeded",
		"message": "Receiver initialized for path: /hook/PATHneedle1A"}}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "EventList"}, live)
	patched := false
	dyn.PrependReactor("patch", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		patched = true
		return true, live, nil
	})
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	doc := "apiVersion: v1\nkind: Event\nmetadata:\n  namespace: flux-system\n  name: gh.1\ninvolvedObject:\n  kind: Receiver\n  name: gh\nreason: Succeeded\nmessage: '%s'\n"
	apply := func(msg string) error {
		return c.ApplyResourceYAML(context.Background(), "", "v1", "events", "Event", "Namespaced", "flux-system", "gh.1", "e-1", fmt.Sprintf(doc, msg), true)
	}
	// the shown text, placeholder and all, never goes back
	if err := apply("Receiver initialized for path: " + crd.WithheldValue); err == nil || patched {
		t.Fatalf("the shown text is refused: %v (patched %v)", err, patched)
	}
	// nor does any edit of an Event whose live text the rule changes
	if err := apply("edited"); err == nil || !strings.Contains(err.Error(), "this Event's message") || patched {
		t.Fatalf("an Event under its subject's rule is not edited: %v (patched %v)", err, patched)
	}
}
