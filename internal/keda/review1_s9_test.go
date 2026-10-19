package keda

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/hpa"
)

// Implementation review 1, finding 3 (Codex's probe, kept): a joined HPA
// clears KEDA's HPAActive=False only once §4 establishes its health - a
// controller that has not reported it, or one behind its generation,
// leaves the ScaledObject unknown, never ok.
func TestReview1JoinedHPAHasToEstablishHealth(t *testing.T) {
	for _, name := range []string{"no-conditions", "unacknowledged-generation"} {
		t.Run(name, func(t *testing.T) {
			h := hpaOf("owned")
			if name == "unacknowledged-generation" {
				h = hpaOf("owned", hpaOK...)
				h.Generation = 2
				g := int64(1)
				h.ObservedGeneration = &g
			}
			if hpa.Rule(h, now, nil).Role == health.OK {
				t.Fatal("invalid control: the standalone HPA is OK")
			}
			s := so(nil, ready, active, noFb, "HPAActive=False:FailedGetResourceMetric:old failure")
			got := ScaledObjectRule(s, SOInputs{Now: now, Version: v221, HPA: current(h)})
			if got.Eval.Role != health.Unknown || got.Eval.Label != "HPA health not established" {
				t.Errorf("against KEDA's False: %+v", got.Eval)
			}
		})
	}
	// without KEDA's False: the HPA's own state, never "healthy"
	fresh := hpaOf("owned")
	fresh.Created = now.Add(-30 * time.Second)
	if v := rule(so(nil, ready, active, noFb, notPause), SOInputs{HPA: current(fresh)}); v.Eval.Role != health.Progressing || v.Eval.Label != "HPA: Waiting for the HPA controller" || v.Eval.Next == nil {
		t.Fatalf("a new HPA not yet reported: %+v", v.Eval)
	}
	lag := hpaOf("owned", hpaOK...)
	lag.Generation = 3
	g := int64(2)
	lag.ObservedGeneration = &g
	if v := rule(so(nil, ready, active, noFb, notPause), SOInputs{HPA: current(lag)}); v.Eval.Role != health.Progressing || v.Eval.Label != "HPA: Syncing…" {
		t.Fatalf("an HPA behind its generation: %+v", v.Eval)
	}
	// established: KEDA's stale copy is a note
	ok := hpaOf("owned", hpaOK...)
	if v := rule(so(nil, ready, active, noFb, "HPAActive=False:FailedGetResourceMetric:old"), SOInputs{HPA: current(ok)}); v.Eval.Role != health.OK {
		t.Fatalf("an established healthy HPA: %+v", v.Eval)
	}
}
