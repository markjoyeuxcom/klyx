package appbridge

import (
	"strings"
	"testing"
)

// Settings slice F §3-§4: A3's offers, reviews, runs and verification, and
// the AI fences, under the cluster's epoch.

// §3: a tightening between RunStep's guard and its write is refused at the
// write; the cluster was unprotected at the guard, so no grant is spent.
func TestATighteningBetweenTheGuardAndTheWriteIsRefusedAtTheWrite(t *testing.T) {
	establish(t)
	reg := installRegistry(t, false, "x")
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	if !rv.Offered {
		t.Fatalf("%+v", rv)
	}
	sf.sc.getHook = func() { tighten(t, reg, "x"); sf.sc.getHook = nil }
	r := sf.run(1, rv.Token)
	if r.Ran || !strings.Contains(r.Reason, "policy changed") {
		t.Fatalf("%+v", r)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatalf("written: %v", sf.sc.recorded())
	}
}

// §3: a reconnect withdraws the offer, sticky, and its review cannot run.
func TestAReconnectWithdrawsTheOfferAndItsReview(t *testing.T) {
	establish(t)
	reg := installRegistry(t, false, "x")
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	reg.ConnUp("x")
	if r := sf.run(1, rv.Token); r.Ran {
		t.Fatalf("a review from before the reconnect ran: %+v", r)
	}
	plan := sf.svc.InvestigationState("x").Exchanges[0].Plan
	if plan == nil || plan.Status != planStatusWithdrawn || !strings.Contains(plan.StatusWhy, "connection restarted") {
		t.Fatalf("withdrawn: %+v", plan)
	}
	if r := sf.review(1); r.Offered {
		t.Fatalf("a withdrawn offer was reviewed again: %+v", r)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatal("written")
	}
}

// §3: a held cluster withdraws its offers, and releasing it revives none.
func TestAHoldWithdrawsOffersAndReleaseRevivesNone(t *testing.T) {
	reg := installRegistry(t, false, "x")
	sf := newStepsFixture(t, pauseWeb)
	if _, err := reg.Hold("x", "the reconnect failed"); err != nil {
		t.Fatal(err)
	}
	if r := sf.review(1); r.Offered || !strings.Contains(r.Reason, "saved but not active") {
		t.Fatalf("%+v", r)
	}
	if _, err := reg.Release("x", false); err != nil {
		t.Fatal(err)
	}
	if r := sf.review(1); r.Offered {
		t.Fatalf("released, the offer came back: %+v", r)
	}
}

// §3: verification checks the epoch, so a move after the write ends it.
func TestVerificationEndsOnAMove(t *testing.T) {
	reg := installRegistry(t, false, "x")
	sf := newStepsFixture(t, pauseWeb)
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	sf.svc.mu.Unlock()
	fence := sf.svc.verifyFence(c)
	if err := fence(); err != nil {
		t.Fatalf("before the move: %v", err)
	}
	tighten(t, reg, "x")
	if err := fence(); err == nil || !strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("after the move: %v", err)
	}
}

// §4: a conversation starts only on an Active cluster, and a move ends its
// turn.
func TestAChatStartsOnlyWhenActiveAndAMoveEndsIt(t *testing.T) {
	reg := installRegistry(t, false, "x")
	if _, err := reg.Begin("x"); err != nil {
		t.Fatal(err)
	}
	f := newToolsFixture(t, answer("hello"))
	if st := f.svc.StartInvestigation("x", ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}); st.ConversationID != "" || !strings.Contains(st.Error, "being applied") {
		t.Fatalf("started while applying: %+v", st)
	}
	if _, err := reg.Publish("x", false); err != nil {
		t.Fatal(err)
	}
	sf := newStepsFixture(t, pauseWeb) // starts under the new epoch
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	sf.svc.mu.Unlock()
	r := &turnRunner{s: sf.svc, c: c}
	if _, err := r.guard(); err != nil {
		t.Fatalf("the guard holds: %v", err)
	}
	reg.ConnUp("x")
	if _, err := r.guard(); err == nil || !strings.Contains(err.Error(), "connection restarted") {
		t.Fatalf("a move ends the turn: %v", err)
	}
}
