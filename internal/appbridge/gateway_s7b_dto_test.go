package appbridge

import (
	"testing"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/health"
)

// Coverage S7b at the bridge: a topology lane carries its kind, the version
// it was read at, its attachment, the attachment's verdict, the controllers
// and the evidence; its gRPC matches reach the lane and the rule alike, and
// each backend keeps the transport its port was selected by.
func TestRouteNodeDTOCarriesTheAttachment(t *testing.T) {
	ref := gwapi.RouteParentRef{Group: gwapi.Group, Kind: "Gateway", Namespace: "infra", Name: "eg", SectionName: "grpc"}
	verdict := health.NativeEval{Included: true, Role: health.Fault, Label: "backend not found"}
	rn := gwapi.RouteNode{
		Namespace: "apps", Name: "api", UID: "u1", Kind: gwapi.KindGRPCRoute, APIVersion: "gateway.networking.k8s.io/v1",
		Attachment: ref.Key(), AttachmentLabel: ref.Label(), ParentRef: ref, Verdict: &verdict,
		Controller: "example.com/gw", OtherControllers: []gwapi.ParentStatus{{ControllerName: "retired.example/gw",
			Accepted: gwapi.Cond{Status: "True"}, ResolvedRefs: gwapi.Cond{Status: "False"}}},
		Evidence: &gwapi.AttachmentEvidence{Features: []string{"the class's controller does not declare support for GRPCRoute"}},
		RuleNames: []string{"", "named"}, HostnameCount: 20, Hostnames: []string{"a.example"},
		Matches:  []gwapi.Match{{GRPCService: "pkg.Svc", GRPCMethod: "Get", Rule: 0}},
		Backends: []gwapi.Backend{{Kind: "Service", Name: "api", Namespace: "apps", Port: 9000, Transport: "TCP", State: gwapi.BackendNotFound, Rule: 0}},
		RuleCount: 2,
	}
	d := toRouteNodeDTO(rn, nil)

	if d.Kind != "GRPCRoute" || d.APIVersion != "gateway.networking.k8s.io/v1" || d.Attachment != ref.Key() || d.AttachmentLabel != "infra/eg · grpc" {
		t.Fatalf("identity: %q %q %q %q", d.Kind, d.APIVersion, d.Attachment, d.AttachmentLabel)
	}
	if d.ParentRef == nil || d.ParentRef.SectionName != "grpc" || d.ParentRef.Kind != "Gateway" {
		t.Fatalf("parentRef: %+v", d.ParentRef)
	}
	if d.Rule.Role != "fault" || d.Rule.Label != "backend not found" || !d.Rule.Included {
		t.Fatalf("the lane's rule is the attachment's verdict: %+v", d.Rule)
	}
	if d.Controller != "example.com/gw" || len(d.Others) != 1 || d.Others[0].Controller != "retired.example/gw" || d.Others[0].ResolvedRefs != "False" {
		t.Fatalf("controllers: %q %+v", d.Controller, d.Others)
	}
	if d.Evidence == nil || len(d.Evidence.Features) != 1 || d.Evidence.Controller != "example.com/gw" {
		t.Fatalf("evidence: %+v", d.Evidence)
	}
	if len(d.RuleNames) != 2 || d.RuleNames[1] != "named" || d.HostnameCount != 20 {
		t.Fatalf("rule names and hostname count: %v %d", d.RuleNames, d.HostnameCount)
	}
	if len(d.Matches) != 1 || d.Matches[0].GRPCService != "pkg.Svc" || d.Matches[0].GRPCMethod != "Get" {
		t.Fatalf("lane matches: %+v", d.Matches)
	}
	if len(d.Rules) == 0 || len(d.Rules[0].Matches) != 1 || d.Rules[0].Matches[0].GRPCService != "pkg.Svc" {
		t.Fatalf("the rule's matches carry the gRPC fields too: %+v", d.Rules)
	}
	if len(d.Backends) != 1 || d.Backends[0].Transport != "TCP" {
		t.Fatalf("backend transport: %+v", d.Backends)
	}
}

// A lane from before S7b (no kind, no verdict) reads as an HTTPRoute judged
// by its own rule.
func TestRouteNodeDTOLegacyLane(t *testing.T) {
	d := toRouteNodeDTO(gwapi.RouteNode{Namespace: "apps", Name: "web"}, nil)
	if d.Kind != "HTTPRoute" || d.Attachment != "" || d.ParentRef != nil || d.RuleNames == nil {
		t.Fatalf("legacy lane: %+v", d)
	}
}
