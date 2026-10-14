package flux

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The Line §3.2 / §8.1: the facts the comparison needs, parsed from the
// objects the Observer already keeps.

func lineObj(kind string, spec, status map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "x/v1", "kind": kind,
		"metadata": map[string]interface{}{"name": "n", "namespace": "flux-system"},
		"spec":     spec, "status": status,
	}}
}

func readyCond(status, reason, msg, at string) map[string]interface{} {
	return map[string]interface{}{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": at}
}

func TestParseSourceRefPrecedenceAndTimes(t *testing.T) {
	cases := []struct {
		kind     string
		ref      map[string]interface{}
		wantKind string
		wantName string
	}{
		{"GitRepository", map[string]interface{}{"branch": "main"}, "branch", "main"},
		{"GitRepository", map[string]interface{}{"branch": "main", "tag": "v1"}, "tag", "v1"},
		{"GitRepository", map[string]interface{}{"tag": "v1", "semver": ">=1"}, "semver", ">=1"},
		{"GitRepository", map[string]interface{}{"semver": ">=1", "name": "refs/heads/x"}, "name", "refs/heads/x"},
		{"GitRepository", map[string]interface{}{"branch": "main", "commit": "abc1234"}, "commit", "abc1234"},
		{"OCIRepository", map[string]interface{}{"tag": "latest", "digest": "sha256:aa"}, "digest", "sha256:aa"},
		{"Bucket", map[string]interface{}{"branch": "main"}, "", ""},
	}
	for _, c := range cases {
		s := ParseSource(lineObj(c.kind, map[string]interface{}{"url": "ssh://git@github.com/acme/fleet", "ref": c.ref},
			map[string]interface{}{
				"artifact":   map[string]interface{}{"revision": "main@sha1:abc", "lastUpdateTime": "2026-10-14T10:00:00Z"},
				"conditions": []interface{}{readyCond("True", "Succeeded", "", "2026-10-14T09:00:00Z")},
			}))
		if s.RefKind != c.wantKind || s.RefName != c.wantName {
			t.Errorf("%s %v: ref %s/%s, want %s/%s", c.kind, c.ref, s.RefKind, s.RefName, c.wantKind, c.wantName)
		}
		if !s.ArtifactUpdated.Equal(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)) || !s.ReadySince.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("%s: artifact %v, ready since %v", c.kind, s.ArtifactUpdated, s.ReadySince)
		}
	}
}

func TestParseAttemptedRevisionAndHelmHistory(t *testing.T) {
	k := ParseKustomization(lineObj("Kustomization", map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "fleet"}},
		map[string]interface{}{"lastAppliedRevision": "main@sha1:aaa", "lastAttemptedRevision": "main@sha1:bbb"}))
	if k.Revision != "main@sha1:aaa" || k.LastAttemptedRevision != "main@sha1:bbb" || k.History != nil {
		t.Fatalf("kustomization facts: %+v", k)
	}
	var hist []interface{}
	for v := 1; v <= 7; v++ { // oldest first, more than HistoryMax
		status := "superseded"
		if v == 7 {
			status = "failed"
		}
		if v == 6 {
			status = "deployed"
		}
		hist = append(hist, map[string]interface{}{"version": int64(v), "chartName": "podinfo", "chartVersion": "6.0." + string(rune('0'+v)), "configDigest": "sha256:c", "status": status, "lastDeployed": "2026-10-14T10:00:00Z"})
	}
	h := ParseHelmRelease(lineObj("HelmRelease", map[string]interface{}{}, map[string]interface{}{"history": hist, "lastAttemptedRevision": "6.0.7"}))
	if len(h.History) != HistoryMax || h.History[0].Version != 7 || h.History[0].Status != "failed" || h.History[1].Status != "deployed" || h.History[4].Version != 3 {
		t.Fatalf("history newest first, capped: %+v", h.History)
	}
	if h.LastAttemptedRevision != "6.0.7" || h.History[1].LastDeployed.IsZero() {
		t.Fatalf("helm attempted and deployed time: %+v", h)
	}
}

// Since is filled only when Ready's own time dates the role; Reason only for
// a role that is neither ok nor unknown.
func TestRuleSinceAndReasonAreReadyBacked(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	base := Resource{Kind: KustomizationKind, Ready: Failed, Reason: "BuildFailed", Message: "kustomize build failed", ReadySince: at}
	if e := ReconciliationRule(base); e.Role != health.Fault || e.Since == nil || !e.Since.Equal(at) || e.Reason != "BuildFailed: kustomize build failed" {
		t.Fatalf("a failure is dated by Ready and carries its reason: %+v", e)
	}
	ok := base
	ok.Ready, ok.Reason, ok.Message = Ready, "ReconciliationSucceeded", "applied"
	if e := ReconciliationRule(ok); e.Since == nil || e.Reason != "" {
		t.Fatalf("ready is dated, with no reason: %+v", e)
	}
	for name, r := range map[string]Resource{
		"suspended":   func() Resource { x := base; x.Suspended = true; return x }(),
		"stale":       func() Resource { x := base; x.Stale = true; return x }(),
		"reconciling": func() Resource { x := base; x.Ready = Reconciling; return x }(),
	} {
		if e := ReconciliationRule(r); e.Since != nil || e.Reason != "" {
			t.Errorf("%s: Ready's time does not date this role: %+v", name, e)
		}
	}
	src := Source{Ready: Failed, Reason: "GitOperationFailed", Message: "auth", ReadySince: at}
	if e := SourceRule(src); e.Since == nil || e.Reason != "GitOperationFailed: auth" {
		t.Fatalf("a failing source is dated: %+v", e)
	}
	long := health.CapReason("R", string(make([]rune, 500)))
	if n := len([]rune(long)); n != health.ReasonMax {
		t.Fatalf("reason capped at %d runes, got %d", health.ReasonMax, n)
	}
}
