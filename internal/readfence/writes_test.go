package readfence

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A3 stage 1 review, finding 5: a fence refusing a write before anything of
// it was forwarded says so; once a write under the same tracker was
// forwarded (a retry, a redirect's hop), a refusal is not marked.
func TestAFenceSaysWhenAWriteWasNotForwarded(t *testing.T) {
	sent := 0
	tr := Transport(roundTrip(func(*http.Request) (*http.Response, error) { sent++; return &http.Response{StatusCode: 200}, nil }))
	open := true
	ctx := WithWriteTracker(With(context.Background(), func() error {
		if !open {
			return errors.New("fenced")
		}
		return nil
	}))
	get, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x/", nil)
	patch, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://x/", nil)

	if _, err := tr.RoundTrip(get); err != nil {
		t.Fatal(err)
	}
	open = false
	_, err := tr.RoundTrip(patch)
	if !errors.Is(err, ErrNotForwarded) || !Failed(err) || sent != 1 {
		t.Fatalf("a refused first write was not forwarded: %v sent=%d", err, sent)
	}
	// a read refused before any write was forwarded sent nothing of the
	// write either (Settings F1 review, finding F6: a refresh's GET)
	if _, err := tr.RoundTrip(get); !errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("a refused read before the write: %v", err)
	}

	open = true
	ctx2 := WithWriteTracker(With(context.Background(), func() error {
		if !open {
			return errors.New("fenced")
		}
		return nil
	}))
	first, _ := http.NewRequestWithContext(ctx2, http.MethodPatch, "http://x/", nil)
	if _, err := tr.RoundTrip(first); err != nil {
		t.Fatal(err)
	}
	open = false
	retry, _ := http.NewRequestWithContext(ctx2, http.MethodPatch, "http://x/", nil)
	if _, err := tr.RoundTrip(retry); errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("a refusal after a forwarded write may follow a landed one: %v", err)
	}
	// nor is a read after it: the write before it may have landed
	after, _ := http.NewRequestWithContext(ctx2, http.MethodGet, "http://x/", nil)
	if _, err := tr.RoundTrip(after); errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("a refused read after a forwarded write: %v", err)
	}
	// without a tracker nothing is marked
	bare, _ := http.NewRequestWithContext(With(context.Background(), func() error { return errors.New("fenced") }), http.MethodPatch, "http://x/", nil)
	if _, err := tr.RoundTrip(bare); errors.Is(err, ErrNotForwarded) {
		t.Fatal("no tracker, no mark")
	}
}

// Settings F1 review, finding F6: a send that is not an HTTP request - a
// child's start, a prompt's handoff - is marked as the transport marks one.
func TestSendMarksAsTheTransportDoes(t *testing.T) {
	open := true
	fence := func() error {
		if !open {
			return errors.New("fenced")
		}
		return nil
	}
	ctx := WithWriteTracker(With(context.Background(), fence))
	open = false
	if err := Send(ctx); !errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("a refused first send: %v", err)
	}
	open = true
	if err := Send(ctx); err != nil {
		t.Fatal(err)
	}
	open = false
	if err := Send(ctx); errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("a refusal after a send went ahead: %v", err)
	}
	// a send that went ahead is a forwarded write to the transport too
	tr := Transport(roundTrip(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200}, nil }))
	patch, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://x/", nil)
	if _, err := tr.RoundTrip(patch); errors.Is(err, ErrNotForwarded) {
		t.Fatalf("the transport forgot the send: %v", err)
	}
	if err := Send(With(context.Background(), fence)); errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("no tracker, no mark: %v", err)
	}
	if err := Send(context.Background()); err != nil {
		t.Fatal(err)
	}
}
