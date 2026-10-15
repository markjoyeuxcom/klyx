package appbridge

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitops/depgraph"
	"github.com/moomora/klyx/internal/lastwrite"
)

// The Line slice 8b: the bound dependency graph - only a Flux unit starts
// it, the caller's cancellation reaches the walk, and the DTO keeps every
// fact the drawing needs.

type depConn struct {
	fakeGitOpsConn
	g   depgraph.Graph
	ctx context.Context
	got []string
}

func (d *depConn) DependencyGraph(ctx context.Context, kind, ns, name, uid string) depgraph.Graph {
	d.ctx, d.got = ctx, []string{kind, ns, name, uid}
	return d.g
}

func TestDependencyGraphBinding(t *testing.T) {
	at := time.Date(2026, 10, 15, 20, 21, 0, 0, time.UTC)
	w := &lastwrite.Record{Time: at.Add(-5 * time.Minute), Manager: "external-secrets", Operation: "Update"}
	conn := &depConn{g: depgraph.Graph{
		Start: "Kustomization/flux-system/apps", Reads: 3, Incomplete: []string{"depth: not followed past depth 6"},
		Nodes: []depgraph.Node{
			{ID: "Kustomization/flux-system/apps", Ref: depgraph.Ref{Group: depgraph.GroupKustomize, Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}, UID: "u1", ReadAt: at,
				Ready: "False", Reason: "DependencyNotReady", ConditionTime: at, ConditionGeneration: 3, Generation: 4, Failing: true,
				Inventory: &depgraph.Inventory{LastApplied: "main@sha1:abc", Entries: 3, Workloads: 2},
				Git:       &depgraph.GitFacts{URL: "ssh://git@x/y.git", RefKind: "branch", RefName: "main", Applied: "abc", Path: "./apps"}},
			{ID: "GitRepository/flux-system/platform", Ref: depgraph.Ref{Group: depgraph.GroupSource, Kind: "GitRepository", Namespace: "flux-system", Name: "platform"}, UID: "u2",
				Ready: "False", Failing: true, UpstreamFailure: true, ChainEnd: true,
				Supply: []depgraph.Supply{{Field: "spec.secretRef", Secret: depgraph.Ref{Kind: "Secret", Namespace: "flux-system", Name: "key"}, SecretUID: "s1",
					Writes:   lastwrite.Writes{Last: w, LastSpec: w},
					Supplier: &depgraph.Supplier{Ref: depgraph.Ref{Group: depgraph.GroupESO, Kind: "ExternalSecret", Namespace: "flux-system", Name: "key"}, UID: "es-1", Ready: "True"}}}},
			{ID: "stub:1:Kustomization/flux-system/deep", Ref: depgraph.Ref{Kind: "Kustomization", Namespace: "flux-system", Name: "deep"}, Stub: depgraph.StubDepth, Note: "not followed past depth 6"},
		},
		Edges:     []depgraph.Edge{{From: "Kustomization/flux-system/apps", To: "GitRepository/flux-system/platform", Type: depgraph.EdgeSource, Reported: true}},
		Proximity: []depgraph.Pair{{Failing: "Kustomization/flux-system/apps", ConditionTime: at, Object: "Secret/flux-system/key", Write: *w}},
	}}
	s := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)

	ctx, cancel := context.WithCancel(context.Background())
	dto := s.DependencyGraph(ctx, "prd", "Kustomization", "flux-system", "apps", "u1")
	if conn.got[0] != "Kustomization" || conn.got[3] != "u1" {
		t.Fatalf("passed %v", conn.got)
	}
	cancel()
	if conn.ctx.Err() == nil {
		t.Fatal("the caller's cancellation does not reach the walk")
	}
	if dto.Error != "" || dto.Start != "Kustomization/flux-system/apps" || dto.Reads != 3 || len(dto.Nodes) != 3 || len(dto.Edges) != 1 || !dto.Edges[0].Reported || dto.Edges[0].Type != "source" {
		t.Fatalf("dto: %+v", dto)
	}
	n0, n1, n2 := dto.Nodes[0], dto.Nodes[1], dto.Nodes[2]
	if n0.ConditionTime != "2026-10-15T20:21:00Z" || n0.ConditionGeneration != 3 || n0.Generation != 4 || n0.Inventory.Workloads != 2 || n0.Git.Applied != "abc" || n0.Supply == nil {
		t.Fatalf("node 0: %+v", n0)
	}
	if !n1.ChainEnd || !n1.UpstreamFailure || n1.Supply[0].Supplier.UID != "es-1" || n1.Supply[0].LastSpecWrite.Manager != "external-secrets" {
		t.Fatalf("node 1: %+v", n1)
	}
	if n2.Stub != "depth" || n2.Note == "" {
		t.Fatalf("stub: %+v", n2)
	}
	if len(dto.Proximity) != 1 || dto.Proximity[0].Write.Manager != "external-secrets" || dto.Incomplete[0] != "depth: not followed past depth 6" {
		t.Fatalf("proximity / incomplete: %+v %v", dto.Proximity, dto.Incomplete)
	}

	if d := s.DependencyGraph(context.Background(), "prd", "GitRepository", "flux-system", "platform", ""); d.Error == "" || d.Nodes == nil {
		t.Fatalf("a source cannot start a graph: %+v", d)
	}
	none := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, time.Now)
	if d := none.DependencyGraph(context.Background(), "x", "Kustomization", "a", "b", ""); d.Error != "cluster not connected: x" {
		t.Fatalf("not connected: %+v", d)
	}
}
