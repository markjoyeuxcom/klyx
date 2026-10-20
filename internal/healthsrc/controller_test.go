package healthsrc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/observe"
)

func TestControllerIssuesMapTheTiers(t *testing.T) {
	healthy := ControllerIssues(capability.Set{
		GitOps:          capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true, Healthy: true}, Argo: capability.ArgoInfo{Present: true, Healthy: true}},
		CertManager:     capability.CertManagerCapability{Present: true, Healthy: true},
		ExternalSecrets: capability.ExternalSecretsCapability{Present: true, Healthy: true},
		// S10: Karpenter's state is its task's, from a complete inventory
		Karpenter: capability.KarpenterCapability{Present: true, Inventory: capability.InventoryComplete,
			Controllers: []capability.KarpenterController{{Namespace: "karpenter", Name: "karpenter", Provider: capability.ProviderAWS, Ready: true}}},
		Network: capability.NetworkCapability{Base: capability.Base{Tier: capability.Healthy}, GatewayAPIVersion: "v1"},
	})
	for _, task := range ControllerTasks {
		if v, ok := healthy[task]; !ok || v != nil {
			t.Fatalf("%s: a healthy controller has an explicit nil entry (so recovery clears), got %v/%v", task, v, ok)
		}
	}
	if absent := ControllerIssues(capability.Set{}); absent[TaskCerts] != nil || absent[TaskGateway] != nil {
		t.Fatal("a tool that is not installed has no controller issue")
	}
	bad := ControllerIssues(capability.Set{
		GitOps:          capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true, Verified: true}, Argo: capability.ArgoInfo{Present: true, Healthy: true, Verified: true}},
		CertManager:     capability.CertManagerCapability{Base: capability.Base{Reason: "cert-manager installed but cert-manager is not ready"}, Present: true, Verified: true},
		ExternalSecrets: capability.ExternalSecretsCapability{Base: capability.Base{Reason: "eso down"}, Present: true, Verified: true},
		Karpenter:       capability.KarpenterCapability{Present: true, Inventory: capability.InventoryUnreadable, InventoryWhy: "forbidden"}, // not read
		Network:         capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "classes unread"}, GatewayAPIVersion: "v1", GatewayUnverified: true},
	})
	want := map[string]observe.ControllerIssue{
		TaskFlux:      {State: ControllerNotReady, Reason: "Flux installed but kustomize-controller is not ready"},
		TaskCerts:     {State: ControllerNotReady, Reason: "cert-manager installed but cert-manager is not ready"},
		TaskESO:       {State: ControllerNotReady, Reason: "eso down"},
		TaskKarpenter: {State: ControllerUnknown, Reason: "the Karpenter controllers could not all be read (forbidden)"}, // never "not ready" unread
		TaskGateway:   {State: ControllerUnknown, Reason: "classes unread"},                                              // unreadable is unknown, never "not ready"
	}
	for task, w := range want {
		if got := bad[task]; got == nil || *got != w {
			t.Errorf("%s: got %+v, want %+v", task, got, w)
		}
	}
	if bad[TaskArgo] != nil {
		t.Error("a healthy Argo beside a broken Flux has no issue of its own")
	}
	unread := ControllerIssues(capability.Set{GitOps: capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true, Why: "Deployment flux-system/kustomize-controller unreadable: forbidden"}}})
	if got := unread[TaskFlux]; got == nil || got.State != ControllerUnknown || got.Reason != "Flux installed but kustomize-controller health is unverified (Deployment flux-system/kustomize-controller unreadable: forbidden)" {
		t.Errorf("an unread Flux controller is unknown: %+v", got)
	}
}

// Detection through to the issue: a current, explicit rejection of every
// class is "not ready"; acceptance unreported or stale is "unknown".
func TestGatewayClassVerdictsThroughDetection(t *testing.T) {
	class := func(gen, observed int, status string) string {
		obs := ""
		if observed > 0 {
			obs = fmt.Sprintf(`,"observedGeneration":%d`, observed)
		}
		return fmt.Sprintf(`{"metadata":{"generation":%d},"status":{"conditions":[{"type":"Accepted","status":%q%s}]}}`, gen, status, obs)
	}
	cases := []struct {
		name, items, state string
	}{
		{"every class rejected now", class(3, 3, "False") + "," + class(1, 1, "False"), ControllerNotReady},
		{"rejection without an observed generation", class(2, 0, "False"), ControllerUnknown},
		{"acceptance without an observed generation", class(2, 0, "True"), ControllerUnknown},
		{"a stale rejection", class(3, 2, "False"), ControllerUnknown},
		{"one rejected, one pending", class(3, 3, "False") + "," + class(2, 1, "True"), ControllerUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset()
			cs.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{{GroupVersion: "gateway.networking.k8s.io/v1"}}
			d := capability.NewDetector(cs).WithGatewayClassReader(func(context.Context, string) ([]byte, error) {
				return []byte(`{"items":[` + tc.items + `]}`), nil
			})
			got := ControllerIssues(d.Detect(context.Background()))[TaskGateway]
			if got == nil || got.State != tc.state {
				t.Fatalf("got %+v, want %s", got, tc.state)
			}
		})
	}
	cs := fake.NewClientset()
	cs.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{{GroupVersion: "gateway.networking.k8s.io/v1"}}
	d := capability.NewDetector(cs).WithGatewayClassReader(func(context.Context, string) ([]byte, error) {
		return []byte(`{"items":[` + class(3, 3, "True") + `]}`), nil
	})
	if got := ControllerIssues(d.Detect(context.Background()))[TaskGateway]; got != nil {
		t.Fatalf("a current acceptance is healthy: %+v", got)
	}
}

// Codex's S1a implementation review 2, finding 2: an operator detection gap
// alone is unknown, never "not ready"; a proven unready operator is still
// not ready alongside a gap.
func TestOperatorDetectionGapIsUnknown(t *testing.T) {
	ready := capability.FluxOperator{Namespace: "flux-system", Name: "fo", RuntimeNamespace: "flux-system", Ready: true}
	unresolved := capability.FluxOperator{Namespace: "ops", Name: "fo-x", Ready: true}
	down := capability.FluxOperator{Namespace: "b", Name: "fo-b", RuntimeNamespace: "b"}
	state := func(op capability.FluxOperatorInfo) string {
		op.Present = true
		got := ControllerIssues(capability.Set{GitOps: capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true, Healthy: true, Verified: true}, FluxOperator: op}})
		if got[TaskFlux] == nil {
			return ""
		}
		return got[TaskFlux].State
	}
	if s := state(capability.FluxOperatorInfo{Verified: true, Complete: false, Operators: []capability.FluxOperator{ready}}); s != ControllerUnknown {
		t.Errorf("a scan cut short: %q", s)
	}
	if s := state(capability.FluxOperatorInfo{Verified: true, Complete: true, Operators: []capability.FluxOperator{ready, unresolved}}); s != ControllerUnknown {
		t.Errorf("an unreadable runtime namespace: %q", s)
	}
	if s := state(capability.FluxOperatorInfo{Verified: true, Complete: false, Operators: []capability.FluxOperator{ready, down}}); s != ControllerNotReady {
		t.Errorf("a proven unready operator beside a gap: %q", s)
	}
	if s := state(capability.FluxOperatorInfo{Verified: true, Complete: true, Operators: []capability.FluxOperator{ready}}); s != "" {
		t.Errorf("healthy: %q", s)
	}
}

// Codex's S1a implementation review 3, finding 2: an operator detection
// gap never hides a proven kustomize-controller failure; an operator-only
// install's synthesized Flux state never invents one.
func TestOperatorGapDoesNotHideFluxFailure(t *testing.T) {
	gapOp := capability.FluxOperatorInfo{Present: true, Verified: true, Complete: false,
		Operators: []capability.FluxOperator{{Namespace: "flux-system", Name: "fo", RuntimeNamespace: "flux-system", Ready: true}}}
	got := ControllerIssues(capability.Set{GitOps: capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true, Verified: true}, FluxOperator: gapOp}})[TaskFlux]
	if got == nil || got.State != ControllerNotReady || !strings.Contains(got.Reason, "kustomize-controller is not ready") || !strings.Contains(got.Reason, "detection is incomplete") {
		t.Fatalf("the failure first, the gap after: %+v", got)
	}
	synth := capability.FluxInfo{Present: true, Verified: true, Synthesized: true}
	if got := ControllerIssues(capability.Set{GitOps: capability.GitOpsCapability{Flux: synth, FluxOperator: gapOp}})[TaskFlux]; got == nil || got.State != ControllerUnknown || strings.Contains(got.Reason, "kustomize-controller") {
		t.Fatalf("an operator-only install: %+v", got)
	}
}
