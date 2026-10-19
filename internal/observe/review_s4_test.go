package observe

import (
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Coverage S4 §3: the pending-episode tracker reads a kind's continuity
// from KindState.Epoch - unchanged across watch mutations, advanced by a
// recorded error and by the complete list that follows it.
func TestS4EpochFollowsContinuity(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	log := &stateLog{}
	defer h.o.WatchKind("cms", log.add)()
	h.eventually(func() bool { st, ok := log.last(); return ok && st.Coverage == Live }, "live")
	first, _ := log.last()
	if first.Epoch == 0 {
		t.Fatalf("a complete list starts an epoch: %+v", first)
	}
	src.put("b", nil)
	h.eventually(func() bool { st, _ := log.last(); return st.Objects == 2 }, "a mutation")
	if st, _ := log.last(); st.Epoch != first.Epoch || st.Generation != first.Generation {
		t.Fatalf("a watch mutation keeps continuity: %+v", st)
	}
	src.liveWatcher().Error(&apierrors.NewServiceUnavailable("down").ErrStatus)
	h.eventually(func() bool { st, _ := log.last(); return st.Epoch > first.Epoch && st.Coverage == Live }, "a relist after an error")
	if st, _ := log.last(); st.Generation != first.Generation {
		t.Fatalf("the same generation, a new epoch: %+v", st)
	}
}
