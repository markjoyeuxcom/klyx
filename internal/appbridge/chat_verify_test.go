package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A3 stage 4: a step's effect from this request's own witness, on a
// fresh read and a watch; the incident from the Observer's verdicts.

// fastVerify shortens every bound and tick for a test.
func fastVerify(t *testing.T, bound time.Duration) {
	t.Helper()
	pb, pt, pi, pm := effectBoundFor, verifyTick, incidentBound, verifyEventsMax
	effectBoundFor = func(string, *unstructured.Unstructured) time.Duration { return bound }
	verifyTick = 5 * time.Millisecond
	t.Cleanup(func() { effectBoundFor, verifyTick, incidentBound, verifyEventsMax = pb, pt, pi, pm })
}

// watchConn serves prepared watches, in order, and records each call; past
// them, a watch that stays quiet.
type watchConn struct {
	*stepsConn
	wmu     sync.Mutex
	watches []*watch.FakeWatcher
	calls   []string
}

func (c *watchConn) ChatWatch(_ context.Context, gvr schema.GroupVersionResource, ns, name, rv string) (watch.Interface, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.calls = append(c.calls, fmt.Sprintf("%s %s/%s @%s", gvr.Resource, ns, name, rv))
	if len(c.watches) == 0 {
		return watch.NewFakeWithChanSize(1, false), nil
	}
	w := c.watches[0]
	c.watches = c.watches[1:]
	return w, nil
}

func (c *watchConn) watched() []string {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return append([]string(nil), c.calls...)
}

// events is a watch holding evs, then closed when closed is set.
func events(closed bool, evs ...watch.Event) *watch.FakeWatcher {
	w := watch.NewFakeWithChanSize(len(evs)+1, false)
	for _, e := range evs {
		w.Action(e.Type, e.Object)
	}
	if closed {
		w.Stop()
	}
	return w
}

func mod(u *unstructured.Unstructured) watch.Event {
	return watch.Event{Type: watch.Modified, Object: u}
}

func newWatchConn(t *testing.T) (*watchConn, *chatFixture) {
	f := newToolsFixture(t)
	return &watchConn{stepsConn: &stepsConn{fakeChatConn: f.conn}}, f
}

// deploy is Deployment shop/web at generation gen, observed at observed.
func deployAt(uid string, gen, observed int64, spec map[string]any, status map[string]any) *unstructured.Unstructured {
	st := map[string]any{"observedGeneration": observed}
	for k, v := range status {
		st[k] = v
	}
	return uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": uid, "generation": gen, "resourceVersion": fmt.Sprint("rv", gen, observed)}, "spec": spec, "status": st})
}

var webFact = actionFact{Kind: "Deployment", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}

func witnessOf(s *ChatService, conn ChatConn, st ActionStepDTO, f actionFact, partner *fleet.RevisionExpect) *witness {
	w := &witness{sn: stepSnap{step: st, fact: f, partner: partner}}
	w.holds = func(u *unstructured.Unstructured) (bool, string) {
		return s.stateEffect(context.Background(), conn, st, f, partner, u, time.Time{})
	}
	return w
}

// §5.2's witnesses, verb by verb: a receipt fixes the generation, a lost
// answer is established only by this request's own token, and a state
// effect by the matrix's predicate.
func TestTheWitnessByVerb(t *testing.T) {
	s := &ChatService{}
	tok, older, later := "2026-10-14T12:00:00.000000002Z", "2026-10-14T11:59:00Z", "2026-10-14T12:00:01Z"
	two := 2
	ks := func(gen int64, suspend bool, requested, handled string) *unstructured.Unstructured {
		ann := map[string]any{}
		if requested != "" {
			ann["reconcile.fluxcd.io/requestedAt"] = requested
		}
		return uobj(map[string]any{"kind": "Kustomization", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web", "generation": gen, "annotations": ann},
			"spec": map[string]any{"suspend": suspend}, "status": map[string]any{"lastHandledReconcileAt": handled}})
	}
	restarted := func(token string, gen, observed, updated int64) *unstructured.Unstructured {
		tm := map[string]any{"spec": map[string]any{}}
		if token != "" {
			tm["metadata"] = map[string]any{"annotations": map[string]any{"kubectl.kubernetes.io/restartedAt": token}}
		}
		return deployAt("uid-web", gen, observed, map[string]any{"replicas": int64(3), "template": tm}, map[string]any{"replicas": int64(3), "updatedReplicas": updated})
	}
	tmpl := map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "i:4"}}}}
	dsDigest, _ := workloads.TemplateDigest(tmpl, false)
	ds := func(tm map[string]any, gen, observed int64) *unstructured.Unstructured {
		return uobj(map[string]any{"kind": "DaemonSet", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web", "generation": gen}, "spec": map[string]any{"template": tm}, "status": map[string]any{"observedGeneration": observed}})
	}
	for _, c := range []struct {
		name    string
		verb    string
		step    ActionStepDTO
		written bool
		gen     int64
		prior   int64
		token   string
		read    *unstructured.Unstructured
		want    int
		why     string
	}{
		{"a receipt's generation", "flux-suspend", ActionStepDTO{}, true, 5, 0, "", ks(4, true, "", ""), judgePending, "predates"},
		{"suspended at the receipt", "flux-suspend", ActionStepDTO{}, true, 5, 0, "", ks(5, true, "", ""), judgeEstablished, ""},
		{"a lost suspension observed", "flux-suspend", ActionStepDTO{}, false, 0, 0, "", ks(4, true, "", ""), judgeEstablished, ""},
		{"reverted by a third party", "flux-suspend", ActionStepDTO{}, true, 5, 0, "", ks(6, false, "", ""), judgePending, "not suspended"},
		{"its token handled", "flux-reconcile", ActionStepDTO{}, true, 0, 0, tok, ks(4, false, tok, tok), judgeEstablished, ""},
		{"a later token handled", "flux-reconcile", ActionStepDTO{}, true, 0, 0, tok, ks(4, false, later, later), judgeFailed, "superseded"},
		{"not handled yet", "flux-reconcile", ActionStepDTO{}, true, 0, 0, tok, ks(4, false, tok, older), judgePending, "has not handled"},
		// a lost answer: the older token was handled, this one never landed
		{"no commit", "flux-reconcile", ActionStepDTO{}, false, 0, 0, tok, ks(4, false, older, older), judgePending, "has not handled"},
		{"a late commit", "flux-reconcile", ActionStepDTO{}, false, 0, 0, tok, ks(4, false, tok, tok), judgeEstablished, ""},
		{"lost, then superseded", "flux-reconcile", ActionStepDTO{}, false, 0, 0, tok, ks(4, false, later, later), judgeFailed, "superseded"},
		{"no new template", "rollout-restart", ActionStepDTO{}, true, 5, 5, tok, restarted(tok, 5, 5, 3), judgeFailed, "wrote no new template"},
		// the review's counterexample: rolled out, but never restarted
		{"lost and rolled out without its token", "rollout-restart", ActionStepDTO{}, false, 0, 0, tok, restarted("", 5, 5, 3), judgePending, "does not carry"},
		{"lost, its token appears", "rollout-restart", ActionStepDTO{}, false, 0, 0, tok, restarted(tok, 6, 6, 3), judgeEstablished, ""},
		{"another restart's token", "rollout-restart", ActionStepDTO{}, true, 6, 5, tok, restarted(later, 7, 7, 3), judgePending, "does not carry"},
		{"not observed", "rollout-restart", ActionStepDTO{}, true, 6, 5, tok, restarted(tok, 6, 5, 3), judgePending, "not observed the restart"},
		{"old pods remain", "rollout-restart", ActionStepDTO{}, true, 6, 5, tok, restarted(tok, 6, 6, 1), judgePending, "not all replaced"},
		{"rolled out", "rollout-restart", ActionStepDTO{}, true, 6, 5, tok, restarted(tok, 6, 6, 3), judgeEstablished, ""},
		{"scaled", "scale", ActionStepDTO{Replicas: &two}, true, 3, 0, "", deployAt("uid-web", 3, 3, map[string]any{"replicas": int64(2)}, map[string]any{"replicas": int64(2)}), judgeEstablished, ""},
		{"status behind", "scale", ActionStepDTO{Replicas: &two}, true, 3, 0, "", deployAt("uid-web", 3, 3, map[string]any{"replicas": int64(2)}, map[string]any{"replicas": int64(3)}), judgePending, "status.replicas"},
		{"replaced", "scale", ActionStepDTO{Replicas: &two}, true, 3, 0, "", deployAt("uid-other", 3, 3, map[string]any{"replicas": int64(2)}, map[string]any{"replicas": int64(2)}), judgeFailed, "replaced"},
		{"a DaemonSet undo by digest", "rollout-undo", ActionStepDTO{}, true, 3, 0, "", ds(tmpl, 3, 3), judgeEstablished, ""},
		{"a DaemonSet not reverted", "rollout-undo", ActionStepDTO{}, true, 3, 0, "", ds(map[string]any{"spec": map[string]any{}}, 3, 3), judgePending, "partner revision"},
	} {
		t.Run(c.verb+"/"+c.name, func(t *testing.T) {
			st := c.step
			st.Verb = c.verb
			f := webFact
			f.Kind = c.read.GetKind()
			if f.Kind == "Kustomization" {
				f.Group, f.Resource = ksGVR.Group, ksGVR.Resource
			}
			var partner *fleet.RevisionExpect
			if c.verb == "rollout-undo" {
				partner = &fleet.RevisionExpect{ObjectUID: "cr", TemplateDigest: dsDigest}
			}
			w := witnessOf(s, nil, st, f, partner)
			w.written, w.gen, w.prior, w.token = c.written, c.gen, c.prior, c.token
			got, why := w.judge(c.read)
			if got != c.want || !strings.Contains(why, c.why) {
				t.Fatalf("got %d %q", got, why)
			}
		})
	}
}

// The fresh read and the single-object watch (§5.2), on a rollout pause
// whose receipt is generation 5.
func TestVerifyObjectReadsThenWatches(t *testing.T) {
	paused := func(uid string, gen, observed int64, p bool) *unstructured.Unstructured {
		return deployAt(uid, gen, observed, map[string]any{"paused": p}, nil)
	}
	errEv := watch.Event{Type: watch.Error, Object: &metav1.Status{Message: "too old resource version"}}
	for _, c := range []struct {
		name    string
		first   *unstructured.Unstructured
		watches []*watch.FakeWatcher
		fresh   *unstructured.Unstructured // the read after a closed watch
		effect  string
		why     string
		calls   int
	}{
		{"already established", paused("uid-web", 5, 5, true), nil, nil, stepEffectEstablished, "", 0},
		{"established by an event", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(false, mod(paused("uid-web", 5, 5, true)))}, nil, stepEffectEstablished, "", 1},
		{"replaced mid-watch", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(false, mod(paused("uid-new", 1, 1, true)))}, nil, stepEffectUnverified, "the object was replaced", 1},
		{"deleted", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(false, watch.Event{Type: watch.Deleted, Object: paused("uid-web", 5, 4, true)})}, nil, stepEffectUnverified, "the object was deleted", 1},
		{"a 410", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(false, errEv)}, nil, stepEffectUnverified, "the watch expired: too old resource version", 1},
		{"the bound", paused("uid-web", 5, 4, true), nil, nil, stepEffectUnverified, "not established within 60ms: its controller has not observed generation 5", 1},
		{"a third party reverts", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(false, mod(paused("uid-web", 6, 6, false)))}, nil, stepEffectUnverified, "not established within 60ms: spec.paused is false", 1},
		{"closed, then read established", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(true)}, paused("uid-web", 5, 5, true), stepEffectEstablished, "", 1},
		{"closed, then watched again", paused("uid-web", 5, 4, true), []*watch.FakeWatcher{events(true), events(false, mod(paused("uid-web", 5, 5, true)))}, paused("uid-web", 5, 4, true), stepEffectEstablished, "", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastVerify(t, 60*time.Millisecond)
			wc, f := newWatchConn(t)
			wc.watches = c.watches
			f.conn.objects[key(deploysGVR, "shop", "web")] = c.first
			if c.fresh != nil {
				first := true
				wc.getHook = func() {
					if first {
						first = false
						f.conn.objects[key(deploysGVR, "shop", "web")] = c.fresh
					}
				}
			}
			w := witnessOf(f.svc, wc, ActionStepDTO{Verb: "rollout-pause"}, webFact, nil)
			w.written, w.gen = true, 5
			effect, why := f.svc.verifyObject(context.Background(), wc, w, func() error { return nil })
			// the reason leads: a failure ends the watch at once, never at the bound
			if effect != c.effect || !strings.HasPrefix(why, c.why) || len(wc.watched()) != c.calls {
				t.Fatalf("%s %q, watches %v", effect, why, wc.watched())
			}
			if c.calls > 0 && wc.watched()[0] != "deployments shop/web @"+c.first.GetResourceVersion() {
				t.Fatalf("the watch is of the one object, from the read: %v", wc.watched())
			}
		})
	}
}

func TestVerifyObjectStopsAtTheFenceAndTheCap(t *testing.T) {
	fastVerify(t, time.Second)
	pending := deployAt("uid-web", 5, 4, map[string]any{"paused": true}, nil)
	wc, f := newWatchConn(t)
	f.conn.objects[key(deploysGVR, "shop", "web")] = pending
	w := witnessOf(f.svc, wc, ActionStepDTO{Verb: "rollout-pause"}, webFact, nil)
	fenced := func() error { return errors.New("the cluster disconnected") }
	if effect, why := f.svc.verifyObject(context.Background(), wc, w, fenced); effect != stepEffectUnverified || why != "the cluster disconnected" {
		t.Fatalf("a quiet watch still checks the fence: %s %q", effect, why)
	}
	verifyEventsMax = 2
	wc.watches = []*watch.FakeWatcher{events(false, mod(pending), mod(pending), mod(pending))}
	if effect, why := f.svc.verifyObject(context.Background(), wc, w, func() error { return nil }); effect != stepEffectUnverified || why != "too many changes to verify" {
		t.Fatalf("%s %q", effect, why)
	}
	if effect, why := f.svc.verifyObject(context.Background(), wc.stepsConn, w, func() error { return nil }); effect != stepEffectUnverified || !strings.Contains(why, "cannot watch") {
		t.Fatalf("a connection without a watch: %s %q", effect, why)
	}
}

// A KEDA pause completes in the Observer's verdict, which no event of the
// object announces: each tick reads it again, and only a verdict evaluated
// after the write counts.
func TestAKEDAPauseIsVerifiedFromTheObserver(t *testing.T) {
	soGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	so := uobj(map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject", "metadata": map[string]any{"name": "q", "namespace": "shop", "uid": "q-uid", "generation": int64(2), "annotations": map[string]any{"autoscaling.keda.sh/paused": "true"}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Paused", "status": "True"}}}})
	fact := actionFact{Kind: "ScaledObject", Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects", Namespace: "shop", Name: "q", UID: "q-uid"}
	ranAt := fixedNow()
	for _, c := range []struct {
		name   string
		at     time.Time
		effect string
	}{
		{"evaluated after the write", ranAt.Add(time.Second), stepEffectEstablished},
		{"evaluated before it", ranAt.Add(-time.Second), stepEffectUnverified},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastVerify(t, 200*time.Millisecond)
			wc, f := newWatchConn(t)
			f.conn.objects[key(soGVR, "shop", "q")] = so
			var mu sync.Mutex
			reads := 0
			prev := verdictOfHook
			verdictOfHook = func(actionFact) subjectVerdict {
				mu.Lock()
				defer mu.Unlock()
				reads++
				v := chatVerdict{Role: "paused", Coverage: "live", Generation: 2, EvaluatedAt: c.at.UTC().Format(time.RFC3339)}
				return subjectVerdict{v: v, found: true, state: "paused", completed: reads > 2} // completes a few ticks later
			}
			t.Cleanup(func() { verdictOfHook = prev })
			st := ActionStepDTO{Verb: "keda-pause"}
			w := &witness{sn: stepSnap{step: st, fact: fact}, written: true}
			w.holds = func(u *unstructured.Unstructured) (bool, string) {
				return f.svc.stateEffect(context.Background(), wc, st, fact, nil, u, ranAt)
			}
			effect, why := f.svc.verifyObject(context.Background(), wc, w, func() error { return nil })
			if effect != c.effect {
				t.Fatalf("%s %q", effect, why)
			}
			if c.effect == stepEffectUnverified && !strings.Contains(why, "evaluated before the write") {
				t.Fatalf("%q", why)
			}
		})
	}
}

// delete-pod's witness (§5.2): one unfiltered watch of the namespace from
// the LIST taken before the delete.
func TestTheDeletionsWitness(t *testing.T) {
	controller := true
	pod := func(uid, ownerKind, ownerUID string) *unstructured.Unstructured {
		p := uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-" + uid, "namespace": "shop", "uid": uid}})
		if ownerUID != "" {
			p.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: ownerKind, Name: "web", UID: types.UID(ownerUID), Controller: &controller}})
		}
		return p
	}
	added := func(u *unstructured.Unstructured) watch.Event { return watch.Event{Type: watch.Added, Object: u} }
	deleted := func(u *unstructured.Unstructured) watch.Event { return watch.Event{Type: watch.Deleted, Object: u} }
	old := pod("old", "ReplicaSet", "rs")
	for _, c := range []struct {
		name   string
		owner  [2]string
		evs    []watch.Event
		effect string
		why    string
	}{
		{"a replacement created", [2]string{"ReplicaSet", "rs"}, []watch.Event{deleted(old), added(pod("new", "ReplicaSet", "rs"))}, stepEffectEstablished, ""},
		{"created before the old one goes", [2]string{"ReplicaSet", "rs"}, []watch.Event{added(pod("new", "ReplicaSet", "rs")), deleted(old)}, stepEffectEstablished, ""},
		// the re-review's counterexample: an existing pod whose labels change
		// to match arrives as MODIFIED, never ADDED, on an unfiltered watch
		{"an existing pod relabelled", [2]string{"ReplicaSet", "rs"}, []watch.Event{deleted(old), mod(pod("existing", "ReplicaSet", "rs"))}, stepEffectUnverified, "no replacement of its ReplicaSet was created"},
		{"a pod adopted", [2]string{"ReplicaSet", "rs"}, []watch.Event{mod(pod("orphan", "ReplicaSet", "rs")), deleted(old)}, stepEffectUnverified, "no replacement"},
		{"another owner's pod", [2]string{"ReplicaSet", "rs"}, []watch.Event{deleted(old), added(pod("other", "ReplicaSet", "rs-2"))}, stepEffectUnverified, "no replacement"},
		{"a replacement deleted again", [2]string{"ReplicaSet", "rs"}, []watch.Event{added(pod("new", "ReplicaSet", "rs")), deleted(pod("new", "ReplicaSet", "rs")), deleted(old)}, stepEffectUnverified, "no replacement"},
		{"and another one created", [2]string{"ReplicaSet", "rs"}, []watch.Event{added(pod("new", "ReplicaSet", "rs")), deleted(pod("new", "ReplicaSet", "rs")), deleted(old), added(pod("new2", "ReplicaSet", "rs"))}, stepEffectEstablished, ""},
		{"not deleted", [2]string{"ReplicaSet", "rs"}, []watch.Event{added(pod("new", "ReplicaSet", "rs"))}, stepEffectUnverified, "the pod's UID is not gone"},
		{"a Job's pod", [2]string{"Job", "job"}, []watch.Event{deleted(old)}, stepEffectEstablished, "its Job's recreation is not established"},
		{"an ownerless pod", [2]string{"", ""}, []watch.Event{deleted(old)}, stepEffectEstablished, "no controller owns it: it is not recreated"},
		{"a 410", [2]string{"ReplicaSet", "rs"}, []watch.Event{{Type: watch.Error, Object: &metav1.Status{Message: "too old"}}}, stepEffectUnverified, "the watch expired: too old"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastVerify(t, 60*time.Millisecond)
			wc, f := newWatchConn(t)
			wc.watches = []*watch.FakeWatcher{events(false, c.evs...)}
			fact := actionFact{Kind: "Pod", Version: "v1", Resource: "pods", Namespace: "shop", Name: "web-old", UID: "old", Object: &objectFacts{OwnerKind: c.owner[0], OwnerUID: c.owner[1]}}
			effect, why := f.svc.verifyDeletion(context.Background(), wc, stepSnap{step: ActionStepDTO{Verb: "delete-pod"}, fact: fact, podsRV: "100"}, func() error { return nil })
			if effect != c.effect || !strings.Contains(why, c.why) || (c.why == "" && why != "") {
				t.Fatalf("%s %q", effect, why)
			}
			if w := wc.watched(); len(w) != 1 || w[0] != "pods shop/ @100" {
				t.Fatalf("one unfiltered watch of the namespace from the LIST: %v", w)
			}
		})
	}
}

func TestTheDeletionsWitnessIsBounded(t *testing.T) {
	fastVerify(t, time.Second)
	wc, f := newWatchConn(t)
	fact := actionFact{Kind: "Pod", Namespace: "shop", Name: "web-old", UID: "old", Object: &objectFacts{OwnerKind: "ReplicaSet", OwnerUID: "rs"}}
	sn := stepSnap{step: ActionStepDTO{Verb: "delete-pod"}, fact: fact, podsRV: "100"}
	if effect, why := f.svc.verifyDeletion(context.Background(), wc, stepSnap{fact: fact}, func() error { return nil }); effect != stepEffectUnverified || !strings.Contains(why, "cannot watch") {
		t.Fatalf("without the LIST's resourceVersion: %s %q", effect, why)
	}
	verifyEventsMax = 2
	other := uobj(map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "x", "namespace": "shop", "uid": "x"}})
	wc.watches = []*watch.FakeWatcher{events(false, mod(other), mod(other), mod(other))}
	if effect, why := f.svc.verifyDeletion(context.Background(), wc, sn, func() error { return nil }); effect != stepEffectUnverified || !strings.Contains(why, "too busy") {
		t.Fatalf("%s %q", effect, why)
	}
	if effect, why := f.svc.verifyDeletion(context.Background(), wc, sn, func() error { return errors.New("AI settings changed") }); effect != stepEffectUnverified || why != "AI settings changed" {
		t.Fatalf("a quiet watch checks the fence: %s %q", effect, why)
	}
}

// waitFor polls cond until it holds or a second passes.
func awaitSettled(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// pinWatchConn makes a steps fixture's pinned connection a watchConn.
func pinWatchConn(sf *stepsFixture) *watchConn {
	wc := &watchConn{stepsConn: sf.sc}
	sf.mu.Lock()
	sf.cur = wc
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	sf.svc.convs[invKey("x")].conn = wc
	sf.svc.mu.Unlock()
	return wc
}

func (sf *stepsFixture) step(n int) ActionStepDTO {
	for _, e := range sf.svc.InvestigationState("x").Exchanges {
		if e.ID == sf.ex && e.Plan != nil {
			return e.Plan.Steps[n-1]
		}
	}
	return ActionStepDTO{}
}

// quietIncident makes a plan's incident settle at its first reading, from
// no verdicts, so no reading outlives its test.
func quietIncident(t *testing.T) {
	incidentBound = 0
	prev := verdictOfHook
	verdictOfHook = func(actionFact) subjectVerdict { return subjectVerdict{} }
	t.Cleanup(func() { verdictOfHook = prev })
}

// incidentSettled: the plan's incident is read and ended.
func (sf *stepsFixture) incidentSettled() func() bool {
	return func() bool {
		for _, e := range sf.svc.InvestigationState("x").Exchanges {
			if e.ID == sf.ex && e.Plan != nil && e.Plan.Incident != nil {
				return e.Plan.Incident.State != incidentPending
			}
		}
		return false
	}
}

func (sf *stepsFixture) settled(n int) func() bool {
	return func() bool {
		r := sf.step(n).Run
		return r != nil && r.Effect != stepEffectPending
	}
}

// End to end: RunStep answers with the request's outcome; verification
// settles the effect in the background, releases the conversation's
// running step, and words a lost answer's effect as §1 says.
func TestARunStepIsVerifiedInTheBackground(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		request string
		why     string
	}{
		{"written", nil, fleet.OutcomeWritten, ""},
		{"a lost answer", errors.New("http2: stream reset"), fleet.OutcomeUnverified, "the requested state, by this request or another writer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastVerify(t, time.Second)
			quietIncident(t)
			sf := newStepsFixture(t, pauseWeb)
			wc := pinWatchConn(sf)
			sf.sc.err = c.err
			wc.watches = []*watch.FakeWatcher{events(false, mod(deployAt("uid-web", 5, 5, map[string]any{"replicas": int64(3), "paused": true}, nil)))}
			r := sf.run(1, sf.review(1).Token)
			if !r.Ran || r.Result.Outcome != c.request || r.Plan.Steps[0].Run.Effect != stepEffectPending {
				t.Fatalf("%+v", r)
			}
			awaitSettled(t, "the effect", sf.settled(1))
			run := sf.step(1).Run
			if run.Effect != stepEffectEstablished || run.EffectWhy != c.why {
				t.Fatalf("%+v", run)
			}
			sf.svc.mu.Lock()
			running := sf.svc.convs[invKey("x")].stepRunning
			sf.svc.mu.Unlock()
			if running != nil {
				t.Fatal("the conversation's running step is released")
			}
			awaitSettled(t, "the incident", sf.incidentSettled())
		})
	}
}

// The deletion's LIST is read before the delete, and its resourceVersion
// is where the witness's watch starts.
func TestADeletionListsItsNamespaceFirst(t *testing.T) {
	fastVerify(t, time.Second)
	quietIncident(t)
	sf := newStepsFixture(t, pauseWeb)
	wc := pinWatchConn(sf)
	controller := true
	p := uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-1", "namespace": "shop", "uid": "uid-pod"}})
	p.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-r", UID: "rs", Controller: &controller}})
	sf.conn.objects[key(podsGVRv1, "shop", "web-1")] = p
	l := &unstructured.UnstructuredList{}
	l.SetResourceVersion("777")
	sf.conn.lists[podsGVRv1.String()+"|shop"] = l
	fact := factOf(podsGVRv1, p, "r1", "t", acquisition())
	sf.plantPlan([]ActionStepDTO{{N: 1, Verb: "delete-pod", State: stepOffered, Danger: true, Target: ChatCheckSubject{Kind: "Pod", Namespace: "shop", Name: "web-1", UID: "uid-pod"}}}, []actionFact{fact}, []*fleet.RevisionExpect{nil})
	replacement := uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-2", "namespace": "shop", "uid": "uid-new"}})
	replacement.SetOwnerReferences(p.GetOwnerReferences())
	sf.sc.err = errors.New("http2: stream reset") // a lost DELETE answer
	wc.watches = []*watch.FakeWatcher{events(false, watch.Event{Type: watch.Deleted, Object: p}, watch.Event{Type: watch.Added, Object: replacement})}
	rv := sf.review(1)
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatalf("%+v %+v", rv, r)
	}
	awaitSettled(t, "the effect", sf.settled(1))
	if run := sf.step(1).Run; run.Effect != stepEffectEstablished || run.EffectWhy != "the pod was removed; by this request or another actor" {
		t.Fatalf("%+v", run)
	}
	if w := wc.watched(); len(w) != 1 || w[0] != "pods shop/ @777" {
		t.Fatalf("%v", w)
	}
	awaitSettled(t, "the incident", sf.incidentSettled())
}

// §5.3's table, row by row, from the Observer's verdicts by UID.
func TestTheIncidentTable(t *testing.T) {
	last := fixedNow()
	fresh := last.Add(time.Second).UTC().Format(time.RFC3339)
	stale := last.Add(-time.Second).UTC().Format(time.RFC3339)
	sub := func(uid string, held, removed int) incidentSubject {
		return incidentSubject{fact: actionFact{Kind: "Deployment", Resource: "deployments", Namespace: "shop", Name: uid, UID: uid}, held: held, removedBy: removed}
	}
	v := func(state, at, coverage string) subjectVerdict {
		return subjectVerdict{v: chatVerdict{Role: state, EvaluatedAt: at, Coverage: coverage}, found: true, state: state}
	}
	verdicts := map[string]subjectVerdict{
		"ok": v("ok", fresh, "live"), "held": v("paused", fresh, "live"), "paused": v("paused", fresh, "live"),
		"progressing": v("progressing", fresh, "live"), "fault": v("fault", fresh, "live"), "degraded": v("degraded", fresh, "live"),
		"unknown": v("unknown", fresh, "live"), "stale": v("ok", stale, "live"), "coverage": v("ok", fresh, "stale"),
		"gone": {gone: true}, "replaced": {replaced: true}, "removed": v("ok", fresh, "live"),
	}
	look := func(f actionFact) subjectVerdict { return verdicts[f.UID] }
	for _, c := range []struct {
		sub     incidentSubject
		reading string
		open    bool
		faulted bool
	}{
		{sub("ok", 0, 0), "recovered", false, false},
		{sub("held", 2, 0), "held by step 2, as intended", false, false},
		{sub("paused", 0, 0), "unverified: paused outside this plan", true, false},
		{sub("progressing", 0, 0), "pending: still progressing", true, true},
		{sub("fault", 0, 0), "pending: fault, a failure in force", true, true},
		{sub("degraded", 0, 0), "pending: degraded, a failure in force", true, true},
		{sub("unknown", 0, 0), "pending: unknown", true, false},
		{sub("stale", 0, 0), "pending: no fresh reading", true, false},
		{sub("coverage", 0, 0), "pending: no fresh reading", true, false},
		{sub("missing", 0, 0), "pending: no fresh reading", true, false},
		{sub("gone", 0, 0), "unverified: gone", true, false},
		{sub("replaced", 0, 0), "unverified: replaced", true, false},
		{sub("removed", 0, 1), "removed by step 1: it cannot itself recover; its replacement is shown on its own", true, false},
		{incidentSubject{fact: actionFact{Kind: "the investigated object", UID: "x"}}, "unverified: Klyx holds no read of it", true, false},
	} {
		rows, recovered, open, faulted := readSubjects(look, []incidentSubject{c.sub}, last)
		if rows[0].Reading != c.reading || (open == 1) != c.open || faulted != c.faulted || (recovered == 1) != (c.reading == "recovered") {
			t.Errorf("%s: %+v recovered %d open %d faulted %v", c.sub.fact.UID, rows[0], recovered, open, faulted)
		}
	}
}

// The incident's outcome (§5.3): resolved as soon as every subject is
// recovered or held and one recovered; at the bound, not resolved on a
// fresh failure, else unverified; a fence ends it unverified.
func TestTheIncidentsOutcome(t *testing.T) {
	last := fixedNow()
	fresh := last.Add(time.Second).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name   string
		states map[string]string // uid to the verdict's state, "" for none
		held   string
		fence  bool
		want   string
	}{
		{"recovered", map[string]string{"a": "ok"}, "", false, incidentResolved},
		{"recovered and held", map[string]string{"a": "ok", "b": "paused"}, "b", false, incidentResolved},
		// a scale to zero or a suspension is held, never recovered
		{"held only", map[string]string{"b": "paused"}, "b", false, incidentUnverif},
		{"a degraded subject", map[string]string{"a": "ok", "b": "degraded"}, "", false, incidentNot},
		{"no fresh reading", map[string]string{"a": "ok", "b": ""}, "", false, incidentUnverif},
		{"fenced", map[string]string{"a": "ok"}, "", true, incidentUnverif},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastVerify(t, time.Second)
			incidentBound = 0 // the first reading is at the bound
			sf := newStepsFixture(t, pauseWeb)
			prev := verdictOfHook
			verdictOfHook = func(f actionFact) subjectVerdict {
				st := c.states[f.UID]
				if st == "" {
					return subjectVerdict{}
				}
				return subjectVerdict{v: chatVerdict{Role: st, EvaluatedAt: fresh, Coverage: "live"}, found: true, state: st}
			}
			t.Cleanup(func() { verdictOfHook = prev })
			var subjects []incidentSubject
			for uid := range c.states {
				s := incidentSubject{fact: actionFact{Kind: "Deployment", Resource: "deployments", Namespace: "shop", Name: uid, UID: uid}}
				if uid == c.held {
					s.held = 1
				}
				subjects = append(subjects, s)
			}
			if c.fence {
				sf.mu.Lock()
				sf.usable = false
				sf.mu.Unlock()
			}
			sf.svc.mu.Lock()
			conv := sf.svc.convs[invKey("x")]
			p := conv.exchanges[0].plan
			p.incident = &IncidentDTO{State: incidentPending}
			sf.svc.mu.Unlock()
			sf.svc.readIncident(conv, p, subjects, last)
			sf.svc.mu.Lock()
			got := *p.incident
			sf.svc.mu.Unlock()
			if got.State != c.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// The incident's subjects (§5.3): the anchor and each step's target
// Klyx's checks found failing, by UID; a step's own hold or removal is
// recorded on its subject.
func TestTheIncidentsSubjects(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	zero := 0
	webF := factOf(deploysGVR, sf.web(), "r1", "t", acquisition())
	podF := actionFact{Kind: "Pod", Version: "v1", Resource: "pods", Namespace: "shop", Name: "p", UID: "uid-pod"}
	ksF := actionFact{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations", Namespace: "flux-system", Name: "web", UID: "uid-ks"}
	steps := []ActionStepDTO{{N: 1, Verb: "flux-suspend", State: stepOffered}, {N: 2, Verb: "scale", Replicas: &zero, State: stepOffered}, {N: 3, Verb: "delete-pod", State: stepOffered}}
	sf.plantPlan(steps, []actionFact{ksF, webF, podF}, []*fleet.RevisionExpect{nil, nil, nil})
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	for _, r := range p.runs {
		r.request = fleet.OutcomeWritten
	}
	// the pod is a target Klyx's checks found failing; the Kustomization is not
	c.exchanges[0].reads = append(c.exchanges[0].reads, ChatRead{ID: "r9", Checks: []ChatCheck{{Failing: true, Subject: ChatCheckSubject{UID: "uid-pod"}}}})
	got := incidentSubjectsLocked(c, p)
	if len(got) != 2 || got[0].fact.UID != "uid-web" || got[0].held != 2 || got[1].fact.UID != "uid-pod" || got[1].removedBy != 3 {
		t.Fatalf("the anchor held by its scale to zero, the failing pod removed by step 3: %+v", got)
	}
}

// §5.4: the next question carries Klyx's record of the steps the operator
// ran, in the outcome words alone - never an error or a reason.
func TestThePlanRecordReachesTheNextQuestion(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		s.settleEffect(c, p, n, stepEffectUnverified, "needle-in-the-reason")
	}
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb)
	sf.sc.err = errors.New("needle-in-the-error")
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.prov.turns = append(sf.prov.turns, answer("noted"))
	sf.svc.Ask("x", sf.conv, "what now?", nil)
	sf.prov.mu.Lock()
	req := sf.prov.reqs[len(sf.prov.reqs)-1]
	sf.prov.mu.Unlock()
	b, _ := json.Marshal(req)
	sent := string(b)
	want := "turn 1's plan, step 1, rollout-pause Deployment shop/web: request unverified; effect unverified"
	if !strings.Contains(sent, "\\u003cklyx-plan-record\\u003e") || !strings.Contains(sent, want) || !strings.Contains(sent, "you ran nothing") {
		t.Fatalf("the record: %s", sent)
	}
	for _, needle := range []string{"needle-in-the-reason", "needle-in-the-error"} {
		if strings.Contains(sent, needle) {
			t.Fatalf("%s reached the model", needle)
		}
	}
	_ = ai.ConvRequest{}
}

// §2.4: a fence ends a running verification "unverified" with its reason,
// and releases the conversation's running step.
func TestAFenceEndsARunningVerification(t *testing.T) {
	fastVerify(t, 5*time.Second)
	quietIncident(t)
	sf := newStepsFixture(t, pauseWeb)
	pinWatchConn(sf) // its watches stay quiet
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.mu.Lock()
	sf.usable = false
	sf.mu.Unlock()
	awaitSettled(t, "the effect", sf.settled(1))
	if run := sf.step(1).Run; run.Effect != stepEffectUnverified || !strings.Contains(run.EffectWhy, "disconnected") {
		t.Fatalf("%+v", run)
	}
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	released := c.stepRunning == nil && c.exchanges[0].plan.incident == nil
	sf.svc.mu.Unlock()
	if !released {
		t.Fatal("released, and no incident follows an unverified effect")
	}
	sf.mu.Lock()
	sf.usable = true
	sf.mu.Unlock()
	if err := sf.svc.verifyFence(&conversation{key: c.key, cluster: c.cluster, conn: c.conn})(); err == nil || !strings.Contains(err.Error(), "cleared") {
		t.Fatalf("a conversation no longer the cluster's: %v", err)
	}
}

// The incident is read only once every offered step ran with its effect
// established.
func TestTheIncidentWaitsForEveryOfferedStep(t *testing.T) {
	quietIncident(t)
	sf := newStepsFixture(t, pauseWeb, scaleWeb)
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	p.runs[0].request, p.runs[0].effect = fleet.OutcomeWritten, stepEffectEstablished
	sf.svc.mu.Unlock()
	sf.svc.maybeIncident(c, p)
	sf.svc.mu.Lock()
	started := p.incident != nil
	p.runs[1].request, p.runs[1].effect = fleet.OutcomeWritten, stepEffectEstablished
	sf.svc.mu.Unlock()
	if started {
		t.Fatal("step 2 has not run")
	}
	sf.svc.maybeIncident(c, p)
	awaitSettled(t, "the incident", sf.incidentSettled())
}

// The Observer's capture tells gone from replaced (§5.3): only a live,
// complete part can say a UID is gone; another UID at its name is a
// replacement; a capped part says neither.
func TestTheCaptureTellsGoneFromReplaced(t *testing.T) {
	pod := actionFact{Kind: "Pod", Version: "v1", Resource: "pods", Namespace: "shop", Name: "web", UID: "uid-old"}
	other := actionFact{Kind: "Pod", Version: "v1", Resource: "pods", Namespace: "shop", Name: "elsewhere", UID: "uid-gone"}
	for _, c := range []struct {
		name              string
		rows              observe.Rows
		f                 actionFact
		found, gone, repl bool
	}{
		{"found", podVerdictRows("uid-old", 1, observe.Live), pod, true, false, false},
		{"replaced at its name", podVerdictRows("uid-new", 1, observe.Live), pod, false, false, true},
		{"gone", podVerdictRows("uid-new", 1, observe.Live), other, false, true, false},
		{"a stale part says neither", podVerdictRows("uid-new", 1, observe.Stale), pod, false, false, false},
		{"a capped part says neither", capped(podVerdictRows("uid-new", 1, observe.Live)), pod, false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newToolsFixture(t)
			f.conn.rows = map[string]observe.Rows{"pods/pods": c.rows}
			sv := f.svc.verdictOf(f.conn, c.f, nil)
			if sv.found != c.found || sv.gone != c.gone || sv.replaced != c.repl {
				t.Fatalf("%+v", sv)
			}
			if c.found && sv.state != "fault" {
				t.Fatalf("a verdict part's state is its Kind: %+v", sv)
			}
		})
	}
}

func capped(r observe.Rows) observe.Rows {
	r.Part.Kinds = []observe.KindCoverage{{Kind: "pods", Truncated: true, Objects: 5000}}
	return r
}

// A lost answer's witness is its attempt's token (§5.2): a late commit of
// this request's reconcile token, handled, establishes the effect.
func TestALostAnswerIsWitnessedByItsAttemptsToken(t *testing.T) {
	fastVerify(t, time.Second)
	quietIncident(t)
	tok := "2026-10-14T12:00:00.000000007Z"
	for _, c := range []struct {
		name    string
		handled string
		effect  string
	}{
		{"its token handled", tok, stepEffectEstablished},
		{"an older token handled", "2026-10-14T11:00:00Z", stepEffectUnverified},
	} {
		t.Run(c.name, func(t *testing.T) {
			sf := newStepsFixture(t, pauseWeb)
			pinWatchConn(sf)
			ks := uobj(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "web", "namespace": "flux-system", "uid": "uid-ks", "generation": int64(2), "annotations": map[string]any{"reconcile.fluxcd.io/requestedAt": c.handled}},
				"spec": map[string]any{}, "status": map[string]any{"lastHandledReconcileAt": c.handled}})
			sf.conn.objects[key(ksGVR, "flux-system", "web")] = ks
			fact := factOf(ksGVR, ks, "r1", "t", acquisition())
			st := ActionStepDTO{N: 1, Verb: "flux-reconcile", State: stepOffered}
			sf.plantPlan([]ActionStepDTO{st}, []actionFact{fact}, []*fleet.RevisionExpect{nil})
			sf.svc.mu.Lock()
			c2 := sf.svc.convs[invKey("x")]
			p := c2.exchanges[0].plan
			p.runs[0].request, p.runs[0].effect, p.running, c2.stepRunning = fleet.OutcomeUnverified, stepEffectPending, 1, p
			sf.svc.mu.Unlock()
			res := ActionResultDTO{Outcome: fleet.OutcomeUnverified, Attempt: &AttemptDTO{UID: "uid-ks", Token: tok}}
			sf.svc.verifyStepEffect(c2, p, 1, stepSnap{n: 1, step: st, fact: fact}, res, sf.now)
			sf.svc.mu.Lock()
			got := p.runs[0].effect
			sf.svc.mu.Unlock()
			if got != c.effect {
				t.Fatalf("%s", got)
			}
			awaitSettled(t, "the incident", func() bool {
				sf.svc.mu.Lock()
				defer sf.svc.mu.Unlock()
				return p.incident == nil || p.incident.State != incidentPending
			})
		})
	}
}
