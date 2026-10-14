package certmanager

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

// The Line §8.1: the parser keeps each condition's lastTransitionTime.
func TestParseKeepsConditionTimes(t *testing.T) {
	u := certObj("False", "DoesNotExist", false)
	conds := u.Object["status"].(map[string]interface{})["conditions"].([]interface{})
	conds[0].(map[string]interface{})["lastTransitionTime"] = "2026-10-14T09:00:00Z"
	conds = append(conds, map[string]interface{}{"type": "Issuing", "status": "False", "reason": "Failed", "lastTransitionTime": "2026-10-14T09:05:00Z"})
	u.Object["status"].(map[string]interface{})["conditions"] = conds
	c := ParseCertificate(u)
	if !c.ReadySince.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) || !c.IssuingChanged.Equal(time.Date(2026, 10, 14, 9, 5, 0, 0, time.UTC)) {
		t.Fatalf("ready since %v, issuing changed %v", c.ReadySince, c.IssuingChanged)
	}
	i := ParseIssuer(&unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ClusterIssuer", "metadata": map[string]interface{}{"name": "le"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True", "lastTransitionTime": "not a time"},
		}},
	}})
	if !i.ReadySince.IsZero() {
		t.Fatalf("an invalid time must read as absent, got %v", i.ReadySince)
	}
}

// Since only when a time matches the role; Reason only for a non-ok role.
func TestCertificateRuleSinceAndReason(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ready := now.Add(-3 * time.Hour)
	issuing := now.Add(-2 * time.Hour)

	failed := Certificate{Ready: Failed, Reason: "DoesNotExist", Message: "secret missing", ReadySince: ready, IssuingChanged: issuing, Generation: 1}
	if e := CertificateRule(failed, now); e.Role != health.Fault || e.Since == nil || !e.Since.Equal(issuing) || e.Reason != "DoesNotExist: secret missing" {
		t.Errorf("failed after an issuance: the later Issuing time dates the role, got %+v", e)
	}
	if e := CertificateRule(failed, now); e.Cond == nil || e.Cond.Type != "Ready" || e.Cond.Status != "False" || !e.Cond.At.Equal(ready) {
		t.Errorf("failed: Ready's own time is condition evidence, got %+v", e.Cond)
	}
	failed.IssuingChanged = time.Time{}
	if e := CertificateRule(failed, now); e.Since != nil || e.Cond == nil || e.Reason == "" {
		t.Errorf("failed, no Issuing condition (it may have been removed unseen): undated, evidence and reason kept, got %+v", e)
	}
	unknown := Certificate{Ready: Unknown, Reason: "Pending", ReadySince: ready, IssuingChanged: issuing, Generation: 1}
	if e := CertificateRule(unknown, now); e.Since != nil || e.Cond == nil || e.Cond.Status != "Unknown" || e.Reason != "" {
		t.Errorf("unknown: an expiry can overlay it unseen, so undated; evidence, no reason, got %+v", e)
	}

	// Codex review of 696afee: notAfter comes from the current Secret, which
	// can be replaced with already-expired material, so it is evidence (in
	// the reason), never the role's start.
	expired := Certificate{Ready: Ready, NotAfter: now.Add(-90 * time.Minute), ReadySince: ready, Generation: 1}
	e := CertificateRule(expired, now)
	if e.Role != health.Fault || e.Since != nil || e.Reason != "Expired: notAfter "+now.Add(-90*time.Minute).Format(time.RFC3339) {
		t.Errorf("expired: undated, notAfter in the reason, got %+v", e)
	}

	// Ready=True, an issuance in flight, an unobserved spec, and no Ready
	// condition at all are not dated by any time the object carries.
	for name, c := range map[string]Certificate{
		"ready":        {Ready: Ready, ReadySince: ready, NotAfter: now.Add(time.Hour)},
		"issuing":      {Ready: Issuing, ReadySince: ready, IssuingChanged: issuing},
		"stale":        {Ready: Failed, Stale: true, ReadySince: ready},
		"no condition": {Ready: Unknown},
	} {
		if e := CertificateRule(c, now); e.Since != nil || e.Reason != "" {
			t.Errorf("%s: want no Since and no Reason, got %+v", name, e)
		}
	}
}

func TestIssuerRuleSinceAndReason(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	if e := IssuerRule(Issuer{Ready: Failed, Reason: "ErrRegisterACMEAccount", Message: "token=abc123secret", ReadySince: at}); e.Since == nil || !e.Since.Equal(at) || strings.Contains(e.Reason, "abc123secret") {
		t.Errorf("failed issuer: dated by Ready, reason scrubbed, got %+v", e)
	}
	if e := IssuerRule(Issuer{Ready: Ready, ReadySince: at}); e.Since == nil || e.Reason != "" {
		t.Errorf("ready issuer: dated, no reason, got %+v", e)
	}
	if e := IssuerRule(Issuer{}); e.Since != nil {
		t.Errorf("no Ready condition: no Since, got %+v", e)
	}
}

// Codex review of 75b3990: Ready=False since 09:00; a spec edit at 10:00
// makes the certificate Syncing...; cert-manager re-observes it at 11:00
// without changing Ready (it keeps the transition time when the status is
// unchanged). An edited certificate's fault is never dated by Ready's time.
func TestCertificateSinceAcrossASpecOverlay(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	c := Certificate{Ready: Failed, Reason: "DoesNotExist", ReadySince: at, IssuingChanged: at.Add(-time.Minute), Generation: 1, ObservedGeneration: 1}
	if e := CertificateRule(c, at); e.Since == nil || !e.Since.Equal(at) {
		t.Fatalf("never edited, Issuing present: dated, got %+v", e)
	}
	c.Generation, c.Stale = 2, true
	if e := CertificateRule(c, at.Add(time.Hour)); e.Role != health.Progressing {
		t.Fatalf("the overlay: progressing, got %+v", e)
	}
	c.Stale, c.ObservedGeneration = false, 2
	e := CertificateRule(c, at.Add(2*time.Hour))
	if e.Role != health.Fault || e.Since != nil || e.Reason == "" || e.Cond == nil || !e.Cond.At.Equal(at) {
		t.Fatalf("after the overlay: fault, undated, Ready's age kept as evidence, got %+v", e)
	}
}

// Codex review of 75b3990: a missing or invalid time leaves Since nil but
// never drops a known failure reason.
func TestCertificateReasonWithoutTime(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) // before the fixture's notAfter
	for name, lt := range map[string]any{"missing": nil, "invalid": "yesterday"} {
		u := certObj("False", "DoesNotExist", false)
		if lt != nil {
			u.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["lastTransitionTime"] = lt
		}
		u.SetGeneration(1)
		e := CertificateRule(ParseCertificate(u), now)
		if e.Role != health.Fault || e.Since != nil || e.Reason != "DoesNotExist: the cert is False" {
			t.Errorf("%s time: got %+v", name, e)
		}
	}
}
