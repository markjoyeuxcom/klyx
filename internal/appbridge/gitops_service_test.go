package appbridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/workloads"
)

type fakeGitOpsConn struct {
	mu     sync.Mutex
	opened int
	closed int
	res    []flux.Resource
	obj    *unstructured.Unstructured
	srcObj *unstructured.Unstructured
	srcs   []flux.Source
	events []workloads.EventSummary

	fluxAvailable bool
	diff          fluxcli.DiffResult
	diffErr       error

	sourceURL string

	reconcileErr   error
	suspendErr     error
	lastSuspend    bool
	withSource     bool
	reconcileCalls int // Reconcile + ReconcileWithSource invocations, for gate tests
	suspendCalls   int
}

func (f *fakeGitOpsConn) OpenGitOps()  { f.mu.Lock(); f.opened++; f.mu.Unlock() }
func (f *fakeGitOpsConn) CloseGitOps() { f.mu.Lock(); f.closed++; f.mu.Unlock() }
func (f *fakeGitOpsConn) GitOpsResources() []flux.Resource {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res
}

func (f *fakeGitOpsConn) GitOpsObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	if f.obj == nil {
		return nil, false
	}
	return f.obj, true
}

func (f *fakeGitOpsConn) GitOpsSources() []flux.Source      { return f.srcs }
func (f *fakeGitOpsConn) GitOpsWatchHealth() (bool, string) { return true, "" }
func (f *fakeGitOpsConn) GitOpsSourceObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	if f.srcObj == nil {
		return nil, false
	}
	return f.srcObj, true
}
func (f *fakeGitOpsConn) FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error) {
	return f.events, nil
}
func (f *fakeGitOpsConn) FluxAvailable() bool { return f.fluxAvailable }
func (f *fakeGitOpsConn) FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error) {
	return f.diff, f.diffErr
}

func (f *fakeGitOpsConn) Reconcile(ctx context.Context, kind, ns, name string) error {
	f.mu.Lock()
	f.reconcileCalls++
	f.mu.Unlock()
	return f.reconcileErr
}
func (f *fakeGitOpsConn) ReconcileWithSource(ctx context.Context, kind, ns, name string) error {
	f.mu.Lock()
	f.withSource = true
	f.reconcileCalls++
	f.mu.Unlock()
	return f.reconcileErr
}
func (f *fakeGitOpsConn) SetSuspend(ctx context.Context, kind, ns, name string, suspend bool) error {
	f.mu.Lock()
	f.lastSuspend = suspend
	f.suspendCalls++
	f.mu.Unlock()
	return f.suspendErr
}

func (f *fakeGitOpsConn) SourceURL(ctx context.Context, kind, ns, name string) (string, bool) {
	if f.sourceURL == "" {
		return "", false
	}
	return f.sourceURL, true
}

func (f *fakeGitOpsConn) GitOpsSummaryFlux(ctx context.Context) (bool, int, int, int, error) {
	return false, 0, 0, 0, nil
}

func TestResolveGitLinkDeepLink(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "app", "namespace": "flux-system"},
		"kind":     "Kustomization",
		"spec": map[string]interface{}{
			"path":      "./apps/x",
			"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "flux-system"},
		},
		"status": map[string]interface{}{"lastAppliedRevision": "main@sha1:abc"},
	}}
	conn := &fakeGitOpsConn{obj: ks, sourceURL: "https://gitlab.com/org/repo.git"}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)

	link := svc.ResolveGitLink("x", "Kustomization", "flux-system", "app")
	if !link.IsDeepLink || link.URL != "https://gitlab.com/org/repo/-/tree/main/apps/x" {
		t.Fatalf("deep link: %+v", link)
	}
}

func TestResolveGitLinkNonKustomizationIsEmpty(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	if link := svc.ResolveGitLink("x", "HelmRelease", "ns", "app"); link.URL != "" || link.IsDeepLink {
		t.Fatalf("HelmRelease must be empty, got %+v", link)
	}
}

func TestResolveGitLinkNoSourceURLIsEmpty(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "app", "namespace": "flux-system"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "src"}},
	}}
	conn := &fakeGitOpsConn{obj: ks} // sourceURL empty -> SourceURL returns ok=false
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	if link := svc.ResolveGitLink("x", "Kustomization", "flux-system", "app"); link.URL != "" || link.IsDeepLink {
		t.Fatalf("want empty link when source url missing, got %+v", link)
	}
}

func TestGitOpsServiceOpenEmitsAndCloseStops(t *testing.T) {
	conn := &fakeGitOpsConn{res: []flux.Resource{
		{Kind: flux.KustomizationKind, Name: "flux-system", Ready: flux.Ready},
	}}
	lookup := func(name string) (GitOpsConn, bool) {
		if name == "x" {
			return conn, true
		}
		return nil, false
	}
	em := &fakeEmitter{}
	svc := NewGitOpsService(lookup, em, func() time.Time { return time.Now() }, 10*time.Millisecond)

	svc.Open("x")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		em.mu.Lock()
		n := em.events
		em.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	em.mu.Lock()
	got := em.events
	em.mu.Unlock()
	if got < 1 {
		t.Fatal("expected at least one gitops:updated emit")
	}

	svc.Close("x")
	conn.mu.Lock()
	opened, closed := conn.opened, conn.closed
	conn.mu.Unlock()
	if opened != 1 || closed != 1 {
		t.Fatalf("want opened=1 closed=1, got %d/%d", opened, closed)
	}
}

func TestGitOpsServiceOpenUnknownClusterNoop(t *testing.T) {
	lookup := func(string) (GitOpsConn, bool) { return nil, false }
	svc := NewGitOpsService(lookup, &fakeEmitter{}, time.Now, time.Second)
	svc.Open("ghost") // must not panic
	svc.Close("ghost")
}

func TestReconcileActionResult(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	if r := svc.Reconcile("x", "Kustomization", "flux-system", "app"); !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
}

func TestReconcileActionSurfacesError(t *testing.T) {
	conn := &fakeGitOpsConn{reconcileErr: errors.New("forbidden: cannot patch")}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	r := svc.Reconcile("x", "Kustomization", "flux-system", "app")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

func TestReconcileWithSourceActionResult(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	if r := svc.ReconcileWithSource("x", "Kustomization", "flux-system", "app"); !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if !conn.withSource {
		t.Fatal("expected ReconcileWithSource to reach the conn")
	}
}

func TestFluxDiffUnavailableHidesAffordance(t *testing.T) {
	conn := &fakeGitOpsConn{fluxAvailable: false}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	dto := svc.FluxDiff("x", "flux-system", "app", "")
	if dto.Available {
		t.Fatalf("want Available=false when flux missing, got %+v", dto)
	}
}

func TestFluxDiffReturnsChanges(t *testing.T) {
	conn := &fakeGitOpsConn{fluxAvailable: true, diff: fluxcli.DiffResult{HasChanges: true, Output: "± spec.replicas: 2 -> 3"}}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	dto := svc.FluxDiff("x", "flux-system", "app", "./apps")
	if !dto.Available || !dto.HasChanges || dto.Output == "" || dto.Error != "" {
		t.Fatalf("want available diff with changes, got %+v", dto)
	}
}

func TestReconcileWithSourceUnknownClusterIsError(t *testing.T) {
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, &fakeEmitter{}, time.Now, time.Second)
	if r := svc.ReconcileWithSource("ghost", "Kustomization", "n", "x"); r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
}

func TestSetSuspendActionPassesFlag(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)
	if r := svc.SetSuspend("x", "Kustomization", "flux-system", "app", true); !r.OK {
		t.Fatalf("want OK, got %+v", r)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if !conn.lastSuspend {
		t.Fatal("expected suspend=true to reach the conn")
	}
}

func TestActionUnknownClusterIsError(t *testing.T) {
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, &fakeEmitter{}, time.Now, time.Second)
	if r := svc.Reconcile("ghost", "Kustomization", "n", "x"); r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
}

// TestProtectGuard_GatesFluxVerbs is the regression for the P0: Reconcile,
// ReconcileWithSource and SetSuspend must consume the typed-confirmation grant
// on a protected cluster exactly like every other destructive verb, so a
// frontend bug or a hostile webview call can never drive a Flux mutation on a
// protected cluster without the confirmation - and no un-consumed grant can
// linger to authorize an unrelated later action. Before the fix these three
// verbs never called checkProtected at all.
func TestProtectGuard_GatesFluxVerbs(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd-cluster", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(nil) })

	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, &fakeEmitter{}, time.Now, time.Second)

	callCounts := func() (reconcile, suspend int) {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.reconcileCalls, conn.suspendCalls
	}

	// Each verb: unarmed refuses without reaching the conn; armed passes exactly
	// once; the grant is one-shot.
	verbs := []struct {
		name string
		call func() ActionResultDTO
	}{
		{"Reconcile", func() ActionResultDTO { return svc.Reconcile("prd-cluster", "Kustomization", "flux-system", "app") }},
		{"ReconcileWithSource", func() ActionResultDTO {
			return svc.ReconcileWithSource("prd-cluster", "Kustomization", "flux-system", "app")
		}},
		{"SetSuspend", func() ActionResultDTO {
			return svc.SetSuspend("prd-cluster", "Kustomization", "flux-system", "app", true)
		}},
	}

	for _, v := range verbs {
		beforeR, beforeS := callCounts()

		// Unarmed: refused, and the mutation never reaches the conn.
		if r := v.call(); r.OK || r.Error == "" {
			t.Fatalf("%s: unarmed on protected cluster must refuse, got %+v", v.name, r)
		}
		if afterR, afterS := callCounts(); afterR != beforeR || afterS != beforeS {
			t.Fatalf("%s: refused verb still reached the conn (reconcile %d->%d, suspend %d->%d)", v.name, beforeR, afterR, beforeS, afterS)
		}

		// Armed: passes exactly once.
		if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
			t.Fatalf("%s: arm: %v", v.name, err)
		}
		if r := v.call(); !r.OK {
			t.Fatalf("%s: armed verb must pass the gate, got %+v", v.name, r)
		}

		// One-shot: the grant was consumed, the next call refuses again.
		if r := v.call(); r.OK {
			t.Fatalf("%s: second call must be refused (one-shot grant)", v.name)
		}
	}

	// FluxDiff is dry-run and must stay ungated even on a protected cluster: with
	// no grant armed it still runs (Available), rather than being refused.
	conn.fluxAvailable = true
	if dto := svc.FluxDiff("prd-cluster", "flux-system", "app", ""); !dto.Available {
		t.Fatalf("FluxDiff must stay ungated on a protected cluster, got %+v", dto)
	}
}
