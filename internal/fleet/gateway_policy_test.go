package fleet

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func ctpGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Resource: "clienttrafficpolicies"}
}
func btpGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Resource: "backendtrafficpolicies"}
}
func btlsGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1alpha3", Resource: "backendtlspolicies"}
}

func policy(apiVersion, kind, ns, name string, targetRef map[string]interface{}, spec map[string]interface{}) *unstructured.Unstructured {
	s := map[string]interface{}{"targetRef": targetRef}
	for k, v := range spec {
		s[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]interface{}{"namespace": ns, "name": name},
		"spec":     s,
	}}
}

// policyDiscovery advertises the served policy resources for fake discovery.
//
// The leading gateway.networking.k8s.io/v1 entry is required so the fake
// discovery's ServerGroups() reports v1 as the gateway group's preferred
// version (it derives PreferredVersion from the first Resources entry per
// group). Without it the trailing v1alpha3 BackendTLSPolicy entry would make
// v1alpha3 preferred, and gwGVR("gateways") would resolve to the wrong version,
// so the seeded v1 Gateway/HTTPRoute would not be found.
func policyDiscovery() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{GroupVersion: "gateway.networking.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "gateways", Namespaced: true, Kind: "Gateway"},
			{Name: "httproutes", Namespaced: true, Kind: "HTTPRoute"},
		}},
		{GroupVersion: "gateway.envoyproxy.io/v1alpha1", APIResources: []metav1.APIResource{
			{Name: "clienttrafficpolicies", Namespaced: true, Kind: "ClientTrafficPolicy"},
			{Name: "backendtrafficpolicies", Namespaced: true, Kind: "BackendTrafficPolicy"},
			{Name: "securitypolicies", Namespaced: true, Kind: "SecurityPolicy"},
			{Name: "envoyextensionpolicies", Namespaced: true, Kind: "EnvoyExtensionPolicy"},
		}},
		{GroupVersion: "gateway.networking.k8s.io/v1alpha3", APIResources: []metav1.APIResource{
			{Name: "backendtlspolicies", Namespaced: true, Kind: "BackendTLSPolicy"},
		}},
	}
}

func TestGatewayTopologyAttachesEnvoyPolicies(t *testing.T) {
	scheme := dynScheme()
	listKinds := gwListKinds(map[schema.GroupVersionResource]string{
		ctpGVR(): "ClientTrafficPolicyList", btpGVR(): "BackendTrafficPolicyList", btlsGVR(): "BackendTLSPolicyList",
		{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Resource: "securitypolicies"}:       "SecurityPolicyList",
		{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Resource: "envoyextensionpolicies"}: "EnvoyExtensionPolicyList",
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	seed := func(gvr schema.GroupVersionResource, o *unstructured.Unstructured) {
		ns, _, _ := unstructured.NestedString(o.Object, "metadata", "namespace")
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", gvr, err)
		}
	}
	seed(gwGVR(), gw("eg", "infra"))
	seed(hrGVR(), hr("share", "apps", "eg", "infra", "share-api"))
	// CTP -> gateway; BTP -> route; BackendTLSPolicy -> the backend Service.
	seed(ctpGVR(), policy("gateway.envoyproxy.io/v1alpha1", "ClientTrafficPolicy", "infra", "ctp",
		map[string]interface{}{"kind": "Gateway", "name": "eg"},
		map[string]interface{}{"http2": map[string]interface{}{}}))
	seed(btpGVR(), policy("gateway.envoyproxy.io/v1alpha1", "BackendTrafficPolicy", "apps", "btp",
		map[string]interface{}{"kind": "HTTPRoute", "name": "share"},
		map[string]interface{}{"retry": map[string]interface{}{"numRetries": int64(3)}}))
	seed(btlsGVR(), policy("gateway.networking.k8s.io/v1alpha3", "BackendTLSPolicy", "apps", "btls",
		map[string]interface{}{"kind": "Service", "name": "share-api"},
		map[string]interface{}{"validation": map[string]interface{}{"hostname": "share-api.apps"}}))

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}}}
	typed := typedfake.NewSimpleClientset(svc)
	typed.Resources = policyDiscovery()

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(topo.Gateway.Policies) != 1 || topo.Gateway.Policies[0].Kind != "ClientTrafficPolicy" {
		t.Fatalf("gateway policies: %+v", topo.Gateway.Policies)
	}
	if len(topo.Routes) != 1 || len(topo.Routes[0].Policies) != 1 || topo.Routes[0].Policies[0].Summary != "retries" {
		t.Fatalf("route policies: %+v", topo.Routes)
	}
	if len(topo.Routes[0].Services[0].Policies) != 1 || topo.Routes[0].Services[0].Policies[0].Kind != "BackendTLSPolicy" {
		t.Fatalf("service policies: %+v", topo.Routes[0].Services[0].Policies)
	}
}

func TestGatewayTopologyPolicyWarnings(t *testing.T) {
	scheme := dynScheme()
	listKinds := gwListKinds(map[schema.GroupVersionResource]string{btpGVR(): "BackendTrafficPolicyList"})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	ns := func(o *unstructured.Unstructured) string {
		s, _, _ := unstructured.NestedString(o.Object, "metadata", "namespace")
		return s
	}
	for gvr, o := range map[schema.GroupVersionResource]*unstructured.Unstructured{
		gwGVR(): gw("eg", "infra"),
		hrGVR(): hr("share", "apps", "eg", "infra", "share-api"),
	} {
		if _, err := dyn.Resource(gvr).Namespace(ns(o)).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// BackendTrafficPolicy list fails (served but forbidden).
	dyn.PrependReactor("list", "backendtrafficpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "gateway.envoyproxy.io", Resource: "backendtrafficpolicies"}, "", nil)
	})

	typed := typedfake.NewSimpleClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps"}})
	// Only BackendTrafficPolicy is served; the others are "not installed".
	typed.Resources = []*metav1.APIResourceList{
		{GroupVersion: "gateway.envoyproxy.io/v1alpha1", APIResources: []metav1.APIResource{
			{Name: "backendtrafficpolicies", Namespaced: true, Kind: "BackendTrafficPolicy"},
		}},
	}

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	var hasNotInstalled, hasForbidden bool
	for _, w := range topo.Warnings {
		if strings.Contains(w, "CRD not installed") {
			hasNotInstalled = true
		}
		if strings.Contains(w, "could not list BackendTrafficPolicy") && strings.Contains(w, "forbidden") {
			hasForbidden = true
		}
	}
	if !hasNotInstalled {
		t.Fatalf("want an informational 'CRD not installed' warning: %+v", topo.Warnings)
	}
	if !hasForbidden {
		t.Fatalf("want an operational 'could not list ... forbidden' warning: %+v", topo.Warnings)
	}
}
