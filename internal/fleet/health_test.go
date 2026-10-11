package fleet

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

type nopSink struct{}

func (nopSink) Summary(observe.Summary) {}
func (nopSink) Rows(observe.Rows)       {}

// Two state changes whose handoffs interleave in reverse order: the older
// read is held between reading state and handing it over while a newer state
// is set and synced. The Observer must end with the newest word (Codex 2a
// review #3) - an older read can never overwrite a newer one.
func TestHealthHandoffNeverPublishesAnOlderState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := NewClusterConn("x", fake.NewClientset(), metafake.NewSimpleMetadataClient(scheme), nil, nil, clock.Real{}, config.MetricsConfig{})
	c.SetHealthSink(nopSink{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.startHealth(ctx)
	h := c.Health()
	defer func() { cancel(); <-h.Done() }()

	reached, release := make(chan struct{}), make(chan struct{})
	first := true
	c.mu.Lock()
	c.healthSyncHook = func() {
		if first {
			first = false
			close(reached)
			<-release
		}
	}
	c.mu.Unlock()

	doneA := make(chan struct{})
	go func() { c.setState(EvStart, ""); close(doneA) }() // reads Connecting, then is held
	<-reached
	doneB := make(chan struct{})
	go func() { // a newer state, synced while the older handoff is held
		c.mu.Lock()
		c.state, c.reason = Failed, "auth"
		c.mu.Unlock()
		c.syncHealth()
		close(doneB)
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-doneA
	<-doneB
	if got := h.Connection(); got != "unreachable" {
		t.Fatalf("the Observer holds %q; the latest state is Failed (unreachable)", got)
	}
}

func TestConnectionWords(t *testing.T) {
	for st, want := range map[ConnState]string{Synced: "connected", Stale: "stale", Connecting: "connecting", Unconnected: "unreachable", Failed: "unreachable"} {
		if got := connectionWord(st, ""); got != want {
			t.Errorf("%v: %q, want %q", st, got, want)
		}
	}
	if got := connectionWord(Degraded, "flux controller crashlooping"); got != "connected · flux controller crashlooping" {
		t.Errorf("degraded is connected with notes: %q", got)
	}
}
