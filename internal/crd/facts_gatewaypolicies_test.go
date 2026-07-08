package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_GRPCRoute(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "GRPCRoute",
		"spec": map[string]interface{}{
			"hostnames":  []interface{}{"grpc.example.com"},
			"parentRefs": []interface{}{map[string]interface{}{"name": "eg", "namespace": "envoy-gateway-system"}},
			"rules": []interface{}{
				map[string]interface{}{
					"matches": []interface{}{
						map[string]interface{}{"method": map[string]interface{}{"service": "echo.Echo", "method": "Ping"}},
						map[string]interface{}{"method": map[string]interface{}{"type": "Exact"}},
					},
					"backendRefs": []interface{}{
						map[string]interface{}{"name": "echo", "port": int64(9000), "weight": int64(100)},
					},
				},
			},
		},
	}}
	facts := BuildFacts("gateway.networking.k8s.io", "grpcroutes", u)
	if f, ok := factByLabel(facts, "hostnames"); !ok || f.Value != "grpc.example.com" {
		t.Fatalf("hostnames fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "parents"); !ok || f.Value != "envoy-gateway-system/eg" {
		t.Fatalf("parents fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "rules"); !ok || f.Value != "1" {
		t.Fatalf("rules fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "method matches"); !ok || f.Value != "echo.Echo/Ping, type=Exact" {
		t.Fatalf("method matches fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "backends"); !ok || f.Value != "echo:9000 (w100)" {
		t.Fatalf("backends fact wrong: %+v", f)
	}
}

func TestBuildFacts_GRPCRoute_NoHostnames(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "GRPCRoute",
		"spec": map[string]interface{}{},
	}}
	facts := BuildFacts("gateway.networking.k8s.io", "grpcroutes", u)
	if f, ok := factByLabel(facts, "hostnames"); !ok || f.Value != "(all)" {
		t.Fatalf("empty hostnames should render (all): %+v", f)
	}
	if f, ok := factByLabel(facts, "rules"); !ok || f.Value != "0" {
		t.Fatalf("rules fact wrong: %+v", f)
	}
}

func TestBuildFacts_SecurityPolicy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "SecurityPolicy",
		"spec": map[string]interface{}{
			"targetRef": map[string]interface{}{"kind": "Gateway", "name": "eg"},
			"jwt": map[string]interface{}{
				"providers": []interface{}{
					map[string]interface{}{"issuer": "https://issuer.example.com"},
				},
			},
			"oidc": map[string]interface{}{
				"clientID": "klyx-app",
				"provider": map[string]interface{}{"issuer": "https://login.example.com"},
			},
		},
	}}
	facts := BuildFacts("gateway.envoyproxy.io", "securitypolicies", u)
	if f, ok := factByLabel(facts, "target"); !ok || f.Value != "Gateway eg" {
		t.Fatalf("target fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "enforces"); !ok || f.Value != "jwt + oidc" {
		t.Fatalf("enforces fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "oidc"); !ok || f.Value != "https://login.example.com (client klyx-app)" {
		t.Fatalf("oidc fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "jwt issuers"); !ok || f.Value != "https://issuer.example.com" {
		t.Fatalf("jwt issuers fact wrong: %+v", f)
	}
}

func TestBuildFacts_BackendTrafficPolicy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "BackendTrafficPolicy",
		"spec": map[string]interface{}{
			"targetRefs": []interface{}{
				map[string]interface{}{"kind": "Service", "name": "api"},
			},
			"rateLimit":      map[string]interface{}{"type": "Local"},
			"circuitBreaker": map[string]interface{}{"maxConnections": int64(1024), "maxParallelRequests": int64(512)},
			"retry":          map[string]interface{}{"numRetries": int64(3)},
			"loadBalancer":   map[string]interface{}{"type": "RoundRobin"},
			"timeout":        map[string]interface{}{"http": map[string]interface{}{"requestTimeout": "5s"}},
		},
	}}
	facts := BuildFacts("gateway.envoyproxy.io", "backendtrafficpolicies", u)
	if f, ok := factByLabel(facts, "target"); !ok || f.Value != "Service api" {
		t.Fatalf("target fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "rate limit"); !ok || f.Value != "Local" {
		t.Fatalf("rate limit fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "circuit breaker"); !ok || f.Value != "maxConnections=1024 maxParallelRequests=512" {
		t.Fatalf("circuit breaker fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "retries"); !ok || f.Value != "3" {
		t.Fatalf("retries fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "lb"); !ok || f.Value != "RoundRobin" {
		t.Fatalf("lb fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "timeouts"); !ok || f.Value != "set" {
		t.Fatalf("timeouts fact wrong: %+v", f)
	}
}

func TestBuildFacts_ClientTrafficPolicy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ClientTrafficPolicy",
		"spec": map[string]interface{}{
			"targetRefs": []interface{}{
				map[string]interface{}{"kind": "Gateway", "name": "eg", "sectionName": "https"},
			},
			"tls": map[string]interface{}{
				"minVersion":       "1.3",
				"clientValidation": map[string]interface{}{"caCertificateRefs": []interface{}{}},
			},
			"tcpKeepalive": map[string]interface{}{"idle": "60s"},
			"http2":        map[string]interface{}{"maxConcurrentStreams": int64(100)},
		},
	}}
	facts := BuildFacts("gateway.envoyproxy.io", "clienttrafficpolicies", u)
	if f, ok := factByLabel(facts, "target"); !ok || f.Value != "Gateway eg (https)" {
		t.Fatalf("target fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "client TLS"); !ok || f.Value != "minVersion=1.3 clientValidation" {
		t.Fatalf("client TLS fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "keepalive"); !ok || f.Value != "on" {
		t.Fatalf("keepalive fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "http2"); !ok || f.Value != "on" {
		t.Fatalf("http2 fact wrong: %+v", f)
	}
}

func TestBuildFacts_BackendTLSPolicy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "BackendTLSPolicy",
		"spec": map[string]interface{}{
			"targetRefs": []interface{}{
				map[string]interface{}{"kind": "Service", "name": "api", "sectionName": "https"},
			},
			"validation": map[string]interface{}{
				"hostname":          "api.svc.cluster.local",
				"caCertificateRefs": []interface{}{map[string]interface{}{"name": "backend-ca"}},
			},
		},
	}}
	facts := BuildFacts("gateway.networking.k8s.io", "backendtlspolicies", u)
	if f, ok := factByLabel(facts, "targets"); !ok || f.Value != "Service api (https)" {
		t.Fatalf("targets fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "SNI hostname"); !ok || f.Value != "api.svc.cluster.local" {
		t.Fatalf("SNI hostname fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "CA"); !ok || f.Value != "backend-ca" {
		t.Fatalf("CA fact wrong: %+v", f)
	}
}

func TestBuildFacts_BackendTLSPolicy_SystemCAs(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "BackendTLSPolicy",
		"spec": map[string]interface{}{
			"validation": map[string]interface{}{
				"wellKnownCACertificates": "System",
			},
		},
	}}
	facts := BuildFacts("gateway.networking.k8s.io", "backendtlspolicies", u)
	if f, ok := factByLabel(facts, "CA"); !ok || f.Value != "System CAs" {
		t.Fatalf("CA fact should read System CAs: %+v", f)
	}
}
