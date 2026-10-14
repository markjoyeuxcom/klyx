package appbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/observe"
)

// fakeCLIProvider is CLI-shaped: it reports readiness, and Test probes it
// instead of sending the plain connectivity prompt.
type fakeCLIProvider struct {
	fakeCompleter
	ready  error
	probed int
}

func (f *fakeCLIProvider) Ready() error { return f.ready }

func (f *fakeCLIProvider) Probe(context.Context) (ai.Result, error) {
	f.probed++
	return ai.Result{Text: "I cannot access files.", ModelUsed: "claude-opus-5-5"}, nil
}

func cliState(p *fakeCLIProvider) *AIState {
	return &AIState{ProviderID: ai.ProviderClaudeCLI, Provider: p, Model: "claude-opus-5-5", Origin: ai.ClaudeCLIOrigin}
}

// A CLI that would refuse - an untested build, a revoked admission - is
// shown as unavailable with its reason, and Explain stops before building
// any evidence or calling it.
func TestAI_CLINotReady(t *testing.T) {
	p := &fakeCLIProvider{ready: &ai.CLIRefusedError{Reason: "This Claude Code build (2.1.300, sha256 0123456789ab) has not been checked by Klyx on this machine yet"}}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("pod-uid", 1)}}
	s := NewAIService(func(string) (AIConn, bool) { return conn, true }, cliState(p), func(string) bool { return false }, nil)

	av := s.Available()
	if av.Ready || !strings.Contains(av.Problem, "has not been checked by Klyx") || av.Origin != ai.ClaudeCLIOrigin {
		t.Fatalf("availability: %+v", av)
	}
	r := s.Explain("c", rowTarget())
	if r.OK || !strings.Contains(r.Error, "has not been checked by Klyx") || p.calls != 0 {
		t.Fatalf("Explain must stop before the call: %+v (calls %d)", r, p.calls)
	}

	p.ready = nil
	p.reply = "The container keeps exiting."
	if av := s.Available(); !av.Ready {
		t.Fatalf("a ready CLI is available: %+v", av)
	}
	if r := s.Explain("c", rowTarget()); !r.OK || r.Origin != ai.ClaudeCLIOrigin {
		t.Fatalf("got %+v", r)
	}
}

// A refusal from the CLI's own checks reads as Klyx's decision, not as a
// model failure.
func TestAI_CLIRefusalWording(t *testing.T) {
	got := modelError(&ai.CLIRefusedError{Reason: "Claude Code is signed in to a Team or Enterprise account"})
	if got != "Klyx did not send this to Claude Code: Claude Code is signed in to a Team or Enterprise account." {
		t.Fatalf("got %q", got)
	}
}

// Codex review 2026-10-13 #10: an answer discarded after the prompt was sent
// never reads as "did not send".
func TestAI_CLIDiscardedWording(t *testing.T) {
	got := modelError(&ai.CLIDiscardedError{Reason: "a concurrent call attempted a tool"})
	if got != "Claude Code received this, but Klyx discarded its answer: a concurrent call attempted a tool." {
		t.Fatalf("got %q", got)
	}
	got = modelError(&ai.CLIDiscardedError{Reason: "Klyx could not finish handing the prompt to Claude Code: broken pipe", Partial: true})
	if !strings.HasPrefix(got, "Part of this may have reached Claude Code") || strings.Contains(got, "did not send") {
		t.Fatalf("got %q", got)
	}
}

func TestAISettings_TestProbesACLI(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	shim := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Status describes the draft's binary without running it: a launcher
	// script is never a tested build.
	st := f.svc.CLIStatus(AISettingsDTO{Provider: "claudeCLI", KeySource: "none", CLIPath: shim})
	if st.Tested || st.Admitted || st.Problem == "" {
		t.Fatalf("status: %+v", st)
	}
	if st := f.svc.CLIStatus(AISettingsDTO{Provider: "openaiCompatible", Model: "m", BaseURL: "http://localhost:11434", KeySource: "none"}); !strings.Contains(st.Problem, "not a CLI") {
		t.Fatalf("status of a non-CLI provider: %+v", st)
	}
	// Test of a launcher script refuses without running it.
	r := f.svc.TestProvider(AISettingsDTO{Provider: "claudeCLI", KeySource: "none", CLIPath: shim})
	if r.OK || r.Error == "" {
		t.Fatalf("test: %+v", r)
	}
}

// The CLI path is a setting the panel edits and saves.
func TestAISettings_CLIPathRoundTrips(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	if r := f.svc.SaveAISettings(AISettingsDTO{Provider: "claudeCLI", KeySource: "none", CLIPath: "  /opt/claude/2.1.296  "}); !r.OK {
		t.Fatalf("save: %+v", r)
	}
	st := f.svc.GetAISettings()
	if st.Persisted.CLIPath != "/opt/claude/2.1.296" || st.Persisted.Provider != "claudeCLI" || st.Diverged {
		t.Fatalf("persisted: %+v", st)
	}
	if !strings.Contains(f.file(t), "cliPath: /opt/claude/2.1.296") {
		t.Fatalf("fleet.yaml:\n%s", f.file(t))
	}
	if a := f.ai.Available(); a.Ready || a.Origin != ai.ClaudeCLIOrigin || a.Problem == "" {
		t.Fatalf("a missing binary is not ready: %+v", a)
	}
}

// Test runs the CLI's probe, not the plain connectivity prompt.
func TestAISettings_TestUsesTheCLIProbe(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	p := &fakeCLIProvider{}
	f.svc.build = func(ai.Settings, ai.KeyStore, func(string) string) (*ai.Built, error) {
		return &ai.Built{ID: ai.ProviderClaudeCLI, Provider: p, Model: "claude-opus-5-5", Origin: ai.ClaudeCLIOrigin}, nil
	}
	r := f.svc.TestProvider(AISettingsDTO{Provider: "claudeCLI", KeySource: "none"})
	if !r.OK || p.probed != 1 || p.calls != 0 || r.Reply != "I cannot access files." || r.Origin != ai.ClaudeCLIOrigin {
		t.Fatalf("test: %+v (probed %d, calls %d)", r, p.probed, p.calls)
	}
}
