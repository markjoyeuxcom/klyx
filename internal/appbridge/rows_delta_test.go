package appbridge

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// ---- deltas and bootstraps on a real Observer (addendum §3.3, §4) ----------

type podsHarness struct {
	o     *observe.Observer
	typed *fake.Clientset
	rec   *rowsSinkRec
	svc   *HealthService
}

const podsEvent = HealthRowsEventPrefix + "c1:pods/pods"

func startPodsObserver(t *testing.T, n int) *podsHarness {
	t.Helper()
	created := metav1.NewTime(time.Now().Add(-time.Hour))
	objs := []runtime.Object{}
	for i := range n {
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("p%d", i), UID: types.UID(fmt.Sprintf("uid-p%d", i)), CreationTimestamp: created},
			Spec:       corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "app"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &created,
				Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: created}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
		})
	}
	typed := fake.NewClientset(objs...)
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kinds, domains := healthsrc.Core(healthsrc.CoreDeps{Cluster: "c1", Typed: typed, Meta: metafake.NewSimpleMetadataClient(scheme)})
	rec := &rowsSinkRec{rows: map[string][]RowsDTO{}}
	sink := NewHealthSink(rec)
	o := observe.New(observe.Config{Cluster: "c1", Sink: sink, Kinds: kinds, Domains: domains, Throttle: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	t.Cleanup(func() { cancel(); <-o.Done() })
	svc := NewHealthService(func() []*observe.Observer { return []*observe.Observer{o} })
	LinkRowsLineage(svc, sink)
	h := &podsHarness{o: o, typed: typed, rec: rec, svc: svc}
	waitRows(t, "pods live", func() bool {
		s, ok := o.Latest()
		if !ok {
			return false
		}
		for _, tk := range s.Tasks {
			if tk.ID == healthsrc.TaskPods {
				return tk.Coverage == observe.Live
			}
		}
		return false
	})
	return h
}

func (h *podsHarness) messages() []RowsDTO {
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	return append([]RowsDTO(nil), h.rec.rows[podsEvent]...)
}

func (h *podsHarness) restart(t *testing.T, i int) {
	p, err := h.typed.CoreV1().Pods("shop").Get(context.Background(), fmt.Sprintf("p%d", i), metav1.GetOptions{})
	if err != nil {
		t.Error(err)
		return
	}
	p.Status.ContainerStatuses[0].RestartCount++
	if _, err := h.typed.CoreV1().Pods("shop").UpdateStatus(context.Background(), p, metav1.UpdateOptions{}); err != nil {
		t.Error(err)
	}
}

// Under churn, every bootstrap reply - the first subscriber's and those of
// consumers joining mid-stream - is a Ver the broadcast published, so the
// recorded broadcast applies on top of it with no wrong base, and every
// consumer ends equal to the Observer's rows. A fresh capture running
// ahead of the broadcast would leave a consumer holding a Ver the next
// delta does not chain from.
func TestBootstrapRepliesChainIntoTheBroadcastUnderChurn(t *testing.T) {
	h := startPodsObserver(t, 30)
	first := h.svc.OpenRows("c1", healthsrc.TaskPods, "pods", "a")
	if first.Rows == nil || !first.Rows.Full {
		t.Fatalf("open: %+v", first)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewPCG(3, 4))
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.restart(t, rng.IntN(30))
			time.Sleep(time.Millisecond)
		}
	}()
	replies := []RowsDTO{*first.Rows}
	for i := range 12 {
		time.Sleep(15 * time.Millisecond)
		var r RowsReplyDTO
		if i%2 == 0 {
			r = h.svc.OpenRows("c1", healthsrc.TaskPods, "pods", fmt.Sprintf("b%d", i))
		} else {
			r = h.svc.RowsBootstrap("a", uint64(i)) // a re-bootstrap answers from the lineage too
		}
		if r.Rows == nil {
			t.Fatalf("reply %d: %+v", i, r)
		}
		replies = append(replies, *r.Rows)
	}
	close(stop)
	wg.Wait()
	time.Sleep(100 * time.Millisecond) // the last broadcast lands
	msgs := h.messages()
	deltas := 0
	for _, m := range msgs {
		if !m.Full {
			deltas++
		}
	}
	if deltas == 0 {
		t.Fatal("churn on a subscribed delta part publishes deltas")
	}
	final, ok := h.o.RowsNow(healthsrc.TaskPods, "pods")
	if !ok {
		t.Fatal("rows now")
	}
	want, _ := toRowsDTO(final)
	for i, r := range replies {
		c := client{}
		if err := c.apply(r); err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if err := c.apply(m); err != nil {
				t.Fatalf("consumer %d (bootstrapped at Ver %d): %v", i, r.Part.Ver, err)
			}
		}
		sameRows(t, c.rows, want.Rows.([]PodRowDTO))
	}
}

// Consumers of one part share one lineage: closing one keeps it, closing
// the last drops it, and the next first subscriber seeds it afresh.
func TestConsumersShareOneLineageUntilTheLastCloses(t *testing.T) {
	h := startPodsObserver(t, 3)
	a := h.svc.OpenRows("c1", healthsrc.TaskPods, "pods", "a")
	b := h.svc.OpenRows("c1", healthsrc.TaskPods, "pods", "b")
	if a.Rows == nil || b.Rows == nil || b.Rows.Part.Ver < a.Rows.Part.Ver {
		t.Fatalf("both open from one lineage: %+v %+v", a, b)
	}
	key := lineageKey{cluster: "c1", incarnation: h.o.Incarnation(), task: healthsrc.TaskPods, part: "pods"}
	h.svc.CloseRows("a")
	if _, ok := h.svc.lineage.current(key); !ok {
		t.Fatal("one consumer left: the lineage stays")
	}
	h.svc.CloseRows("b")
	if _, ok := h.svc.lineage.current(key); ok {
		t.Fatal("the last close drops the lineage")
	}
	c := h.svc.OpenRows("c1", healthsrc.TaskPods, "pods", "c")
	if c.Rows == nil || !c.Rows.Full {
		t.Fatalf("a new first subscriber: %+v", c)
	}
	if cur, ok := h.svc.lineage.current(key); !ok || cur.Part.Ver != c.Rows.Part.Ver {
		t.Fatal("its reply is the lineage's published state")
	}
}
