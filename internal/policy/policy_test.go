package policy

import (
	"sync"
	"testing"
)

func TestAnUnknownClusterAdmitsNothing(t *testing.T) {
	r := New()
	if _, ok := r.Snapshot("x"); ok {
		t.Fatal("an unknown cluster has a snapshot")
	}
	if _, err := r.Begin("x"); err != ErrUnknown {
		t.Fatal(err)
	}
	r.ConnUp("x") // ignored
	if _, ok := r.Snapshot("x"); ok {
		t.Fatal("ConnUp created a cluster")
	}
}

// Slice F §1: every step moves the epoch, and a change in progress admits
// nothing - so no guard can pair the old protection with the new epoch.
func TestEveryStepMovesTheEpoch(t *testing.T) {
	r := New()
	r.Seed("prd", false)
	s0, _ := r.Snapshot("prd")
	if s0.State != Active || s0.Protected {
		t.Fatalf("%+v", s0)
	}
	r.Seed("prd", true) // a seed never changes a known cluster
	if s, _ := r.Snapshot("prd"); s != s0 {
		t.Fatalf("re-seed changed it: %+v", s)
	}
	s1, err := r.Begin("prd")
	if err != nil || s1.State != Applying || s1.Epoch == s0.Epoch || s1.Protected {
		t.Fatalf("begin: %+v %v", s1, err)
	}
	if _, err := r.Begin("prd"); err == nil {
		t.Fatal("begin twice")
	}
	s2, err := r.Publish("prd", true)
	if err != nil || s2.State != Active || !s2.Protected || s2.Epoch == s1.Epoch || s2.Epoch == s0.Epoch {
		t.Fatalf("publish: %+v %v", s2, err)
	}
	if _, err := r.Publish("prd", false); err == nil {
		t.Fatal("publish outside a change")
	}
	s3, _ := r.Hold("prd", "the reconnect failed")
	if s3.State != Held || s3.Reason == "" || s3.Epoch == s2.Epoch {
		t.Fatalf("hold: %+v", s3)
	}
	s4, err := r.Release("prd", true)
	if err != nil || s4.State != Active || s4.Reason != "" || s4.Epoch == s3.Epoch || s4.Epoch == s2.Epoch {
		t.Fatalf("release: %+v %v", s4, err)
	}
	r.ConnUp("prd")
	s5, _ := r.Snapshot("prd")
	if s5.Epoch == s4.Epoch || s5.State != Active || !s5.Protected {
		t.Fatalf("conn up: %+v", s5)
	}
	if s5.Epoch.String() == s4.Epoch.String() {
		t.Fatal("the names agree")
	}
}

func TestBeginFromHeldRetries(t *testing.T) {
	r := New()
	r.Seed("a", false)
	_, _ = r.Hold("a", "failed")
	if s, err := r.Begin("a"); err != nil || s.State != Applying || s.Reason != "" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestTheRegistryIsSafeForConcurrentUse(t *testing.T) {
	r := New()
	r.Seed("a", false)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.ConnUp("a") }()
		go func() { defer wg.Done(); _, _ = r.Snapshot("a") }()
	}
	wg.Wait()
	if s, _ := r.Snapshot("a"); s.Epoch.conn != 50 {
		t.Fatalf("%+v", s)
	}
}

// Settings S2 §2.5: a forgotten cluster admits nothing, and seeded again
// under its name it starts above its tombstone, so no epoch captured
// before the removal can match.
func TestAForgottenClusterNeverRepeatsAnEpoch(t *testing.T) {
	r := New()
	r.Seed("a", true)
	r.ConnUp("a")
	old, _ := r.Snapshot("a")
	r.Forget("a")
	if _, ok := r.Snapshot("a"); ok {
		t.Fatal("a forgotten cluster is known")
	}
	r.ConnUp("a") // late: ignored
	r.Forget("a") // twice: no change
	r.Seed("a", false)
	s, ok := r.Snapshot("a")
	if !ok || s.Epoch == old.Epoch || s.Protected || s.State != Active {
		t.Fatalf("re-seeded %+v, before %+v", s, old)
	}
	if p, _ := s.Epoch.Moved(old.Epoch); !p {
		t.Fatalf("the policy epoch did not move above the tombstone: %v from %v", s.Epoch, old.Epoch)
	}
}
