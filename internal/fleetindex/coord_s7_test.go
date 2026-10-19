package fleetindex

import (
	"testing"

	"github.com/moomora/klyx/internal/observe"
)

// Coverage S7a: a live Observer kind its budget truncated is partial, never
// live - its entries are a subset of the kind.
func TestStatusOfATruncatedObserverKind(t *testing.T) {
	if got := statusOf(observe.KindState{Coverage: observe.Live}); got.State != StateLive {
		t.Errorf("live: %+v", got)
	}
	got := statusOf(observe.KindState{Coverage: observe.Live, Truncated: true, NotRetained: 1417})
	if got.State != StatePartial || got.Err != "1417 left out by Klyx's budget for the kind" {
		t.Errorf("truncated: %+v", got)
	}
	if got := statusOf(observe.KindState{Coverage: observe.Stale, Truncated: true}); got.State != StateStale {
		t.Errorf("a stale kind stays stale: %+v", got)
	}
}
