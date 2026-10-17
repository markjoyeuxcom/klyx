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
