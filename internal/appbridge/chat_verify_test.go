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
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
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
	w.holds = func(ctx context.Context, u *unstructured.Unstructured) (bool, string) {
		return s.stateEffect(ctx, conn, st, f, partner, u, time.Time{})
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
			got, why := w.judge(context.Background(), c.read)
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
				return subjectVerdict{v: v, found: true, state: "paused", completed: reads > 2, at: c.at} // completes a few ticks later
			}
			t.Cleanup(func() { verdictOfHook = prev })
			st := ActionStepDTO{Verb: "keda-pause"}
			w := &witness{sn: stepSnap{step: st, fact: fact}, written: true}
			w.holds = func(ctx context.Context, u *unstructured.Unstructured) (bool, string) {
				return f.svc.stateEffect(ctx, wc, st, fact, nil, u, ranAt)
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
	prevBound, prev := incidentBound, verdictOfHook
	incidentBound = 0
	verdictOfHook = func(actionFact) subjectVerdict { return subjectVerdict{} }
	t.Cleanup(func() { incidentBound, verdictOfHook = prevBound, prev })
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
	fresh, stale := last.Add(500*time.Millisecond), last.Add(-time.Second)
	zero := 0
	sub := func(uid string, held, removed int) incidentSubject {
		s := incidentSubject{fact: actionFact{Kind: "Deployment", Resource: "deployments", Namespace: "shop", Name: uid, UID: uid}, held: held, removedBy: removed}
		if held > 0 {
			s.holdStep = ActionStepDTO{N: held, Verb: "scale", Replicas: &zero}
		}
		return s
	}
	v := func(state string, at time.Time, coverage string) subjectVerdict {
		return subjectVerdict{v: chatVerdict{Role: state, EvaluatedAt: at.UTC().Format(time.RFC3339), Coverage: coverage}, found: true, state: state, at: at, captured: at}
	}
	scaledToZero := v("paused", fresh, "live")
	scaledToZero.v.Reason = "ScaledToZero"
	verdicts := map[string]subjectVerdict{
		"ok": v("ok", fresh, "live"), "held": scaledToZero, "paused": v("paused", fresh, "live"), "held otherwise": v("paused", fresh, "live"),
		"progressing": v("progressing", fresh, "live"), "fault": v("fault", fresh, "live"), "degraded": v("degraded", fresh, "live"),
		"unknown": v("unknown", fresh, "live"), "stale": v("ok", stale, "live"), "coverage": v("ok", fresh, "stale"),
		"gone": {gone: true, at: fresh, captured: fresh}, "replaced": {replaced: true, at: fresh, captured: fresh}, "gone before": {gone: true, at: stale, captured: stale}, "replaced before": {replaced: true, at: stale, captured: stale},
		"removed": v("ok", fresh, "live"),
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
		// stage 4 review, finding 5: a pause that is not the step's own hold
		{sub("held otherwise", 2, 0), "unverified: paused outside this plan", true, false},
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
		// stage 4 review, finding 3: an absence from before the write is no
		// fresh reading
		{sub("gone before", 0, 0), "pending: no fresh reading", true, false},
		{sub("replaced before", 0, 0), "pending: no fresh reading", true, false},
		{sub("removed", 0, 1), "removed by step 1: it cannot itself recover; its replacement is shown on its own", true, false},
		{incidentSubject{fact: actionFact{Kind: "Pod", Resource: "pods", UID: "removed"}, removedBy: 1, removedLost: true}, "removed, by step 1's request or another actor: it cannot itself recover; its replacement is shown on its own", true, false},
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
// fresh failure, else unverified; a fence ends it unverified. The bound is
// decided on the last reading completed within it: here the clock passes
// the bound during the second reading, which is not counted.
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
			incidentBound = time.Hour
			sf := newStepsFixture(t, pauseWeb)
			clock := boundClock(sf, last, len(c.states))
			prev := verdictOfHook
			verdictOfHook = func(f actionFact) subjectVerdict {
				clock()
				st := c.states[f.UID]
				if st == "" {
					return subjectVerdict{}
				}
				sv := subjectVerdict{v: chatVerdict{Role: st, EvaluatedAt: fresh, Coverage: "live"}, found: true, state: st, at: last.Add(time.Second), captured: last.Add(time.Second)}
				if st == "paused" {
					sv.v.Reason = "ScaledToZero"
				}
				return sv
			}
			t.Cleanup(func() { verdictOfHook = prev })
			var subjects []incidentSubject
			for uid := range c.states {
				s := incidentSubject{fact: actionFact{Kind: "Deployment", Resource: "deployments", Namespace: "shop", Name: uid, UID: uid}}
				if uid == c.held {
					zero := 0
					s.held, s.holdStep = 1, ActionStepDTO{N: 1, Verb: "scale", Replicas: &zero}
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

// boundClock is the service clock for an incident test: the bound is open
// for the first reading of n subjects, and passes at the first lookup of
// the second, so that reading runs into it. Its tick is called per lookup.
func boundClock(sf *stepsFixture, last time.Time, n int) func() {
	var mu sync.Mutex
	now, calls := last.Add(time.Second), 0
	sf.svc.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if calls++; calls == n+1 {
			now = last.Add(incidentBound)
		}
	}
}

// The lab's undo-only case (stage 6): the investigated workload read
// faulted for minutes, and the reading made at the bound failed for the
// bound's reason alone. A reading that runs into the bound is not counted,
// whatever it read; the bound is decided on the last one within it, and a
// fault seen there is not resolved. With no reading within it, unverified.
func TestTheBoundIsDecidedOnTheLastReadingWithinIt(t *testing.T) {
	last := fixedNow()
	fault := subjectVerdict{v: chatVerdict{Role: "degraded", EvaluatedAt: last.Add(time.Second).UTC().Format(time.RFC3339), Coverage: "live"}, found: true, state: "degraded", at: last.Add(time.Second), captured: last.Add(time.Second)}
	ok := fault
	ok.v.Role, ok.state = "ok", "ok"
	for _, c := range []struct {
		name          string
		first, second subjectVerdict // the second runs into the bound
		within        bool           // the first completes within the bound
		want, why     string
	}{
		{"its lookups failed at the bound", fault, subjectVerdict{}, true, incidentNot, "a subject is not recovered 1h0m0s after the last step"},
		{"it would have resolved", fault, ok, true, incidentNot, "a subject is not recovered 1h0m0s after the last step"},
		{"none within the bound", ok, ok, false, incidentUnverif, "Klyx completed no reading within the bound"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fastVerify(t, time.Second)
			incidentBound = time.Hour
			sf := newStepsFixture(t, pauseWeb)
			n := 1
			if !c.within {
				n = 0 // the bound passes at the first lookup
			}
			clock := boundClock(sf, last, n)
			readings := 0
			prev := verdictOfHook
			verdictOfHook = func(actionFact) subjectVerdict {
				clock()
				if readings++; readings == 1 {
					return c.first
				}
				return c.second
			}
			t.Cleanup(func() { verdictOfHook = prev })
			subjects := []incidentSubject{{fact: actionFact{Kind: "Deployment", Resource: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}}}
			sf.svc.mu.Lock()
			conv := sf.svc.convs[invKey("x")]
			p := conv.exchanges[0].plan
			p.incident = &IncidentDTO{State: incidentPending}
			sf.svc.mu.Unlock()
			sf.svc.readIncident(conv, p, subjects, last)
			sf.svc.mu.Lock()
			got := *p.incident
			sf.svc.mu.Unlock()
			if got.State != c.want || got.Why != c.why {
				t.Fatalf("%+v", got)
			}
			if c.within && (len(got.Subjects) != 1 || got.Subjects[0].Verdict != "degraded") {
				t.Fatalf("the rows are the last reading within the bound: %+v", got.Subjects)
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
			sv := f.svc.verdictOf(context.Background(), f.conn, c.f, nil)
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

// ---- stage 4 review: the regressions ----

// capturedWorkloads is a live workloads capture evaluated at at.
func capturedWorkloads(at time.Time, ws ...workloads.Workload) observe.Rows {
	r := observe.Rows{Task: "workloads", EvaluatedAt: at, CapturedAt: at, Part: observe.Part{ID: "workloads", Classifier: "verdict", Coverage: observe.Live}}
	var rows []healthsrc.Row[workloads.Workload]
	for _, w := range ws {
		rows = append(rows, healthsrc.Row[workloads.Workload]{Ref: healthsrc.RowRef{UID: string(w.UID), GVR: "apps/v1/deployments", Namespace: w.Namespace, Name: w.Name, Generation: w.Generation}, Data: w})
	}
	r.Rows = healthsrc.RowSet[workloads.Workload]{Rows: rows}
	return r
}

// blockingGet holds every read until its context ends.
type blockingGet struct {
	ChatConn
	entered chan context.Context
	release chan struct{} // as blockingRows's
}

func (c *blockingGet) ChatGet(ctx context.Context, _ schema.GroupVersionResource, _, _ string) (*unstructured.Unstructured, error) {
	select {
	case c.entered <- ctx:
	default:
	}
	<-ctx.Done()
	if c.release != nil {
		<-c.release
	}
	return nil, ctx.Err()
}

// blockingRows holds the Observer's capture until its context ends.
type blockingRows struct {
	ChatConn
	entered chan context.Context
	// release, when set, holds a cancelled read until it closes: the
	// caller then sees the read end only after what the test did meanwhile
	release chan struct{}
}

func (c *blockingRows) HealthRowsNow(ctx context.Context, _, _ string) (observe.Rows, bool) {
	select {
	case c.entered <- ctx:
	default:
	}
	<-ctx.Done()
	if c.release != nil {
		<-c.release
	}
	return observe.Rows{}, false
}

// Finding 1: every read waits under the verification's bound, from before
// the first one, and a cleared conversation ends a blocked Observer read.
func TestEveryReadIsBoundedAndEndsWithTheConversation(t *testing.T) {
	fastVerify(t, 30*time.Millisecond)
	wc, f := newWatchConn(t)
	bg := &blockingGet{ChatConn: wc, entered: make(chan context.Context, 1)}
	w := witnessOf(f.svc, bg, ActionStepDTO{Verb: "rollout-pause"}, webFact, nil)
	start := time.Now()
	effect, why := f.svc.verifyObject(context.Background(), bg, w, func() error { return nil })
	ctx := <-bg.entered
	if _, ok := ctx.Deadline(); !ok || effect != stepEffectUnverified || !strings.HasPrefix(why, "not established within") || time.Since(start) > time.Second {
		t.Fatalf("%s %q after %s", effect, why, time.Since(start))
	}

	sf := newStepsFixture(t, pauseWeb)
	br := &blockingRows{ChatConn: sf.sc, entered: make(chan context.Context, 1)}
	sf.mu.Lock()
	sf.cur = br
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	c.conn = br
	p := c.exchanges[0].plan
	p.incident = &IncidentDTO{State: incidentPending}
	sf.svc.mu.Unlock()
	done := make(chan struct{})
	go func() { sf.svc.readIncident(c, p, []incidentSubject{{fact: webFact}}, sf.now); close(done) }()
	read := <-br.entered
	sf.svc.Clear("x", sf.conv)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a cleared conversation still waits on the Observer")
	}
	if read.Err() == nil || p.incident.State != incidentUnverif || !strings.Contains(p.incident.Why, "cleared") {
		t.Fatalf("%v %+v", read.Err(), p.incident)
	}
}

// Finding 2: a fence that fails during a successful read publishes
// nothing positive, and a settle under a failed fence is unverified.
func TestAFenceDuringASuccessfulReadPublishesNothing(t *testing.T) {
	fastVerify(t, time.Second)
	wc, f := newWatchConn(t)
	f.conn.objects[key(deploysGVR, "shop", "web")] = deployAt("uid-web", 5, 5, map[string]any{"paused": true}, nil)
	valid := true
	wc.getHook = func() { valid = false }
	w := witnessOf(f.svc, wc, ActionStepDTO{Verb: "rollout-pause"}, webFact, nil)
	fence := func() error {
		if !valid {
			return errors.New("the cluster disconnected")
		}
		return nil
	}
	if effect, why := f.svc.verifyObject(context.Background(), wc, w, fence); effect != stepEffectUnverified || why != "the cluster disconnected" {
		t.Fatalf("%s %q", effect, why)
	}

	sf := newStepsFixture(t, pauseWeb)
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	p.runs[0].request, p.running, c.stepRunning = fleet.OutcomeWritten, 1, p
	sf.svc.mu.Unlock()
	sf.mu.Lock()
	sf.usable = false
	sf.mu.Unlock()
	sf.svc.settleEffect(c, p, 1, stepEffectEstablished, "")
	sf.svc.mu.Lock()
	r := *p.runs[0]
	sf.svc.mu.Unlock()
	if r.effect != stepEffectUnverified || !strings.Contains(r.effectWhy, "disconnected") {
		t.Fatalf("%+v", r)
	}
	p.incident = &IncidentDTO{State: incidentPending}
	sf.svc.settleIncident(c, p, incidentResolved, "every subject recovered", nil)
	if p.incident.State != incidentUnverif {
		t.Fatalf("%+v", p.incident)
	}
}

// Finding 3: a capture evaluated a fraction of a second after the write is
// fresh, by its own clock; an absence from a capture before the write is
// no fresh reading.
func TestFreshnessIsTheCapturesOwnClock(t *testing.T) {
	write := fixedNow().Add(100 * time.Millisecond)
	f := newToolsFixture(t)
	ok := workloads.Workload{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-web", Generation: 2, Desired: 1, Health: health.Verdict{Kind: health.OK, Label: "Ready"}}
	look := func(fact actionFact) subjectVerdict { return f.svc.verdictOf(context.Background(), f.conn, fact, nil) }
	for _, c := range []struct {
		name    string
		rows    observe.Rows
		reading string
	}{
		{"half a second after", capturedWorkloads(fixedNow().Add(500*time.Millisecond), ok), "recovered"},
		{"before the write", capturedWorkloads(fixedNow(), ok), "pending: no fresh reading"},
		{"gone, after", capturedWorkloads(fixedNow().Add(500 * time.Millisecond)), "unverified: gone"},
		{"gone, before", capturedWorkloads(fixedNow()), "pending: no fresh reading"},
	} {
		f.conn.rows = map[string]observe.Rows{"workloads/workloads": c.rows}
		rows, _, _, _ := readSubjects(look, []incidentSubject{{fact: webFact}}, write)
		if rows[0].Reading != c.reading {
			t.Errorf("%s: %+v", c.name, rows[0])
		}
	}
}

// The lab's plan case (stage 6): a capture taken after the last write is a
// reading after it, though its part's data last changed before it - a
// suspended unit stays quiet, and its evaluation time stops. The data must
// still postdate the subject's own write, and no capture before the last
// write is fresh, whatever its data's time.
func TestAQuietPartIsReadByItsCapture(t *testing.T) {
	ownWrite, lastWrite := fixedNow(), fixedNow().Add(2*time.Second)
	f := newToolsFixture(t)
	ok := workloads.Workload{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-web", Generation: 2, Desired: 1, Health: health.Verdict{Kind: health.OK, Label: "Ready"}}
	look := func(fact actionFact) subjectVerdict { return f.svc.verdictOf(context.Background(), f.conn, fact, nil) }
	quiet := func(data, captured time.Time, ws ...workloads.Workload) observe.Rows {
		if ws == nil {
			ws = []workloads.Workload{ok}
		}
		r := capturedWorkloads(data, ws...)
		r.CapturedAt = captured
		return r
	}
	other := ok
	other.UID, other.Name = "uid-other", "other"
	for _, c := range []struct {
		name    string
		rows    observe.Rows
		wrote   time.Time
		reading string
	}{
		{"quiet since its own write", quiet(ownWrite.Add(time.Second), lastWrite.Add(time.Minute)), ownWrite, "recovered"},
		{"quiet, no write of its own", quiet(ownWrite.Add(-time.Hour), lastWrite.Add(time.Minute)), time.Time{}, "recovered"},
		{"its data before its own write", quiet(ownWrite.Add(-time.Second), lastWrite.Add(time.Minute)), ownWrite, "pending: no fresh reading"},
		{"captured before the last write", quiet(ownWrite.Add(time.Second), lastWrite.Add(-time.Millisecond)), ownWrite, "pending: no fresh reading"},
		{"gone, its absence quiet since", quiet(ownWrite.Add(-time.Hour), lastWrite.Add(time.Minute), other), time.Time{}, "unverified: gone"},
		{"gone, captured before the last write", quiet(ownWrite.Add(-time.Hour), lastWrite.Add(-time.Millisecond), other), time.Time{}, "pending: no fresh reading"},
	} {
		f.conn.rows = map[string]observe.Rows{"workloads/workloads": c.rows}
		rows, _, _, _ := readSubjects(look, []incidentSubject{{fact: webFact, wrote: c.wrote}}, lastWrite)
		if rows[0].Reading != c.reading {
			t.Errorf("%s: %+v", c.name, rows[0])
		}
		if want := c.rows.CapturedAt.UTC().Format(time.RFC3339); rows[0].Verdict != "" && rows[0].At != want {
			t.Errorf("%s: the row is dated by its capture, %s: %+v", c.name, want, rows[0])
		}
	}
}

// A subject's own write is its latest step's send time.
func TestASubjectsOwnWriteIsItsLatestStep(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	webF := factOf(deploysGVR, sf.web(), "r1", "t", acquisition())
	ksF := actionFact{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations", Namespace: "flux-system", Name: "web", UID: "uid-ks"}
	steps := []ActionStepDTO{{N: 1, Verb: "rollout-pause", State: stepOffered}, {N: 2, Verb: "flux-suspend", State: stepOffered}, {N: 3, Verb: "rollout-resume", State: stepOffered}}
	sf.plantPlan(steps, []actionFact{webF, ksF, webF}, []*fleet.RevisionExpect{nil, nil, nil})
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	at := fixedNow()
	for i, r := range p.runs {
		r.request, r.sentAt = fleet.OutcomeWritten, at.Add(time.Duration(i)*time.Second)
	}
	got := incidentSubjectsLocked(c, p)
	if len(got) != 1 || !got[0].wrote.Equal(at.Add(2*time.Second)) {
		t.Fatalf("web's own latest write is step 3's: %+v", got)
	}
}

// RunStep records when the request was sent, before its answer.
func TestRunStepRecordsTheSendTime(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {}
	t.Cleanup(func() { verifyStepHook = prev })
	tok := sf.review(1).Token
	sent := sf.now
	sf.sc.onWrite = func() { sf.now = sent.Add(time.Second) } // the answer arrives a second later
	if r := sf.run(1, tok); !r.Ran {
		t.Fatal(r)
	}
	sf.svc.mu.Lock()
	r := *sf.svc.convs[invKey("x")].exchanges[0].plan.runs[0]
	sf.svc.mu.Unlock()
	if !r.sentAt.Equal(sent) || !r.ranAt.Equal(sent.Add(time.Second)) {
		t.Fatalf("sent %v ran %v", r.sentAt, r.ranAt)
	}
}

// Finding 4: a restart is never established by a count scaled to zero
// after it, whatever the kind.
func TestARestartNeedsADesiredPod(t *testing.T) {
	tok := "2026-10-14T12:00:00.000000002Z"
	tm := map[string]any{"metadata": map[string]any{"annotations": map[string]any{"kubectl.kubernetes.io/restartedAt": tok}}, "spec": map[string]any{}}
	for kind, status := range map[string]map[string]any{
		"Deployment":  {"replicas": int64(0), "updatedReplicas": int64(0)},
		"StatefulSet": {"updatedReplicas": int64(0), "currentRevision": "r2", "updateRevision": "r2"},
		"DaemonSet":   {"updatedNumberScheduled": int64(0), "desiredNumberScheduled": int64(0)},
	} {
		st := map[string]any{"observedGeneration": int64(7)}
		for k, v := range status {
			st[k] = v
		}
		u := uobj(map[string]any{"kind": kind, "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web", "generation": int64(7)}, "spec": map[string]any{"replicas": int64(0), "template": tm}, "status": st})
		w := &witness{sn: stepSnap{step: ActionStepDTO{Verb: "rollout-restart"}, fact: actionFact{Kind: kind, UID: "uid-web"}}, written: true, gen: 6, prior: 5, token: tok}
		if got, why := w.judge(context.Background(), u); got != judgePending || !strings.Contains(why, "no pods are desired") {
			t.Errorf("%s: %d %q", kind, got, why)
		}
	}
}

// Finding 5: a pause that is not the step's own hold, read from a real
// capture, is not attributed to the plan.
func TestAHoldIsReadFromItsEvidence(t *testing.T) {
	f := newToolsFixture(t)
	zero := 0
	scaled := workloads.Workload{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-web", Generation: 6, Health: health.Verdict{Kind: health.Paused, Label: "Scaled to 0", Reason: "ScaledToZero", Rule: "workload.scaled-to-zero"}}
	f.conn.rows = map[string]observe.Rows{"workloads/workloads": capturedWorkloads(fixedNow().Add(time.Second), scaled)}
	look := func(fact actionFact) subjectVerdict { return f.svc.verdictOf(context.Background(), f.conn, fact, nil) }
	for _, c := range []struct {
		step    ActionStepDTO
		reading string
	}{
		{ActionStepDTO{N: 1, Verb: "scale", Replicas: &zero}, "held by step 1, as intended"},
		{ActionStepDTO{N: 1, Verb: "flux-suspend"}, "unverified: paused outside this plan"},
		{ActionStepDTO{N: 1, Verb: "cordon"}, "unverified: paused outside this plan"},
	} {
		rows, _, _, _ := readSubjects(look, []incidentSubject{{fact: webFact, held: 1, holdStep: c.step}}, fixedNow())
		if rows[0].Reading != c.reading {
			t.Errorf("%s: %+v", c.step.Verb, rows[0])
		}
	}
}

// Finding 6: retention counts the plan record and the incident.
func TestRetentionCountsTheRecordAndTheIncident(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	e := sf.svc.convs[invKey("x")].exchanges[0]
	before := exchangeBytes(e)
	e.planRecord = strings.Repeat("r", 1200)
	e.plan.incident = &IncidentDTO{State: incidentPending, Subjects: []IncidentSubjectDTO{{Kind: "Deployment", Name: "web", UID: "uid-web", Reading: strings.Repeat("p", 500)}}}
	b, _ := json.Marshal(e.plan.incident)
	if got := exchangeBytes(e) - before; got != 1200+len(b) {
		t.Fatalf("counted %d", got)
	}
}

// Finding 7: the record names the turn a plan was proposed in, whatever
// retention dropped since.
func TestTheRecordKeepsItsTurn(t *testing.T) {
	quietIncident(t)
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {} // turn 1's step stays running
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb)
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	for i := 0; i < chatRetainExchange; i++ {
		sf.prov.turns = append(sf.prov.turns, answer("noted"))
		sf.svc.Ask("x", sf.conv, "and?", nil)
	}
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	c := sf.svc.convs[invKey("x")]
	if c.exchanges[0].id != sf.ex || c.exchanges[1].turn == 2 {
		t.Fatal("retention kept turn 1's running plan and dropped turn 2")
	}
	if rec := planRecordLocked(c); !strings.Contains(rec, "turn 1's plan, step 1") {
		t.Fatalf("%s", rec)
	}
}

// Stage 4 re-review: every positive result lands inside its bound,
// measured from the start - a slow first read, a deletion event that wins
// the race against the end, an incident reading that finishes past it.
func TestEveryPositiveResultLandsInsideItsBound(t *testing.T) {
	fastVerify(t, time.Second)
	effectBoundFor = func(_ string, u *unstructured.Unstructured) time.Duration {
		if u == nil {
			return time.Second // the ceiling before the read
		}
		return 20 * time.Millisecond // the object's own bound
	}
	wc, f := newWatchConn(t)
	f.conn.objects[key(deploysGVR, "shop", "web")] = deployAt("uid-web", 5, 5, map[string]any{"paused": true}, nil)
	wc.getHook = func() { time.Sleep(60 * time.Millisecond) } // a slow read
	w := witnessOf(f.svc, wc, ActionStepDTO{Verb: "rollout-pause"}, webFact, nil)
	if effect, why := f.svc.verifyObject(context.Background(), wc, w, func() error { return nil }); effect != stepEffectUnverified || !strings.HasPrefix(why, "not established within 20ms") {
		t.Fatalf("a slow read earns no time back: %s %q", effect, why)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the end has passed, with the deletion's event queued
	wd, fd := newWatchConn(t)
	pod := uobj(map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "p", "namespace": "shop", "uid": "old"}})
	wd.watches = []*watch.FakeWatcher{events(false, watch.Event{Type: watch.Deleted, Object: pod})}
	for i := 0; i < 32; i++ {
		wd.watches = append(wd.watches, events(false, watch.Event{Type: watch.Deleted, Object: pod}))
		effect, _ := fd.svc.verifyDeletion(ctx, wd, stepSnap{step: ActionStepDTO{Verb: "delete-pod"}, fact: actionFact{Kind: "Pod", Namespace: "shop", Name: "p", UID: "old"}, podsRV: "1"}, func() error { return nil })
		if effect == stepEffectEstablished {
			t.Fatalf("run %d: a deletion established past its end", i)
		}
	}

	incidentBound = 20 * time.Millisecond
	sf := newStepsFixture(t, pauseWeb)
	last := sf.now
	prev := verdictOfHook
	verdictOfHook = func(actionFact) subjectVerdict {
		sf.mu.Lock()
		sf.now = last.Add(21 * time.Millisecond) // the reading finishes past the bound
		sf.mu.Unlock()
		return subjectVerdict{v: chatVerdict{Role: "ok", Coverage: "live"}, found: true, state: "ok", at: last.Add(21 * time.Millisecond), captured: last.Add(21 * time.Millisecond)}
	}
	t.Cleanup(func() { verdictOfHook = prev })
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	p.incident = &IncidentDTO{State: incidentPending}
	sf.svc.mu.Unlock()
	sf.svc.readIncident(c, p, []incidentSubject{{fact: webFact}}, last)
	if p.incident.State == incidentResolved {
		t.Fatalf("%+v", p.incident)
	}
}

// Stage 4 re-review: a blocked Observer read waits only the time left
// until the last write's bound, never a full bound from when reading began.
func TestAnIncidentWaitsOnlyTheTimeLeft(t *testing.T) {
	fastVerify(t, time.Second)
	incidentBound = time.Hour
	sf := newStepsFixture(t, pauseWeb)
	br := &blockingRows{ChatConn: sf.sc, entered: make(chan context.Context, 1)}
	sf.mu.Lock()
	sf.cur = br
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	c.conn = br
	p := c.exchanges[0].plan
	p.incident = &IncidentDTO{State: incidentPending}
	sf.svc.mu.Unlock()
	lastWrite := sf.now.Add(-time.Hour + 20*time.Millisecond) // 20ms of the bound left
	done := make(chan struct{})
	go func() { sf.svc.readIncident(c, p, []incidentSubject{{fact: webFact}}, lastWrite); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the incident waited past the time left in its bound")
	}
	if p.incident.State != incidentUnverif {
		t.Fatalf("%+v", p.incident)
	}
}

// Stage 5: a settled effect and an incident's reading tell the drawer to
// read the conversation back.
func TestAPlanChangeTellsTheDrawer(t *testing.T) {
	quietIncident(t)
	establish(t)
	em := &fakeEmitterWithData{}
	sf := newStepsFixture(t, pauseWeb)
	sf.svc.em = em
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	sf.svc.mu.Unlock()
	sf.svc.maybeIncident(c, p)
	awaitSettled(t, "the incident", sf.incidentSettled())
	n := 0
	for _, e := range em.events_() {
		if e.name == "chat-plan:"+sf.conv {
			n++
		}
	}
	if n < 3 { // the effect, the incident's start, its settling
		t.Fatalf("%d plan events: %+v", n, em.events_())
	}
}

// The A3 lab: a fence that fails at one tick and holds again by the next
// check still ends the reading - with its own reason, never with what the
// reads it cancelled returned ("no fresh reading").
func TestABriefFenceFailureEndsWithItsReason(t *testing.T) {
	fastVerify(t, time.Second)
	sf := newStepsFixture(t, pauseWeb)
	br := &blockingRows{ChatConn: sf.sc, entered: make(chan context.Context, 1), release: make(chan struct{})}
	sf.mu.Lock()
	sf.cur = br
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	c.conn = br
	p := c.exchanges[0].plan
	p.incident = &IncidentDTO{State: incidentPending}
	sf.svc.mu.Unlock()
	done := make(chan struct{})
	go func() { sf.svc.readIncident(c, p, []incidentSubject{{fact: webFact}}, sf.now); close(done) }()
	read := <-br.entered
	sf.mu.Lock()
	sf.usable = false // a blip
	sf.mu.Unlock()
	<-read.Done() // the watcher saw it and cancelled the blocked read
	sf.mu.Lock()
	sf.usable = true // and the fence holds again
	sf.mu.Unlock()
	close(br.release) // only now does the cancelled read return
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the reading did not end")
	}
	if p.incident.State != incidentUnverif || !strings.Contains(p.incident.Why, "disconnected") {
		t.Fatalf("%+v", p.incident)
	}
}

// The same for a verification: the fence fails at one tick, cancels the
// blocked read, and holds again before the read returns; the effect ends
// unverified with the fence's reason, never with the cancelled read's.
func TestABriefFenceFailureEndsAVerificationWithItsReason(t *testing.T) {
	fastVerify(t, time.Second)
	quietIncident(t)
	sf := newStepsFixture(t, pauseWeb)
	bg := &blockingGet{ChatConn: sf.sc, entered: make(chan context.Context, 1), release: make(chan struct{})}
	sf.mu.Lock()
	sf.cur = bg
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	c.conn = bg
	p := c.exchanges[0].plan
	p.runs[0].request, p.runs[0].effect, p.running, c.stepRunning = fleet.OutcomeWritten, stepEffectPending, 1, p
	sn := stepSnap{n: 1, step: p.dto.Steps[0], fact: p.facts[0]}
	sf.svc.mu.Unlock()
	done := make(chan struct{})
	go func() {
		sf.svc.verifyStepEffect(c, p, 1, sn, ActionResultDTO{Outcome: fleet.OutcomeWritten}, sf.now)
		close(done)
	}()
	read := <-bg.entered
	sf.mu.Lock()
	sf.usable = false
	sf.mu.Unlock()
	<-read.Done()
	sf.mu.Lock()
	sf.usable = true
	sf.mu.Unlock()
	close(bg.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the verification did not end")
	}
	sf.svc.mu.Lock()
	r := *p.runs[0]
	sf.svc.mu.Unlock()
	if r.effect != stepEffectUnverified || !strings.Contains(r.effectWhy, "disconnected") {
		t.Fatalf("%+v", r)
	}
}

// The A3 lab: a step's change to an object a GitOps unit manages, the unit
// not suspended, may be reverted at its next reconcile - the consequence the
// operator reviewed says so - so the subject's recovery or hold is not
// counted while it may, and the incident cannot resolve on it.
func TestARecoveryAUnitMayRevertIsNotCounted(t *testing.T) {
	sf := newStepsFixture(t, scaleWeb)
	sf.sc.owner = fleet.GitOpsOwnership{OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "a1-web", Basis: "flux-inventory"}
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {}
	t.Cleanup(func() { verifyStepHook = prev })
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	owner, applies := p.runs[0].owner, p.runs[0].applies
	subjects := incidentSubjectsLocked(c, p)
	sf.svc.mu.Unlock()
	if owner != "Kustomization flux-system/a1-web" || !applies || len(subjects) != 1 || subjects[0].revertBy != owner || subjects[0].revertStep != 1 {
		t.Fatalf("%q %v %+v", owner, applies, subjects)
	}
	at := fixedNow().Add(time.Second)
	ok := func(actionFact) subjectVerdict {
		return subjectVerdict{v: chatVerdict{Kind: "ok", Coverage: "live"}, found: true, state: "ok", at: at, captured: at}
	}
	rows, recovered, open, faulted := readSubjects(ok, subjects, fixedNow())
	if recovered != 0 || open != 1 || faulted || rows[0].Reading != "recovered for now, but Kustomization flux-system/a1-web may revert step 1's change: it is not suspended" {
		t.Fatalf("%+v %d %d %v", rows, recovered, open, faulted)
	}
}

// The unit that may revert a step's change is followed through the plan's
// later steps: a written suspension of it ends the caveat, a resume that
// may have been written brings it back; a step before the change, another
// unit's step, and an unverified or refused suspension change nothing.
func TestTheRevertCaveatFollowsTheUnitThroughThePlan(t *testing.T) {
	const ks = "Kustomization flux-system/a1-web"
	type st struct{ verb, unit, request string }
	for _, c := range []struct {
		name    string
		before  *st // a step before the change
		after   []st
		applies bool // the unit applied when the change ran
		want    string
	}{
		{"nothing after", nil, nil, true, ks},
		{"a written suspension", nil, []st{{"flux-suspend", "a1-web", fleet.OutcomeWritten}}, true, ""},
		{"an unverified suspension", nil, []st{{"flux-suspend", "a1-web", fleet.OutcomeUnverified}}, true, ks},
		{"a refused suspension", nil, []st{{"flux-suspend", "a1-web", fleet.OutcomeRefused}}, true, ks},
		{"another unit's suspension", nil, []st{{"flux-suspend", "other", fleet.OutcomeWritten}}, true, ks},
		{"suspended, then resumed", nil, []st{{"flux-suspend", "a1-web", fleet.OutcomeWritten}, {"flux-resume", "a1-web", fleet.OutcomeWritten}}, true, ks},
		{"resumed by a lost answer", nil, []st{{"flux-resume", "a1-web", fleet.OutcomeUnverified}}, false, ks},
		{"a refused resume", nil, []st{{"flux-resume", "a1-web", fleet.OutcomeRefused}}, false, ""},
		{"suspended before the change", &st{"flux-suspend", "a1-web", fleet.OutcomeWritten}, nil, true, ks},
		{"suspended when it ran", nil, nil, false, ""},
	} {
		sf := newStepsFixture(t, pauseWeb)
		webF := factOf(deploysGVR, sf.web(), "r1", "t", acquisition())
		unit := func(name string) actionFact {
			return actionFact{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations", Namespace: "flux-system", Name: name, UID: "uid-" + name}
		}
		var steps []ActionStepDTO
		var facts []actionFact
		var reqs []string
		add := func(s st, f actionFact) {
			steps = append(steps, ActionStepDTO{N: len(steps) + 1, Verb: s.verb, State: stepOffered})
			facts, reqs = append(facts, f), append(reqs, s.request)
		}
		if c.before != nil {
			add(*c.before, unit(c.before.unit))
		}
		change := len(steps)
		add(st{"rollout-pause", "", fleet.OutcomeWritten}, webF)
		for _, s := range c.after {
			add(s, unit(s.unit))
		}
		sf.plantPlan(steps, facts, make([]*fleet.RevisionExpect, len(steps)))
		sf.svc.mu.Lock()
		cv := sf.svc.convs[invKey("x")]
		p := cv.exchanges[0].plan
		for i, r := range reqs {
			p.runs[i].request = r
		}
		p.runs[change].owner, p.runs[change].applies = ks, c.applies
		got := incidentSubjectsLocked(cv, p)
		sf.svc.mu.Unlock()
		if len(got) != 1 || got[0].revertBy != c.want || (c.want == "") != (got[0].revertStep == 0) || (c.want != "" && got[0].revertStep != change+1) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// A run under a suspended unit records that it applies nothing: the
// subject's recovery is counted.
func TestARunUnderASuspendedUnitHasNoCaveat(t *testing.T) {
	sf := newStepsFixture(t, scaleWeb)
	sf.sc.owner = fleet.GitOpsOwnership{OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "a1-web", Basis: "flux-inventory", OwnerSuspended: true}
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {}
	t.Cleanup(func() { verifyStepHook = prev })
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	p := c.exchanges[0].plan
	r := *p.runs[0]
	subjects := incidentSubjectsLocked(c, p)
	sf.svc.mu.Unlock()
	if r.owner != "Kustomization flux-system/a1-web" || r.applies || len(subjects) != 1 || subjects[0].revertBy != "" {
		t.Fatalf("%q %v %+v", r.owner, r.applies, subjects)
	}
}

// Argo CD's application may revert when it syncs: no suspension of a Flux
// unit ends that, and the reading says when.
func TestArgoMayRevertWhenItSyncs(t *testing.T) {
	sub := incidentSubject{fact: webFact, revertBy: "Argo CD's Application argocd/web", revertStep: 1}
	at := fixedNow().Add(time.Second)
	ok := func(actionFact) subjectVerdict {
		return subjectVerdict{v: chatVerdict{Kind: "ok", Coverage: "live"}, found: true, state: "ok", at: at, captured: at}
	}
	rows, recovered, open, _ := readSubjects(ok, []incidentSubject{sub}, fixedNow())
	if recovered != 0 || open != 1 || rows[0].Reading != "recovered for now, but Argo CD's Application argocd/web may revert step 1's change when it syncs" {
		t.Fatalf("%+v", rows)
	}
}

// Who may revert is what the consequence says: the unit managing the
// target, applying its desired state - Argo CD's application always, a
// Flux unit while it is not suspended, followed through the plan's earlier
// steps in order.
func TestWhoMayRevertIsTheConsequences(t *testing.T) {
	ks := actionFact{Kind: "Kustomization", Namespace: "flux-system", Name: "a1-web"}
	other := actionFact{Kind: "Kustomization", Namespace: "flux-system", Name: "other"}
	suspend := stepSnapPrior{n: 1, verb: "flux-suspend", fact: ks}
	resume := stepSnapPrior{n: 2, verb: "flux-resume", fact: ks}
	flux := fleet.GitOpsOwnership{OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "a1-web", Basis: "flux-inventory"}
	suspended := flux
	suspended.OwnerSuspended = true
	for _, c := range []struct {
		name    string
		owner   fleet.GitOpsOwnership
		earlier []stepSnapPrior
		who     string
		applies bool
		line    string
	}{
		{"flux, not suspended", flux, nil, "Kustomization flux-system/a1-web", true, "which may revert this change"},
		{"flux, suspended", suspended, nil, "Kustomization flux-system/a1-web", false, "is suspended"},
		{"flux, an earlier step suspends it", flux, []stepSnapPrior{suspend}, "Kustomization flux-system/a1-web", false, "; step 1 suspends it"},
		{"flux, suspended then resumed", flux, []stepSnapPrior{suspend, resume}, "Kustomization flux-system/a1-web", true, "; step 1 suspends it; step 2 resumes it"},
		{"flux, another unit suspended", flux, []stepSnapPrior{{n: 1, verb: "flux-suspend", fact: other}}, "Kustomization flux-system/a1-web", true, "which may revert this change"},
		{"argo", fleet.GitOpsOwnership{OwnerKind: "Application", OwnerNamespace: "argocd", OwnerName: "web", Basis: "argo-resources"}, nil, "Argo CD's Application argocd/web", true, "when it syncs"},
		{"none", fleet.GitOpsOwnership{}, nil, "", false, "no GitOps unit"},
	} {
		f := newToolsFixture(t)
		sc := &stepsConn{fakeChatConn: f.conn, owner: c.owner}
		out, who, applies := f.svc.consequenceWords(context.Background(), sc, stepSnap{step: ActionStepDTO{Verb: "rollout-pause"}, earlier: c.earlier}, webFact)
		if who != c.who || applies != c.applies || !strings.Contains(strings.Join(out, "\n"), c.line) {
			t.Errorf("%s: %q %v %q", c.name, who, applies, out)
		}
	}
}
