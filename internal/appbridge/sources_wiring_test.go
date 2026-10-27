package appbridge

import (
	"testing"
	"time"
)

// Settings S3a §13: a bucket's read through its host is work on the host,
// admitted by S2's lifecycle: refused while the host is reserved, and ended
// when its connection is retired.
func TestABucketReadIsWorkOnItsHost(t *testing.T) {
	r := newLifeRig(t, true)
	lease := AlertHostLeaser(r.life)
	var release func()
	if !waitUntil(t, 5*time.Second, func() bool {
		_, _, rel, err := lease("prd", "the alerts of lgtm-we")
		if err == nil {
			release = rel
		}
		return err == nil
	}) {
		t.Fatal("the host never admitted the read")
	}
	release()
	tok, err := r.pol.Reserve("prd")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lease("prd", "the alerts of lgtm-we"); err == nil {
		t.Fatal("a reserved host admitted a new read")
	}
	r.pol.Unreserve(tok)
	_, ctx, rel, err := lease("prd", "the alerts lgtm-we reads for its members")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	// a bucket's lease is held between its reads too: the host's plans name
	// it (Codex's review of S3a-2, R6)
	ends, _ := r.life.ClusterWork("prd")
	named := false
	for _, w := range ends {
		named = named || w.Kind == "alerts" && w.What == "the alerts lgtm-we reads for its members"
	}
	if !named {
		t.Fatalf("the host's plan does not name the bucket: %+v", ends)
	}
	r.reconnect()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the host's retirement did not end the read")
	}
}
