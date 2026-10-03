package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/argo"
)

type fakeArgoConn struct {
	apps     []argo.App
	projects []argo.Project
	appSets  []argo.ApplicationSet
	listErr  error
	calls    []string
	syncOpts []argo.SyncOptions // full options captured per SyncArgoApp call
}

func (f *fakeArgoConn) ListArgoApps(context.Context) ([]argo.App, error) {
	return f.apps, f.listErr
}
func (f *fakeArgoConn) ListArgoProjects(context.Context) ([]argo.Project, error) {
	return f.projects, f.listErr
}
func (f *fakeArgoConn) ListArgoApplicationSets(context.Context) ([]argo.ApplicationSet, error) {
	return f.appSets, f.listErr
}
func (f *fakeArgoConn) RefreshArgoApp(_ context.Context, ns, name string) error {
	f.calls = append(f.calls, "refresh:"+ns+"/"+name)
	return nil
}
func (f *fakeArgoConn) SyncArgoApp(_ context.Context, ns, name string, opts argo.SyncOptions) error {
	f.calls = append(f.calls, "sync:"+ns+"/"+name+"@"+opts.Revision)
	f.syncOpts = append(f.syncOpts, opts)
	return nil
}

func TestListApplications(t *testing.T) {
	conn := &fakeArgoConn{apps: []argo.App{
		{Namespace: "argocd", Name: "broken-app", SyncStatus: "OutOfSync", HealthStatus: "Degraded", ReconciledAt: time.Unix(1000, 0), Resources: []argo.Resource{{Kind: "Deployment", Namespace: "apps", Name: "api", Status: "OutOfSync"}}},
		{Namespace: "argocd", Name: "ok-app", SyncStatus: "Synced", HealthStatus: "Healthy", AutoSync: true, ApplicationSet: "platform-set"},
	}}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	r := s.ListApplications("c")
	if !r.Available || len(r.Apps) != 2 {
		t.Fatalf("got %+v", r)
	}
	if !r.Apps[0].Broken || r.Apps[0].ReconciledUnix != 1000 {
		t.Fatalf("broken/reconciled mapping wrong: %+v", r.Apps[0])
	}
	if len(r.Apps[0].Resources) != 1 || r.Apps[0].Resources[0].Kind != "Deployment" {
		t.Fatalf("resource mapping wrong: %+v", r.Apps[0].Resources)
	}
	if r.Apps[1].Broken || !r.Apps[1].AutoSync || r.Apps[1].ApplicationSet != "platform-set" {
		t.Fatalf("healthy mapping wrong: %+v", r.Apps[1])
	}
}

func TestListApplicationsNotInstalled(t *testing.T) {
	gr := schema.GroupResource{Group: "argoproj.io", Resource: "applications"}
	conn := &fakeArgoConn{listErr: apierrors.NewNotFound(gr, "")}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	r := s.ListApplications("c")
	if r.Available || r.Message == "" {
		t.Fatalf("missing CRD must read unavailable with message: %+v", r)
	}
}

func TestListApplicationsError(t *testing.T) {
	conn := &fakeArgoConn{listErr: errors.New("boom")}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.ListApplications("c"); r.Available || r.Message != "boom" {
		t.Fatalf("list error must surface: %+v", r)
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
		appSets: []argo.ApplicationSet{{
			Namespace:      "argocd",
			Name:           "platform-set",
			Generators:     []string{"clusters"},
			Project:        "platform",
			DestServer:     "https://kubernetes.default.svc",
			GeneratedApps:  3,
			GeneratedNames: []string{"dev-platform"},
			Healthy:        true,
		}},
	}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.ListProjects("c"); !r.Available || len(r.Projects) != 1 || r.Projects[0].Roles != 2 {
		t.Fatalf("projects: %+v", r)
	} else if len(r.Projects[0].RoleNames) != 2 || len(r.Projects[0].ClusterResourceWhitelist) != 1 {
		t.Fatalf("project detail mapping: %+v", r.Projects[0])
	}
	if r := s.ListApplicationSets("c"); !r.Available || len(r.ApplicationSets) != 1 || r.ApplicationSets[0].GeneratedApps != 3 {
		t.Fatalf("applicationsets: %+v", r)
	} else if r.ApplicationSets[0].DestServer == "" || len(r.ApplicationSets[0].GeneratedNames) != 1 {
		t.Fatalf("applicationset detail mapping: %+v", r.ApplicationSets[0])
	}
}

func TestRefreshAndSync(t *testing.T) {
	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.RefreshApp("c", "argocd", "demo"); !r.OK {
		t.Fatalf("refresh: %+v", r)
	}
	if r := s.SyncApp("c", "argocd", "demo", ArgoSyncOptionsDTO{Revision: "main", Prune: true}); !r.OK {
		t.Fatalf("sync: %+v", r)
	}
	if len(conn.calls) != 2 || conn.calls[0] != "refresh:argocd/demo" || conn.calls[1] != "sync:argocd/demo@main" {
		t.Fatalf("calls: %v", conn.calls)
	}
	miss := NewArgoService(func(string) (ArgoConn, bool) { return nil, false })
	if r := miss.RefreshApp("nope", "ns", "n"); r.OK {
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
	t.Cleanup(func() { SetProtectGuard(nil) })

	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })

	// Unarmed real sync (prune is a real mutation): refused before the conn.
	if r := s.SyncApp("prd-cluster", "argocd", "app", ArgoSyncOptionsDTO{Prune: true}); r.OK || r.Error == "" {
		t.Fatalf("unarmed sync on protected cluster must refuse, got %+v", r)
	}
	if len(conn.calls) != 0 {
		t.Fatalf("refused sync still reached the conn: %v", conn.calls)
	}

	// Armed: passes exactly once.
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if r := s.SyncApp("prd-cluster", "argocd", "app", ArgoSyncOptionsDTO{Prune: true}); !r.OK {
		t.Fatalf("armed sync must pass the gate, got %+v", r)
	}
	// One-shot: the grant was consumed, the next call refuses again.
	if r := s.SyncApp("prd-cluster", "argocd", "app", ArgoSyncOptionsDTO{Prune: true}); r.OK {
		t.Fatal("second sync must be refused (one-shot grant)")
	}
	if len(conn.calls) != 1 {
		t.Fatalf("exactly one sync should have reached the conn, got %v", conn.calls)
	}

	// Dry-run mutates nothing: it succeeds without an arm and without consuming
	// any grant.
	if r := s.SyncApp("prd-cluster", "argocd", "app", ArgoSyncOptionsDTO{DryRun: true}); !r.OK {
		t.Fatalf("dry-run must succeed without a grant, got %+v", r)
	}
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatalf("arm after dry-run: %v", err)
	}
	// The dry-run left the freshly-armed grant untouched, so a real sync passes.
	if r := s.SyncApp("prd-cluster", "argocd", "app", ArgoSyncOptionsDTO{Prune: true}); !r.OK {
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
	t.Cleanup(func() { SetProtectGuard(nil) })

	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })
	if r := s.RefreshApp("prd-cluster", "argocd", "app"); r.OK || r.Error == "" {
		t.Fatalf("unarmed refresh on a protected cluster must refuse, got %+v", r)
	}
	if len(conn.calls) != 0 {
		t.Fatalf("a refused refresh reached the conn: %v", conn.calls)
	}
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatal(err)
	}
	if r := s.RefreshApp("prd-cluster", "argocd", "app"); !r.OK {
		t.Fatalf("armed refresh must pass, got %+v", r)
	}
	if r := s.RefreshApp("prd-cluster", "argocd", "app"); r.OK {
		t.Fatal("the grant is one-shot")
	}
	// Unprotected clusters are not gated.
	if r := s.RefreshApp("dev-cluster", "argocd", "app"); !r.OK {
		t.Fatalf("an unprotected refresh needs no grant, got %+v", r)
	}
}

// TestSyncApp_OptionsThread asserts the prune/force/replace flags thread through
// toArgoSyncOptions to the conn, and default false when the modal omits them.
func TestSyncApp_OptionsThread(t *testing.T) {
	conn := &fakeArgoConn{}
	s := NewArgoService(func(string) (ArgoConn, bool) { return conn, true })

	if r := s.SyncApp("c", "argocd", "app", ArgoSyncOptionsDTO{Prune: true, Force: true, Replace: true}); !r.OK {
		t.Fatalf("sync: %+v", r)
	}
	if r := s.SyncApp("c", "argocd", "app", ArgoSyncOptionsDTO{}); !r.OK {
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
