package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/readfence"
)

// Settings slice F §4: the call's fence is checked on every HTTP attempt
// and at the CLI's prompt handoff, not only at the provider's entry.

// redirectRT answers the first POST with a same-host 307, which the redirect
// policy permits, and moves the fence; it counts the POSTs it receives.
type redirectRT struct {
	posts atomic.Int32
	moved *atomic.Bool
}

func (r *redirectRT) RoundTrip(req *http.Request) (*http.Response, error) {
	n := r.posts.Add(1)
	if n == 1 {
		r.moved.Store(true)
		h := http.Header{}
		h.Set("Location", "/v1/chat/completions/again")
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)), Request: req}, nil
}

// Codex's design probe, now the safe behaviour: a permitted redirect after a
// move is refused before its POST, and the call says the first attempt may
// have been sent.
func TestAMoveRefusesTheRedirectsSend(t *testing.T) {
	moved := &atomic.Bool{}
	rt := &redirectRT{moved: moved}
	p := NewOpenAICompatible("http://127.0.0.1:1", "", "m", Harden(&http.Client{Transport: rt, Timeout: 5 * time.Second}))
	fence := func() error {
		if moved.Load() {
			return errors.New("aks-we-prd: its policy changed")
		}
		return nil
	}
	ctx := readfence.WithWriteTracker(readfence.With(context.Background(), fence))
	_, err := p.Complete(ctx, Request{System: "s", User: "u", Purpose: PurposeExplain})
	if got := rt.posts.Load(); got != 1 {
		t.Fatalf("%d POSTs reached the transport, want 1", got)
	}
	if !readfence.Failed(err) {
		t.Fatalf("not a fence refusal: %v", err)
	}
	if errors.Is(err, readfence.ErrNotForwarded) {
		t.Fatal("the first attempt was forwarded: never 'not sent'")
	}
}

// Before anything was forwarded, a refusal says nothing was sent.
func TestAFenceRefusalBeforeAnySendSaysNotSent(t *testing.T) {
	rt := &redirectRT{moved: &atomic.Bool{}}
	p := NewOpenAICompatible("http://127.0.0.1:1", "", "m", Harden(&http.Client{Transport: rt, Timeout: 5 * time.Second}))
	ctx := readfence.WithWriteTracker(readfence.With(context.Background(), func() error { return errors.New("held") }))
	_, err := p.Complete(ctx, Request{System: "s", User: "u", Purpose: PurposeExplain})
	if rt.posts.Load() != 0 || !errors.Is(err, readfence.ErrNotForwarded) {
		t.Fatalf("%d posts, %v", rt.posts.Load(), err)
	}
}

// A move during the CLI's preflight, after launch and before release: the
// prompt is never written to its stdin.
func TestAMoveDuringTheCLIsPreflightSendsNothing(t *testing.T) {
	t.Parallel()
	f := newFakeCLI(t)
	var moved atomic.Bool
	f.p.afterLaunch = func() { moved.Store(true) }
	ctx, cancel := context.WithTimeout(readfence.With(context.Background(), func() error {
		if moved.Load() {
			return errors.New("aks-we-prd: its connection restarted")
		}
		return nil
	}), 20*time.Second)
	defer cancel()
	_, err := f.p.Complete(ctx, Request{System: "SYSTEM-TEXT", User: "EVIDENCE-TEXT", Purpose: PurposeExplain})
	if err == nil || !strings.Contains(err.Error(), "connection restarted") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "record.json")); !os.IsNotExist(err) {
		t.Fatal("the evidence reached the CLI's stdin")
	}
}
