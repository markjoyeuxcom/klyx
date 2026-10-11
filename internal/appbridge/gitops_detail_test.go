package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/workloads"
)

func TestToDetailDTOApplyFailed(t *testing.T) {
	d := flux.Detail{
		Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "x",
		AppliedRevision: "main@a", AttemptedRevision: "main@b",
		Conditions: []flux.Condition{{Type: "Ready", Status: "False", Reason: "BuildFailed", Message: "boom"}},
		Inventory:  []flux.InventoryEntry{{Namespace: "ns", Name: "cm", Kind: "ConfigMap", Version: "v1"}},
	}
	dto := toDetailDTO(d)
	if !dto.ApplyFailed {
		t.Fatal("want ApplyFailed when attempted != applied")
	}
	if len(dto.Conditions) != 1 || dto.Conditions[0].Reason != "BuildFailed" {
		t.Fatalf("conditions: %+v", dto.Conditions)
	}
	if len(dto.Inventory) != 1 || dto.Inventory[0].Kind != "ConfigMap" {
		t.Fatalf("inventory: %+v", dto.Inventory)
	}
}

func TestToDetailDTOApplyOK(t *testing.T) {
	dto := toDetailDTO(flux.Detail{Kind: flux.KustomizationKind, AppliedRevision: "main@a", AttemptedRevision: "main@a"})
	if dto.ApplyFailed {
		t.Fatal("want ApplyFailed false when equal")
	}
}

func TestToDetailDTOApplyFailedHelmReleaseNeverFlags(t *testing.T) {
	// HelmRelease v2 has no lastAppliedRevision but does have lastAttemptedRevision,
	// so the attempted!=applied heuristic must NOT fire for HelmReleases.
	d := flux.Detail{
		Kind: flux.HelmReleaseKind, Namespace: "argocd", Name: "argo-cd",
		AppliedRevision: "", AttemptedRevision: "9.5.21",
	}
	if toDetailDTO(d).ApplyFailed {
		t.Fatal("HelmRelease must never report ApplyFailed from revision mismatch")
	}
}

func TestToDetailDTOReasonAndDependsOn(t *testing.T) {
	d := flux.Detail{
		Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "apps",
		Reason:    "DependencyNotReady",
		DependsOn: []flux.DependencyRef{{Namespace: "flux-system", Name: "infra"}, {Namespace: "data", Name: "db"}},
	}
	dto := toDetailDTO(d)
	if dto.Reason != "DependencyNotReady" {
		t.Fatalf("reason: %q", dto.Reason)
	}
	if len(dto.DependsOn) != 2 || dto.DependsOn[1].Namespace != "data" || dto.DependsOn[1].Name != "db" {
		t.Fatalf("dependsOn: %+v", dto.DependsOn)
	}
}

// ---- GetGitOpsSummary tests -----------------------------------------------

type fakeGitOpsSummaryConn struct {
	present   bool
	total     int
	notReady  int
	suspended int
	err       error
}

func (f *fakeGitOpsSummaryConn) FluxObject(context.Context, string, string, string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not used")
}
func (f *fakeGitOpsSummaryConn) Reconcile(ctx context.Context, kind, ns, name string) error {
	return nil
}
func (f *fakeGitOpsSummaryConn) ReconcileWithSource(ctx context.Context, kind, ns, name string) error {
	return nil
}
func (f *fakeGitOpsSummaryConn) FluxEvents(ctx context.Context, kind, ns, name string) ([]workloads.EventSummary, error) {
	return nil, nil
}
func (f *fakeGitOpsSummaryConn) FluxAvailable() bool { return false }
func (f *fakeGitOpsSummaryConn) FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error) {
	return fluxcli.DiffResult{}, nil
}
func (f *fakeGitOpsSummaryConn) SetSuspend(ctx context.Context, kind, ns, name string, suspend bool) error {
	return nil
}
func (f *fakeGitOpsSummaryConn) GitOpsSummaryFlux(ctx context.Context) (bool, int, int, int, error) {
	return f.present, f.total, f.notReady, f.suspended, f.err
}

func newSummarySvc(conn GitOpsConn) *GitOpsService {
	lookup := func(name string) (GitOpsConn, bool) {
		if name == "x" {
			return conn, true
		}
		return nil, false
	}
	return NewGitOpsService(lookup, time.Now)
}

func TestGetGitOpsSummaryMissIsEmpty(t *testing.T) {
	svc := newSummarySvc(&fakeGitOpsSummaryConn{present: true, total: 5})
	dto := svc.GetGitOpsSummary("ghost") // unknown cluster
	if dto.FluxPresent || dto.Total != 0 {
		t.Fatalf("want zero DTO for unknown cluster, got %+v", dto)
	}
}

func TestGetGitOpsSummaryMapsValues(t *testing.T) {
	conn := &fakeGitOpsSummaryConn{present: true, total: 3, notReady: 1, suspended: 1}
	svc := newSummarySvc(conn)
	dto := svc.GetGitOpsSummary("x")
	if !dto.FluxPresent || dto.Total != 3 || dto.NotReady != 1 || dto.Suspended != 1 {
		t.Fatalf("mapping wrong: %+v", dto)
	}
}

func TestGetGitOpsSummaryAbsentFlux(t *testing.T) {
	conn := &fakeGitOpsSummaryConn{present: false}
	svc := newSummarySvc(conn)
	dto := svc.GetGitOpsSummary("x")
	if dto.FluxPresent {
		t.Fatalf("want FluxPresent=false when flux absent, got %+v", dto)
	}
}

func TestGetGitOpsSummaryErrorIsEmpty(t *testing.T) {
	conn := &fakeGitOpsSummaryConn{present: true, err: errors.New("forbidden")}
	svc := newSummarySvc(conn)
	dto := svc.GetGitOpsSummary("x")
	if dto.FluxPresent || dto.Total != 0 {
		t.Fatalf("want zero DTO on error, got %+v", dto)
	}
}

// ---- GetResourceDetail tests -----------------------------------------------

func TestGetResourceDetailReadsConn(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "flux-system", "namespace": "flux-system"},
		"status": map[string]interface{}{
			"lastAppliedRevision": "main@a", "lastAttemptedRevision": "main@a",
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
		},
	}}
	conn := &fakeGitOpsConn{obj: obj}
	lookup := func(name string) (GitOpsConn, bool) {
		if name == "x" {
			return conn, true
		}
		return nil, false
	}
	svc := NewGitOpsService(lookup, func() time.Time { return time.Now() })
	dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "flux-system")
	if dto.Name != "flux-system" || dto.Kind != "Kustomization" {
		t.Fatalf("detail: %+v", dto)
	}
	// an unknown cluster is an error for the requested key, never an
	// empty detail (design §5b)
	miss := svc.GetResourceDetail("ghost", "Kustomization", "a", "b")
	if miss.Error != "cluster not connected" || miss.Name != "b" || miss.UID != "" {
		t.Fatalf("want the request's error for an unknown cluster, got %+v", miss)
	}
}
