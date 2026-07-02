package gwapi

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func gwObj(name, ns, class string, listeners, conds []interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       map[string]interface{}{"gatewayClassName": class, "listeners": listeners},
		"status": map[string]interface{}{
			"addresses":  []interface{}{map[string]interface{}{"type": "IPAddress", "value": "192.0.2.10"}},
			"conditions": conds,
		},
	}}
}

func TestParseGateway(t *testing.T) {
	u := gwObj("eg-external", "envoy-gateway-system", "envoy-gateway",
		[]interface{}{
			map[string]interface{}{
				"name": "https", "protocol": "HTTPS", "port": int64(443), "hostname": "*.example.com",
				"tls": map[string]interface{}{"certificateRefs": []interface{}{map[string]interface{}{"name": "wildcard-example-com"}}},
			},
			map[string]interface{}{"name": "http", "protocol": "HTTP", "port": int64(80)},
		},
		[]interface{}{
			map[string]interface{}{"type": "Accepted", "status": "True"},
			map[string]interface{}{"type": "Programmed", "status": "True"},
		})
	g := ParseGateway(u)
	if g.Name != "eg-external" || g.Namespace != "envoy-gateway-system" || g.ClassName != "envoy-gateway" {
		t.Fatalf("ids: %+v", g)
	}
	if !g.Accepted || !g.Programmed {
		t.Fatalf("status: %+v", g)
	}
	if len(g.Listeners) != 2 || g.Listeners[0].Port != 443 || g.Listeners[0].Hostname != "*.example.com" || g.Listeners[1].Protocol != "HTTP" {
		t.Fatalf("listeners: %+v", g.Listeners)
	}
	if len(g.Listeners[0].CertificateRefs) != 1 || g.Listeners[0].CertificateRefs[0].Kind != "Secret" || g.Listeners[0].CertificateRefs[0].Namespace != "envoy-gateway-system" || g.Listeners[0].CertificateRefs[0].Name != "wildcard-example-com" {
		t.Fatalf("listener certificate refs: %+v", g.Listeners[0].CertificateRefs)
	}
	if len(g.Addresses) != 1 || g.Addresses[0].Value != "192.0.2.10" {
		t.Fatalf("addresses: %+v", g.Addresses)
	}
}

func TestParseGatewayFloat64Port(t *testing.T) {
	// json.Unmarshal into interface{} yields float64 for the port.
	u := gwObj("eg", "infra", "envoy-gateway",
		[]interface{}{
			map[string]interface{}{"name": "https", "protocol": "HTTPS", "port": float64(443)},
		}, nil)
	g := ParseGateway(u)
	if len(g.Listeners) != 1 || g.Listeners[0].Port != 443 {
		t.Fatalf("float64 port must decode: %+v", g.Listeners)
	}
}

func TestParseGatewayNotProgrammed(t *testing.T) {
	u := gwObj("g", "n", "c", nil, []interface{}{
		map[string]interface{}{"type": "Accepted", "status": "True"},
		map[string]interface{}{"type": "Programmed", "status": "False"},
	})
	if g := ParseGateway(u); !g.Accepted || g.Programmed {
		t.Fatalf("want accepted, not programmed: %+v", g)
	}
}

func TestGatewayRefStalenessFromProgrammedCondition(t *testing.T) {
	gw := func(gen int64, condObserved interface{}) *unstructured.Unstructured {
		cond := map[string]interface{}{"type": "Programmed", "status": "True"}
		if condObserved != nil {
			cond["observedGeneration"] = condObserved
		}
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata":   map[string]interface{}{"name": "eg", "namespace": "infra"},
			"spec":       map[string]interface{}{"gatewayClassName": "envoy"},
			"status":     map[string]interface{}{"conditions": []interface{}{cond}},
		}}
		u.SetGeneration(gen)
		return u
	}

	// Listener edit (gen 7) not yet observed (gen 6): stale - Programmed=true
	// describes the previous spec.
	r := ParseGatewayRef(gw(7, int64(6)))
	if !r.Stale || r.Generation != 7 || r.ObservedGeneration != 6 {
		t.Fatalf("stale: %+v", r)
	}
	if r := ParseGatewayRef(gw(7, int64(7))); r.Stale {
		t.Fatalf("caught-up gateway must not be stale: %+v", r)
	}
	if r := ParseGatewayRef(gw(7, nil)); r.Stale {
		t.Fatalf("missing observedGeneration must read not-stale: %+v", r)
	}
}
