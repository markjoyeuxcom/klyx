package fleet

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// Settings S2a §2.1-§2.2: a connection that stops, an identity, a usability
// move pending until it is published, and the registry's lifecycle.

func fakeClusterConn(name string) *ClusterConn {
	typed := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
	mscheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(mscheme)
	meta := metadatafake.NewSimpleMetadataClient(mscheme, podMeta("p1", "default"))
	return NewClusterConn(name, typed, meta, nil, capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{})
}

func settledGoroutines(t *testing.T, most int, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	n := runtime.NumGoroutine()
	for n > most && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func TestAStoppedConnectionLeavesNoLoops(t *testing.T) {
	for _, observed := range []bool{false, true} {
		name := map[bool]string{false: "informers", true: "observer"}[observed]
		t.Run(name, func(t *testing.T) {
			before := runtime.NumGoroutine()
			c := fakeClusterConn("a")
			if observed {
				c.SetHealthSink(nopSink{})
			}
			c.Start(context.Background())
			waitFor(t, 5*time.Second, func() bool { s := c.Snapshot().State; return s == Synced || s == Degraded })
			if !c.Stop(5 * time.Second) {
				t.Fatal("the loops did not return within the bound")
			}
			if n := settledGoroutines(t, before, 5*time.Second); n > before {
				buf := make([]byte, 1<<20)
				t.Fatalf("%d goroutines after Stop, %d before:\n%s", n, before, buf[:runtime.Stack(buf, true)])
			}
			if s := c.Snapshot(); s.State != Stopped || s.State.Usable() {
				t.Fatalf("state %v", s.State)
			}
			// never moves again, and a second Stop is a no-op
			c.setState(EvConnError, "late")
			c.setState(EvSynced, "")
			if c.Snapshot().State != Stopped || !c.Stop(time.Second) {
				t.Fatal("a stopped connection moved")
			}
		})
	}
}

func TestAConnectionStoppedBeforeItStartsRunsNothing(t *testing.T) {
	before := runtime.NumGoroutine()
	c := fakeClusterConn("a")
	c.Stop(time.Second)
	c.Start(context.Background())
	if n := settledGoroutines(t, before, 2*time.Second); n > before || c.Snapshot().State != Stopped {
		t.Fatalf("%d goroutines, state %v", n, c.Snapshot().State)
	}
}

func TestTheObserverCarriesTheConnectionsIdentity(t *testing.T) {
	c := fakeClusterConn("a")
	c.SetHealthSink(nopSink{})
	c.Start(context.Background())
	defer c.Stop(5 * time.Second)
	if h := c.Health(); h == nil || h.Incarnation() != uint64(c.ID()) || c.ID() == 0 {
		t.Fatalf("observer %v, connection %d", h, c.ID())
	}
	if d := fakeClusterConn("a"); d.ID() <= c.ID() {
		t.Fatalf("a later connection's identity %d is not above %d", d.ID(), c.ID())
	}
}

// A move into usable is pending - reported as the state before it - until
// the lifecycle acknowledges it: the incarnation is published before
// usability is seen (S2 §12).
func TestAUsabilityMoveIsPendingUntilAcknowledged(t *testing.T) {
	c := &ClusterConn{name: "a", state: Connecting, id: newConnID()}
	var evs []UsabilityEvent
	c.SetOnUsable(func(ev UsabilityEvent) { evs = append(evs, ev) })
	c.setState(EvSynced, "")
	if len(evs) != 1 || evs[0].ID != c.ID() || evs[0].Cluster != "a" {
		t.Fatalf("%+v", evs)
	}
	if s := c.Snapshot().State; s != Connecting {
		t.Fatalf("visible before it was published: %v", s)
	}
	c.setState(EvCapUnhealthy, "") // usable to usable: still pending
	if s := c.Snapshot().State; s != Connecting {
		t.Fatalf("%v", s)
	}
	c.AckUsable(evs[0].Inc)
	if s := c.Snapshot().State; s != Degraded {
		t.Fatalf("after the acknowledgement: %v", s)
	}
	// a move out of usable clears a pending one; a stale acknowledgement is ignored
	c.setState(EvConnError, "down")
	c.setState(EvSynced, "")
	if len(evs) != 2 || c.Snapshot().State != Failed {
		t.Fatalf("%+v %v", evs, c.Snapshot().State)
	}
	c.setState(EvConnError, "down again")
	c.setState(EvSynced, "")
	c.AckUsable(evs[1].Inc) // superseded by the third move
	if s := c.Snapshot().State; s != Failed {
		t.Fatalf("a superseded acknowledgement published: %v", s)
	}
	c.AckUsable(evs[2].Inc)
	if s := c.Snapshot().State; s != Synced {
		t.Fatalf("%v", s)
	}
}

func TestTheRegistryRetiresInstallsDropsAndRetries(t *testing.T) {
	failB := true
	built := map[string]int{}
	factory := func(cc config.ClusterConfig) (Conn, error) {
		built[cc.Name]++
		if cc.Name == "b" && failB {
			return nil, errors.New("context b not found")
		}
		return &fakeConn{name: cc.Name, snap: Snapshot{Name: cc.Name, State: Synced}}, nil
	}
	r := NewRegistry(&config.Config{Clusters: []config.ClusterConfig{{Name: "a"}, {Name: "b"}}}, factory)
	r.Start(context.Background())
	if _, ok := r.Conn("b"); ok {
		t.Fatal("a failed build is not a connection")
	}
	a1, ok := r.Conn("a")
	if !ok {
		t.Fatal("a")
	}
	_, id1, _ := r.Retire("a")
	if r.IsCurrent("a", id1) || !r.IsRetired(id1) {
		t.Fatal("retired identity still current")
	}
	if _, ok := r.Conn("a"); ok {
		t.Fatal("a retired connection is looked up")
	}
	if _, _, ok := r.Retire("a"); ok {
		t.Fatal("retired twice")
	}
	p, err := r.Prepare(config.ClusterConfig{Name: "a", Context: "a2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Install(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a2, _ := r.Conn("a")
	if a2 == a1 || !r.IsCurrent("a", p.ID()) || r.IsCurrent("a", id1) || p.ID() <= id1 {
		t.Fatalf("install: %v %v", a2, p.ID())
	}
	// a current connection is never replaced or dropped without retirement
	q, _ := r.Prepare(config.ClusterConfig{Name: "a"})
	if err := r.Install(context.Background(), q); err == nil {
		t.Fatal("installed over a current connection")
	}
	if err := r.Drop("a"); err == nil {
		t.Fatal("dropped a current connection")
	}
	// a failed build is retried; a running one is not
	if err := r.Retry(context.Background(), "b"); err == nil {
		t.Fatal("retried while the context is still missing")
	}
	failB = false
	if err := r.Retry(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Conn("b"); !ok || built["b"] != 3 {
		t.Fatalf("retry: built %d", built["b"])
	}
	if err := r.Retry(context.Background(), "a"); err == nil {
		t.Fatal("retried a running connection")
	}
	// removal: retire, then drop
	_, idA, _ := r.Retire("a")
	if err := r.Drop("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Conn("a"); ok || len(r.Snapshots()) != 1 || r.IsCurrent("a", idA) {
		t.Fatalf("after the removal: %v", r.Snapshots())
	}
	// added again under the same name: a new identity, above the old
	if err := r.Add(context.Background(), config.ClusterConfig{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if r.IsCurrent("a", idA) || !r.IsRetired(idA) {
		t.Fatal("a removed identity came back")
	}
}
