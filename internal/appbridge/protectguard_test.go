package appbridge

import (
	"testing"
	"time"
)

func newTestGuard(protectedName string, now *time.Time) *ProtectGuard {
	return NewProtectGuard(
		func(name string) bool { return name == protectedName },
		func() time.Time { return *now },
	)
}

func TestProtectGuard_UnprotectedAlwaysPasses(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd", &now)
	if err := g.Check("dev"); err != nil {
		t.Fatalf("unprotected cluster refused: %v", err)
	}
	if err := g.Arm("dev", "whatever"); err != nil {
		t.Fatalf("arming an unprotected cluster must be a no-op: %v", err)
	}
}

func TestProtectGuard_ProtectedRequiresArm(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd", &now)

	if err := g.Check("prd"); err == nil {
		t.Fatal("unarmed protected cluster must be refused")
	}
	if err := g.Arm("prd", "wrong-name"); err == nil {
		t.Fatal("mismatched confirmation must be refused")
	}
	if err := g.Arm("prd", "prd"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if err := g.Check("prd"); err != nil {
		t.Fatalf("armed check refused: %v", err)
	}
	// One-shot: the grant is consumed.
	if err := g.Check("prd"); err == nil {
		t.Fatal("grant must be one-shot")
	}
}

func TestProtectGuard_GrantExpires(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd", &now)
	if err := g.Arm("prd", "prd"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	now = now.Add(armTTL + time.Second)
	if err := g.Check("prd"); err == nil {
		t.Fatal("expired grant must be refused")
	}
}

func TestProtectGuard_NilReceiverPasses(t *testing.T) {
	var g *ProtectGuard
	if err := g.Check("anything"); err != nil {
		t.Fatalf("nil guard must pass (gating not configured): %v", err)
	}
}

// TestProtectGuard_GatesDestructiveVerbs proves the end-to-end wiring: with a
// guard installed, an unarmed destructive verb on a protected cluster is
// refused at the bridge regardless of frontend behavior.
func TestProtectGuard_GatesDestructiveVerbs(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd-cluster", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(nil) })

	svc := NewPodsService(func(string) (PodsConn, bool) { return &fakeLivePodConn{}, true }, &anyEmitter{})

	if r := svc.DeletePod("prd-cluster", "ns", "pod"); r.OK || r.Error == "" {
		t.Fatalf("unarmed DeletePod on protected cluster must refuse, got %+v", r)
	}
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if r := svc.DeletePod("prd-cluster", "ns", "pod"); !r.OK {
		t.Fatalf("armed DeletePod must pass the gate: %+v", r)
	}
	// The grant was consumed by the first delete.
	if r := svc.DeletePod("prd-cluster", "ns", "pod"); r.OK {
		t.Fatal("second DeletePod must be refused (one-shot grant)")
	}
}

func TestConfigService_ArmProtected(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(nil) })

	svc := NewConfigService("/dev/null", nil, nil)
	// cfg is nil but ArmProtected only touches the guard.
	if r := svc.ArmProtected("prd", "nope"); r.OK {
		t.Fatal("mismatched typed name must refuse")
	}
	if r := svc.ArmProtected("prd", "prd"); !r.OK {
		t.Fatalf("arm failed: %+v", r)
	}
	if err := g.Check("prd"); err != nil {
		t.Fatalf("grant not recorded: %v", err)
	}
}
