package fleet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/capability"
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

// lockedHelmRunner counts helm execs and the most ever running at once; the
// Observer and the lens may call it concurrently.
type lockedHelmRunner struct {
	mu       sync.Mutex
	n        int
	inflight int
	max      int
	out      string
	err      error
	delay    time.Duration
}

func (r *lockedHelmRunner) Run(context.Context, ...string) ([]byte, error) {
	r.mu.Lock()
	r.n++
	r.inflight++
	if r.inflight > r.max {
		r.max = r.inflight
	}
	out, err, delay := r.out, r.err, r.delay
	r.mu.Unlock()
	time.Sleep(delay)
	r.mu.Lock()
	r.inflight--
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
func (r *lockedHelmRunner) set(out string, err error) {
	r.mu.Lock()
	r.out, r.err = out, err
	r.mu.Unlock()
}
func (r *lockedHelmRunner) execs() int     { r.mu.Lock(); defer r.mu.Unlock(); return r.n }
func (r *lockedHelmRunner) maxAtOnce() int { r.mu.Lock(); defer r.mu.Unlock(); return r.max }

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
	r.set("["+rel("web", "failed")+","+rel("api", "failed")+"]", nil)
	if _, err := c.HelmReleases(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the lens read reaches the summary", included(2))
	if r.execs() != 2 {
		t.Fatalf("the summary ran its own helm list after the lens read: %d execs", r.execs())
	}

	// Concurrent lens reads (two lenses, the attention summary) are the
	// snapshot's single-flight reads: never two helm lists at once.
	r.mu.Lock()
	r.delay = 30 * time.Millisecond
	r.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.HelmReleases(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if r.maxAtOnce() != 1 || r.execs() > 4 {
		t.Fatalf("lens reads overlapped or did not coalesce: %d at once, %d execs", r.maxAtOnce(), r.execs())
	}

	// A failed lens read is the snapshot's failure: the lens gets it, the
	// summary turns stale with the last good counts retained.
	r.set("", errors.New("helm list: Kubernetes cluster unreachable"))
	if _, err := c.HelmReleases(ctx); err == nil {
		t.Fatal("the lens must see the failure")
	}
	waitUntil(t, "the summary opens its freshness gap", func() bool {
		p, ok := s.part("helm", "releases")
		return ok && p.Coverage == observe.Stale && p.Native.Included == 2 && len(p.Kinds) == 1 && p.Kinds[0].Error != ""
	})
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

func (s *summarySink) task(id string) (observe.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.last.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return observe.Task{}, false
}

// The re-probe keeps controller issues and the network overlay current; a
// failed probe confirms nothing (1c spec §1).
func TestReprobeKeepsControllerIssuesCurrent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	typed := fake.NewClientset()
	// a dynamic client registers the CRD-backed tasks (absent here: the fake
	// serves no groups), so their controller issues have a home
	c := NewClusterConn("x", typed, metafake.NewSimpleMetadataClient(scheme), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{})
	s := &summarySink{}
	c.SetHealthSink(s)
	ctx, cancel := context.WithCancel(context.Background())
	c.startHealth(ctx)
	h := c.Health()
	defer func() { cancel(); <-h.Done() }()
	c.mu.Lock()
	c.state = Synced
	c.mu.Unlock()

	certsDown := capability.Set{CertManager: capability.CertManagerCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "cert-manager installed but cert-manager is not ready"}, Present: true},
		Network: capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "1 GatewayClass(es) exist but no controller has Accepted any"}, GatewayAPIVersion: "v1"}}
	healthy := capability.Set{CertManager: capability.CertManagerCapability{Base: capability.Base{Tier: capability.Healthy}, Present: true, Healthy: true},
		Network: capability.NetworkCapability{Base: capability.Base{Tier: capability.Healthy}, GatewayAPIVersion: "v1"}}
	var mu sync.Mutex
	script := []func() (capability.Set, error){
		func() (capability.Set, error) { return certsDown, nil },
		func() (capability.Set, error) {
			return capability.Set{}, errors.New("discovery: the server is unavailable")
		},
		func() (capability.Set, error) { return healthy, nil },
	}
	step, entered := make(chan struct{}), make(chan struct{}, 1)
	oldDetect, oldEvery := reprobeDetect, capReprobeEvery
	reprobeDetect = func(ctx context.Context, _ *ClusterConn) (capability.Set, error) {
		select { // the previous probe has been fully handled
		case entered <- struct{}{}:
		case <-ctx.Done():
			return capability.Set{}, ctx.Err()
		}
		select {
		case <-step:
		case <-ctx.Done():
			return capability.Set{}, ctx.Err()
		}
		mu.Lock()
		defer mu.Unlock()
		f := script[0]
		if len(script) > 1 {
			script = script[1:]
		}
		return f()
	}
	capReprobeEvery = time.Millisecond
	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go func() { c.reprobeLoop(loopCtx); close(loopDone) }()
	defer func() { // the loop reads the seams: stop it before restoring them
		stopLoop()
		close(step)
		<-loopDone
		reprobeDetect, capReprobeEvery = oldDetect, oldEvery
	}()

	controller := func(task string) *observe.ControllerIssue { tk, _ := s.task(task); return tk.Controller }
	<-entered
	step <- struct{}{}
	waitUntil(t, "the re-probe reports both controllers", func() bool {
		return controller("certs") != nil && controller("gateway") != nil && controller("gateway").State == "not ready"
	})
	if snap := c.Snapshot(); snap.State != Degraded || snap.Reason != certsDown.Network.Reason {
		t.Fatalf("a degraded data plane overlays a Synced cluster: %v %q", snap.State, snap.Reason)
	}
	<-entered
	step <- struct{}{} // the failed probe
	<-entered          // ...has been handled
	time.Sleep(50 * time.Millisecond)
	if controller("certs") == nil || controller("gateway") == nil || c.Snapshot().State != Degraded {
		t.Fatal("a failed probe confirmed nothing, yet the issues or the overlay changed")
	}
	step <- struct{}{} // the healthy probe
	waitUntil(t, "a healthy probe clears both and lifts the overlay", func() bool {
		return controller("certs") == nil && controller("gateway") == nil && c.Snapshot().State == Synced
	})
}
