package healthnotify

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// The Line slice 10 note §2-§3: the evidence fold, the episode machine,
// triggers, de-duplication, pending items, delivery and bounds.

type rig struct {
	t       *testing.T
	now     time.Time
	e       *Engine
	sent    []Notification
	lists   []Baseline
	prot    map[string]bool
	sendErr error
}

func newRig(t *testing.T, s Settings) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), prot: map[string]bool{"prd": true}}
	r.e = New(func() time.Time { return r.now }, Hooks{
		Send: func(n Notification) error {
			if r.sendErr != nil {
				return r.sendErr
			}
			r.sent = append(r.sent, n)
			return nil
		},
		List:      func(b Baseline) { r.lists = append(r.lists, b) },
		Protected: func(c string) bool { return r.prot[c] },
		Place:     func(c string) string { return c + " · North Europe" },
	}, 0)
	r.e.SetSettings(s)
	return r
}

func (r *rig) step(d time.Duration) {
	r.now = r.now.Add(d)
	r.e.Flush()
}

func item(uid, from, to string) observe.Transition {
	return observe.Transition{Key: uid, NS: "shop", Name: "web-" + uid, GVR: "v1/pods", From: from, To: to, Label: to + " label"}
}

func batch(kind string, items ...observe.Transition) observe.TransitionBatch {
	return observe.TransitionBatch{Cluster: "prd", Incarnation: 1, Task: "pods", Part: "pods", Source: "pods", GVR: "v1/pods", Mode: observe.ModeWatch, Kind: kind, Items: items}
}

// in names another partition counting the same objects.
func in(part string, b observe.TransitionBatch) observe.TransitionBatch {
	b.Part = part
	return b
}

var (
	on   = Settings{Enabled: true, Scope: ScopeProtected}
	recs = Settings{Enabled: true, Scope: ScopeProtected, Recoveries: true}
)

func (r *rig) bodies() []string {
	var out []string
	for _, n := range r.sent {
		out = append(out, n.Body)
	}
	return out
}

func (r *rig) fold(uid string) int {
	r.e.mu.Lock()
	defer r.e.mu.Unlock()
	if ep := r.e.clusters["prd"].episodes[uid]; ep != nil {
		return ep.fold
	}
	return -1 // no episode
}

func TestBaselineFaultsAreListedNeverNotified(t *testing.T) {
	r := newRig(t, recs)
	r.e.Apply(batch(observe.BatchBaseline, item("a", observe.StateAbsent, "fault"), item("b", observe.StateAbsent, "ok")))
	r.e.Apply(observe.TransitionBatch{Cluster: "prd", Incarnation: 1, Task: "workloads", Part: "workloads", Source: "deployments", GVR: "apps/v1/deployments", Mode: observe.ModeWatch, Kind: observe.BatchBaseline,
		Items: []observe.Transition{{Key: "d", Name: "api", NS: "shop", From: observe.StateAbsent, To: "fault"}}})
	r.step(BaselineWindow)
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatalf("baseline notified: %v", r.bodies())
	}
	if len(r.lists) != 1 || r.lists[0].Count != 2 || r.lists[0].Reason != ListConnect || r.lists[0].Items[1].Kind != "Deployment" {
		t.Fatalf("one listing per connect: %+v", r.lists)
	}
	// a baseline fault that recovers closes - silently: its onset was never sent
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	r.step(BurstWindow)
	if len(r.sent) != 0 || r.fold("a") != -1 {
		t.Fatalf("a baseline recovery notified: %v", r.bodies())
	}
}

func TestNewEpisodesCoalescePerCluster(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchBaseline, item("a", observe.StateAbsent, "ok")))
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(30 * time.Second)
	r.e.Apply(batch(observe.BatchChanges, item("b", observe.StateAbsent, "fault"), item("c", "ok", "fault")))
	if len(r.sent) != 0 {
		t.Fatal("sent before the window closed")
	}
	r.step(30 * time.Second)
	if len(r.sent) != 1 || r.sent[0].Count != 3 || r.sent[0].Faults != 3 || r.sent[0].Single != nil || r.sent[0].Title != "prd · North Europe" ||
		!strings.HasPrefix(r.sent[0].Body, "3 new faults on prd · North Europe: Pod shop/web-a: fault label") {
		t.Fatalf("one notification: %+v", r.sent)
	}
}

func TestSingleNotificationCarriesItsObject(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	if len(r.sent) != 1 || r.sent[0].Single == nil {
		t.Fatalf("%+v", r.sent)
	}
	o := *r.sent[0].Single
	if o.Cluster != "prd" || o.Task != "pods" || o.Part != "pods" || o.GVR != "v1/pods" || o.UID != "a" || o.Namespace != "shop" || r.sent[0].Body != "Pod shop/web-a: fault label" {
		t.Fatalf("object %+v body %q", o, r.sent[0].Body)
	}
}

func TestInputsTheMachineIgnores(t *testing.T) {
	r := newRig(t, recs)
	sampled := batch(observe.BatchChanges, item("h", "ok", "fault"))
	sampled.Mode, sampled.Task, sampled.Part, sampled.GVR = observe.ModeSampled, "helm", "releases", ""
	r.e.Apply(sampled)
	noKey := item("", "ok", "fault")
	derived := item("d", "ok", "fault")
	derived.Derived = true
	restated := item("s", "fault", "fault")
	restated.Restated = true
	r.e.Apply(batch(observe.BatchChanges, noKey, derived, restated))
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatalf("sampled, keyless, derived or restated items notified: %v", r.bodies())
	}
	// a derived item never closes an episode either
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	gone := item("a", "fault", "ok")
	gone.Derived = true
	r.e.Apply(batch(observe.BatchChanges, gone))
	if r.fold("a") != foldFault {
		t.Fatal("a derived item closed the episode")
	}
}

func TestUnknownAndQuietKeepTheEpisode(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchChanges, item("n", "ok", "fault")))
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("n", "fault", "unknown")))
	if r.fold("n") != foldUnknown {
		t.Fatal("unknown suspends")
	}
	r.e.Apply(batch(observe.BatchChanges, item("n", "unknown", "fault")))
	r.e.Apply(batch(observe.BatchChanges, item("n", "fault", "degraded")))
	if r.fold("n") != foldQuiet {
		t.Fatal("degraded keeps it open and quiet")
	}
	r.e.Apply(batch(observe.BatchChanges, item("n", "degraded", "fault")))
	r.step(DedupeWindow + BurstWindow)
	if len(r.sent) != 1 {
		t.Fatalf("a return to fault is the same episode: %v", r.bodies())
	}
}

func TestRecoveriesSettleFromRetainedState(t *testing.T) {
	for _, via := range []string{"unknown", "degraded", ""} {
		r := newRig(t, recs)
		r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
		r.step(BurstWindow)
		from := "fault"
		if via != "" {
			r.e.Apply(batch(observe.BatchChanges, item("a", "fault", via)))
			from = via
		}
		r.e.Apply(batch(observe.BatchChanges, item("a", from, "ok")))
		r.step(BurstWindow)
		if got := r.bodies(); len(got) != 2 || got[1] != "Recovered: Pod shop/web-a: ok label" || r.sent[1].Recoveries != 1 {
			t.Fatalf("via %q: %v", via, got)
		}
	}
	// recoveries off: none
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	r.step(BurstWindow)
	if len(r.sent) != 1 {
		t.Fatalf("recoveries off: %v", r.bodies())
	}
}

func TestNoRecoveryWithoutASentOnset(t *testing.T) {
	cases := map[string]func(r *rig){
		"queued": func(r *rig) {}, // recovers before its window
		"focus-suppressed": func(r *rig) {
			r.e.SetFocus("prd", true)
			r.step(BurstWindow)
			r.e.SetFocus("prd", false)
		},
		"failed": func(r *rig) {
			r.sendErr = errors.New("no permission")
			r.step(BurstWindow)
			r.sendErr = nil
		},
	}
	for name, before := range cases {
		r := newRig(t, recs)
		r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
		before(r)
		r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
		r.step(BurstWindow)
		if len(r.sent) != 0 {
			t.Fatalf("%s: %v", name, r.bodies())
		}
	}
	// withheld by de-duplication: a flap's second episode recovers silently
	r := newRig(t, recs)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	r.step(BurstWindow) // the first recovery goes
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.step(DedupeWindow) // the recovery allowance has expired; the onset was withheld
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 2 {
		t.Fatalf("a withheld onset's recovery notified: %v", got)
	}
}

func TestPendingItemsAreRecheckedAtDispatch(t *testing.T) {
	r := newRig(t, recs)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault"), item("b", "ok", "fault"), item("c", "ok", "fault")))
	r.step(10 * time.Second)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok"), item("b", "fault", observe.StateGone)))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 1 || r.sent[0].Single == nil || r.sent[0].Single.UID != "c" {
		t.Fatalf("only c goes, and no recovery of an onset never sent: %v", got)
	}
	// a mixed notification: c recovers while d breaks
	r.e.Apply(batch(observe.BatchChanges, item("c", "fault", "ok"), item("d", "ok", "fault")))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 2 || got[1] != "1 new fault and 1 recovery on prd · North Europe: Recovered: Pod shop/web-c: ok label; Pod shop/web-d: fault label" ||
		r.sent[1].Faults != 1 || r.sent[1].Recoveries != 1 || r.sent[1].Single != nil {
		t.Fatalf("mixed: %q", got)
	}
	// a recovery is dropped when the UID breaks again before dispatch
	r.step(DedupeWindow)
	r.e.Apply(batch(observe.BatchChanges, item("d", "fault", "ok")))
	r.e.Apply(batch(observe.BatchChanges, item("d", "ok", "fault")))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 3 || got[2] != "Pod shop/web-d: fault label" {
		t.Fatalf("the recovery of a UID broken again went out: %q", got)
	}
}

func TestSuspendedOnsetGoesAsHistory(t *testing.T) {
	r := newRig(t, on)
	b := batch(observe.BatchChanges, item("a", "ok", "fault"))
	b.At = time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	r.e.Apply(b)
	r.e.Apply(batch(observe.BatchLoss))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 1 || got[0] != "Pod shop/web-a: fault label · observed 12:00; coverage lost since" {
		t.Fatalf("%q", got)
	}
}

func TestPartitionsFoldPerUID(t *testing.T) {
	r := newRig(t, recs)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.e.Apply(in("other", batch(observe.BatchChanges, item("a", "ok", "fault"))))
	r.step(BurstWindow)
	if len(r.sent) != 1 {
		t.Fatalf("one UID, one onset: %v", r.bodies())
	}
	// P1 to ok while P2 holds fault: the episode stays, no recovery
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	if r.fold("a") != foldFault {
		t.Fatal("P1's ok closed an episode P2 still holds")
	}
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	// P1's loss leaves P2's covered fault
	r.e.Apply(batch(observe.BatchLoss))
	if r.fold("a") != foldFault {
		t.Fatal("a one-part loss suspended a fault another part covers")
	}
	// P1's baseline without a leaves P2's evidence
	r.e.Apply(batch(observe.BatchBaseline))
	if r.fold("a") != foldFault {
		t.Fatal("a one-part replacement erased another part's evidence")
	}
	r.e.Apply(in("other", batch(observe.BatchChanges, item("a", "fault", "ok"))))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 2 || !strings.HasPrefix(got[1], "Recovered: ") {
		t.Fatalf("the last part's ok recovers: %v", got)
	}
}

func TestLossAndAfterGap(t *testing.T) {
	r := newRig(t, recs)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault"), item("u", "ok", "fault")))
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("u", "fault", "unknown")))
	r.e.Apply(batch(observe.BatchLoss))
	if r.fold("a") != foldCoverage {
		t.Fatal("a loss suspends")
	}
	// during the gap: a recovered, b broke; after the gap only the changes come
	r.e.Apply(batch(observe.BatchAfterGap, item("a", "fault", "ok"), item("b", "ok", "fault")))
	r.step(BaselineWindow)
	r.step(BurstWindow)
	if len(r.sent) != 1 || r.fold("a") != -1 {
		t.Fatalf("after a gap nothing notifies, and a closes silently: %v", r.bodies())
	}
	if len(r.lists) != 1 || r.lists[0].Reason != ListAfterGap || r.lists[0].Count != 1 || r.lists[0].Items[0].UID != "b" {
		t.Fatalf("listing %+v", r.lists)
	}
	// u was not in the delta: its retained unknown keeps it suspended
	if r.fold("u") != foldUnknown {
		t.Fatalf("an empty delta resumed a retained unknown: %d", r.fold("u"))
	}
	// so a genuine recovery and a later fault are a new episode
	r.e.Apply(batch(observe.BatchChanges, item("u", "unknown", "ok")))
	r.step(DedupeWindow)
	r.e.Apply(batch(observe.BatchChanges, item("u", "ok", "fault")))
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 3 || !strings.HasPrefix(got[1], "Recovered: Pod shop/web-u") || got[2] != "Pod shop/web-u: fault label" {
		t.Fatalf("%q", got)
	}
	// an after-gap's retained fault resumes; a later fault is the same episode
	r.e.Apply(batch(observe.BatchLoss))
	r.e.Apply(batch(observe.BatchAfterGap))
	if r.fold("b") != foldFault {
		t.Fatal("a retained fault resumes after an empty delta")
	}
}

func TestScopeAndDisabled(t *testing.T) {
	r := newRig(t, on)
	other := batch(observe.BatchChanges, item("x", "ok", "fault"))
	other.Cluster = "dev"
	r.e.Apply(other)
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatal("protected scope notified an unprotected cluster")
	}
	r.e.SetSettings(Settings{Enabled: true, Scope: ScopeAll})
	other.Items = []observe.Transition{item("y", "ok", "fault")}
	r.e.Apply(other)
	r.step(BurstWindow)
	if len(r.sent) != 1 {
		t.Fatal("scope all")
	}
	// disabled: onsets open episodes that never notify; enabling sends no backlog
	d := newRig(t, Settings{})
	d.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	d.e.SetSettings(on)
	d.step(BurstWindow)
	d.e.Apply(batch(observe.BatchChanges, item("a", "fault", "unknown"), item("a", "unknown", "fault")))
	d.step(BurstWindow)
	if len(d.sent) != 0 {
		t.Fatalf("a backlog: %v", d.bodies())
	}
	// disabling during the window cancels; narrowing the scope drops at dispatch
	w := newRig(t, on)
	w.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	w.e.SetSettings(Settings{})
	w.e.SetSettings(on)
	w.step(BurstWindow)
	w.e.Apply(batch(observe.BatchChanges, item("b", "ok", "fault")))
	w.prot["prd"] = false
	w.step(BurstWindow)
	if len(w.sent) != 0 {
		t.Fatalf("disabled or out of scope at dispatch: %v", w.bodies())
	}
}

func TestDedupeAcrossThirtyMinutes(t *testing.T) {
	r := newRig(t, on)
	flap := func() {
		r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
		r.step(BurstWindow)
		r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	}
	flap()
	r.step(10 * time.Minute)
	flap()
	if len(r.sent) != 1 {
		t.Fatalf("a flap within 30 minutes notified again: %v", r.bodies())
	}
	r.step(DedupeWindow)
	flap()
	if len(r.sent) != 2 {
		t.Fatalf("after 30 minutes: %v", r.bodies())
	}
}

func TestFocusSuppressionConsumesTheAllowance(t *testing.T) {
	r := newRig(t, on)
	r.e.SetFocus("prd", true)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.e.SetFocus("prd", false)
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok"), item("a", "ok", "fault")))
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatalf("sent while focused, released after, or the allowance not consumed: %v", r.bodies())
	}
	// focused elsewhere: sent
	r.e.SetFocus("dev", true)
	r.e.Apply(batch(observe.BatchChanges, item("b", "ok", "fault")))
	r.step(BurstWindow)
	if len(r.sent) != 1 {
		t.Fatal("focused on another cluster")
	}
}

func TestEndAndReincarnationCancelPendingItems(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.e.Apply(batch(observe.BatchEnd))
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatal("an ended incarnation's item went out")
	}
	r.e.Apply(batch(observe.BatchChanges, item("b", "ok", "fault")))
	next := batch(observe.BatchBaseline, item("a", observe.StateAbsent, "fault"))
	next.Incarnation = 2
	r.e.Apply(next)
	r.e.Apply(batch(observe.BatchChanges, item("z", "ok", "fault"))) // incarnation 1, after 2: ignored
	r.step(BurstWindow)
	if len(r.sent) != 0 || len(r.lists) != 1 || r.lists[0].Items[0].UID != "a" {
		t.Fatalf("sent %v lists %+v", r.bodies(), r.lists)
	}
}

func TestReplacementIsANewEpisode(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", observe.StateGone), item("a2", observe.StateAbsent, "fault")))
	r.step(BurstWindow)
	if len(r.sent) != 2 || r.sent[1].Single.UID != "a2" || r.fold("a") != -1 {
		t.Fatalf("%v", r.bodies())
	}
	// a repeated UID in one batch is one episode
	r.e.Apply(batch(observe.BatchChanges, item("r", "ok", "fault"), item("r", "fault", "fault")))
	r.step(BurstWindow)
	if len(r.sent) != 3 || r.sent[2].Count != 1 {
		t.Fatalf("%v", r.bodies())
	}
}

func TestBodyNamesThreeAndCountsTheRest(t *testing.T) {
	r := newRig(t, on)
	var items []observe.Transition
	for i := 0; i < 57; i++ {
		items = append(items, item(fmt.Sprint(i), "ok", "fault"))
	}
	r.e.Apply(batch(observe.BatchChanges, items...))
	// recovered overflow items are subtracted, not counted
	r.e.Apply(batch(observe.BatchChanges, item("40", "fault", "ok"), item("41", "fault", observe.StateGone)))
	r.step(BurstWindow)
	if len(r.sent) != 1 || r.sent[0].Count != 55 || !strings.HasSuffix(r.sent[0].Body, "+52 more") {
		t.Fatalf("%+v", r.sent)
	}
}

func TestAPartitionOverItsCapIsLostUntilItsBaseline(t *testing.T) {
	r := newRig(t, on)
	r.e = New(func() time.Time { return r.now }, Hooks{
		Send:      func(n Notification) error { r.sent = append(r.sent, n); return nil },
		List:      func(b Baseline) { r.lists = append(r.lists, b) },
		Protected: func(string) bool { return true },
	}, 2)
	r.e.SetSettings(on)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.e.Apply(batch(observe.BatchChanges, item("b", "ok", "degraded"), item("c", "ok", "fault")))
	if r.fold("a") != foldCoverage || r.fold("c") != -1 {
		t.Fatalf("over its cap the partition is lost: a %d c %d", r.fold("a"), r.fold("c"))
	}
	// a full turnover's delta passes through twice the cap, and fits
	fresh := newRig(t, on)
	fresh.e = New(func() time.Time { return fresh.now }, Hooks{Protected: func(string) bool { return true }}, 2)
	fresh.e.Apply(batch(observe.BatchBaseline, item("p", observe.StateAbsent, "degraded"), item("q", observe.StateAbsent, "fault")))
	fresh.e.Apply(batch(observe.BatchChanges, item("x", observe.StateAbsent, "degraded"), item("y", observe.StateAbsent, "fault"),
		item("p", "degraded", observe.StateGone), item("q", "fault", observe.StateGone)))
	if f := fresh.e.clusters["prd"].parts[partKey{"pods", "pods", "pods"}]; f.overCap || fresh.fold("y") != foldFault {
		t.Fatal("a turnover that fits was taken as over the cap")
	}
	// an after-gap does not restore it; its after-cap baseline does, silently
	r.e.Apply(batch(observe.BatchAfterGap))
	if r.fold("a") != foldCoverage {
		t.Fatal("an after-gap restored an over-cap partition")
	}
	after := batch(observe.BatchBaseline, item("a", observe.StateAbsent, "fault"))
	after.AfterCap = true
	r.e.Apply(after)
	if r.fold("a") != foldFault {
		t.Fatal("the after-cap baseline settles it")
	}
	r.step(BaselineWindow)
	r.step(BurstWindow)
	if len(r.sent) != 1 || len(r.lists) != 1 || r.lists[0].Reason != ListAfterCap || r.lists[0].Items[0].UID != "a" {
		// a was already open: the baseline resumes it, and lists the fault it holds
		t.Fatalf("sent %v lists %+v", r.bodies(), r.lists)
	}
}

func TestFullDedupeDropsRatherThanEvicts(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchBaseline))
	r.e.mu.Lock()
	for i := 0; i < MaxDedupe; i++ {
		r.e.clusters["prd"].dedupe[dedupeKey{fmt.Sprint("old", i), fault}] = r.now
	}
	r.e.mu.Unlock()
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.step(BurstWindow)
	r.step(BaselineWindow)
	r.e.mu.Lock()
	_, guarded := r.e.clusters["prd"].dedupe[dedupeKey{"old0", fault}]
	r.e.mu.Unlock()
	if len(r.sent) != 0 || !guarded || len(r.lists) != 1 || r.lists[0].Reason != ListDropped || r.lists[0].Count != 1 {
		t.Fatalf("sent %v guarded %v lists %+v", r.bodies(), guarded, r.lists)
	}
}

// inFlight dispatches a's onset into a Send that blocks until released,
// returning the release and the Flush's end.
func inFlight(t *testing.T, fail bool) (r *rig, release func(), done chan struct{}) {
	r = newRig(t, recs)
	entered, unblock := make(chan struct{}), make(chan struct{})
	first := true
	r.e.hooks.Send = func(n Notification) error {
		if first {
			first = false
			close(entered)
			<-unblock
			if fail {
				return errors.New("refused")
			}
		}
		r.sent = append(r.sent, n)
		return nil
	}
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.now = r.now.Add(BurstWindow)
	done = make(chan struct{})
	go func() { r.e.Flush(); close(done) }()
	<-entered
	return r, func() { close(unblock) }, done
}

func TestARecoveryDuringAnInFlightOnset(t *testing.T) {
	// the send succeeds: the recovery queued during the call goes
	r, release, done := inFlight(t, false)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	release()
	<-done
	r.step(BurstWindow)
	if got := r.bodies(); len(got) != 2 || got[1] != "Recovered: Pod shop/web-a: ok label" {
		t.Fatalf("succeeded: %q", got)
	}
	// it fails: the recovery is withdrawn with it
	r, release, done = inFlight(t, true)
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	release()
	<-done
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatalf("failed: %q", r.bodies())
	}
	// an episode still open when the send fails is not sent: its later
	// recovery is silent
	r, release, done = inFlight(t, true)
	release()
	<-done
	r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	r.step(BurstWindow)
	if len(r.sent) != 0 {
		t.Fatalf("failed, still open: %q", r.bodies())
	}
	// end, reincarnation or disabling during the call revive nothing
	for _, cut := range []func(r *rig){
		func(r *rig) { r.e.Apply(batch(observe.BatchEnd)) },
		func(r *rig) { b := batch(observe.BatchBaseline); b.Incarnation = 2; r.e.Apply(b) },
		func(r *rig) { r.e.SetSettings(Settings{}); r.e.SetSettings(recs) },
	} {
		r, release, done = inFlight(t, true)
		r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
		cut(r)
		release()
		<-done
		r.step(BurstWindow)
		if len(r.sent) != 0 {
			t.Fatalf("revived: %q", r.bodies())
		}
	}
}

func TestSendingNeverBlocksApply(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	e := New(func() time.Time { return now }, Hooks{
		Send:      func(Notification) error { close(entered); <-release; return nil },
		Protected: func(string) bool { return true },
	}, 0)
	e.SetSettings(on)
	e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	now = now.Add(BurstWindow)
	go e.Flush()
	<-entered
	done := make(chan struct{})
	go func() { e.Apply(batch(observe.BatchChanges, item("b", "ok", "fault"))); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Apply waited on a blocked Send")
	}
	close(release)
}

func TestPendingWorkEndsWithItsEpisode(t *testing.T) {
	// a flapping UID before one dispatch leaves at most its live episode's onset
	r := newRig(t, on)
	for i := 0; i < 2000; i++ {
		r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
		r.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	}
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.e.mu.Lock()
	n := len(r.e.clusters["prd"].pending)
	r.e.mu.Unlock()
	if n != 1 {
		t.Fatalf("closed episodes' onsets accumulate: %d pending", n)
	}
	r.step(BurstWindow)
	if len(r.sent) != 1 {
		t.Fatalf("%v", r.bodies())
	}
	// a new episode ends an older recovery for good, even if it closes before dispatch
	r2 := newRig(t, recs)
	r2.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r2.step(BurstWindow)
	r2.e.Apply(batch(observe.BatchChanges, item("a", "fault", "ok")))
	r2.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r2.e.Apply(batch(observe.BatchChanges, item("a", "fault", observe.StateGone)))
	r2.step(BurstWindow)
	if got := r2.bodies(); len(got) != 1 {
		t.Fatalf("an older recovery outlived a later episode: %q", got)
	}
}

func TestCheckpointsListTheFaultsTheyHold(t *testing.T) {
	r := newRig(t, on)
	r.e.Apply(batch(observe.BatchChanges, item("a", "ok", "fault")))
	r.e.Apply(in("other", batch(observe.BatchChanges, item("a", "ok", "fault"))))
	r.step(BurstWindow)
	// an empty after-gap of each part: a, retained, listed once across both
	r.e.Apply(batch(observe.BatchLoss))
	r.e.Apply(in("other", batch(observe.BatchLoss)))
	r.e.Apply(batch(observe.BatchAfterGap))
	r.e.Apply(in("other", batch(observe.BatchAfterGap)))
	r.step(BaselineWindow)
	if len(r.lists) != 1 || r.lists[0].Reason != ListAfterGap || r.lists[0].Count != 1 || r.lists[0].Items[0].UID != "a" {
		t.Fatalf("after-gap listing %+v", r.lists)
	}
	// an after-cap baseline holding the same fault lists it too
	after := batch(observe.BatchBaseline, item("a", observe.StateAbsent, "fault"))
	after.AfterCap = true
	r.e.Apply(after)
	r.step(BaselineWindow)
	if len(r.lists) != 2 || r.lists[1].Reason != ListAfterCap || r.lists[1].Count != 1 {
		t.Fatalf("after-cap listing %+v", r.lists)
	}
	if len(r.sent) != 1 {
		t.Fatalf("a checkpoint notified: %v", r.bodies())
	}
}
