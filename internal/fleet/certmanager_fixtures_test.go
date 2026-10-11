package fleet

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// cert-manager object fixtures for the chain tests.

func certUnstructured(name, ns, ready, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"secretName": name + "-tls",
			"issuerRef":  map[string]interface{}{"name": "lab-acme-staging", "kind": "ClusterIssuer"},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready, "reason": reason},
		}},
	}}
}

func clusterIssuerUnstructured(name, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]interface{}{"name": name},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready},
		}},
	}}
}
