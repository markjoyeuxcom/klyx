package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/keda"
	"strings"
	"testing"
	"time"
)

type review1BoundConn struct {
	fakeKEDAConn
	in fleet.KEDAInspection
}

func (c *review1BoundConn) KEDAInspect(context.Context, string, string, string, string) (fleet.KEDAInspection, error) {
	return c.in, nil
}

// Implementation review 1, finding 8 (Codex's probe, kept): the final
// inspector reply is bounded to 256 KiB as it crosses, references dropped
// from the longest list first and counted.
func TestReview1FinalInspectionDTOBound(t *testing.T) {
	c := &review1BoundConn{in: fleet.KEDAInspection{UID: "so-1", Handler: "keda/keda-operator", Version: "2.21.0"}}
	// Exactly the 16 x 32-reference shape used by the fleet probe. Each
	// authentication is below MaxAuthObject and each string is at its bound.
	for i := 0; i < 16; i++ {
		a := fleet.KEDAAuthCheck{Kind: "TriggerAuthentication", Name: fmt.Sprintf("ta-%02d", i), State: "exists", Visible: map[string]string{"keda/keda-operator": "visible"}, RefsCut: i > 0}
		for _, source := range []string{"secretTargetRef", "configMapTargetRef"} {
			kind := "Secret"
			if source == "configMapTargetRef" {
				kind = "ConfigMap"
			}
			for j := 0; j < 16; j++ {
				r := fleet.KEDARef{Source: source, Kind: kind, Namespace: "team", Name: fmt.Sprintf("s%02d", j) + strings.Repeat("a", 250), Key: strings.Repeat("k", 200), State: "unverified", Why: "past the read limit"}
				if i == 0 {
					r.State = "not found"
					r.Why = ""
				}
				a.Refs = append(a.Refs, r)
			}
		}
		c.in.Auths = append(c.in.Auths, a)
	}
	s := NewKEDAService(func(string) (KEDAConn, bool) { return c, true })
	got := s.Inspect("x", "scaledobjects", "team", "web", "so-1")
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 256<<10 {
		t.Fatalf("final inspector DTO is %d bytes; limit %d", len(b), 256<<10)
	}
	// counts survive the fit
	for _, a := range got.Auths {
		if len(a.Refs)+a.RefsOmitted != 32 || (a.RefsOmitted > 0 && !a.RefsCut) {
			t.Fatalf("%s: %d listed, %d omitted, cut %v", a.Name, len(a.Refs), a.RefsOmitted, a.RefsCut)
		}
	}
	// unverified references go before any finding: the first
	// authentication's 32 "not found" all stay
	if len(got.Auths[0].Refs) != 32 || got.Auths[0].RefsOmitted != 0 {
		t.Fatalf("findings were dropped before unverified references: %d listed, %d omitted", len(got.Auths[0].Refs), got.Auths[0].RefsOmitted)
	}
}

// Implementation review 1, finding 7: the evidence revision changes with
// every scaler input the on-demand evidence reads - generation, labels,
// target, references - and not with status.
func TestReview1EvidenceRev(t *testing.T) {
	base := func() *keda.Scaler {
		return &keda.Scaler{Meta: keda.Meta{Namespace: "team", Name: "web", Generation: 3, Labels: map[string]string{"team": "a"}},
			Triggers: []keda.Trigger{{Type: "prometheus", AuthName: "ta", AuthKind: keda.KindTriggerAuth}}, TriggerN: 1}
	}
	ref := evidenceRev(base(), "apps/Deployment/web")
	for name, mut := range map[string]func(s *keda.Scaler) string{
		"generation": func(s *keda.Scaler) string { s.Generation = 4; return "apps/Deployment/web" },
		"labels":     func(s *keda.Scaler) string { s.Labels["team"] = "b"; return "apps/Deployment/web" },
		"labels cut": func(s *keda.Scaler) string { s.LabelsCut = true; return "apps/Deployment/web" },
		"target":     func(s *keda.Scaler) string { return "apps/StatefulSet/web" },
		"reference":  func(s *keda.Scaler) string { s.Triggers[0].AuthName = "other"; return "apps/Deployment/web" },
		"kind": func(s *keda.Scaler) string {
			s.Triggers[0].AuthKind = keda.KindClusterTriggerAuth
			return "apps/Deployment/web"
		},
	} {
		s := base()
		if evidenceRev(s, mut(s)) == ref {
			t.Errorf("%s did not change the revision", name)
		}
	}
	s := base()
	s.Conds = []keda.Cond{{Type: "Ready", Status: "False"}}
	s.LastActive = time.Now()
	if evidenceRev(s, "apps/Deployment/web") != ref {
		t.Error("status changed the revision")
	}
}
