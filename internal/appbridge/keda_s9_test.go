package appbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
)

const s9Needle = "S9needleQ7"

func s9Objects() (so, ta *unstructured.Unstructured) {
	so = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
		"metadata": map[string]any{"namespace": "team", "name": "web", "uid": "so-1"},
		"spec": map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{map[string]any{"type": "prometheus",
			"metadata":          map[string]any{"serverAddress": "http://" + s9Needle + ".svc", "query": s9Needle, "password": s9Needle},
			"authenticationRef": map[string]any{"name": "ta"}}}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	ta = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "TriggerAuthentication",
		"metadata": map[string]any{"namespace": "team", "name": "ta", "uid": "ta-1"},
		"spec": map[string]any{"secretTargetRef": []any{map[string]any{"parameter": "p", "name": "secret-" + s9Needle, "key": "k"}},
			"hashiCorpVault": map[string]any{"address": "https://" + s9Needle, "authentication": "token", "credential": map[string]any{"token": s9Needle}},
			"podIdentity":    map[string]any{"provider": "aws", "roleArn": "arn:" + s9Needle}}}}
	return so, ta
}

// Coverage S9 §9: trigger metadata, the Vault token, an authentication's
// reference names, details and role never reach chat or AI Explain.
func TestS9LiteralNeedles(t *testing.T) {
	so, ta := s9Objects()
	for _, c := range []struct {
		gvr schema.GroupVersionResource
		u   *unstructured.Unstructured
	}{{schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}, so},
		{schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "triggerauthentications"}, ta}} {
		b, _ := json.Marshal(chatProject(c.gvr, c.u))
		if strings.Contains(string(b), s9Needle) {
			t.Fatalf("chat projection of %s disclosed: %s", c.gvr.Resource, b)
		}
	}
	project := func(key string, v any) string {
		b, _ := json.Marshal(v)
		var m any
		_ = json.Unmarshal(b, &m)
		out, _ := json.Marshal((&scrubber{}).project(m, aiEvidenceFields[key]))
		return string(out)
	}
	pso := keda.ProjectScaledObject(so)
	soRow := healthsrc.Row[healthsrc.ScaledObjectRow]{Data: healthsrc.ScaledObjectRow{ScaledObject: pso,
		Verdict: keda.ScaledObjectRule(pso, keda.SOInputs{Now: time.Now(), HPA: keda.HPAEvidence{Current: true, Complete: true}})}}
	authRow := healthsrc.Row[healthsrc.AuthRow]{Data: healthsrc.AuthRow{Auth: keda.ProjectAuth(ta, false)}}
	for key, v := range map[string]any{healthsrc.TaskKEDA + "/scaledobjects": scaledObjectDTO(soRow), healthsrc.TaskKEDA + "/authentications": kedaAuthDTO(authRow)} {
		if s := project(key, v); strings.Contains(s, s9Needle) {
			t.Fatalf("AI evidence %s disclosed: %s", key, s)
		}
	}
	// the DTOs themselves carry no metadata or token at all
	b, _ := json.Marshal(scaledObjectDTO(soRow))
	if strings.Contains(string(b), s9Needle) {
		t.Fatalf("the ScaledObject DTO carries trigger metadata: %s", b)
	}
	b, _ = json.Marshal(kedaAuthDTO(authRow))
	if strings.Count(string(b), s9Needle) != 3 { // the Secret's name, Vault's address and the role ARN - never the token
		t.Fatalf("the authentication DTO: %s", b)
	}
}

// S2b's FactRow gate: a KEDA-owned HPA's row carries no verdict.
func TestS9FactRowsCarryNoVerdict(t *testing.T) {
	h := &hpa.HPA{Namespace: "team", Name: "keda-hpa-web", Controller: &hpa.Owner{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "web", UID: "so-1"}}
	owned := hpaRowDTO(healthsrc.Row[healthsrc.HPARow]{Eval: health.NativeEval{}, Data: healthsrc.HPARow{HPA: h, KEDAOwner: "web"}})
	if owned.Rule != nil || owned.Reason != "" {
		t.Fatalf("a fact row carried a verdict: %+v", owned.Rule)
	}
	plain := hpaRowDTO(healthsrc.Row[healthsrc.HPARow]{Eval: health.NativeEval{Role: health.OK, Label: "2 replicas"}, Data: healthsrc.HPARow{HPA: &hpa.HPA{Name: "plain"}}})
	if plain.Rule == nil || plain.Rule.Role != "ok" {
		t.Fatalf("a counted row lost its verdict: %+v", plain.Rule)
	}
}

// fakeKEDAConn records the verbs that reach the connection.
type fakeKEDAConn struct{ paused []string }

func (f *fakeKEDAConn) KEDAInspect(context.Context, string, string, string, string) (fleet.KEDAInspection, error) {
	return fleet.KEDAInspection{}, nil
}
func (f *fakeKEDAConn) KEDAJobsOf(context.Context, string, string, string) (fleet.KEDAJobs, error) {
	return fleet.KEDAJobs{}, nil
}
func (f *fakeKEDAConn) ReplicaOwner(context.Context, string, string, string) fleet.ReplicaOwnership {
	return fleet.ReplicaOwnership{}
}
func (f *fakeKEDAConn) KEDASetPaused(_ context.Context, resource, ns, name, _ string, pause bool) error {
	f.paused = append(f.paused, resource+" "+ns+"/"+name+" "+map[bool]string{true: "pause", false: "resume"}[pause])
	return nil
}

// S9 §8: Pause and Resume consume the typed-confirmation grant on a
// protected cluster - refused unarmed, one-shot when armed - and an
// unprotected cluster is not gated.
func TestKEDASetPaused_ProtectedGrant(t *testing.T) {
	now := time.Now()
	g := newTestGuard("prd-cluster", &now)
	SetProtectGuard(g)
	t.Cleanup(func() { SetProtectGuard(nil) })
	conn := &fakeKEDAConn{}
	s := NewKEDAService(func(string) (KEDAConn, bool) { return conn, true })
	if r := s.SetPaused("prd-cluster", "scaledobjects", "team", "web", "so-1", true); r.OK || r.Error == "" {
		t.Fatalf("an unarmed pause on a protected cluster must refuse, got %+v", r)
	}
	if len(conn.paused) != 0 {
		t.Fatalf("a refused pause reached the conn: %v", conn.paused)
	}
	if err := g.Arm("prd-cluster", "prd-cluster"); err != nil {
		t.Fatal(err)
	}
	if r := s.SetPaused("prd-cluster", "scaledobjects", "team", "web", "so-1", true); !r.OK {
		t.Fatalf("an armed pause must pass, got %+v", r)
	}
	if r := s.SetPaused("prd-cluster", "scaledobjects", "team", "web", "so-1", false); r.OK {
		t.Fatal("the grant is one-shot")
	}
	if r := s.SetPaused("dev-cluster", "scaledjobs", "team", "batch", "sj-1", false); !r.OK {
		t.Fatalf("an unprotected resume needs no grant, got %+v", r)
	}
	if strings.Join(conn.paused, "; ") != "scaledobjects team/web pause; scaledjobs team/batch resume" {
		t.Fatalf("the verbs that reached the conn: %v", conn.paused)
	}
}
