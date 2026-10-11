package fleet

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// The connection's pods come from the health Observer's pods kind (1c 2d):
// one pod watch, its barrier, its count, its freshness.

type seamAPI struct {
	typed *fake.Clientset
	meta  *metafake.FakeMetadataClient

	mu        sync.Mutex
	podsErr   error         // pod lists fail with this
	podsGate  chan struct{} // pod lists wait for this
	nodesGate chan struct{} // node lists wait for this
	podWatch  []*watch.RaceFreeFakeWatcher
}

func seamNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}

func seamPod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name)}}
}

func newSeamAPI(t *testing.T, objs ...runtime.Object) *seamAPI {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := &seamAPI{typed: fake.NewClientset(objs...), meta: metafake.NewSimpleMetadataClient(scheme)}
	a.typed.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		a.mu.Lock()
		gate, err := a.podsGate, a.podsErr
		a.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	a.typed.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		a.mu.Lock()
		gate := a.nodesGate
		a.mu.Unlock()
		if gate != nil {
			<-gate
		}
		return false, nil, nil
	})
	// Pod watches the test can break (the tracker's own events are not
	// needed: lists carry the state).
	a.typed.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		w := watch.NewRaceFreeFake()
		a.mu.Lock()
		a.podWatch = append(a.podWatch, w)
		a.mu.Unlock()
		return true, w, nil
	})
	return a
}

func (a *seamAPI) breakPodWatch(err error) {
	a.mu.Lock()
	a.podsErr = err
	ws := a.podWatch
	a.podWatch = nil
	a.mu.Unlock()
	status := apierrors.NewServiceUnavailable("the server is shutting down").ErrStatus
	for _, w := range ws {
		w.Error(&status)
	}
}

// conn returns a connection and the context to Start it with.
func (a *seamAPI) conn(t *testing.T) (*ClusterConn, context.Context, context.CancelFunc) {
	t.Helper()
	c := NewClusterConn("x", a.typed, a.meta, nil, capability.NewDetector(a.typed), clock.Real{}, config.MetricsConfig{})
	c.SetHealthSink(nopSink{})
	ctx, cancel := context.WithCancel(context.Background())
	return c, ctx, cancel
}

func stop(c *ClusterConn, cancel context.CancelFunc) {
	cancel()
	if h := c.Health(); h != nil {
		<-h.Done()
	}
}

func TestPodsComeFromTheObserverNotASecondWatch(t *testing.T) {
	a := newSeamAPI(t, seamNode("n1"), seamPod("a"), seamPod("b"), seamPod("c"))
	c, ctx, cancel := a.conn(t)
	c.Start(ctx)
	defer stop(c, cancel)
	waitUntil(t, "synced", func() bool { s := c.Snapshot(); return s.State == Synced && s.Pods == 3 })
	if s := c.Snapshot(); !s.PodsKnown || s.PodsStale {
		t.Fatalf("a current count: %+v", s)
	}
	c.mu.RLock()
	podInf := c.podInf
	c.mu.RUnlock()
	if podInf != nil {
		t.Fatal("with an Observer the metadata pod informer is not started")
	}
	for _, act := range a.meta.Actions() {
		if act.GetResource().Resource == "pods" {
			t.Fatalf("pods are watched once, by the Observer: %v", act)
		}
	}
}

func TestAnEmptyPodListIsAKnownZero(t *testing.T) {
	a := newSeamAPI(t, seamNode("n1"))
	c, ctx, cancel := a.conn(t)
	c.Start(ctx)
	defer stop(c, cancel)
	waitUntil(t, "synced with no pods", func() bool { s := c.Snapshot(); return s.State == Synced && s.PodsKnown && s.Pods == 0 })
}

func TestAForbiddenFirstPodReadNeverSyncsAndIsNotZero(t *testing.T) {
	a := newSeamAPI(t, seamNode("n1"))
	a.podsErr = apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("rbac"))
	c, ctx, cancel := a.conn(t)
	c.connectTimeout = 300 * time.Millisecond
	c.Start(ctx)
	defer stop(c, cancel)
	waitUntil(t, "the connect fails, naming pods", func() bool {
		s := c.Snapshot()
		return s.State == Failed && strings.Contains(s.Reason, "pods") && strings.Contains(strings.ToLower(s.Reason), "forbidden")
	})
	if s := c.Snapshot(); s.PodsKnown || s.Pods != 0 {
		t.Fatalf("no pod read is unknown, never an observed zero: %+v", s)
	}
}

// A failed relist turns the connection Stale with the last good count;
// node events during the failure do not recover it; the pods recovering
// do, and the capability overlay is applied on that recovery.
func TestPodRelistFailureAndRecovery(t *testing.T) {
	a := newSeamAPI(t, seamNode("n1"), seamPod("a"), seamPod("b"))
	c, ctx, cancel := a.conn(t)
	c.Start(ctx)
	defer stop(c, cancel)
	waitUntil(t, "synced", func() bool { s := c.Snapshot(); return s.State == Synced && s.Pods == 2 })
	c.mu.Lock()
	c.caps.Network = capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "no GatewayClass accepted"}, GatewayAPIVersion: "v1"}
	c.mu.Unlock()
	before := c.Snapshot().LastSync

	a.breakPodWatch(errors.New("connection refused"))
	waitUntil(t, "stale with the last good count", func() bool {
		s := c.Snapshot()
		return s.State == Stale && s.PodsKnown && s.PodsStale && s.Pods == 2
	})
	if got := c.Snapshot().LastSync; !got.Equal(before) {
		t.Fatalf("a failure advanced lastSync: %v -> %v", before, got)
	}
	// A node event during the pod failure cannot recover the connection.
	n := seamNode("n2")
	if _, err := a.typed.CoreV1().Nodes().Create(context.Background(), n, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the node event arrived", func() bool { return c.Snapshot().NodesTotal == 2 })
	time.Sleep(100 * time.Millisecond)
	if s := c.Snapshot(); s.State != Stale {
		t.Fatalf("a node event recovered a connection whose pods are failing: %v", s.State)
	}
	// The pods recover: the first recovered state carries the overlay.
	a.mu.Lock()
	a.podsErr = nil
	a.mu.Unlock()
	waitUntil(t, "recovered, overlaid", func() bool {
		s := c.Snapshot()
		return s.State == Degraded && s.Reason == "no GatewayClass accepted" && !s.PodsStale
	})
}

func TestTheBarrierNeedsNodesAndPodsInEitherOrder(t *testing.T) {
	for _, podsFirst := range []bool{true, false} {
		a := newSeamAPI(t, seamNode("n1"), seamPod("a"))
		nodes, pods := make(chan struct{}), make(chan struct{})
		a.nodesGate, a.podsGate = nodes, pods
		c, ctx, cancel := a.conn(t)
		c.Start(ctx)
		first, second := nodes, pods
		if podsFirst {
			first, second = pods, nodes
		}
		close(first)
		time.Sleep(150 * time.Millisecond)
		if s := c.Snapshot(); s.State == Synced {
			t.Fatalf("podsFirst=%v: synced with one source still listing", podsFirst)
		}
		close(second)
		waitUntil(t, "synced once both are current", func() bool { return c.Snapshot().State == Synced })
		stop(c, cancel)
	}
}

func TestTeardownLeavesNoPodListener(t *testing.T) {
	a := newSeamAPI(t, seamNode("n1"), seamPod("a"))
	c, ctx, cancel := a.conn(t)
	c.Start(ctx)
	waitUntil(t, "synced", func() bool { return c.Snapshot().State == Synced })
	cancel()
	<-c.Health().Done() // the listener ends with its Observer
	before := c.Snapshot()
	a.breakPodWatch(errors.New("late"))
	time.Sleep(100 * time.Millisecond)
	if after := c.Snapshot(); after.State != before.State || after.PodsStale != before.PodsStale {
		t.Fatalf("a late pod callback after teardown: %+v -> %+v", before, after)
	}
}
