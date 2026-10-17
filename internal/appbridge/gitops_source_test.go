package appbridge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

func TestToSourceDTO(t *testing.T) {
	dto := toSourceDTO(flux.Source{
		Kind: flux.GitRepositoryKind, Namespace: "flux-system", Name: "apps",
		Ready: flux.Failed, Reason: "GitOperationFailed", Revision: "main@sha1:abc", URL: "https://x/y",
	})
	if dto.Kind != "GitRepository" || dto.Ready != "Failed" || dto.Reason != "GitOperationFailed" {
		t.Fatalf("dto: %+v", dto)
	}
	if dto.Revision != "main@sha1:abc" || dto.URL != "https://x/y" {
		t.Fatalf("dto fields: %+v", dto)
	}
}

func TestGetResourceDetailEmbedsBoundSource(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "flux-system"}},
		"status":   map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "reason": "BuildFailed"}}},
	}}
	src := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]interface{}{"name": "flux-system", "namespace": "flux-system"},
		"status":   map[string]interface{}{"artifact": map[string]interface{}{"revision": "main@sha1:def"}, "conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}},
	}}
	conn := &fakeGitOpsConn{obj: ks, srcObj: src}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)

	dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps")
	if dto.Source == nil {
		t.Fatal("expected an embedded bound source")
	}
	if dto.Source.Kind != "GitRepository" || dto.Source.Revision != "main@sha1:def" || dto.Source.Ready != "Ready" {
		t.Fatalf("embedded source: %+v", dto.Source)
	}
}

func TestGetResourceDetailEmbedsEvents(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system"},
		"spec":     map[string]interface{}{},
	}}
	conn := &fakeGitOpsConn{obj: ks, events: []workloads.EventSummary{
		{Type: "Warning", Reason: "DriftDetected", Message: "Deployment/x configured", Kind: "Kustomization", Name: "apps"},
	}}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps")
	if len(dto.Events) != 1 || dto.Events[0].Reason != "DriftDetected" || dto.Events[0].Type != "Warning" {
		t.Fatalf("embedded events: %+v", dto.Events)
	}
}

func TestGetResourceDetailNoBoundSource(t *testing.T) {
	// A resource with no sourceRef and no source object: Source stays nil.
	hr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]interface{}{"name": "x", "namespace": "y"},
		"spec":     map[string]interface{}{},
	}}
	conn := &fakeGitOpsConn{obj: hr}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	if dto := svc.GetResourceDetail("x", "HelmRelease", "y", "x"); dto.Source != nil {
		t.Fatalf("expected nil source, got %+v", dto.Source)
	}
}

// Design §5b: a detail carries the read object's identity and as-of time.
func TestGetResourceDetailCarriesIdentity(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system", "uid": "ks-1", "generation": int64(4)},
		"spec":     map[string]interface{}{},
	}}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	conn := &fakeGitOpsConn{obj: ks}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, func() time.Time { return at })
	dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps")
	if dto.UID != "ks-1" || dto.Generation != 4 || dto.AsOfUnix != at.Unix() || dto.Error != "" {
		t.Fatalf("identity: %+v", dto)
	}
}

// A failed read is the request's outcome, keyed to the request: never an
// empty detail, and never an identity.
func TestGetResourceDetailReadErrorIsTheRequestsOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		conn *fakeGitOpsConn
		want string
	}{
		{"forbidden", &fakeGitOpsConn{objErr: errors.New("kustomizations.kustomize.toolkit.fluxcd.io \"apps\" is forbidden")}, "forbidden"},
		{"gone", &fakeGitOpsConn{}, "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := tc.conn
			svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
			dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps")
			if !strings.Contains(dto.Error, tc.want) || dto.Kind != "Kustomization" || dto.Name != "apps" || dto.UID != "" || dto.Conditions != nil {
				t.Fatalf("%+v", dto)
			}
		})
	}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return nil, false }, time.Now)
	if dto := svc.GetResourceDetail("gone", "Kustomization", "ns", "apps"); dto.Error != "cluster not connected" || dto.Name != "apps" {
		t.Fatalf("a cluster miss is an error for the requested key: %+v", dto)
	}
}

// The bound source's absence is never silent: not found and unreadable each
// say so, and neither fails the detail itself.
func TestGetResourceDetailNamesWhyItsSourceIsMissing(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system", "uid": "ks-1"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "flux-system"}},
	}}
	for _, tc := range []struct {
		name string
		conn *fakeGitOpsConn
		want string
	}{
		{"gone", &fakeGitOpsConn{obj: ks}, "bound source GitRepository flux-system/flux-system was not found"},
		{"forbidden", &fakeGitOpsConn{obj: ks, srcErr: errors.New("gitrepositories is forbidden")}, "bound source GitRepository flux-system/flux-system is unverified: gitrepositories is forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := tc.conn
			svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
			dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps")
			if !strings.HasPrefix(dto.SourceWarning, tc.want) || dto.Source != nil || dto.Error != "" || dto.UID != "ks-1" {
				t.Fatalf("%+v", dto)
			}
		})
	}
}

// Coverage S1c §2 and §4: a Kustomization bound to an ExternalArtifact gets
// the artifact's own rule (no artifact is a fault, whatever Ready says),
// its producer as its own line from the shared composition rows, and the
// feature-gate refusal named.
func TestGetResourceDetailBoundArtifact(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "ExternalArtifact", "name": "apps-a"}},
		"status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "reason": "AccessDenied",
			"message": "can't access 'ExternalArtifact/flux-system/apps-a', ExternalArtifact feature gate is disabled"}}},
	}}
	eaOf := func(uid, generator string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "ExternalArtifact",
			"metadata": map[string]interface{}{"name": "apps-a", "namespace": "flux-system", "uid": uid,
				"labels": map[string]interface{}{"source.extensions.fluxcd.io/generator": generator}},
			"spec":   map[string]interface{}{"sourceRef": map[string]interface{}{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "apps"}},
			"status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "reason": "Succeeded"}}},
		}}
	}
	ea := eaOf("uid-ea", "uid-ag")
	failing := health.NativeEval{Included: true, Role: health.Fault, Label: "Failed"}
	gen := &composition.Generator{Common: composition.Common{Kind: "ArtifactGenerator", Namespace: "flux-system", Name: "apps", UID: "uid-ag"}}
	// the shared rows also hold the artifact's predecessor, whose producer
	// was another generator: it lends the artifact read nothing
	old := &healthsrc.ProducerView{State: "verified", Kind: "ArtifactGenerator", Namespace: "flux-system", Name: "old", Label: "Ready", Role: "ok"}
	rows := observe.Rows{Part: observe.Part{Coverage: observe.Live}, Rows: healthsrc.RowSet[healthsrc.CompositionRow]{Rows: []healthsrc.Row[healthsrc.CompositionRow]{
		{Eval: failing, Data: healthsrc.CompositionRow{Generator: gen}},
		{Data: healthsrc.CompositionRow{Artifact: &composition.Artifact{Common: composition.Common{Namespace: "flux-system", Name: "apps-a", UID: "uid-old"}}, Producer: old}}}}}
	conn := &fakeGitOpsConn{obj: ks, srcObj: ea, rows: rows, rowsOK: true}
	svc := NewGitOpsService(func(string) (GitOpsConn, bool) { return conn, true }, time.Now)
	dto := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps")
	if dto.Source == nil || dto.Source.Kind != "ExternalArtifact" || dto.Source.Rule.Label != composition.LabelNoArtifact {
		t.Fatalf("the artifact's own rule: %+v", dto.Source)
	}
	if p := dto.SourceProducer; p == nil || p.State != "verified" || p.Name != "apps" || !p.Failing {
		t.Fatalf("the producer line: %+v", dto.SourceProducer)
	}
	if dto.SourceNote != "kustomize-controller runs without its ExternalArtifact feature gate" {
		t.Fatalf("the feature gate: %q", dto.SourceNote)
	}
	// a replacement recording another generator: its own evidence decides
	// (Codex's S1c implementation review, finding 2)
	conn.srcObj = eaOf("uid-new", "uid-other")
	if p := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps").SourceProducer; p == nil || p.State != composition.ProducerDisagree || p.Label != "" {
		t.Fatalf("a replaced artifact: %+v", p)
	}
	// rows not read: never a health
	conn.srcObj, conn.rowsOK = ea, false
	if p := svc.GetResourceDetail("x", "Kustomization", "flux-system", "apps").SourceProducer; p == nil || p.State != composition.ProducerUnreadable || p.Label != "" {
		t.Fatalf("unread rows: %+v", p)
	}
}
