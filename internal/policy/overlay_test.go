package policy

import "testing"

// Settings S2 §4.1: the reservation is an overlay over the activation
// state. Reserving and unreserving change nothing else; only a committed
// transition moves the epoch.

func TestAReservationChangesNothingButAdmission(t *testing.T) {
	r := New()
	r.Seed("prd", false)
	before, _ := r.Snapshot("prd")
	tok, err := r.Reserve("prd")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := r.Snapshot("prd")
	if !s.Reserved || s.State != Active || s.Epoch != before.Epoch || s.Protected != before.Protected {
		t.Fatalf("reserved: %+v", s)
	}
	if _, err := r.Reserve("prd"); err != ErrReserved {
		t.Fatalf("reserved twice: %v", err)
	}
	r.Unreserve(Token{cluster: "prd", n: tok.n + 1}) // not the owner
	if s, _ := r.Snapshot("prd"); !s.Reserved {
		t.Fatal("another token removed the reservation")
	}
	r.Unreserve(tok)
	if s, _ := r.Snapshot("prd"); s != before {
		t.Fatalf("unreserved: %+v, was %+v", s, before)
	}
	r.Unreserve(tok) // again: nothing
	if s, _ := r.Snapshot("prd"); s != before {
		t.Fatal("a second unreserve changed it")
	}
}

// A refused Retry on a Held cluster leaves it Held, with its reason, its
// epoch and its attempt, exactly as it was.
func TestUnreservingAHeldClusterLeavesItHeld(t *testing.T) {
	r := New()
	r.Seed("prd", false)
	tok, _ := r.Reserve("prd")
	held, err := r.HoldAttempt(tok, "connecting with context prd-2", 7, true)
	if err != nil || held.State != Held || held.Attempt != 7 || held.Reason == "" {
		t.Fatalf("%+v %v", held, err)
	}
	r.Unreserve(tok)
	held, _ = r.Snapshot("prd")
	retry, _ := r.Reserve("prd")
	r.Unreserve(retry)
	if s, _ := r.Snapshot("prd"); s != held {
		t.Fatalf("after a refused retry: %+v, was %+v", s, held)
	}
}

// An absent name gets a placeholder that admits nothing and is published
// nowhere; a refused connect leaves the name absent, and a committed one
// seeds it above its tombstone, still reserved until its Apply ends.
func TestAPlaceholderIsPublishedNowhere(t *testing.T) {
	r := New()
	r.Seed("prd", false)
	if _, err := r.Begin("prd"); err != nil {
		t.Fatal(err)
	}
	gone, _ := r.Snapshot("prd")
	r.Forget("prd")
	tok, err := r.Reserve("prd")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Snapshot("prd"); ok {
		t.Fatal("a placeholder has a snapshot")
	}
	if _, err := r.Begin("prd"); err != ErrUnknown {
		t.Fatalf("a placeholder took a step: %v", err)
	}
	if _, err := r.Advance(tok, true); err != ErrUnknown {
		t.Fatalf("a placeholder advanced: %v", err)
	}
	r.ConnUp("prd")
	r.Forget("prd") // a placeholder keeps the cluster's tombstone
	again, err := r.Reserve("prd")
	if err != nil {
		t.Fatal(err)
	}
	r.Unreserve(tok) // the old token: nothing
	other, _ := r.Reserve("other")
	r.Unreserve(other)
	r.mu.Lock()
	_, left := r.m["other"]
	r.mu.Unlock()
	if _, ok := r.Snapshot("other"); ok || left {
		t.Fatal("a refused connect left its name")
	}
	r.Seed("prd", true)
	s, ok := r.Snapshot("prd")
	if !ok || s.State != Active || !s.Protected || !s.Reserved {
		t.Fatalf("seeded over the placeholder: %+v %v", s, ok)
	}
	if s.Epoch.policy <= gone.Epoch.policy {
		t.Fatalf("seeded at %v, not above the tombstone %v", s.Epoch, gone.Epoch)
	}
	r.Unreserve(again)
	if s, _ := r.Snapshot("prd"); s.Reserved {
		t.Fatal("still reserved")
	}
}

func TestAdvanceMovesTheEpochOnceUnderItsReservation(t *testing.T) {
	r := New()
	r.Seed("prd", false)
	if _, err := r.Advance(Token{cluster: "prd", n: 1}, true); err != ErrNotReserved {
		t.Fatalf("advanced without a reservation: %v", err)
	}
	before, _ := r.Snapshot("prd")
	tok, _ := r.Reserve("prd")
	s, err := r.Advance(tok, true)
	if err != nil || !s.Protected || s.State != Active || s.Epoch.policy != before.Epoch.policy+1 || s.Epoch.conn != before.Epoch.conn {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := r.HoldAttempt(tok, "x", 0, false); err == nil {
		t.Fatal("a hold bound to no attempt")
	}
	if _, err := r.HoldAttempt(tok, "connecting", 3, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(tok, false); err == nil {
		t.Fatal("advanced a held cluster")
	}
}

// Only the bound attempt's first usability releases a hold, exactly once,
// with the protection the hold recorded - during another Apply's
// reservation too, which unreserving then never undoes.
func TestOnlyTheBoundAttemptReleasesItsHold(t *testing.T) {
	r := New()
	r.Seed("prd", false)
	tok, _ := r.Reserve("prd")
	_, _ = r.HoldAttempt(tok, "connecting", 5, true)
	r.Unreserve(tok)
	held, _ := r.Snapshot("prd")
	if s, ok := r.ReleaseAttempt("prd", 4); ok || s != held {
		t.Fatalf("a stale attempt released it: %+v", s)
	}
	if _, ok := r.ReleaseAttempt("prd", 0); ok {
		t.Fatal("attempt zero released it")
	}
	revert, _ := r.Reserve("prd") // a Revert being decided
	s, ok := r.ReleaseAttempt("prd", 5)
	if !ok || s.State != Active || !s.Protected || s.Attempt != 0 || s.Reason != "" || s.Epoch == held.Epoch || !s.Reserved {
		t.Fatalf("released: %+v %v", s, ok)
	}
	if again, ok := r.ReleaseAttempt("prd", 5); ok || again != s {
		t.Fatal("released twice")
	}
	r.Unreserve(revert)
	if now, _ := r.Snapshot("prd"); now.State != Active || now.Epoch != s.Epoch || now.Reserved {
		t.Fatalf("unreserving undid the release: %+v", now)
	}
	// F's own steps drop an attempt binding
	tok, _ = r.Reserve("prd")
	_, _ = r.HoldAttempt(tok, "connecting", 9, false)
	r.Unreserve(tok)
	if _, err := r.Hold("prd", "failed"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.ReleaseAttempt("prd", 9); ok {
		t.Fatal("an attempt released a hold it no longer owns")
	}
}
