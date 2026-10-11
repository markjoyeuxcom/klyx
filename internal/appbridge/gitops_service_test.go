package appbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/workloads"
)

type fakeGitOpsConn struct {
	mu     sync.Mutex
	obj    *unstructured.Unstructured // the Kustomization/HelmRelease FluxObject returns
	objErr error
	srcObj *unstructured.Unstructured // the source FluxObject returns
	srcErr error
	events []workloads.EventSummary

	fluxAvailable bool
	diff          fluxcli.DiffResult
	diffErr       error

	sourceURL string // a GitRepository with this spec.url, when srcObj is unset

	reconcileErr   error
	suspendErr     error
	lastSuspend    bool
	withSource     bool
	reconcileCalls int // Reconcile + ReconcileWithSource invocations, for gate tests
	suspendCalls   int
	armedUID       string // the uid the last verb was armed against
}

// FluxObject: the direct read (design §5b) - a missing object is NotFound,
// an injected error is returned as is.
func (f *fakeGitOpsConn) FluxObject(_ context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: kind}, name)
	switch kind {
	case "Kustomization", "HelmRelease":
		if f.objErr != nil {
			return nil, f.objErr
		}
		if f.obj == nil {
			return nil, notFound
		}
		return f.obj, nil
	}
	switch {
	case f.srcErr != nil:
		return nil, f.srcErr
	case f.srcObj != nil:
		return f.srcObj, nil
	case f.sourceURL != "":
		return &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"url": f.sourceURL}}}, nil
	}
	return nil, notFound
}

func (f *fakeGitOpsConn) FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error) {
	return f.events, nil
}
func (f *fakeGitOpsConn) FluxAvailable() bool { return f.fluxAvailable }
func (f *fakeGitOpsConn) FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error) {
	return f.diff, f.diffErr
}

func (f *fakeGitOpsConn) Reconcile(ctx context.Context, kind, ns, name, uid string) error {
	f.mu.Lock()
	f.reconcileCalls++
	f.armedUID = uid
	f.mu.Unlock()
	return f.reconcileErr
}
func (f *fakeGitOpsConn) ReconcileWithSource(ctx context.Context, kind, ns, name, uid string) error {
	f.mu.Lock()
	f.withSource = true
	f.armedUID = uid
	f.reconcileCalls++
	f.mu.Unlock()
	return f.reconcileErr
}
func (f *fakeGitOpsConn) SetSuspend(ctx context.Context, kind, ns, name, uid string, suspend bool) error {
	f.mu.Lock()
	f.lastSuspend = suspend
	f.armedUID = uid
	f.suspendCalls++
	f.mu.Unlock()
	return f.suspendErr
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
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)

	link := svc.ResolveGitLink("x", "Kustomization", "flux-system", "app")
	if !link.IsDeepLink || link.URL != "https://gitlab.com/org/repo/-/tree/main/apps/x" {
		t.Fatalf("deep link: %+v", link)
	}
}

func TestResolveGitLinkNonKustomizationIsEmpty(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	if link := svc.ResolveGitLink("x", "HelmRelease", "ns", "app"); link.URL != "" || link.IsDeepLink {
		t.Fatalf("HelmRelease must be empty, got %+v", link)
	}
}

func TestResolveGitLinkNoSourceURLIsEmpty(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "app", "namespace": "flux-system"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "src"}},
	}}
	repo := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	conn := &fakeGitOpsConn{obj: ks, srcObj: repo} // the source has no spec.url
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	if link := svc.ResolveGitLink("x", "Kustomization", "flux-system", "app"); link.URL != "" || link.IsDeepLink {
		t.Fatalf("want empty link when source url missing, got %+v", link)
	}
}

func TestReconcileActionResult(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	if r := svc.Reconcile("x", "Kustomization", "flux-system", "app", "uid-app"); !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
}

func TestReconcileActionSurfacesError(t *testing.T) {
	conn := &fakeGitOpsConn{reconcileErr: errors.New("forbidden: cannot patch")}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	r := svc.Reconcile("x", "Kustomization", "flux-system", "app", "uid-app")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

func TestReconcileWithSourceActionResult(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	if r := svc.ReconcileWithSource("x", "Kustomization", "flux-system", "app", "uid-app"); !r.OK || r.Error != "" {
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
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	dto := svc.FluxDiff("x", "flux-system", "app", "")
	if dto.Available {
		t.Fatalf("want Available=false when flux missing, got %+v", dto)
	}
}

func TestFluxDiffReturnsChanges(t *testing.T) {
	conn := &fakeGitOpsConn{fluxAvailable: true, diff: fluxcli.DiffResult{HasChanges: true, Output: "± spec.replicas: 2 -> 3"}}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	dto := svc.FluxDiff("x", "flux-system", "app", "./apps")
	if !dto.Available || !dto.HasChanges || dto.Output == "" || dto.Error != "" {
		t.Fatalf("want available diff with changes, got %+v", dto)
	}
}

func TestReconcileWithSourceUnknownClusterIsError(t *testing.T) {
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, time.Now)
	if r := svc.ReconcileWithSource("ghost", "Kustomization", "n", "x", "uid-x"); r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
}

func TestSetSuspendActionPassesFlag(t *testing.T) {
	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	if r := svc.SetSuspend("x", "Kustomization", "flux-system", "app", "uid-app", true); !r.OK {
		t.Fatalf("want OK, got %+v", r)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if !conn.lastSuspend {
		t.Fatal("expected suspend=true to reach the conn")
	}
}

func TestActionUnknownClusterIsError(t *testing.T) {
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, time.Now)
	if r := svc.Reconcile("ghost", "Kustomization", "n", "x", "uid-x"); r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
}

// TestProtectGuard_GatesFluxVerbs is the regression for the P0: Reconcile,
// ReconcileWithSource and SetSuspend must consume the typed-confirmation grant
// on a protected cluster exactly like every other destructive verb, so a
// frontend bug can never drive a Flux mutation on a protected cluster without
// the confirmation - and no un-consumed grant can
// linger to authorize an unrelated later action. Before the fix these three
// verbs never called checkProtected at all.
func TestProtectGuard_GatesFluxVerbs(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd-cluster", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(nil) })

	conn := &fakeGitOpsConn{}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)

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
		{"Reconcile", func() ActionResultDTO { return svc.Reconcile("prd-cluster", "Kustomization", "flux-system", "app", "uid-app") }},
		{"ReconcileWithSource", func() ActionResultDTO {
			return svc.ReconcileWithSource("prd-cluster", "Kustomization", "flux-system", "app", "uid-app")
		}},
		{"SetSuspend", func() ActionResultDTO {
			return svc.SetSuspend("prd-cluster", "Kustomization", "flux-system", "app", "uid-app", true)
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

// Design §5b: a read that fails on the way to the link says so - an
// unreadable or missing object is never "no Git source".
func TestResolveGitLinkReadFailuresAreErrors(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "app", "namespace": "flux-system"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "src"}},
	}}
	for _, tc := range []struct {
		name string
		conn *fakeGitOpsConn
		want string
	}{
		{"kustomization forbidden", &fakeGitOpsConn{objErr: errors.New("kustomizations is forbidden")}, "Kustomization flux-system/app: kustomizations is forbidden"},
		{"kustomization gone", &fakeGitOpsConn{}, "Kustomization flux-system/app: "},
		{"source forbidden", &fakeGitOpsConn{obj: ks, srcErr: errors.New("gitrepositories is forbidden")}, "GitRepository flux-system/src: gitrepositories is forbidden"},
		{"source gone", &fakeGitOpsConn{obj: ks}, "GitRepository flux-system/src: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := tc.conn
			svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
			link := svc.ResolveGitLink("x", "Kustomization", "flux-system", "app")
			if !strings.HasPrefix(link.Error, tc.want) || link.URL != "" || link.CopyText != "" {
				t.Fatalf("link %+v, want error prefix %q", link, tc.want)
			}
		})
	}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, time.Now)
	if link := svc.ResolveGitLink("gone", "Kustomization", "ns", "app"); link.Error != "cluster not connected" {
		t.Fatalf("a cluster miss is an error: %+v", link)
	}
}

// Design §5b (Codex review of 7d389eb #1): each verb carries the UID it was
// armed against to the conn, and a refused replacement is the verb's error.
func TestFluxVerbsCarryTheArmedUID(t *testing.T) {
	for name, call := range map[string]func(*GitOpsService) ActionResultDTO{
		"reconcile":   func(s *GitOpsService) ActionResultDTO { return s.Reconcile("x", "Kustomization", "flux-system", "app", "uid-A") },
		"with-source": func(s *GitOpsService) ActionResultDTO { return s.ReconcileWithSource("x", "Kustomization", "flux-system", "app", "uid-A") },
		"suspend":     func(s *GitOpsService) ActionResultDTO { return s.SetSuspend("x", "Kustomization", "flux-system", "app", "uid-A", true) },
	} {
		t.Run(name, func(t *testing.T) {
			conn := &fakeGitOpsConn{}
			svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
			if r := call(svc); !r.OK || conn.armedUID != "uid-A" {
				t.Fatalf("result %+v, armed %q", r, conn.armedUID)
			}
			conn.reconcileErr, conn.suspendErr = fleet.ErrReplaced, fleet.ErrReplaced
			if r := call(svc); r.OK || !strings.Contains(r.Error, "replaced since the action was armed") {
				t.Fatalf("a refused replacement is the verb's error: %+v", r)
			}
		})
	}
}
