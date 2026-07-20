package appbridge

import (
	"context"
	"testing"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/hubble"
	"github.com/moomora/klyx/internal/routemetrics"
)

type fakeGatewayConn struct {
	hubbleStats  hubble.FlowStats
	hubbleStatus hubble.Status
	refs         []gwapi.GatewayRef
	served       bool
	topo         gwapi.Topology
	err          error
}

func (f *fakeGatewayConn) ListGateways(ctx context.Context) ([]gwapi.GatewayRef, bool, error) {
	return f.refs, f.served, nil
}
func (f *fakeGatewayConn) GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error) {
	return f.topo, f.err
}
func (f *fakeGatewayConn) HubbleFlowStats(context.Context) (hubble.FlowStats, hubble.Status) {
	return f.hubbleStats, f.hubbleStatus
}
func (f *fakeGatewayConn) RouteMetrics(context.Context, []string) (map[string]routemetrics.RouteMetrics, routemetrics.Status) {
	return nil, routemetrics.Status{}
}

func TestListGatewaysDTO(t *testing.T) {
	conn := &fakeGatewayConn{served: true, refs: []gwapi.GatewayRef{{
		Namespace: "infra", Name: "eg", ClassName: "envoy-gateway", Accepted: true, Programmed: true,
		Addresses: []gwapi.GatewayAddress{{Type: "IPAddress", Value: "192.0.2.10"}},
		Listeners: []gwapi.Listener{{Name: "https", Protocol: "HTTPS", Port: 443, CertificateRefs: []gwapi.CertificateRef{{Kind: "Secret", Namespace: "infra", Name: "eg-tls"}}}},
	}}}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	out := svc.ListGateways("x")
	if !out.GatewayAPIServed || len(out.Gateways) != 1 || out.Gateways[0].Name != "eg" {
		t.Fatalf("list: %+v", out)
	}
	if len(out.Gateways[0].Addresses) != 1 || out.Gateways[0].Addresses[0].Value != "192.0.2.10" {
		t.Fatalf("addresses: %+v", out.Gateways[0].Addresses)
	}
	if len(out.Gateways[0].Listeners) != 1 || out.Gateways[0].Listeners[0].Port != 443 {
		t.Fatalf("listeners: %+v", out.Gateways[0].Listeners)
	}
	if len(out.Gateways[0].Listeners[0].CertificateRefs) != 1 || out.Gateways[0].Listeners[0].CertificateRefs[0].Name != "eg-tls" {
		t.Fatalf("listener cert refs: %+v", out.Gateways[0].Listeners[0].CertificateRefs)
	}
}

func TestGetGatewayTopologyDTO(t *testing.T) {
	conn := &fakeGatewayConn{topo: gwapi.Topology{
		Gateway:  gwapi.GatewayNode{Namespace: "infra", Name: "eg", ClassName: "envoy-gateway", Programmed: true, Listeners: []gwapi.Listener{{Name: "http", Protocol: "HTTP", Port: 80}}, Addresses: []gwapi.GatewayAddress{{Type: "IPAddress", Value: "192.0.2.10"}}},
		Routes:   []gwapi.RouteNode{{Namespace: "apps", Name: "share", ParentSectionName: "http", Accepted: true, Matches: []gwapi.Match{{PathType: "PathPrefix", PathValue: "/x"}}, Services: []gwapi.ServiceNode{{Name: "share-api", Resolved: true, Type: "ClusterIP", Port: 80}}, Pods: gwapi.PodCount{Ready: 2, Total: 2}}},
		Warnings: []string{"heads up"},
	}}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	d := svc.GetGatewayTopology("x", "infra", "eg")
	if d.Gateway.Name != "eg" || !d.Gateway.Programmed || len(d.Routes) != 1 {
		t.Fatalf("topology: %+v", d)
	}
	if len(d.Gateway.Addresses) != 1 || d.Gateway.Addresses[0].Value != "192.0.2.10" {
		t.Fatalf("gateway addresses: %+v", d.Gateway.Addresses)
	}
	if d.Routes[0].Services[0].Name != "share-api" || d.Routes[0].Pods.Ready != 2 {
		t.Fatalf("route: %+v", d.Routes[0])
	}
	if d.Routes[0].ParentSectionName != "http" {
		t.Fatalf("route parent section: %+v", d.Routes[0])
	}
	if len(d.Warnings) != 1 || d.Error != "" {
		t.Fatalf("warnings/error: %+v", d)
	}
}

func TestGatewayTopologyDTOPolicies(t *testing.T) {
	conn := &fakeGatewayConn{topo: gwapi.Topology{
		Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "eg", Policies: []gwapi.PolicyRef{
			{Kind: "ClientTrafficPolicy", Namespace: "infra", Name: "ctp", TargetKind: "Gateway", TargetNamespace: "infra", TargetName: "eg", Summary: "http2"},
		}},
		Routes: []gwapi.RouteNode{{
			Namespace: "apps", Name: "share",
			Policies: []gwapi.PolicyRef{{Kind: "BackendTrafficPolicy", Namespace: "apps", Name: "btp", TargetKind: "HTTPRoute", TargetName: "share", Summary: "retries + timeout", Details: []gwapi.PolicyDetail{{Key: "retries", Value: "3"}, {Key: "request timeout", Value: "30s"}}}},
			Services: []gwapi.ServiceNode{{Namespace: "apps", Name: "share-api", Resolved: true, Policies: []gwapi.PolicyRef{{Kind: "BackendTLSPolicy", Name: "btls", TargetKind: "Service", TargetName: "share-api", Summary: "hostname"}}}},
		}},
	}}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	d := svc.GetGatewayTopology("x", "infra", "eg")

	if len(d.Gateway.Policies) != 1 || d.Gateway.Policies[0].Kind != "ClientTrafficPolicy" || d.Gateway.Policies[0].TargetName != "eg" {
		t.Fatalf("gateway policy DTO: %+v", d.Gateway.Policies)
	}
	rp := d.Routes[0].Policies
	if len(rp) != 1 || rp[0].Summary != "retries + timeout" || len(rp[0].Details) != 2 || rp[0].Details[0].Key != "retries" || rp[0].Details[0].Value != "3" {
		t.Fatalf("route policy DTO: %+v", rp)
	}
	sp := d.Routes[0].Services[0].Policies
	if len(sp) != 1 || sp[0].Kind != "BackendTLSPolicy" {
		t.Fatalf("service policy DTO: %+v", sp)
	}
}

func TestGetGatewayTopologyErrorSurfaced(t *testing.T) {
	conn := &fakeGatewayConn{err: context.DeadlineExceeded}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	d := svc.GetGatewayTopology("x", "infra", "eg")
	if d.Error == "" {
		t.Fatalf("a core error must surface in Error, got %+v", d)
	}
}

func TestGatewayUnknownClusterEmpty(t *testing.T) {
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return nil, false })
	if out := svc.ListGateways("ghost"); out.GatewayAPIServed || len(out.Gateways) != 0 {
		t.Fatalf("want empty, got %+v", out)
	}
}

func TestGatewayTopologyDTOCilium(t *testing.T) {
	conn := &fakeGatewayConn{topo: gwapi.Topology{
		Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "eg"},
		Routes: []gwapi.RouteNode{{
			Namespace: "apps", Name: "share",
			Services: []gwapi.ServiceNode{{Namespace: "apps", Name: "share-api", Resolved: true,
				CNPs: []gwapi.PolicyRef{{Kind: "CiliumNetworkPolicy", Namespace: "apps", Name: "share-allow", TargetKind: "Pods", TargetNamespace: "apps", TargetName: "share-api", Summary: "ingress", Inferred: true, Match: gwapi.MatchSelector}}}},
		}},
		ClusterPolicies: []gwapi.PolicyRef{{Kind: "CiliumClusterwideNetworkPolicy", Name: "cluster-deny", Summary: "ingress default-deny", Inferred: true, Match: gwapi.MatchClusterWide}},
	}}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	d := svc.GetGatewayTopology("x", "infra", "eg")

	cnps := d.Routes[0].Services[0].CNPs
	if len(cnps) != 1 || cnps[0].Match != "selector" || cnps[0].TargetKind != "Pods" || !cnps[0].Inferred {
		t.Fatalf("service cnps DTO: %+v", cnps)
	}
	if len(d.ClusterPolicies) != 1 || d.ClusterPolicies[0].Match != "cluster-wide" || d.ClusterPolicies[0].Kind != "CiliumClusterwideNetworkPolicy" {
		t.Fatalf("cluster policies DTO: %+v", d.ClusterPolicies)
	}
}

func TestGatewayTopologyGlobalReach(t *testing.T) {
	conn := &fakeGatewayConn{topo: gwapi.Topology{
		Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "eg"},
		Routes: []gwapi.RouteNode{{
			Namespace: "apps", Name: "share",
			Services: []gwapi.ServiceNode{{Namespace: "apps", Name: "share-api", Resolved: true, Global: true}},
		}},
	}}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	// Inject a globalReach that confirms one peer + flags an off-fleet one.
	svc.SetGlobalReach(func(cluster, ns, name string) ([]string, bool) {
		if ns == "apps" && name == "share-api" {
			return []string{"homelab-orange"}, true
		}
		return nil, false
	})

	d := svc.GetGatewayTopology("homelab-blue", "infra", "eg")
	s := d.Routes[0].Services[0]
	if !s.Global || len(s.MeshClusters) != 1 || s.MeshClusters[0] != "homelab-orange" || !s.MeshUnconfirmed {
		t.Fatalf("global reach: %+v", s)
	}
}

func TestGatewayTopologyGlobalReachEmptyIsNonNil(t *testing.T) {
	conn := &fakeGatewayConn{topo: gwapi.Topology{
		Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "eg"},
		Routes:  []gwapi.RouteNode{{Namespace: "apps", Name: "share", Services: []gwapi.ServiceNode{{Namespace: "apps", Name: "share-api", Resolved: true, Global: true}}}},
	}}
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	svc.SetGlobalReach(func(cluster, ns, name string) ([]string, bool) { return nil, true })
	d := svc.GetGatewayTopology("x", "infra", "eg")
	s := d.Routes[0].Services[0]
	if s.MeshClusters == nil || len(s.MeshClusters) != 0 || !s.MeshUnconfirmed {
		t.Fatalf("meshClusters must be non-nil empty + unconfirmed: %+v", s)
	}
}

func TestGatewayTopologyNonGlobalNoReach(t *testing.T) {
	conn := &fakeGatewayConn{topo: gwapi.Topology{
		Gateway: gwapi.GatewayNode{Namespace: "infra", Name: "eg"},
		Routes:  []gwapi.RouteNode{{Namespace: "apps", Name: "share", Services: []gwapi.ServiceNode{{Namespace: "apps", Name: "share-api", Resolved: true}}}},
	}}
	called := false
	svc := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	svc.SetGlobalReach(func(cluster, ns, name string) ([]string, bool) { called = true; return nil, false })
	d := svc.GetGatewayTopology("x", "infra", "eg")
	if d.Routes[0].Services[0].Global || called {
		t.Fatalf("non-global service must not call globalReach: global=%v called=%v", d.Routes[0].Services[0].Global, called)
	}
}
