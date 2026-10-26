package appbridge

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/policy"
)

// Settings S2a §2.2-§2.5: the lifecycle publishes usability under its
// gate, retires an identity with a trusted end before anything replaces
// it, and forgets a removed cluster everywhere.

func lifeFactory(cc config.ClusterConfig) (fleet.Conn, error) {
	typed := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
	mscheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(mscheme)
	meta := metadatafake.NewSimpleMetadataClient(mscheme)
	return fleet.NewClusterConn(cc.Name, typed, meta, nil, capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{}), nil
}

type lifeRig struct {
	t    *testing.T
	ctx  context.Context
	reg  *fleet.Registry
	pol  *policy.Registry
	sink *HealthSink
	em   *fakeEmitter
	ix   *fleetindex.Index
	life *Lifecycle
}

func newLifeRig(t *testing.T, run bool) *lifeRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &lifeRig{t: t, ctx: ctx, pol: policy.New(), em: &fakeEmitter{}}
	r.reg = fleet.NewRegistry(&config.Config{Clusters: []config.ClusterConfig{{Name: "prd"}}}, lifeFactory)
	r.pol.Seed("prd", true)
	r.sink = NewHealthSink(r.em)
	r.ix = fleetindex.New(fleetindex.Settings{})
	t.Cleanup(r.ix.Stop)
	r.reg.SetIndex(r.ix)
	r.life = NewLifecycle(r.reg, r.pol, r.sink, r.ix)
	if run {
		go r.life.Run(ctx)
	}
	r.reg.Start(ctx)
	return r
}

func (r *lifeRig) id() fleet.ConnID {
	r.t.Helper()
	c, ok := r.reg.Conn("prd")
	if !ok {
		r.t.Fatal("no connection")
	}
	return c.(interface{ ID() fleet.ConnID }).ID()
}

func (r *lifeRig) reconnect() {
	r.t.Helper()
	p, err := r.reg.Prepare(config.ClusterConfig{Name: "prd", Context: "prd-2"})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.life.Reconnect(r.ctx, p, "prd reconnected"); err != nil {
		r.t.Fatal(err)
	}
}

func batchOf(inc uint64, seq uint64, kind string, items ...observe.Transition) observe.TransitionBatch {
	return observe.TransitionBatch{Cluster: "prd", Incarnation: inc, Seq: seq, Task: "pods", Part: "pods", Source: "pods", GVR: "v1/pods", Kind: kind, Items: items, At: time.Now()}
}

// Codex's re-review probe, end to end: a retired connection's pending
// onset is dropped by the lifecycle's trusted end, never sent while the
// replacement connects; the control, never retired, is sent.
func TestARetiredConnectionsPendingNotificationIsNeverSent(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(map[bool]string{false: "kept", true: "retired"}[retire], func(t *testing.T) {
			start := time.Now()
			var offset time.Duration
			defer func(c func() time.Time) { notifyClock = c }(notifyClock)
			notifyClock = func() time.Time { return start.Add(offset) }
			r := newLifeRig(t, true)
			var sent []string
			s := NewNotificationsService(filepath.Join(t.TempDir(), "fleet.yaml"), &config.Config{Notifications: &config.NotificationsConfig{Enabled: true, Scope: "all"}},
				func(string) (config.ClusterConfig, bool) { return config.ClusterConfig{}, false },
				func(_, _, body string, _ map[string]any) error { sent = append(sent, body); return nil }, nil, nil)
			r.sink.AddListener(NotificationsListener(s))
			inc := uint64(r.id())
			r.sink.Transitions(batchOf(inc, 1, observe.BatchBaseline))
			r.sink.Transitions(batchOf(inc, 2, observe.BatchChanges, observe.Transition{Key: "u1", NS: "shop", Name: "web", From: "ok", To: "fault", Label: "CrashLoopBackOff"}))
			if retire {
				r.reconnect()
				// a late publication of the retired identity is dropped
				r.sink.Transitions(batchOf(inc, 3, observe.BatchChanges, observe.Transition{Key: "u2", NS: "shop", Name: "api", From: "ok", To: "fault", Label: "CrashLoopBackOff"}))
			}
			offset = 2 * time.Minute
			s.engine.Flush()
			want := map[bool]int{false: 1, true: 0}[retire]
			if len(sent) != want {
				t.Fatalf("sent %v, want %d", sent, want)
			}
		})
	}
}

func TestARemovalForgetsTheClusterEverywhere(t *testing.T) {
	r := newLifeRig(t, true)
	guard := NewProtectGuard(r.pol, time.Now)
	r.life.OnForget(guard.Forget)
	old := uint64(r.id())
	r.sink.Summary(observe.Summary{Cluster: "prd", Incarnation: old, Seq: 1})
	r.sink.Transitions(batchOf(old, 1, observe.BatchChanges, observe.Transition{Key: "u1", NS: "shop", Name: "web", From: "ok", To: "fault"}))
	if err := guard.Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	if len(r.sink.log.Recent("prd", 10)) == 0 || !coverageNames(r.ix, "prd") {
		t.Fatal("rig")
	}
	if err := r.life.Remove("prd", "prd removed"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.pol.Snapshot("prd"); ok {
		t.Fatal("the policy registry still knows prd")
	}
	guard.mu.Lock()
	_, armed := guard.armed["prd"]
	guard.mu.Unlock()
	if armed {
		t.Fatal("prd's grant survived its removal")
	}
	if _, ok := r.reg.Conn("prd"); ok || coverageNames(r.ix, "prd") || len(r.sink.log.Recent("prd", 10)) != 0 {
		t.Fatal("prd is still in the registry, the index or the log")
	}
	// a late publication of the removed identity recreates nothing
	events := r.em.events
	r.sink.Summary(observe.Summary{Cluster: "prd", Incarnation: old, Seq: 2})
	r.sink.Transitions(batchOf(old, 2, observe.BatchChanges, observe.Transition{Key: "u1", NS: "shop", Name: "web", From: "ok", To: "fault"}))
	if len(r.sink.log.Recent("prd", 10)) != 0 || r.em.events != events {
		t.Fatal("a removed identity's publication was taken")
	}
	// added again under its name: a new identity, above the old, publishing
	if err := r.reg.Add(r.ctx, config.ClusterConfig{Name: "prd"}); err != nil {
		t.Fatal(err)
	}
	r.pol.Seed("prd", false)
	if inc := uint64(r.id()); inc <= old {
		t.Fatalf("new identity %d is not above %d", inc, old)
	} else {
		r.sink.Transitions(batchOf(inc, 1, observe.BatchChanges, observe.Transition{Key: "u1", NS: "shop", Name: "web", From: "ok", To: "fault"}))
	}
	if len(r.sink.log.Recent("prd", 10)) == 0 {
		t.Fatal("the re-added cluster's publication was dropped")
	}
}

func coverageNames(ix *fleetindex.Index, name string) bool {
	for _, c := range ix.Coverage() {
		if c.Cluster == name {
			return true
		}
	}
	return false
}

// Slice F §1 through the queue (S2 §12): while the lifecycle has not
// applied a connection's move into usable, the move is not seen and the
// epoch has not moved; once applied, the incarnation is published, then
// the move is seen.
func TestUsabilityIsPublishedBeforeItIsSeen(t *testing.T) {
	r := newLifeRig(t, false)
	before, _ := r.pol.Snapshot("prd")
	if !waitUntil(t, 5*time.Second, func() bool {
		r.life.qmu.Lock()
		defer r.life.qmu.Unlock()
		return len(r.life.queue) == 1
	}) {
		t.Fatal("no usability event")
	}
	c, _ := r.reg.Conn("prd")
	if st := c.Snapshot().State; st.Usable() {
		t.Fatalf("usable before its incarnation was published: %v", st)
	}
	if s, _ := r.pol.Snapshot("prd"); s.Epoch != before.Epoch {
		t.Fatal("the epoch moved before the event was applied")
	}
	go r.life.Run(r.ctx)
	if !waitUntil(t, 5*time.Second, func() bool { return c.Snapshot().State.Usable() }) {
		t.Fatal("never usable")
	}
	if s, _ := r.pol.Snapshot("prd"); s.Epoch == before.Epoch {
		t.Fatal("seen usable with the old epoch")
	}
	// a retired identity's queued move is never published
	after, _ := r.pol.Snapshot("prd")
	r.life.applyUsable(fleet.UsabilityEvent{Cluster: "prd", ID: r.id() + 1000, Inc: 1})
	if s, _ := r.pol.Snapshot("prd"); s.Epoch != after.Epoch {
		t.Fatal("a move of an identity that is not current was published")
	}
}

type endRec struct {
	mu   sync.Mutex
	ends int
}

func (e *endRec) OnSummary(observe.Summary) {}
func (e *endRec) OnRows(observe.Rows)       {}
func (e *endRec) OnTransitions(b observe.TransitionBatch) {
	if b.Kind == observe.BatchEnd {
		e.mu.Lock()
		e.ends++
		e.mu.Unlock()
	}
}

// The sink takes a publication only from a cluster's current identity, one
// it never saw included, and a trusted end is delivered once.
func TestTheSinkTakesOnlyTheCurrentIdentityAndEndsOnce(t *testing.T) {
	r := newLifeRig(t, true)
	rec := &endRec{}
	r.sink.AddListener(rec)
	stranger := uint64(r.id()) + 1000
	r.sink.Transitions(batchOf(stranger, 1, observe.BatchChanges, observe.Transition{Key: "u1", NS: "shop", Name: "web", From: "ok", To: "fault"}))
	if len(r.sink.log.Recent("prd", 10)) != 0 {
		t.Fatal("an identity that is not current published")
	}
	inc := uint64(r.id())
	r.sink.EndIdentity("prd", inc, "unreachable")
	r.sink.EndIdentity("prd", inc, "unreachable")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.ends != 1 {
		t.Fatalf("the trusted end was delivered %d times", rec.ends)
	}
}
