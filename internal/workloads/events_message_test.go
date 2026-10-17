package workloads

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/crd"
)

// Coverage S1b §6: an ImageUpdateAutomation event message passes the
// allowlist where every event row is made; another kind's is unchanged.
func TestSummarizeEventsAppliesMessageRules(t *testing.T) {
	const needle = "EVENTneedle4K"
	evs := []corev1.Event{
		{Type: "Normal", Reason: "Succeeded", Message: "pushed commit 'abc1234' to branch 'main'\nbump image " + needle,
			InvolvedObject: corev1.ObjectReference{APIVersion: "image.toolkit.fluxcd.io/v1", Kind: "ImageUpdateAutomation", Name: "apps"}},
		{Type: "Warning", Reason: "GitOperationFailed", Message: "push option ci.variable=" + needle + " rejected",
			InvolvedObject: corev1.ObjectReference{Kind: "ImageUpdateAutomation", Name: "apps"}},
		{Type: "Warning", Reason: "BuildFailed", Message: "kustomize build failed: " + needle,
			InvolvedObject: corev1.ObjectReference{Kind: "Kustomization", Name: "apps"}},
	}
	byReason := map[string]string{}
	for _, e := range SummarizeEvents(evs) {
		byReason[e.Reason] = e.Message
	}
	if byReason["Succeeded"] != "pushed commit 'abc1234' to branch 'main'" {
		t.Fatalf("the push summary's first line only: %q", byReason["Succeeded"])
	}
	if byReason["GitOperationFailed"] != crd.AutomationWithheld {
		t.Fatalf("withheld whole, without a group too: %q", byReason["GitOperationFailed"])
	}
	if !strings.Contains(byReason["BuildFailed"], needle) {
		t.Fatalf("another kind's message is unchanged: %q", byReason["BuildFailed"])
	}
}

// Coverage S1c §6: Alert and Provider event messages are withheld whole; a
// Kustomization's event passes Origins whatever it references now.
func TestSummarizeEventsNotificationAndOrigins(t *testing.T) {
	const needle = "TOKENneedle5W"
	evs := []corev1.Event{
		{Type: "Warning", Reason: "NotificationDispatchFailed", Message: "failed to send to https://hooks.slack.com/services/T/B/" + needle,
			InvolvedObject: corev1.ObjectReference{Kind: "Alert", Name: "a"}},
		{Type: "Warning", Reason: "ArtifactFailed", Message: "failed to download artifact from https://store.example/a?sig=" + needle,
			InvolvedObject: corev1.ObjectReference{Kind: "Kustomization", Name: "apps"}},
	}
	for _, e := range SummarizeEvents(evs) {
		if strings.Contains(e.Message, needle) {
			t.Fatalf("%s crossed: %q", e.Reason, e.Message)
		}
	}
}

// Codex's Stage 0 review 2, finding 1: a typed Event row shares the generic
// Event classifier - its related object's rule applies, and a subject with
// no kind withholds the text.
func TestSummarizeEventsSharesTheEventClassifier(t *testing.T) {
	const needle = "EVENTneedle2R"
	evs := []corev1.Event{
		{Reason: "NotificationDispatchFailed", Message: "failed to send to https://hooks.example/T/" + needle,
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"}, Related: &corev1.ObjectReference{Kind: "Provider", Name: "slack"}},
		{Reason: "Succeeded", Message: "Receiver initialized for path: /hook/" + needle, InvolvedObject: corev1.ObjectReference{Name: "gh"}},
		{Reason: "Succeeded", Message: "Receiver initialized for path: /hook/" + needle,
			InvolvedObject: corev1.ObjectReference{Kind: "Pod"}, Related: &corev1.ObjectReference{Name: "gh"}},
		{Reason: "Pulled", Message: "Successfully pulled image", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"}},
	}
	for _, e := range SummarizeEvents(evs) {
		if strings.Contains(e.Message, needle) {
			t.Fatalf("%s: %q", e.Reason, e.Message)
		}
		if e.Reason == "Pulled" && e.Message != "Successfully pulled image" {
			t.Fatalf("a pod event without a related object is unchanged: %q", e.Message)
		}
	}
}
