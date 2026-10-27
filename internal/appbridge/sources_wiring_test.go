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
	_, ctx, rel, err := lease("prd", "the alerts of lgtm-we")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	r.reconnect()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the host's retirement did not end the read")
	}
}
