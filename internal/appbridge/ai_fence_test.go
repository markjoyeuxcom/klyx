package appbridge

import (
	"errors"
	"strings"
	"testing"

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
