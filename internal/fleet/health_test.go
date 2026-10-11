package fleet

import (
	"context"
	"sync"
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

type summarySink struct {
	mu   sync.Mutex
	last observe.Summary
}

func (s *summarySink) Summary(x observe.Summary) { s.mu.Lock(); s.last = x; s.mu.Unlock() }
func (s *summarySink) Rows(observe.Rows)         {}
func (s *summarySink) part(task, part string) (observe.Part, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.last.Tasks {
		for _, p := range t.Parts {
			if t.ID == task && p.ID == part {
				return p, true
			}
		}
	}
	return observe.Part{}, false
}

// lockedHelmRunner counts helm execs; the Observer and the lens may call
// it concurrently.
type lockedHelmRunner struct {
	mu  sync.Mutex
	n   int
	out string
}

func (r *lockedHelmRunner) Run(context.Context, ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return []byte(r.out), nil
}
func (r *lockedHelmRunner) set(out string) { r.mu.Lock(); r.out = out; r.mu.Unlock() }
func (r *lockedHelmRunner) execs() int     { r.mu.Lock(); defer r.mu.Unlock(); return r.n }

// The Helm lens's read is the health snapshot's read: one helm list serves
// both, and the summary reflects it with no exec of its own (1c §1.1).
func TestHelmLensReadFeedsTheHealthSnapshot(t *testing.T) {
	rel := func(name, status string) string {
		return `{"name":"` + name + `","namespace":"shop","revision":1,"updated":"2026-10-11T12:00:00Z","status":"` + status + `","chart":"web-1.0.0","app_version":"1"}`
	}
	r := &lockedHelmRunner{out: "[" + rel("web", "failed") + "]"}
	defer withHelmRunner(r)()
	oldDetect := helmDetect
	helmDetect = func() bool { return true }
	defer func() { helmDetect = oldDetect }()

	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := NewClusterConn("x", fake.NewClientset(), metafake.NewSimpleMetadataClient(scheme), nil, nil, clock.Real{}, config.MetricsConfig{})
	c.WithKubeContext("kind-x")
	s := &summarySink{}
	c.SetHealthSink(s)
	ctx, cancel := context.WithCancel(context.Background())
	c.startHealth(ctx)
	h := c.Health()
	defer func() { cancel(); <-h.Done() }()

	included := func(n int) func() bool {
		return func() bool {
			p, ok := s.part("helm", "releases")
			return ok && p.Coverage == observe.Live && p.Native != nil && p.Native.Included == n
		}
	}
	waitUntil(t, "the snapshot's own first read", included(1))
	if r.execs() != 1 {
		t.Fatalf("want one exec for the first read, got %d", r.execs())
	}
	r.set("[" + rel("web", "failed") + "," + rel("api", "failed") + "]")
	if _, err := c.HelmReleases(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the lens read reaches the summary", included(2))
	if r.execs() != 2 {
		t.Fatalf("the summary ran its own helm list after the lens read: %d execs", r.execs())
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}
