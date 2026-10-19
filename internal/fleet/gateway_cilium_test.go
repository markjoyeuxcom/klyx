package fleet

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func cnpGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
}
func ccnpGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
}

func cnp(ns, name string, endpointSelector map[string]interface{}, spec map[string]interface{}) *unstructured.Unstructured {
	s := map[string]interface{}{"endpointSelector": endpointSelector}
	for k, v := range spec {
		s[k] = v
	}
	meta := map[string]interface{}{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	kind := "CiliumNetworkPolicy"
	if ns == "" {
		kind = "CiliumClusterwideNetworkPolicy"
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2", "kind": kind, "metadata": meta, "spec": s,
	}}
}

func ciliumDiscovery() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{
		{Name: "ciliumnetworkpolicies", Namespaced: true, Kind: "CiliumNetworkPolicy"},
		{Name: "ciliumclusterwidenetworkpolicies", Namespaced: false, Kind: "CiliumClusterwideNetworkPolicy"},
	}}}
}

func TestAttachCiliumPolicies(t *testing.T) {
	scheme := dynScheme()
	listKinds := gwListKinds(map[schema.GroupVersionResource]string{
		cnpGVR():  "CiliumNetworkPolicyList",
		ccnpGVR(): "CiliumClusterwideNetworkPolicyList",
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	put := func(gvr schema.GroupVersionResource, o *unstructured.Unstructured) {
		ns, _, _ := unstructured.NestedString(o.Object, "metadata", "namespace")
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", gvr, err)
		}
	}
	put(gwGVR(), gw("eg", "infra"))
	put(hrGVR(), hr("share", "apps", "eg", "infra", "share-api"))
	// narrow CNP matching the share-api service selector {app: share-api}
	put(cnpGVR(), cnp("apps", "share-allow", map[string]interface{}{"matchLabels": map[string]interface{}{"app": "share-api"}},
		map[string]interface{}{"ingress": []interface{}{map[string]interface{}{}}}))
	// namespace-wide CNP (empty selector) in apps
	put(cnpGVR(), cnp("apps", "ns-deny", map[string]interface{}{}, map[string]interface{}{"egress": []interface{}{map[string]interface{}{}}}))
	// broad CCNP (empty selector) → header context
	put(ccnpGVR(), cnp("", "cluster-deny", map[string]interface{}{}, map[string]interface{}{"ingress": []interface{}{map[string]interface{}{}}}))

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "share-api"}, Ports: []corev1.ServicePort{{Port: 80}}}}
	typed := typedfake.NewSimpleClientset(svc)
	typed.Resources = ciliumDiscovery()

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	cnps := topo.Routes[0].Services[0].CNPs
	var hasSelector, hasNsWide bool
	for _, p := range cnps {
		if p.Name == "share-allow" && p.Match == "selector" {
			hasSelector = true
		}
		if p.Name == "ns-deny" && p.Match == "namespace-wide" {
			hasNsWide = true
		}
	}
	if !hasSelector {
		t.Fatalf("selector CNP not attached: %+v", cnps)
	}
	if !hasNsWide {
		t.Fatalf("namespace-wide CNP not attached: %+v", cnps)
	}
	if len(topo.ClusterPolicies) != 1 || topo.ClusterPolicies[0].Name != "cluster-deny" || topo.ClusterPolicies[0].Match != "cluster-wide" {
		t.Fatalf("cluster-wide CCNP not in header bucket: %+v", topo.ClusterPolicies)
	}
}

func TestAttachCiliumExpressionsOnlyWarns(t *testing.T) {
	scheme := dynScheme()
	listKinds := gwListKinds(map[schema.GroupVersionResource]string{cnpGVR(): "CiliumNetworkPolicyList"})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	put := func(gvr schema.GroupVersionResource, o *unstructured.Unstructured) {
		ns, _, _ := unstructured.NestedString(o.Object, "metadata", "namespace")
		_, _ = dyn.Resource(gvr).Namespace(ns).Create(context.Background(), o, metav1.CreateOptions{})
	}
	put(gwGVR(), gw("eg", "infra"))
	put(hrGVR(), hr("share", "apps", "eg", "infra", "share-api"))
	put(cnpGVR(), cnp("apps", "expr-only", map[string]interface{}{"matchExpressions": []interface{}{map[string]interface{}{"key": "tier", "operator": "Exists"}}},
		map[string]interface{}{"ingress": []interface{}{map[string]interface{}{}}}))

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "share-api"}}}
	typed := typedfake.NewSimpleClientset(svc)
	// only CNP served; CCNP "not installed"
	typed.Resources = []*metav1.APIResourceList{{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{
		{Name: "ciliumnetworkpolicies", Namespaced: true, Kind: "CiliumNetworkPolicy"},
	}}}

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, _ := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if len(topo.Routes[0].Services[0].CNPs) != 0 {
		t.Fatalf("expressions-only must not attach: %+v", topo.Routes[0].Services[0].CNPs)
	}
	var warned, notInstalled bool
	for _, w := range topo.Warnings {
		if strings.Contains(w, "matchExpressions-only selector not evaluated") {
			warned = true
		}
		if strings.Contains(w, "CiliumClusterwideNetworkPolicy CRD not installed") {
			notInstalled = true
		}
	}
	if !warned {
		t.Fatalf("want expressions-only warning: %+v", topo.Warnings)
	}
	if !notInstalled {
		t.Fatalf("want CCNP not-installed warning: %+v", topo.Warnings)
	}
}
