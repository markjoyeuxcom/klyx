package appbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/argo"
)

type fakeArgoConn struct {
	projects []argo.Project
	listErr  error
	verbErr  error
	calls    []string
	syncOpts []argo.SyncOptions // full options captured per SyncArgoApp call
}

func (f *fakeArgoConn) ListArgoProjects(context.Context) ([]argo.Project, error) {
	return f.projects, f.listErr
}
func (f *fakeArgoConn) RefreshArgoApp(_ context.Context, ns, name, uid string) error {
	f.calls = append(f.calls, "refresh:"+ns+"/"+name+"#"+uid)
	return f.verbErr
}
func (f *fakeArgoConn) SyncArgoApp(_ context.Context, ns, name, uid string, opts argo.SyncOptions) error {
	f.calls = append(f.calls, "sync:"+ns+"/"+name+"#"+uid+"@"+opts.Revision)
	f.syncOpts = append(f.syncOpts, opts)
	return f.verbErr
}

// The rows converters' pure field conversions keep the Argo mapping the
// list endpoints had (1c slice 3 §5.4).
func TestArgoAppFields(t *testing.T) {
	broken := argoAppFields(argo.App{Namespace: "argocd", Name: "broken-app", SyncStatus: "OutOfSync", HealthStatus: "Degraded", ReconciledAt: time.Unix(1000, 0),
		Resources: []argo.Resource{{Kind: "Deployment", Namespace: "apps", Name: "api", Status: "OutOfSync"}}})
	if !broken.Broken || broken.ReconciledUnix != 1000 || len(broken.Resources) != 1 || broken.Resources[0].Kind != "Deployment" {
		t.Fatalf("broken mapping: %+v", broken)
	}
	ok := argoAppFields(argo.App{Namespace: "argocd", Name: "ok-app", SyncStatus: "Synced", HealthStatus: "Healthy", AutoSync: true, ApplicationSet: "platform-set"})
	if ok.Broken || !ok.AutoSync || ok.ApplicationSet != "platform-set" || ok.Conditions == nil || ok.Resources == nil {
		t.Fatalf("healthy mapping: %+v", ok)
	}
	set := argoAppSetFields(argo.ApplicationSet{Namespace: "argocd", Name: "platform-set", Generators: []string{"clusters"}, DestServer: "https://kubernetes.default.svc", GeneratedApps: 3, GeneratedNames: []string{"dev-platform"}, Healthy: true})
	if set.GeneratedApps != 3 || set.DestServer == "" || len(set.GeneratedNames) != 1 || set.Conditions == nil {
		t.Fatalf("applicationset mapping: %+v", set)
	}
}

func TestListProjectsAndApplicationSets(t *testing.T) {
	conn := &fakeArgoConn{
		projects: []argo.Project{{
			Namespace:                "argocd",
			Name:                     "platform",
			SourceRepos:              []string{"https://git"},
			Destinations:             []string{"in-cluster/apps"},
			Roles:                    2,
			RoleNames:                []string{"reader", "syncer"},
			OrphanWarn:               true,
			ClusterResourceWhitelist: []argo.ResourceRule{{Group: "*", Kind: "*"}},
		}},
	}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.ListProjects("c"); !r.Available || len(r.Projects) != 1 || r.Projects[0].Roles != 2 {
		t.Fatalf("projects: %+v", r)
	} else if len(r.Projects[0].RoleNames) != 2 || len(r.Projects[0].ClusterResourceWhitelist) != 1 {
		t.Fatalf("project detail mapping: %+v", r.Projects[0])
	}
}

func TestRefreshAndSync(t *testing.T) {
	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.RefreshApp("c", "argocd", "demo", "uid-demo"); !r.OK {
		t.Fatalf("refresh: %+v", r)
	}
	if r := s.SyncApp("c", "argocd", "demo", "uid-demo", ArgoSyncOptionsDTO{Revision: "main", Prune: true}); !r.OK {
		t.Fatalf("sync: %+v", r)
	}
	if len(conn.calls) != 2 || conn.calls[0] != "refresh:argocd/demo#uid-demo" || conn.calls[1] != "sync:argocd/demo#uid-demo@main" {
		t.Fatalf("calls: %v", conn.calls)
	}
	miss := NewArgoService(func(string) (ArgoConn, bool) { return nil, false })
	if r := miss.RefreshApp("nope", "ns", "n", "uid-n"); r.OK {
		t.Fatal("cluster miss must fail")
	}
}

// TestSyncApp_ProtectedGrant asserts SyncApp consumes the typed-confirmation
// grant on a protected cluster exactly like every other destructive verb: an
// unarmed real sync is refused, an armed one passes exactly once (one-shot),
// and a dry-run mutates nothing so it never touches (or needs) the grant.
func TestSyncApp_ProtectedGrant(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd-cluster", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(defaultTestGuard) })

	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })

	// Unarmed real sync (prune is a real mutation): refused before the conn.
	if r := s.SyncApp("prd-cluster", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{Prune: true}); r.OK || r.Error == "" {
		t.Fatalf("unarmed sync on protected cluster must refuse, got %+v", r)
	}
	if len(conn.calls) != 0 {
		t.Fatalf("refused sync still reached the conn: %v", conn.calls)
	}

	// Armed: passes exactly once.
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if r := s.SyncApp("prd-cluster", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{Prune: true}); !r.OK {
		t.Fatalf("armed sync must pass the gate, got %+v", r)
	}
	// One-shot: the grant was consumed, the next call refuses again.
	if r := s.SyncApp("prd-cluster", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{Prune: true}); r.OK {
		t.Fatal("second sync must be refused (one-shot grant)")
	}
	if len(conn.calls) != 1 {
		t.Fatalf("exactly one sync should have reached the conn, got %v", conn.calls)
	}

	// Dry-run mutates nothing: it succeeds without an arm and without consuming
	// any grant.
	if r := s.SyncApp("prd-cluster", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{DryRun: true}); !r.OK {
		t.Fatalf("dry-run must succeed without a grant, got %+v", r)
	}
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatalf("arm after dry-run: %v", err)
	}
	// The dry-run left the freshly-armed grant untouched, so a real sync passes.
	if r := s.SyncApp("prd-cluster", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{Prune: true}); !r.OK {
		t.Fatalf("dry-run must not consume the grant, got %+v", r)
	}
}

// Review 2026-10-03 (D): refresh patches the Application like Flux's
// reconcile annotation, so it consumes the protected grant too - refused
// unarmed, one-shot when armed.
func TestRefreshApp_ProtectedGrant(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd-cluster", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(defaultTestGuard) })

	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.RefreshApp("prd-cluster", "argocd", "app", "uid-app"); r.OK || r.Error == "" {
		t.Fatalf("unarmed refresh on a protected cluster must refuse, got %+v", r)
	}
	if len(conn.calls) != 0 {
		t.Fatalf("a refused refresh reached the conn: %v", conn.calls)
	}
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatal(err)
	}
	if r := s.RefreshApp("prd-cluster", "argocd", "app", "uid-app"); !r.OK {
		t.Fatalf("armed refresh must pass, got %+v", r)
	}
	if r := s.RefreshApp("prd-cluster", "argocd", "app", "uid-app"); r.OK {
		t.Fatal("the grant is one-shot")
	}
	// Unprotected clusters are not gated.
	if r := s.RefreshApp("dev-cluster", "argocd", "app", "uid-app"); !r.OK {
		t.Fatalf("an unprotected refresh needs no grant, got %+v", r)
	}
}

// TestSyncApp_OptionsThread asserts the prune/force/replace flags thread through
// toArgoSyncOptions to the conn, and default false when the modal omits them.
func TestSyncApp_OptionsThread(t *testing.T) {
	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })

	if r := s.SyncApp("c", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{Prune: true, Force: true, Replace: true}); !r.OK {
		t.Fatalf("sync: %+v", r)
	}
	if r := s.SyncApp("c", "argocd", "app", "uid-app", ArgoSyncOptionsDTO{}); !r.OK {
		t.Fatalf("sync (defaults): %+v", r)
	}
	if len(conn.syncOpts) != 2 {
		t.Fatalf("want 2 captured sync options, got %d", len(conn.syncOpts))
	}
	if got := conn.syncOpts[0]; !got.Prune || !got.Force || !got.Replace {
		t.Fatalf("flags must thread through: %+v", got)
	}
	if got := conn.syncOpts[1]; got.Prune || got.Force || got.Replace {
		t.Fatalf("omitted flags must default false: %+v", got)
	}
}

// Design §5b: the Argo verbs carry the Application the operator armed, and a
// refused replacement is the verb's error.
func TestArgoVerbsCarryTheArmedUID(t *testing.T) {
	conn := &fakeArgoConn{verbErr: fmt.Errorf("refresh Application argocd/demo: %w - nothing was written", fleet.ErrReplaced)}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.RefreshApp("c", "argocd", "demo", "uid-A"); r.OK || !strings.Contains(r.Error, "replaced since the action was armed") {
		t.Fatalf("refresh: %+v", r)
	}
	if r := s.SyncApp("c", "argocd", "demo", "uid-A", ArgoSyncOptionsDTO{}); r.OK || !strings.Contains(r.Error, "replaced since the action was armed") {
		t.Fatalf("sync: %+v", r)
	}
	if conn.calls[0] != "refresh:argocd/demo#uid-A" || conn.calls[1] != "sync:argocd/demo#uid-A@" {
		t.Fatalf("calls: %v", conn.calls)
	}
}
