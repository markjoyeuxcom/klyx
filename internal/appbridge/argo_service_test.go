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
