package appbridge

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/policy"
)

// Settings S2 §4.1: a reservation refuses new admissions - a guard, an AI
// start, a lease - and leaves what was admitted before it valid under its
// epoch. Only a committed transition moves the epoch and ends that.

func TestAReservationRefusesNewWorkAndKeepsWhatWasAdmitted(t *testing.T) {
	reg := installRegistry(t, false, "prd")
	tk, err := guardFor("prd")
	if err != nil {
		t.Fatal(err)
	}
	pinned, why := admitNow("prd")
	if why != "" {
		t.Fatal(why)
	}
	tok, err := reg.Reserve("prd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardFor("prd"); err == nil || !strings.Contains(err.Error(), "applying a change") {
		t.Fatalf("a guard during the reservation: %v", err)
	}
	if _, why := admitNow("prd"); why == "" {
		t.Fatal("an AI start was admitted during the reservation")
	}
	if err := tk.Valid(); err != nil {
		t.Fatalf("a ticket admitted before the reservation: %v", err)
	}
	if p := epochProblem("prd", pinned); p != "" {
		t.Fatalf("work pinned before the reservation: %s", p)
	}
	reg.Unreserve(tok)
	if _, err := guardFor("prd"); err != nil {
		t.Fatalf("after a refusal: %v", err)
	}
	if err := tk.Valid(); err != nil {
		t.Fatalf("a refusal before the write ended a ticket: %v", err)
	}
	tok, _ = reg.Reserve("prd")
	if _, err := reg.Advance(tok, true); err != nil {
		t.Fatal(err)
	}
	reg.Unreserve(tok)
	if tk.Valid() == nil || epochProblem("prd", pinned) == "" {
		t.Fatal("a committed change left earlier work valid")
	}
}

func TestALeaseIsRefusedDuringAReservation(t *testing.T) {
	r := newLifeRig(t, true)
	before, err := r.life.Acquire("prd", Work{Kind: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	defer before.Release()
	tok, err := r.pol.Reserve("prd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.life.Acquire("prd", Work{Kind: "logs"}); err == nil || !strings.Contains(err.Error(), "applying a change") {
		t.Fatalf("a lease during the reservation: %v", err)
	}
	if before.Ctx.Err() != nil || before.Retired() {
		t.Fatal("a reservation ended a lease admitted before it")
	}
	r.pol.Unreserve(tok)
	le, err := r.life.Acquire("prd", Work{Kind: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	le.Release()
	if s, _ := r.pol.Snapshot("prd"); s.State != policy.Active || s.Reserved {
		t.Fatalf("%+v", s)
	}
}
