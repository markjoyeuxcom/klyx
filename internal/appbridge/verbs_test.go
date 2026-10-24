package appbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
)

// A3 §0: every armed call consumes its grant, an out-of-range count too.
func TestScaleOutOfRangeConsumesTheGrant(t *testing.T) {
	prev := activeProtectGuard.Load()
	t.Cleanup(func() { activeProtectGuard.Store(prev) })
	SetProtectGuard(NewProtectGuard(func(c string) bool { return c == "prd" }, time.Now))
	conn := &fakeWLConn{}
	svc := wlSvc(conn)

	if err := activeProtectGuard.Load().Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	r := svc.ScaleWorkload("prd", "Deployment", "shop", "cart", "uid-cart", maxScaleReplicas+1)
	if r.OK || r.Outcome != fleet.OutcomeRefused || !r.NotSent {
		t.Fatalf("an out-of-range count is refused, nothing sent: %+v", r)
	}
	if r := svc.ScaleWorkload("prd", "Deployment", "shop", "cart", "uid-cart", 2); r.OK {
		t.Fatalf("the refused call spent the grant; the next one needs its own: %+v", r)
	}
	if conn.lastScaleN != 0 {
		t.Fatal("neither call reached the cluster")
	}
}

// A3 §5.1: a matrix verb's request is written (with its receipt), refused
// before anything was sent, refused by the server, or unverified.
func TestMatrixVerbsClassifyTheirRequest(t *testing.T) {
	gr := schema.GroupResource{Group: "apps", Resource: "deployments"}
	for _, c := range []struct {
		name    string
		err     error
		outcome string
		notSent bool
	}{
		{"written", nil, fleet.OutcomeWritten, false},
		{"refused by the server", apierrors.NewForbidden(gr, "api", errors.New("rbac")), fleet.OutcomeRefused, false},
		{"replaced", fmt.Errorf("rollout restart Deployment ns/api: %w", fleet.ErrReplaced), fleet.OutcomeRefused, false},
		{"lost answer", apierrors.NewInternalError(errors.New("etcd")), fleet.OutcomeUnverified, false},
		{"deadline", context.DeadlineExceeded, fleet.OutcomeUnverified, false},
	} {
		conn := &fakeWLConn{rolloutErr: c.err}
		r := wlSvc(conn).RolloutRestart("dev", "Deployment", "ns", "api", "uid-api")
		if r.Outcome != c.outcome || r.NotSent != c.notSent {
			t.Errorf("%s: %+v", c.name, r)
		}
		if c.err == nil && (r.Receipt == nil || r.Receipt.Token != "t-1" || r.Receipt.Generation != 7 || r.Receipt.PriorGeneration != 6) {
			t.Errorf("a written request carries its receipt: %+v", r.Receipt)
		}
		if c.err != nil && r.Receipt != nil {
			t.Errorf("%s: only a written request has a receipt", c.name)
		}
	}
	none := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false })
	if r := none.RolloutRestart("gone", "Deployment", "ns", "api", "uid-api"); r.Outcome != fleet.OutcomeRefused || !r.NotSent {
		t.Fatalf("a missing connection sends nothing: %+v", r)
	}
}

// A3 §4.5: the manual undo passes the revision its history showed, and
// there is no undo without one (stage 1 review, finding 1).
func TestUndoPassesTheShownRevision(t *testing.T) {
	conn := &fakeWLConn{}
	svc := wlSvc(conn)
	if r := svc.RolloutUndo("dev", "Deployment", "shop", "cart", "uid-cart", 2, "rs-uid", "digest"); !r.OK {
		t.Fatal(r)
	}
	if conn.lastExpect == nil || *conn.lastExpect != (fleet.RevisionExpect{ObjectUID: "rs-uid", TemplateDigest: "digest"}) {
		t.Fatalf("expectation: %+v", conn.lastExpect)
	}
	for _, ids := range [][2]string{{"", ""}, {"rs-uid", ""}, {"", "digest"}} {
		conn.lastExpect, conn.lastUndoRev = nil, 0
		r := svc.RolloutUndo("dev", "Deployment", "shop", "cart", "uid-cart", 2, ids[0], ids[1])
		if r.OK || r.Outcome != fleet.OutcomeRefused || !r.NotSent || conn.lastUndoRev != 0 {
			t.Fatalf("%v: refused before the cluster: %+v", ids, r)
		}
	}
	// the refusal comes after the grant: it consumes it
	prev := activeProtectGuard.Load()
	t.Cleanup(func() { activeProtectGuard.Store(prev) })
	SetProtectGuard(NewProtectGuard(func(c string) bool { return c == "prd" }, time.Now))
	if err := activeProtectGuard.Load().Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	svc.RolloutUndo("prd", "Deployment", "shop", "cart", "uid-cart", 2, "", "")
	if r := svc.RolloutUndo("prd", "Deployment", "shop", "cart", "uid-cart", 2, "rs-uid", "digest"); r.OK {
		t.Fatal("the refused undo spent the grant")
	}
}

// A lost answer keeps the token its request was sent with; only an
// accepted write has a receipt.
func TestALostAnswerKeepsItsAttempt(t *testing.T) {
	conn := &fakeWLConn{rolloutErr: apierrors.NewInternalError(errors.New("etcd")), attemptToken: "t-lost"}
	r := wlSvc(conn).RolloutRestart("dev", "Deployment", "ns", "api", "uid-api")
	if r.Outcome != fleet.OutcomeUnverified || r.Receipt != nil || r.Attempt == nil || r.Attempt.Token != "t-lost" {
		t.Fatalf("%+v %+v", r, r.Attempt)
	}
	conn = &fakeWLConn{rolloutErr: apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api", errors.New("rbac")), attemptToken: "t-refused"}
	if r := wlSvc(conn).RolloutRestart("dev", "Deployment", "ns", "api", "uid-api"); r.Attempt != nil {
		t.Fatalf("a refused request's attempt is not carried: %+v", r.Attempt)
	}
}
