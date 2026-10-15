package appbridge

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/obslog"
)

// The Line slice 2b end to end: a real Observer over the native certificates
// feeds the HealthSink's observation log. A fault present at the first live
// capture is a baseline proof with an unknown start (the fixture's Ready has
// no transition time); once it recovers, its proof closes.
func TestObservationLogEndToEnd(t *testing.T) {
	h := startCertObserver(t, "e2e", nil)
	log := h.sink.ObservationLog()
	waitRows(t, "the failing certificate's proof", func() bool {
		p, ok, _ := log.ProofOf("e2e", "uid-bad")
		return ok && p.Basis == obslog.ProofUnknown && p.Role == "fault" && p.GVR == "cert-manager.io/v1/certificates"
	})
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	if _, err := h.dyn.Resource(gvr).Namespace("shop").Update(context.Background(), certObj("bad", "True"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitRows(t, "the recovered certificate's proof closes", func() bool {
		_, ok, _ := log.ProofOf("e2e", "uid-bad")
		return !ok
	})
	var sawChange bool
	for _, e := range log.Recent("e2e", 0) {
		if e.Key == "uid-bad" && e.From == "fault" && e.To == "ok" && e.Kind == "changes" {
			sawChange = true
		}
	}
	if !sawChange {
		t.Fatalf("the recovery is logged as an observed change: %+v", log.Recent("e2e", 0))
	}
}
