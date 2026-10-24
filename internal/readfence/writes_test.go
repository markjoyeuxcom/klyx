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
	// a read refused is a fence failure, never a write's
	if _, err := tr.RoundTrip(get); errors.Is(err, ErrNotForwarded) || !Failed(err) {
		t.Fatalf("a refused read: %v", err)
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
	// without a tracker nothing is marked
	bare, _ := http.NewRequestWithContext(With(context.Background(), func() error { return errors.New("fenced") }), http.MethodPatch, "http://x/", nil)
	if _, err := tr.RoundTrip(bare); errors.Is(err, ErrNotForwarded) {
		t.Fatal("no tracker, no mark")
	}
}
