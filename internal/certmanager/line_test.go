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

	failed := Certificate{Ready: Failed, Reason: "DoesNotExist", Message: "secret missing", ReadySince: ready, IssuingChanged: issuing}
	if e := CertificateRule(failed, now); e.Role != health.Fault || e.Since == nil || !e.Since.Equal(issuing) || e.Reason != "DoesNotExist: secret missing" {
		t.Errorf("failed after an issuance: the later Issuing time dates the role, got %+v", e)
	}
	failed.IssuingChanged = time.Time{}
	if e := CertificateRule(failed, now); e.Since == nil || !e.Since.Equal(ready) {
		t.Errorf("failed, no Issuing condition: Ready's time, got %+v", e)
	}
	unknown := Certificate{Ready: Unknown, Reason: "Pending", ReadySince: ready}
	if e := CertificateRule(unknown, now); e.Since == nil || e.Reason != "" {
		t.Errorf("unknown with a Ready condition: dated, no reason, got %+v", e)
	}

	expired := Certificate{Ready: Ready, NotAfter: now.Add(-90 * time.Minute), ReadySince: ready}
	e := CertificateRule(expired, now)
	if e.Role != health.Fault || e.Since == nil || !e.Since.Equal(now.Add(-90*time.Minute)) || !strings.HasPrefix(e.Reason, "Expired: notAfter ") {
		t.Errorf("expired is derived from notAfter, got %+v", e)
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
