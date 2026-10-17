package crd

import (
	"encoding/json"
	"strings"
	"testing"
)

const automationNeedle = "AUTOMATIONneedle7Q"

// Coverage S1b §6: automation messages are an allowlist.
func TestAutomationMessage(t *testing.T) {
	cases := []struct {
		name, in, want string
		changed        bool
	}{
		{"empty", "", "", false},
		{"ready", "repository up-to-date", "repository up-to-date", false},
		{"no change", "no change since last reconciliation", "no change since last reconciliation", false},
		{"in progress", "reconciliation in progress", "reconciliation in progress", false},
		{"new generation", "processing object: new generation 3 -> 4", "processing object: new generation 3 -> 4", false},
		{"push summary alone", "pushed commit 'abc1234' to branch 'main'", "pushed commit 'abc1234' to branch 'main'", false},
		{"push summary, commit message dropped", "pushed commit 'abc1234' to branch 'main'\nUpdate image " + automationNeedle, "pushed commit 'abc1234' to branch 'main'", true},
		{"refspecs", "pushed commit 'abc1234' to branch 'auto' and refspecs 'refs/heads/auto:refs/heads/main'", "pushed commit 'abc1234' to branch 'auto' and refspecs 'refs/heads/auto:refs/heads/main'", false},
		{"two refspecs, forced", "pushed commit 'abc1234' to branch 'auto' and refspecs '+refs/heads/auto:refs/heads/smth/else', 'refs/heads/auto:refs/heads/foo'\nmsg", "pushed commit 'abc1234' to branch 'auto' and refspecs '+refs/heads/auto:refs/heads/smth/else', 'refs/heads/auto:refs/heads/foo'", true},
		{"a Git rejection quoting a push option", "failed to update source: failed to push to remote: command error on refs/heads/main: push option ci.variable=" + automationNeedle + " rejected", AutomationWithheld, true},
		{"a template error", "template: commit:1: unexpected {{end}} " + automationNeedle, AutomationWithheld, true},
		{"a malformed branch", "pushed commit 'abc1234' to branch 'ma..in'", AutomationWithheld, true},
		{"a quote in the branch", "pushed commit 'abc1234' to branch 'main' " + automationNeedle + "'", AutomationWithheld, true},
		{"a space in the branch", "pushed commit 'abc1234' to branch 'main " + automationNeedle + "'", AutomationWithheld, true},
		{"trailing text on the first line", "pushed commit 'abc1234' to branch 'main' " + automationNeedle, AutomationWithheld, true},
		{"a short hash", "pushed commit 'abc12' to branch 'main'", AutomationWithheld, true},
		{"a glob refspec", "pushed commit 'abc1234' to branch 'auto' and refspecs 'refs/heads/*:refs/heads/*'", AutomationWithheld, true},
		{"a deletion refspec", "pushed commit 'abc1234' to branch 'auto' and refspecs ':refs/heads/main'", AutomationWithheld, true},
		{"a fixed message with more after it", "repository up-to-date " + automationNeedle, AutomationWithheld, true},
		{"generation not integers", "processing object: new generation 3 -> " + automationNeedle, AutomationWithheld, true},
	}
	for _, c := range cases {
		got, changed := AutomationMessage(c.in)
		if got != c.want || changed != c.changed {
			t.Errorf("%s: got %q (changed %v), want %q (%v)", c.name, got, changed, c.want, c.changed)
		}
	}
	if p, ok := ParsePushSummary("pushed commit 'abc1234' to branch 'auto' and refspecs 'refs/heads/auto:refs/heads/main'"); !ok || p.Branch != "auto" || p.Commit != "abc1234" || len(p.Refspecs) != 1 {
		t.Fatalf("parse: %+v %v", p, ok)
	}
	if got, _ := MessageRule("Kustomization", "kustomize build failed "+automationNeedle); !strings.Contains(got, automationNeedle) {
		t.Fatal("another kind's message passes unchanged")
	}
}

func iua(conds ...any) map[string]any {
	return map[string]any{
		"apiVersion": "image.toolkit.fluxcd.io/v1", "kind": "ImageUpdateAutomation",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system"},
		"spec": map[string]any{"git": map[string]any{
			"commit": map[string]any{"messageTemplate": "deploy {{ .Values.token }} " + automationNeedle,
				"messageTemplateValues": map[string]any{"token": automationNeedle}},
			"push": map[string]any{"branch": "main", "options": map[string]any{"ci.variable": automationNeedle}},
		}},
		"status": map[string]any{"conditions": conds},
	}
}

// S1b §6: the registry entries and the condition message rule, through the
// sanitiser every path reads.
func TestSanitizeImageUpdateAutomation(t *testing.T) {
	s := SanitizeEmbedded("image.toolkit.fluxcd.io", "ImageUpdateAutomation", iua(
		map[string]any{"type": "Ready", "status": "False", "reason": "GitOperationFailed", "message": "failed to push: push option ci.variable=" + automationNeedle + " rejected"},
		map[string]any{"type": "Reconciling", "status": "True", "reason": "Progressing", "message": "reconciliation in progress"},
	))
	b, _ := json.Marshal(s.Object)
	if strings.Contains(string(b), automationNeedle) {
		t.Fatalf("a needle crossed: %s", b)
	}
	if !strings.Contains(string(b), AutomationWithheld) || !strings.Contains(string(b), "GitOperationFailed") || !strings.Contains(string(b), "reconciliation in progress") {
		t.Fatalf("the reason and allowlisted messages stay: %s", b)
	}
	if !s.Payload || !s.Messages || s.DescribeMode != DescribeWithheld || s.Hold() != HoldPayload {
		t.Fatalf("policy: %+v", s)
	}
	// Payload-free, with an unsafe retained condition: Describe is held for
	// the message, and an edit is not blocked by it.
	o := iua(map[string]any{"type": "Ready", "status": "False", "reason": "GitOperationFailed", "message": "push option ci.variable=" + automationNeedle})
	delete(o, "spec")
	s = SanitizeEmbedded("image.toolkit.fluxcd.io", "ImageUpdateAutomation", o)
	if s.Payload || !s.Messages || s.Hold() != HoldMessages || !s.EditEligible || s.DescribeMode != DescribeWithheld {
		t.Fatalf("payload-free policy: %+v", s)
	}
	// All allowlisted: nothing held.
	o = iua(map[string]any{"type": "Ready", "status": "True", "reason": "Succeeded", "message": "repository up-to-date"})
	delete(o, "spec")
	if s = SanitizeEmbedded("image.toolkit.fluxcd.io", "ImageUpdateAutomation", o); s.Hold() != "" || s.Messages {
		t.Fatalf("fixed messages hold nothing: %+v", s)
	}
	// Unexpected shapes are withheld, never passed raw.
	o = iua(map[string]any{"type": "Ready", "message": map[string]any{"x": automationNeedle}}, automationNeedle)
	delete(o, "spec")
	s = SanitizeEmbedded("image.toolkit.fluxcd.io", "ImageUpdateAutomation", o)
	if b, _ := json.Marshal(s.Object); strings.Contains(string(b), automationNeedle) || !s.Messages {
		t.Fatalf("unexpected shapes withheld: %s", b)
	}
	o = iua()
	delete(o, "spec")
	o["status"] = map[string]any{"conditions": automationNeedle}
	if b, _ := json.Marshal(SanitizeEmbedded("image.toolkit.fluxcd.io", "ImageUpdateAutomation", o).Object); strings.Contains(string(b), automationNeedle) {
		t.Fatalf("conditions of another type withheld: %s", b)
	}
	if !IsPayloadKind("image.toolkit.fluxcd.io", "ImageUpdateAutomation") || !MessageSensitive("ImageUpdateAutomation") || MessageSensitive("Kustomization") {
		t.Fatal("registration")
	}
}
