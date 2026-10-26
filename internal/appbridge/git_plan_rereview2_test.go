package appbridge

// Codex's second F2b re-review control, adopted: a refresh's cleanup owns
// its admission, under an identical clock, and one refresh per repository
// still holds.

import (
	"testing"
	"time"
)

func TestF2bReviewR2RefreshCleanupOwnsItsAdmission(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	h.svc.d.Now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
	a, err := h.svc.admitFetch(fleetURL)
	if err != nil {
		t.Fatal(err)
	}
	h.svc.mu.Lock()
	first := h.svc.fetching[fleetURL]
	h.svc.mu.Unlock()
	a()
	b, err := h.svc.admitFetch(fleetURL)
	if err != nil {
		t.Fatal(err)
	}
	defer b()
	h.svc.mu.Lock()
	second := h.svc.fetching[fleetURL]
	h.svc.mu.Unlock()
	if first.n == second.n || !first.started.Equal(second.started) {
		t.Fatalf("identical clock control: first %+v, second %+v", first, second)
	}
	a() // a late repeated cleanup cannot release the newer refresh
	h.svc.mu.Lock()
	current, held := h.svc.fetching[fleetURL]
	h.svc.mu.Unlock()
	if !held || current.n != second.n {
		t.Fatal("old release removed the newer admission")
	}
	if release, err := h.svc.admitFetch(fleetURL); err == nil {
		release()
		t.Fatal("one-refresh-per-repository exclusion was lost")
	}
}
