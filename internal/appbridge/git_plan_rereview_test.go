package appbridge

// Codex's F2b re-review probe (RR1), adopted: a refresh that replaces an
// ended one, of the same repository within the same second, is other work.

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func f2bR1WaitFetch(t *testing.T, s *GitService) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		s.mu.Lock()
		_, running := s.fetching[fleetURL]
		s.mu.Unlock()
		if running {
			return
		}
		if time.Now().After(end) {
			t.Fatal("fetch never admitted")
		}
	}
}

func TestF2bReviewR1AReplacementRefreshChangesThePlan(t *testing.T) {
	h := newGitHarness(t, gitFleet, nil)
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 26, 12, 0, 0, 100_000_000, time.UTC).UnixNano())
	h.svc.d.Now = func() time.Time { return time.Unix(0, now.Load()) }
	firstGate := make(chan struct{})
	h.store.mu.Lock()
	h.store.gate = firstGate
	h.store.mu.Unlock()
	first := make(chan ActionResultDTO, 1)
	go func() { first <- h.svc.FetchGitRepo(fleetURL) }()
	f2bR1WaitFetch(t, h.svc)
	h.write(strings.Replace(gitFleet, "    - url: git@github.com:acme/fleet.git\n      matches: [\"https://ghp_matchtok@github.com/acme/alias\"]\n", "", 1))
	p := h.svc.PlanGitFromFile()
	if len(p.Continues) != 1 {
		t.Fatalf("first plan: %+v", p)
	}
	close(firstGate)
	if r := <-first; !r.OK {
		t.Fatalf("first: %+v", r)
	}
	now.Add(int64(200 * time.Millisecond))
	secondGate := make(chan struct{})
	h.store.mu.Lock()
	h.store.gate = secondGate
	h.store.mu.Unlock()
	defer close(secondGate)
	second := make(chan ActionResultDTO, 1)
	go func() { second <- h.svc.FetchGitRepo(fleetURL) }()
	f2bR1WaitFetch(t, h.svc)
	q := h.svc.PlanGitFromFile()
	if p.Digest == q.Digest {
		t.Errorf("two distinct refreshes have the same reviewed identity and plan digest: %+v", p.Continues)
	}
	if r := h.svc.ApplyGit(p.Digest); r.OK {
		t.Fatal("old plan applied although the refresh it named ended and another began")
	}
}
