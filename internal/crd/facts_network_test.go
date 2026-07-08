package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_Ingress(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Ingress",
		"spec": map[string]interface{}{
			"ingressClassName": "nginx",
			"defaultBackend": map[string]interface{}{
				"service": map[string]interface{}{"name": "fallback"},
			},
			"rules": []interface{}{
				map[string]interface{}{
					"host": "app.example.com",
					"http": map[string]interface{}{
						"paths": []interface{}{
							map[string]interface{}{
								"path": "/api",
								"backend": map[string]interface{}{
									"service": map[string]interface{}{
										"name": "api",
										"port": map[string]interface{}{"number": int64(8080)},
									},
								},
							},
						},
					},
				},
			},
			"tls": []interface{}{
				map[string]interface{}{
					"secretName": "app-tls",
					"hosts":      []interface{}{"app.example.com"},
				},
			},
		},
		"status": map[string]interface{}{
			"loadBalancer": map[string]interface{}{
				"ingress": []interface{}{map[string]interface{}{"ip": "203.0.113.5"}},
			},
		},
	}}
	facts := BuildFacts("networking.k8s.io", "ingresses", u)
	if f, ok := factByLabel(facts, "class"); !ok || f.Value != "nginx" {
		t.Fatalf("class fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "rules"); !ok || f.Value != "1" {
		t.Fatalf("rules fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "hosts"); !ok || f.Value != "app.example.com" {
		t.Fatalf("hosts fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "backends"); !ok || f.Value != "app.example.com /api -> api:8080" {
		t.Fatalf("backends fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "app-tls"); !ok || f.Value != "app.example.com" {
		t.Fatalf("tls fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "default backend"); !ok || f.Value != "fallback" {
		t.Fatalf("default backend fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "address"); !ok || f.Value != "203.0.113.5" {
		t.Fatalf("address fact wrong: %+v", f)
	}
}

func TestBuildFacts_Ingress_PendingAddress(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Ingress",
		"spec": map[string]interface{}{"rules": []interface{}{}},
	}}
	facts := BuildFacts("networking.k8s.io", "ingresses", u)
	if f, ok := factByLabel(facts, "address"); !ok || f.Value != "pending" || f.Tone != "warning" {
		t.Fatalf("pending address fact wrong: %+v", f)
	}
}

func TestBuildFacts_NetworkPolicy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "NetworkPolicy",
		"spec": map[string]interface{}{
			"podSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "db"},
			},
			"policyTypes": []interface{}{"Ingress", "Egress"},
			"ingress": []interface{}{
				map[string]interface{}{
					"from": []interface{}{
						map[string]interface{}{
							"namespaceSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{"kubernetes.io/metadata.name": "monitoring"},
							},
							"podSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{"app": "prometheus"},
							},
						},
					},
					"ports": []interface{}{
						map[string]interface{}{"protocol": "TCP", "port": int64(9090)},
					},
				},
			},
			"egress": []interface{}{
				map[string]interface{}{
					"to": []interface{}{
						map[string]interface{}{
							"ipBlock": map[string]interface{}{"cidr": "10.0.0.0/8"},
						},
					},
				},
			},
		},
	}}
	facts := BuildFacts("networking.k8s.io", "networkpolicies", u)
	if f, ok := factByLabel(facts, "applies to"); !ok || f.Value != "app=db" {
		t.Fatalf("applies to fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "types"); !ok || f.Value != "Ingress, Egress" {
		t.Fatalf("types fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "ingress rules"); !ok || f.Value != "1" {
		t.Fatalf("ingress rules fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "egress rules"); !ok || f.Value != "1" {
		t.Fatalf("egress rules fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "rule 1"); !ok || f.Value != "from ns=monitoring pods app=prometheus on TCP/9090" {
		t.Fatalf("ingress rule prose wrong: %+v", f)
	}
	// The egress rule 1 also has label "rule 1"; ensure the CIDR peer renders.
	var sawEgress bool
	for _, f := range facts {
		if f.Group == "Egress" && f.Value == "to cidr 10.0.0.0/8" {
			sawEgress = true
		}
	}
	if !sawEgress {
		t.Fatalf("egress cidr rule prose missing: %+v", facts)
	}
}

func TestBuildFacts_NetworkPolicy_EmptySelector(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "NetworkPolicy",
		"spec": map[string]interface{}{
			"podSelector": map[string]interface{}{},
		},
	}}
	facts := BuildFacts("networking.k8s.io", "networkpolicies", u)
	if f, ok := factByLabel(facts, "applies to"); !ok || f.Value != "all pods in namespace" {
		t.Fatalf("empty selector fact wrong: %+v", f)
	}
}

func TestBuildFacts_EndpointSlice(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":        "EndpointSlice",
		"addressType": "IPv4",
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"kubernetes.io/service-name": "api"},
		},
		"endpoints": []interface{}{
			map[string]interface{}{"conditions": map[string]interface{}{"ready": true}},
			map[string]interface{}{"conditions": map[string]interface{}{"ready": false}},
			map[string]interface{}{}, // nil ready => counted ready
		},
		"ports": []interface{}{
			map[string]interface{}{"name": "http", "port": int64(8080), "protocol": "TCP"},
		},
	}}
	facts := BuildFacts("discovery.k8s.io", "endpointslices", u)
	if f, ok := factByLabel(facts, "address type"); !ok || f.Value != "IPv4" {
		t.Fatalf("address type fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "ready"); !ok || f.Value != "2/3" || f.Tone != "warning" {
		t.Fatalf("ready fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "ports"); !ok || f.Value != "http:8080/TCP" {
		t.Fatalf("ports fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "service"); !ok || f.Value != "api" {
		t.Fatalf("service fact wrong: %+v", f)
	}
}

func TestBuildFacts_EndpointSlice_AllReady(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":        "EndpointSlice",
		"addressType": "IPv4",
		"endpoints": []interface{}{
			map[string]interface{}{"conditions": map[string]interface{}{"ready": true}},
		},
	}}
	facts := BuildFacts("discovery.k8s.io", "endpointslices", u)
	if f, ok := factByLabel(facts, "ready"); !ok || f.Value != "1/1" || f.Tone != "success" {
		t.Fatalf("all-ready fact wrong: %+v", f)
	}
}

func TestBuildFacts_Service(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Service",
		"spec": map[string]interface{}{
			"type":                  "LoadBalancer",
			"clusterIP":             "10.96.0.1",
			"externalTrafficPolicy": "Local",
			"sessionAffinity":       "ClientIP",
			"externalIPs":           []interface{}{"198.51.100.1"},
		},
		"status": map[string]interface{}{
			"loadBalancer": map[string]interface{}{
				"ingress": []interface{}{map[string]interface{}{"hostname": "lb.example.com"}},
			},
		},
	}}
	facts := BuildFacts("", "services", u)
	if f, ok := factByLabel(facts, "type"); !ok || f.Value != "LoadBalancer" {
		t.Fatalf("type fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "cluster IP"); !ok || f.Value != "10.96.0.1" {
		t.Fatalf("cluster IP fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "LB address"); !ok || f.Value != "lb.example.com" {
		t.Fatalf("LB address fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "external IPs"); !ok || f.Value != "198.51.100.1" {
		t.Fatalf("external IPs fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "traffic policy"); !ok || f.Value != "Local" {
		t.Fatalf("traffic policy fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "session affinity"); !ok || f.Value != "ClientIP" {
		t.Fatalf("session affinity fact wrong: %+v", f)
	}
}

func TestBuildFacts_Service_HeadlessAndPendingLB(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Service",
		"spec": map[string]interface{}{
			"type":            "LoadBalancer",
			"clusterIP":       "None",
			"sessionAffinity": "None",
		},
	}}
	facts := BuildFacts("", "services", u)
	if f, ok := factByLabel(facts, "cluster IP"); !ok || f.Value != "None (headless)" {
		t.Fatalf("headless fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "LB address"); !ok || f.Value != "pending" || f.Tone != "warning" {
		t.Fatalf("pending LB fact wrong: %+v", f)
	}
	if _, ok := factByLabel(facts, "session affinity"); ok {
		t.Fatalf("session affinity 'None' must be suppressed")
	}
}
