package healthsrc

import (
	"testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/observe"
)

func TestControllerIssuesMapTheTiers(t *testing.T) {
	healthy := ControllerIssues(capability.Set{
		GitOps:          capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true, Healthy: true}, Argo: capability.ArgoInfo{Present: true, Healthy: true}},
		CertManager:     capability.CertManagerCapability{Present: true, Healthy: true},
		ExternalSecrets: capability.ExternalSecretsCapability{Present: true, Healthy: true},
		Karpenter:       capability.KarpenterCapability{Present: true, Healthy: true},
		Network:         capability.NetworkCapability{Base: capability.Base{Tier: capability.Healthy}, GatewayAPIVersion: "v1"},
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
		Karpenter:       capability.KarpenterCapability{Base: capability.Base{Reason: "karpenter unverified"}, Present: true}, // not read
		Network:         capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "classes unread"}, GatewayAPIVersion: "v1", GatewayUnverified: true},
	})
	want := map[string]observe.ControllerIssue{
		TaskFlux:      {State: ControllerNotReady, Reason: "Flux installed but kustomize-controller is not ready"},
		TaskCerts:     {State: ControllerNotReady, Reason: "cert-manager installed but cert-manager is not ready"},
		TaskESO:       {State: ControllerNotReady, Reason: "eso down"},
		TaskKarpenter: {State: ControllerUnknown, Reason: "karpenter unverified"}, // never "not ready" unread
		TaskGateway:   {State: ControllerUnknown, Reason: "classes unread"},       // unreadable is unknown, never "not ready"
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
