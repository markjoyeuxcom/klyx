package appbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/policy"
	"github.com/moomora/klyx/internal/readfence"
)

// Settings slice F §4: Explain and Suggest-fix pin the revision and the
// cluster's epoch at their start; the fence refuses every later read and
// send once either moves.
func TestAnExplainsFenceFollowsTheRevisionAndTheEpoch(t *testing.T) {
	reg := installRegistry(t, false, "x")
	s := NewAIService(nil, &AIState{}, nil, nil)
	st := s.load()
	fence, why := s.callFence(st, "x")
	if why != "" || fence() != nil {
		t.Fatalf("admitted: %q %v", why, fence())
	}
	tighten(t, reg, "x")
	if err := fence(); err == nil || !strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("after a move: %v", err)
	}
	fence2, _ := s.callFence(s.load(), "x")
	SetAIState(s, &AIState{})
	if err := fence2(); err == nil || !strings.Contains(err.Error(), "AI settings changed") {
		t.Fatalf("after a revision: %v", err)
	}
	if _, why := s.callFence(s.load(), "unknown"); why == "" {
		t.Fatal("an unknown cluster was admitted")
	}
	if _, err := reg.Hold("x", "the reconnect failed"); err != nil {
		t.Fatal(err)
	}
	if _, why := s.callFence(s.load(), "x"); !strings.Contains(why, "saved but not active") {
		t.Fatalf("a held cluster: %q", why)
	}
}

// What a fence refusal tells the person: nothing sent, or maybe sent.
func TestAFenceRefusalSaysWhatWasSent(t *testing.T) {
	notSent := modelError(errors.Join(&readfence.Error{Err: errors.New("x: its policy changed")}, readfence.ErrNotForwarded))
	if !strings.HasPrefix(notSent, "Nothing was sent: x: its policy changed") {
		t.Fatalf("%q", notSent)
	}
	maybe := modelError(&readfence.Error{Err: errors.New("x: its connection restarted")})
	if !strings.Contains(maybe, "may have been sent") || !strings.Contains(maybe, "connection restarted") {
		t.Fatalf("%q", maybe)
	}
}

// fencedProvider honours its call's fence, as the hardened transport does,
// and counts the sends that went past it.
type fencedProvider struct{ calls int }

func (p *fencedProvider) Complete(ctx context.Context, _ ai.Request) (ai.Result, error) {
	if err := readfence.Check(ctx); err != nil {
		return ai.Result{}, err
	}
	p.calls++
	return ai.Result{Text: "synthetic reply"}, nil
}

// racingProtection is a protection hook preempted after its read: the
// registry tightens before it returns the older answer (Codex's probe),
// the first time it is asked about target.
func racingProtection(t *testing.T, reg *policy.Registry, target string, race bool) func(string) bool {
	return func(c string) bool {
		snap, _ := reg.Snapshot(c)
		if race && c == target {
			race = false
			tighten(t, reg, c)
		}
		return snap.Protected
	}
}

// Settings F1 review, finding F1: Explain and Suggest-fix pin the epoch
// before egress is decided, so a protection change that a preempted read
// missed moves the pinned epoch and nothing is sent; unchanged, the call
// goes ahead.
func TestAnAICallsEgressIsDecidedUnderItsPinnedEpoch(t *testing.T) {
	root := crd.InstanceDetail{Kind: "Deployment", Namespace: "app", Name: "api", YAML: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n"}
	calls := map[string]func(*AIService) AIResultDTO{
		"explain": func(s *AIService) AIResultDTO { return s.Explain("c", rowTarget()) },
		"suggest-fix": func(s *AIService) AIResultDTO {
			return s.SuggestFix("c", "apps", "v1", "deployments", "app", "api", "api-uid")
		},
	}
	for name, call := range calls {
		for _, race := range []bool{false, true} {
			t.Run(map[bool]string{false: name + "/unchanged", true: name + "/tightened"}[race], func(t *testing.T) {
				p := &fencedProvider{}
				conn := podConn()
				conn.details["apps/deployments/app/api"] = root
				st := &AIState{Provider: p, ProviderID: ai.ProviderAnthropic, Origin: ai.DefaultAnthropicOrigin}
				s := NewAIService(func(string) (AIConn, bool) { return conn, true }, st, racingProtection(t, installRegistry(t, false, "c"), "c", race), nil)
				r := call(s)
				if race && (r.OK || p.calls != 0 || !strings.Contains(r.Error, "policy changed")) {
					t.Fatalf("egress admitted under an old permission and a new epoch: calls=%d %+v", p.calls, r)
				}
				if !race && (!r.OK || p.calls != 1) {
					t.Fatalf("control: an unchanged unprotected call: calls=%d %+v", p.calls, r)
				}
			})
		}
	}
}
