package healthsrc

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/observe"
)

func event(ns, name, typ string) *corev1.Event {
	return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Type: typ, Reason: "BackOff",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: ns, Name: name}, Count: 3}
}

func startEvents(t *testing.T, typed *fake.Clientset) *sink {
	t.Helper()
	snaps, domains := Events(EventsDeps{Cluster: "c1", Typed: typed})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Snapshots: snaps, Domains: domains, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	return s
}

func TestEventsTaskCountsWarningsAsEvidenceWithABoundedRead(t *testing.T) {
	typed := fake.NewClientset(event("shop", "api", "Warning"), event("shop", "web", "Warning"), event("shop", "ok", "Normal"))
	var mu sync.Mutex
	var opts []metav1.ListOptions
	typed.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		opts = append(opts, a.(k8stesting.ListActionImpl).GetListOptions())
		mu.Unlock()
		return false, nil, nil
	})
	s := startEvents(t, typed)
	waitFor(t, "two Warning events, live, uncapped", func() bool {
		p, ok := nativePart(s, TaskEvents, "warnings")
		return ok && p.Coverage == observe.Live && p.Classifier == "evidence" && p.Evidence != nil && *p.Evidence == 2 &&
			p.Counts == nil && p.Native == nil && len(p.Top) == 0 && !p.Kinds[0].Truncated && p.Kinds[0].CadenceSec == 60
	})
	mu.Lock()
	defer mu.Unlock()
	if len(opts) == 0 || opts[0].FieldSelector != "type=Warning" || opts[0].Limit != EventsLimit {
		t.Fatalf("the read must be bounded and Warning-only: %+v", opts)
	}
}

func TestACappedEventsReadIsTruncatedNeverAQuietCount(t *testing.T) {
	typed := fake.NewClientset()
	typed.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.EventList{ListMeta: metav1.ListMeta{Continue: "more"}, Items: []corev1.Event{*event("shop", "api", "Warning")}}, nil
	})
	s := startEvents(t, typed)
	waitFor(t, "capped and marked truncated", func() bool {
		p, ok := nativePart(s, TaskEvents, "warnings")
		return ok && p.Coverage == observe.Live && *p.Evidence == 1 && p.Kinds[0].Truncated
	})
}
