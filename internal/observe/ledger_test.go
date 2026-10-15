package observe

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
)

// The role ledger and transition stream (slice 2b §9). Objects are
// ConfigMaps projected with their ObjectMeta (so membership is known); data
// bad=1 is fault, skip=1 is not evaluated, since=<RFC3339> is a proven start,
// until=<RFC3339> is progressing until then and fault after (a boundary).

type cmObj struct {
	metav1.ObjectMeta
	Bad, Skip    bool
	Since, Until string
}

func projectCMObj(obj any) (any, error) {
	cm := obj.(*corev1.ConfigMap)
	return &cmObj{ObjectMeta: metav1.ObjectMeta{Name: cm.Name, Namespace: cm.Namespace, UID: cm.UID},
		Bad: cm.Data["bad"] == "1", Skip: cm.Data["skip"] == "1", Since: cm.Data["since"], Until: cm.Data["until"]}, nil
}

func ledgerKind(name string, src *fakeSource) KindSpec {
	k := cmKind(name, src)
	k.Project = projectCMObj
	return k
}

func evalLedger(kinds ...string) func(Capture) PartResult {
	return func(c Capture) PartResult {
		var counts Counts
		var roles []RoleEntry
		var next *time.Time
		capped := false
		for _, kind := range kinds {
			for _, o := range c.Objects[kind] {
				v := o.(*cmObj)
				counts.Total++
				role := "ok"
				var boundary *time.Time
				switch {
				case v.Bad:
					role = "fault"
				case v.Until != "":
					t, _ := time.Parse(time.RFC3339, v.Until)
					if c.Now.Before(t) {
						role = "progressing"
						if next == nil || t.Before(*next) {
							next = &t
						}
					} else {
						role, boundary = "fault", &t
					}
				}
				switch role {
				case "fault":
					counts.Fault++
				case "progressing":
					counts.Progressing++
				default:
					counts.OK++
				}
				if v.Skip || c.RoleCap == 0 {
					continue
				}
				if len(roles) == c.RoleCap {
					capped = true
					continue
				}
				e := RoleEntry{Source: kind, GVR: "v1/configmaps", Key: string(v.UID), NS: v.Namespace, Name: v.Name, Role: role, Label: role, Boundary: boundary}
				if v.Since != "" {
					t, _ := time.Parse(time.RFC3339, v.Since)
					e.Since = &t
				}
				roles = append(roles, e)
			}
		}
		return PartResult{Counts: &counts, Next: next, Roles: roles, RolesCapped: capped}
	}
}

func ledgerPart(id string, counted []string, deps ...string) PartSpec {
	return PartSpec{ID: id, Entity: id, Scope: "all namespaces", Classifier: "verdict", Counted: counted, Deps: deps,
		Evaluate: evalLedger(counted...), Ledger: true}
}

func ledgerHarness(t *testing.T, src *fakeSource, cfg Config) *harness {
	t.Helper()
	if cfg.Kinds == nil {
		cfg.Kinds = []KindSpec{ledgerKind("cms", src)}
	}
	if cfg.Domains == nil {
		cfg.Domains = []DomainSpec{{ID: "things", Parts: []PartSpec{ledgerPart("cms", []string{"cms"})}}}
	}
	return start(t, cfg)
}

func (s *recSink) allBatches() []TransitionBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TransitionBatch(nil), s.batches...)
}

// waitBatch steps the clock until a batch after index from matches, and
// returns it with its index.
func (h *harness) waitBatch(from int, match func(TransitionBatch) bool, msg string) (TransitionBatch, int) {
	h.t.Helper()
	var got TransitionBatch
	idx := -1
	h.eventually(func() bool {
		bs := h.sink.allBatches()
		for i := from; i < len(bs); i++ {
			if match(bs[i]) {
				got, idx = bs[i], i
				return true
			}
		}
		return false
	}, msg)
	return got, idx
}

func kindIs(kind string) func(TransitionBatch) bool {
	return func(b TransitionBatch) bool { return b.Kind == kind }
}

func itemsOf(b TransitionBatch) string {
	var out []string
	for _, it := range b.Items {
		s := it.Name + ":" + it.From + ">" + it.To
		if it.Restated {
			s += "(restated)"
		}
		if it.Derived {
			s += "(derived)"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// baseline waits for the first baseline and returns the next batch index.
func (h *harness) baseline(want string) int {
	h.t.Helper()
	b, i := h.waitBatch(0, kindIs(BatchBaseline), "baseline")
	if got := itemsOf(b); got != want {
		h.t.Fatalf("baseline %q, want %q", got, want)
	}
	if b.Seq == 0 || b.Mode != ModeWatch || b.GVR != "" && !strings.Contains(b.GVR, "/") {
		h.t.Fatalf("baseline header: %+v", b)
	}
	return i + 1
}

// ---- lifecycle ----------------------------------------------------------------

func TestLedgerBaselineThenOneChangeAtCapture(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	src.put("b", map[string]string{"bad": "1"})
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok b:absent>fault")
	src.put("a", map[string]string{"bad": "1"})
	b, _ := h.waitBatch(next, kindIs(BatchChanges), "a flips")
	if itemsOf(b) != "a:ok>fault" || b.PrevAt == nil || !b.At.After(*b.PrevAt) {
		t.Fatalf("changes: %q at %v prev %v", itemsOf(b), b.At, b.PrevAt)
	}
}

func TestLedgerFlipAndFlipBackBetweenEvaluationsIsNothing(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	// Within one frozen-clock window: no evaluation sees the fault.
	src.put("a", map[string]string{"bad": "1"})
	src.put("a", nil)
	src.put("z", nil) // a later, visible change
	b, _ := h.waitBatch(next, kindIs(BatchChanges), "z appears")
	if itemsOf(b) != "z:absent>ok" {
		t.Fatalf("the flip and flip-back leaked: %q", itemsOf(b))
	}
}

func TestLedgerDeleteIsGoneAndRecreationIsGonePlusNew(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	src.put("b", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok b:absent>ok")
	src.remove("a")
	b, i := h.waitBatch(next, kindIs(BatchChanges), "a deleted")
	if itemsOf(b) != "a:ok>gone" {
		t.Fatalf("delete: %q", itemsOf(b))
	}
	src.putUID("b", "b-2", nil)
	b, _ = h.waitBatch(i+1, kindIs(BatchChanges), "b recreated")
	if itemsOf(b) != "b:ok>gone b:absent>ok" || b.Items[0].Key != "b" || b.Items[1].Key != "b-2" {
		t.Fatalf("recreation: %q %+v", itemsOf(b), b.Items)
	}
}

func TestLedgerLiveToStaleFreezesThenAfterGapHasOnlyChanges(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	src.put("b", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok b:absent>ok")
	src.mu.Lock()
	src.listErr = apierrors.NewInternalError(fmt.Errorf("etcd down"))
	src.mu.Unlock()
	src.liveWatcher().Error(&apierrors.NewInternalError(fmt.Errorf("stream broke")).ErrStatus)
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "the loss")
	if loss.Loss == nil || loss.Loss.Reason != LossCoverage || loss.Loss.Coverage != Stale {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	src.put("b", map[string]string{"bad": "1"}) // unseen while stale
	h.settle(300 * time.Millisecond)
	for _, x := range h.sink.allBatches()[i+1:] {
		if x.Kind != BatchLoss || x.Source != "cms" {
			t.Fatalf("a frozen ledger emits nothing: %+v", x)
		}
	}
	src.mu.Lock()
	src.listErr = nil
	src.mu.Unlock()
	b, _ := h.waitBatch(i+1, kindIs(BatchAfterGap), "the after-gap")
	if itemsOf(b) != "b:ok>fault" || b.PrevAt == nil || !b.PrevAt.Equal(loss.At) {
		t.Fatalf("after-gap: %q prev %v loss at %v", itemsOf(b), b.PrevAt, loss.At)
	}
}

// ---- continuity (finding 1 of revision 2) ---------------------------------------

func TestLedgerBenignErrorWithAnIdenticalRelistIsAGap(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	// A visible change pins the last publish, so the relist below lands in
	// one throttle window: no other mutation, no RowsNow.
	src.put("z", nil)
	_, i := h.waitBatch(next, kindIs(BatchChanges), "z")
	lists, _ := src.counts()
	src.liveWatcher().Error(&apierrors.NewResourceExpired("too old").ErrStatus)
	// Wait in real time, the fake clock held: the error and the relist land
	// inside one throttle window, so one evaluation sees both.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if l, _ := src.counts(); l == lists+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the reflector never relisted")
		}
	}
	time.Sleep(50 * time.Millisecond) // the Replace lands after the list returns
	loss, j := h.waitBatch(i+1, kindIs(BatchLoss), "the gap is seen")
	gap, _ := h.waitBatch(j+1, kindIs(BatchAfterGap), "and reconciled")
	if loss.Loss.Reason != LossRelist || len(gap.Items) != 0 || !gap.At.Equal(loss.At) {
		t.Fatalf("loss %+v at %v; after-gap %q at %v", loss.Loss, loss.At, itemsOf(gap), gap.At)
	}
}

func TestLedgerHeldRelistIsALossUntilTheList(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	gate := make(chan struct{})
	src.mu.Lock()
	src.listGate = gate
	src.mu.Unlock()
	src.liveWatcher().Error(&apierrors.NewResourceExpired("too old").ErrStatus)
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "a relisting source is not live")
	if loss.Loss.Reason != LossRelist {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	h.settle(300 * time.Millisecond)
	if n := len(h.sink.allBatches()); n != i+1 {
		t.Fatalf("no after-gap while the relist is held: %+v", h.sink.allBatches()[i+1:])
	}
	close(gate)
	h.waitBatch(i+1, kindIs(BatchAfterGap), "the relist reconciles")
}

func TestLedgerResumableWatchRotationIsContinuous(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	src.put("a", map[string]string{"touch": "1"}) // an event first: not a "very short watch"
	src.liveWatcher().Stop()                      // a clean close: the reflector re-watches from its resourceVersion
	h.eventually(func() bool { _, w := src.counts(); return w == 2 }, "a resumed watch")
	src.put("a", map[string]string{"bad": "1"})
	b, _ := h.waitBatch(next, func(b TransitionBatch) bool { return b.Kind != BatchChanges || len(b.Items) > 0 }, "the change")
	if b.Kind != BatchChanges || itemsOf(b) != "a:ok>fault" {
		t.Fatalf("rotation broke continuity: %+v", b)
	}
}

// ---- connection, Final, bootstraps -----------------------------------------------

func TestLedgerConnectionDownAndUpWithoutChanges(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	h.o.SetConnection("connected · flux degraded") // a controller note: nothing
	h.settle(300 * time.Millisecond)
	if n := len(h.sink.allBatches()); n != next {
		t.Fatalf("a controller note emitted: %+v", h.sink.allBatches()[next:])
	}
	h.o.SetConnection("unreachable")
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "down")
	if loss.Loss.Reason != LossConnection {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	h.o.SetConnection("connected")
	gap, _ := h.waitBatch(i+1, kindIs(BatchAfterGap), "up")
	if len(gap.Items) != 0 {
		t.Fatalf("after-gap: %q", itemsOf(gap))
	}
}

func TestLedgerBootstrapEmitsOnceAndFinalEnds(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>ok")
	src.put("a", map[string]string{"bad": "1"})
	deadline := time.Now().Add(3 * time.Second)
	for { // a bootstrap before the throttle tick commits and emits
		r, ok := h.o.RowsNow("things", "cms")
		if ok && r.Part.Counts.Fault == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bootstrap never saw the fault")
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.cancel() // before the scheduled publish
	<-h.o.Done()
	bs := h.sink.allBatches()[next:]
	var kinds []string
	for _, b := range bs {
		kinds = append(kinds, b.Kind+"["+itemsOf(b)+"]")
	}
	if strings.Join(kinds, " ") != "changes[a:ok>fault] end[]" {
		t.Fatalf("exactly once, then end: %v", kinds)
	}
	for i := 1; i < len(bs); i++ {
		if bs[i].Seq <= bs[i-1].Seq {
			t.Fatalf("seq not increasing: %d then %d", bs[i-1].Seq, bs[i].Seq)
		}
	}
}

// ---- membership --------------------------------------------------------------------

func TestLedgerUnevaluatedIsNotGone(t *testing.T) {
	src := newSource()
	src.put("a", map[string]string{"bad": "1"})
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>fault")
	src.put("a", map[string]string{"bad": "1", "skip": "1"}) // still there, not evaluated
	b, i := h.waitBatch(next, kindIs(BatchChanges), "skipped")
	if itemsOf(b) != "a:fault>unevaluated" {
		t.Fatalf("skip: %q", itemsOf(b))
	}
	src.remove("a")
	b, _ = h.waitBatch(i+1, kindIs(BatchChanges), "removed")
	if itemsOf(b) != "a:unevaluated>gone" {
		t.Fatalf("remove: %q", itemsOf(b))
	}
}

// Review of 5fcb5f2: an object without a readable UID makes membership
// unknown - a deletion and a skipped object look alike - so the partition is
// suspended (one loss), claims nothing, and reconciles once membership is
// readable again.
func TestLedgerUnknownMembershipSuspendsThePartition(t *testing.T) {
	src := newSource()
	src.put("a", map[string]string{"bad": "1"})
	src.put("b", nil)
	k := ledgerKind("cms", src)
	k.Project = func(obj any) (any, error) {
		v, err := projectCMObj(obj)
		if obj.(*corev1.ConfigMap).Data["nouid"] == "1" {
			v.(*cmObj).UID = ""
		}
		return v, err
	}
	h := ledgerHarness(t, src, Config{Kinds: []KindSpec{k}})
	next := h.baseline("a:absent>fault b:absent>ok")
	src.put("b", map[string]string{"nouid": "1"})
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "membership unknown")
	if loss.Loss.Reason != LossMembership {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	src.remove("a") // unseen while suspended: no claim either way
	h.settle(300 * time.Millisecond)
	if bs := h.sink.allBatches(); len(bs) != i+1 {
		t.Fatalf("a suspended partition emits nothing: %+v", bs[i+1:])
	}
	src.put("b", nil)
	gap, _ := h.waitBatch(i+1, kindIs(BatchAfterGap), "membership readable again")
	if itemsOf(gap) != "a:fault>gone" || gap.PrevAt == nil || !gap.PrevAt.Equal(loss.At) {
		t.Fatalf("after-gap: %q prev %v", itemsOf(gap), gap.PrevAt)
	}
}

// ---- since and boundaries ------------------------------------------------------------

func TestLedgerSameRoleNewSinceIsRestated(t *testing.T) {
	src := newSource()
	src.put("a", map[string]string{"bad": "1", "since": "2026-10-11T09:00:00Z"})
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>fault")
	src.put("a", map[string]string{"bad": "1", "since": "2026-10-11T11:00:00Z"})
	b, _ := h.waitBatch(next, kindIs(BatchChanges), "restated")
	if itemsOf(b) != "a:fault>fault(restated)" || b.Items[0].Since == nil || b.Items[0].Since.Hour() != 11 {
		t.Fatalf("restated: %q %+v", itemsOf(b), b.Items)
	}
}

func TestLedgerBoundaryCrossingCarriesItsBoundary(t *testing.T) {
	src := newSource()
	until := t0.Add(2 * time.Second).Format(time.RFC3339)
	src.put("a", map[string]string{"until": until})
	h := ledgerHarness(t, src, Config{})
	next := h.baseline("a:absent>progressing")
	b, _ := h.waitBatch(next, kindIs(BatchChanges), "the boundary passes")
	if itemsOf(b) != "a:progressing>fault" || b.Items[0].Boundary == nil || b.Items[0].Boundary.Format(time.RFC3339) != until {
		t.Fatalf("boundary: %q %+v", itemsOf(b), b.Items)
	}
}

// ---- bounds ----------------------------------------------------------------------------

func TestLedgerCapIsOneLossThenABaselineReplacement(t *testing.T) {
	src := newSource()
	for _, n := range []string{"a", "b", "c"} {
		src.put(n, nil)
	}
	h := ledgerHarness(t, src, Config{RoleCap: 3})
	next := h.baseline("a:absent>ok b:absent>ok c:absent>ok")
	src.put("d", map[string]string{"bad": "1"})
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "capped")
	if loss.Loss.Reason != LossCapped {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	src.put("e", nil) // still capped: no second loss
	h.settle(300 * time.Millisecond)
	src.remove("a")
	src.remove("e")
	b, j := h.waitBatch(i+1, kindIs(BatchBaseline), "back under the cap")
	if !b.AfterCap || itemsOf(b) != "b:absent>ok c:absent>ok d:absent>fault" {
		t.Fatalf("replacement: afterCap %v %q", b.AfterCap, itemsOf(b))
	}
	for _, x := range h.sink.allBatches()[i+1 : j] {
		if x.Kind == BatchLoss {
			t.Fatalf("one loss per capped episode: %+v", x)
		}
	}
}

func TestLedgerFullTurnoverIsTwiceTheKeys(t *testing.T) {
	src := newSource()
	for i := 0; i < 4; i++ {
		src.put(fmt.Sprintf("old%d", i), nil)
	}
	h := ledgerHarness(t, src, Config{RoleCap: 4})
	next := h.baseline("old0:absent>ok old1:absent>ok old2:absent>ok old3:absent>ok")
	src.mu.Lock() // swap every object between two lists: a relist turnover
	src.objs = map[string]*corev1.ConfigMap{}
	src.mu.Unlock()
	for i := 0; i < 4; i++ {
		src.mu.Lock()
		src.objs[fmt.Sprintf("new%d", i)] = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("new%d", i), Namespace: "ns", UID: types.UID(fmt.Sprint("u", i))}}
		src.mu.Unlock()
	}
	src.liveWatcher().Error(&apierrors.NewResourceExpired("too old").ErrStatus)
	b, _ := h.waitBatch(next, kindIs(BatchAfterGap), "the turnover")
	if len(b.Items) != 8 {
		t.Fatalf("a full turnover of 4 keys is 8 items: %q", itemsOf(b))
	}
}

// ---- absence, dependencies, served versions -------------------------------------------------

func TestLedgerAbsentKindIsALossThenANewBaseline(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	pr := newProbe("served")
	k := withProbe(ledgerKind("cms", src), pr.probe)
	h := ledgerHarness(t, src, Config{Kinds: []KindSpec{k}, ReprobeEvery: time.Second})
	next := h.baseline("a:absent>ok")
	pr.set("absent")
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "uninstalled")
	if loss.Loss.Reason != LossAbsent {
		t.Fatalf("a capability loss: %+v", loss.Loss)
	}
	pr.set("served")
	b, _ := h.waitBatch(i+1, kindIs(BatchBaseline), "reinstalled")
	if b.Identity.Sources[0].Generation == loss.Identity.Sources[0].Generation {
		t.Fatal("a reappearance is a new generation")
	}
}

func TestLedgerMixedKindAbsenceAffectsOnlyItsPartition(t *testing.T) {
	srcA, srcB := newSource(), newSource()
	srcA.put("a", nil)
	srcB.put("b", nil)
	pr := newProbe("served")
	kb := withProbe(ledgerKind("bs", srcB), pr.probe)
	h := ledgerHarness(t, srcA, Config{Kinds: []KindSpec{ledgerKind("as", srcA), kb}, ReprobeEvery: time.Second,
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{ledgerPart("mixed", []string{"as", "bs"})}}}})
	h.waitBatch(0, func(b TransitionBatch) bool { return b.Kind == BatchBaseline && b.Source == "bs" }, "bs baseline")
	_, i := h.waitBatch(0, func(b TransitionBatch) bool { return b.Kind == BatchBaseline && b.Source == "as" }, "as baseline")
	pr.set("absent")
	loss, j := h.waitBatch(i+1, kindIs(BatchLoss), "bs uninstalled")
	if loss.Source != "bs" || loss.Loss.Reason != LossAbsent {
		t.Fatalf("loss: %+v", loss)
	}
	srcA.put("a", map[string]string{"bad": "1"})
	b, _ := h.waitBatch(j+1, kindIs(BatchChanges), "as still observed")
	if b.Source != "as" || itemsOf(b) != "a:ok>fault" {
		t.Fatalf("as: %+v", b)
	}
}

func TestLedgerDependencyGapSuspendsTheCountedPartition(t *testing.T) {
	src, dep := newSource(), newSource()
	src.put("a", nil)
	dep.put("d", nil)
	h := ledgerHarness(t, src, Config{Kinds: []KindSpec{ledgerKind("cms", src), ledgerKind("deps", dep)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{ledgerPart("cms", []string{"cms"}, "deps")}}}})
	next := h.baseline("a:absent>ok")
	dep.liveWatcher().Error(&apierrors.NewResourceExpired("too old").ErrStatus)
	loss, i := h.waitBatch(next, kindIs(BatchLoss), "the dependency relisted")
	if loss.Source != "cms" || loss.Loss.Reason != LossRelist {
		t.Fatalf("loss: %+v", loss)
	}
	h.waitBatch(i+1, kindIs(BatchAfterGap), "reconciled")
}

func TestLedgerBatchesCarryTheServedGVRAcrossAVersionChange(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	pr := newProbe("v1beta2")
	k := ledgerKind("widgets", src)
	k.GVR, k.Probe = "example.io/widgets", pr.probe
	h := ledgerHarness(t, src, Config{Kinds: []KindSpec{k}, ReprobeEvery: time.Second,
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{ledgerPart("widgets", []string{"widgets"})}}}})
	b, i := h.waitBatch(0, kindIs(BatchBaseline), "baseline")
	if b.GVR != "example.io/v1beta2/widgets" {
		t.Fatalf("baseline GVR %q", b.GVR)
	}
	pr.set("v1")
	loss, j := h.waitBatch(i+1, kindIs(BatchLoss), "the version change is a gap")
	if loss.Loss.Reason != LossGeneration && loss.Loss.Reason != LossCoverage {
		t.Fatalf("loss: %+v", loss.Loss)
	}
	gap, _ := h.waitBatch(j+1, kindIs(BatchAfterGap), "reconciled at the new version")
	if gap.GVR != "example.io/v1/widgets" {
		t.Fatalf("after-gap GVR %q (loss carried %q)", gap.GVR, loss.GVR)
	}
}

// ---- sampled sources ----------------------------------------------------------------------

func TestLedgerSampledReadsAndDerivedBoundaries(t *testing.T) {
	type rel struct{ name, status, until string }
	reads := make(chan []rel, 4)
	var current []rel
	read := func(context.Context) ([]any, bool, error) {
		select {
		case current = <-reads:
		default:
		}
		out := make([]any, len(current))
		for i, r := range current {
			out[i] = r
		}
		return out, false, nil
	}
	eval := func(c Capture) PartResult {
		var roles []RoleEntry
		var next *time.Time
		for _, o := range c.Objects["helm"] {
			r := o.(rel)
			role, boundary := r.status, (*time.Time)(nil)
			if r.until != "" {
				u, _ := time.Parse(time.RFC3339, r.until)
				if c.Now.Before(u) {
					role = "progressing"
					if next == nil || u.Before(*next) {
						next = &u
					}
				} else {
					role, boundary = "degraded", &u
				}
			}
			roles = append(roles, RoleEntry{Source: "helm", Key: "ns/" + r.name, NS: "ns", Name: r.name, Role: role, Boundary: boundary})
		}
		n := len(roles)
		return PartResult{Evidence: &n, Roles: roles, Next: next}
	}
	reads <- []rel{{"a", "ok", ""}, {"b", "ok", ""}}
	snap := SnapshotSpec{Name: "helm", Scope: "all namespaces", Cadence: time.Hour, Timeout: 5 * time.Second, Read: read}
	h := start(t, Config{Snapshots: []SnapshotSpec{snap}, Domains: []DomainSpec{{ID: "helm", Parts: []PartSpec{{ID: "releases", Counted: []string{"helm"}, Evaluate: eval, Ledger: true}}}}})
	b, i := h.waitBatch(0, kindIs(BatchBaseline), "baseline")
	if b.Mode != ModeSampled || b.ReadAt == nil {
		t.Fatalf("baseline: %+v", b)
	}
	// One read later: a fails, b is gone, and c and d are pending with two
	// different deadlines ahead.
	now := h.clk.Now()
	ua, ub := now.Add(30*time.Second).Format(time.RFC3339), now.Add(40*time.Second).Format(time.RFC3339)
	reads <- []rel{{"a", "fault", ""}, {"c", "ok", ua}, {"d", "ok", ub}}
	h.o.Refresh("helm")
	b, j := h.waitBatch(i+1, kindIs(BatchChanges), "the next read")
	if b.ReadAt == nil || b.PrevReadAt == nil || b.ReadAt.Equal(*b.PrevReadAt) || itemsOf(b) != "a:ok>fault b:ok>gone c:absent>progressing d:absent>progressing" {
		t.Fatalf("sampled: %q read %v prev %v", itemsOf(b), b.ReadAt, b.PrevReadAt)
	}
	// Both deadlines pass before one delayed evaluation from the same read.
	h.clk.Step(time.Minute)
	d, _ := h.waitBatch(j+1, func(b TransitionBatch) bool { return b.Kind == BatchChanges && len(b.Items) > 0 }, "derived")
	if itemsOf(d) != "c:progressing>degraded(derived) d:progressing>degraded(derived)" ||
		d.Items[0].Boundary.Format(time.RFC3339) != ua || d.Items[1].Boundary.Format(time.RFC3339) != ub ||
		!d.ReadAt.Equal(*d.PrevReadAt) || d.At.Equal(*d.Items[0].Boundary) {
		t.Fatalf("derived: %q %+v at %v", itemsOf(d), d.Items, d.At)
	}
}

// ---- review of 5fcb5f2: the capture clock, a late read, the served GVR -------------------

// directObserver builds an unstarted Observer with one live cms generation,
// for driving evaluate and publish directly.
func directObserver(t *testing.T, clk *testingclock.FakeClock, sink *recSink, gvr string, parts ...PartSpec) *Observer {
	t.Helper()
	k := ledgerKind("cms", newSource())
	k.GVR = gvr
	if len(parts) == 0 {
		parts = []PartSpec{ledgerPart("cms", []string{"cms"})}
	}
	o := New(Config{Cluster: "direct", Clock: clk, Sink: sink, Kinds: []KindSpec{k}, Domains: []DomainSpec{{ID: "things", Parts: parts}}})
	ks := o.kinds["cms"]
	ks.refs, ks.probed, ks.served, ks.version = 1, true, true, "v1beta2"
	ks.gen = &kindGen{o: o, ks: ks, id: 1, epoch: 1, version: "v1beta2", cancel: func() {}, fresh: freshness{cov: Live},
		model: map[string]any{"ns/a": &cmObj{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: "a"}, Bad: true}}}
	return o
}

// The rules evaluate at the capture's own clock read, and the batch is
// stamped with it: a slow part earlier in the cycle cannot make a later part
// report a pre-deadline role after the deadline.
func TestLedgerRulesAndBatchShareTheCaptureClock(t *testing.T) {
	clk := testingclock.NewFakeClock(t0)
	sink := &recSink{}
	slow := PartSpec{ID: "slow", Counted: []string{"cms"}, Evaluate: func(Capture) PartResult {
		clk.Step(2 * time.Minute)
		return PartResult{}
	}}
	o := directObserver(t, clk, sink, "", slow, ledgerPart("late", []string{"cms"}))
	o.kinds["cms"].gen.model = map[string]any{"ns/a": &cmObj{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: "a"},
		Until: t0.Add(time.Minute).Format(time.RFC3339)}}
	for _, p := range o.domains[0].parts {
		o.dirty[p.key] = true
	}
	o.publish(t0)
	bs := sink.allBatches()
	if len(bs) != 1 || len(bs[0].Items) != 1 || !bs[0].At.Equal(t0.Add(2*time.Minute)) || bs[0].Items[0].To != "fault" {
		t.Fatalf("captured at %v past the 12:01 boundary must read fault: %+v", bs[0].At, bs)
	}
}

// A successful read that lands after the previous one went overdue - before
// the loop's boundary check - keeps the gap.
func TestLedgerLateSuccessfulReadKeepsTheOverdueGap(t *testing.T) {
	clk := testingclock.NewFakeClock(t0)
	sink := &recSink{}
	eval := func(c Capture) PartResult {
		var roles []RoleEntry
		for _, v := range c.Objects["helm"] {
			roles = append(roles, RoleEntry{Source: "helm", Key: "ns/a", NS: "ns", Name: "a", Role: v.(string)})
		}
		return PartResult{Roles: roles}
	}
	o := New(Config{Cluster: "direct", Clock: clk, Sink: sink, Snapshots: []SnapshotSpec{{Name: "helm", Cadence: time.Minute}},
		Domains: []DomainSpec{{ID: "helm", Parts: []PartSpec{{ID: "releases", Counted: []string{"helm"}, Evaluate: eval, Ledger: true}}}}})
	ss := o.snaps["helm"]
	g := &snapGen{o: o, ss: ss, ctx: context.Background(), id: 1, fresh: freshness{cov: Checking}}
	ss.gen = g
	g.apply(SnapshotRead{Items: []any{"ok"}})
	p := o.domains[0].parts[0]
	o.evaluate(p, clk.Now(), false)
	clk.Step(3 * time.Minute) // the loop was busy, or the machine slept, past 2x cadence
	g.apply(SnapshotRead{Items: []any{"fault"}})
	o.applyBoundaries(clk.Now())
	o.evaluate(p, clk.Now(), false)
	bs := sink.allBatches()
	if len(bs) != 3 || bs[1].Kind != BatchLoss || bs[2].Kind != BatchAfterGap {
		t.Fatalf("a late read must not erase the overdue gap: %+v", bs)
	}
}

// An absence loss names the last served version, never the unversioned
// descriptor.
func TestLedgerAbsenceLossKeepsTheServedVersion(t *testing.T) {
	clk := testingclock.NewFakeClock(t0)
	sink := &recSink{}
	o := directObserver(t, clk, sink, "example.io/widgets")
	p := o.domains[0].parts[0]
	o.evaluate(p, clk.Now(), false)
	o.mu.Lock()
	o.applyProbeLocked(o.kinds["cms"], Probe{Served: false})
	o.mu.Unlock()
	clk.Step(time.Second)
	o.evaluate(p, clk.Now(), false)
	bs := sink.allBatches()
	if len(bs) != 2 || bs[1].Kind != BatchLoss || bs[1].GVR != "example.io/v1beta2/widgets" || bs[1].Identity.Sources[0].GVR != "example.io/v1beta2/widgets" {
		t.Fatalf("absence loss GVR: %+v", bs)
	}
}
