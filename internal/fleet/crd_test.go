package fleet

import (
	"context"
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/crd"
)

func TestGetInstanceDetailEventsErrorDegrades(t *testing.T) {
	wGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{wGVR: "WidgetList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]interface{}{"name": "w1", "namespace": "team-a", "uid": "uid-1"},
		"status":   map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	typed := typedfake.NewSimpleClientset()
	typed.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)
	})
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "widgets", "team-a", "w1")
	if err != nil {
		t.Fatalf("an events error must not fail the detail: %v", err)
	}
	if !strings.Contains(d.YAML, "kind: Widget") || len(d.Conditions) != 1 {
		t.Fatalf("yaml/conditions must survive an events error: %+v", d)
	}
	if len(d.Events) != 0 {
		t.Fatalf("want no events on error, got %d", len(d.Events))
	}
}

func TestGetInstanceDetailNoUIDSkipsEvents(t *testing.T) {
	wGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{wGVR: "WidgetList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]interface{}{"name": "w1", "namespace": "team-a"}, // no uid
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	// Seed an unrelated event; the fake does not apply the uid field selector, so
	// without the empty-uid guard this would leak into the result.
	ev := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "x.evt", Namespace: "team-a"}, Type: "Normal", Reason: "X"}
	typed := typedfake.NewSimpleClientset(ev)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "widgets", "team-a", "w1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(d.Events) != 0 {
		t.Fatalf("a uid-less object must skip the event list, got %d events", len(d.Events))
	}
}

func TestApplyResourceYAMLDryRun(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{gvr: "ConfigMapList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"namespace": "default", "name": "app"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	patched := false
	dyn.PrependReactor("patch", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patched = true
		if action.GetNamespace() != "default" {
			t.Fatalf("patch namespace = %q", action.GetNamespace())
		}
		return true, obj, nil
	})
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	err := c.ApplyResourceYAML(context.Background(), "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  namespace: default\n  name: app\n", true)
	if err != nil {
		t.Fatalf("dry-run apply: %v", err)
	}
	if !patched {
		t.Fatal("want dynamic patch")
	}
}

func TestApplyResourceYAMLRejectsTargetMismatch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{gvr: "ConfigMapList"})
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	err := c.ApplyResourceYAML(context.Background(), "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "apiVersion: v1\nkind: Secret\nmetadata:\n  namespace: default\n  name: app\n", true)
	if err == nil || !strings.Contains(err.Error(), "target mismatch") {
		t.Fatalf("want target mismatch, got %v", err)
	}
}

func TestApplyResourceYAMLRejectsMaskedSecret(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{gvr: "SecretList"})
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	err := c.ApplyResourceYAML(context.Background(), "", "v1", "secrets", "Secret", "Namespaced", "default", "app", "apiVersion: v1\nkind: Secret\nmetadata:\n  namespace: default\n  name: app\ndata:\n  password: <masked>\n", true)
	if err == nil || !strings.Contains(err.Error(), "masked Secret") {
		t.Fatalf("want masked secret rejection, got %v", err)
	}
}

func crdUnstructured(group, kind, plural, scope string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": plural + "." + group},
		"spec": map[string]interface{}{
			"group":    group,
			"names":    map[string]interface{}{"kind": kind, "plural": plural},
			"scope":    scope,
			"versions": []interface{}{map[string]interface{}{"name": "v1", "served": true, "storage": true}},
		},
	}}
}

func TestListCRDs(t *testing.T) {
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{crd.GVR: "CustomResourceDefinitionList"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds,
		crdUnstructured("cilium.io", "CiliumEndpoint", "ciliumendpoints", "Namespaced"),
		crdUnstructured("cert-manager.io", "Certificate", "certificates", "Namespaced"),
	)
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	infos, err := c.ListCRDs(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("want 2 CRDs, got %d", len(infos))
	}
	byKind := map[string]crd.Info{}
	for _, i := range infos {
		byKind[i.Kind] = i
	}
	if byKind["CiliumEndpoint"].Plural != "ciliumendpoints" || byKind["CiliumEndpoint"].Version != "v1" {
		t.Fatalf("cilium: %+v", byKind["CiliumEndpoint"])
	}
}

func TestCountResourceUncapped(t *testing.T) {
	scheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(scheme)
	mc := metadatafake.NewSimpleMetadataClient(scheme,
		partialMeta("example.com", "v1", "Widget", "a", "w1"),
		partialMeta("example.com", "v1", "Widget", "b", "w2"),
		partialMeta("example.com", "v1", "Widget", "b", "w3"),
	)
	c := NewClusterConn("x", nil, mc, nil, nil, clock.Real{}, config.MetricsConfig{})

	n, capped, err := c.CountResource(context.Background(), "example.com", "v1", "widgets")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 || capped {
		t.Fatalf("want 3 uncapped, got %d capped=%v", n, capped)
	}
}

func TestListInstances(t *testing.T) {
	scheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(scheme)
	mc := metadatafake.NewSimpleMetadataClient(scheme,
		partialMeta("example.com", "v1", "Widget", "team-a", "w1"),
		partialMeta("example.com", "v1", "Widget", "team-b", "w2"),
	)
	c := NewClusterConn("x", nil, mc, nil, nil, clock.Real{}, config.MetricsConfig{})

	items, next, err := c.ListInstances(context.Background(), "example.com", "v1", "widgets", 100, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 instances, got %d", len(items))
	}
	byName := map[string]crd.InstanceMeta{}
	for _, m := range items {
		byName[m.Name] = m
	}
	if byName["w1"].Namespace != "team-a" {
		t.Fatalf("w1 namespace: %q", byName["w1"].Namespace)
	}
	if next != "" {
		t.Fatalf("fake should report no continue token, got %q", next)
	}
}

func TestListServiceInstancesExposeNetworkFields(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "external-gateway", Namespace: "envoy-gateway-system"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeLoadBalancer,
			ClusterIP: "10.43.12.9",
			Ports: []corev1.ServicePort{{
				Name: "https", Port: 443, Protocol: corev1.ProtocolTCP,
				TargetPort: intstr.FromInt(8443), NodePort: 32443,
			}},
		},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "192.0.2.10"}}}},
	}
	typed := typedfake.NewSimpleClientset(svc)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	items, _, err := c.ListInstances(context.Background(), "", "v1", "services", 100, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 service, got %d", len(items))
	}
	f := items[0].Fields
	if f["type"] != "LoadBalancer" || f["clusterIP"] != "10.43.12.9" || f["externalIP"] != "192.0.2.10" {
		t.Fatalf("service fields: %+v", f)
	}
	if !strings.Contains(f["ports"], "https 443/TCP->8443") || !strings.Contains(f["ports"], "node:32443") {
		t.Fatalf("ports: %q", f["ports"])
	}
}

func TestListEndpointSliceInstancesExposeEndpointFields(t *testing.T) {
	ready := true
	notReady := false
	name := "http"
	port := int32(8080)
	proto := corev1.ProtocolTCP
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-abc",
			Namespace: "apps",
			Labels:    map[string]string{"kubernetes.io/service-name": "api"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
			{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
		},
		Ports: []discoveryv1.EndpointPort{{Name: &name, Port: &port, Protocol: &proto}},
	}
	typed := typedfake.NewSimpleClientset(slice)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	items, _, err := c.ListInstances(context.Background(), "discovery.k8s.io", "v1", "endpointslices", 100, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 endpointslice, got %d", len(items))
	}
	f := items[0].Fields
	if f["service"] != "api" || f["addressType"] != "IPv4" || f["endpoints"] != "1/2" {
		t.Fatalf("endpoint fields: %+v", f)
	}
	if f["addresses"] != "10.0.0.1, 10.0.0.2" || f["ports"] != "http 8080/TCP" {
		t.Fatalf("addresses/ports: %+v", f)
	}
}

func TestListStorageInstancesExposeBindingFields(t *testing.T) {
	class := "fast"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-api-0", Namespace: "apps"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeName:       "pvc-123",
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-123"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName:              "fast",
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
			ClaimRef:                      &corev1.ObjectReference{Namespace: "apps", Name: "data-api-0"},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	typed := typedfake.NewSimpleClientset(pvc, pv)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	pvcs, _, err := c.ListInstances(context.Background(), "", "v1", "persistentvolumeclaims", 100, "")
	if err != nil {
		t.Fatalf("pvc list: %v", err)
	}
	if f := pvcs[0].Fields; f["status"] != "Bound" || f["class"] != "fast" || f["size"] != "20Gi" || f["modes"] != "RWO" || f["volume"] != "pvc-123" {
		t.Fatalf("pvc fields: %+v", f)
	}

	pvs, _, err := c.ListInstances(context.Background(), "", "v1", "persistentvolumes", 100, "")
	if err != nil {
		t.Fatalf("pv list: %v", err)
	}
	if f := pvs[0].Fields; f["status"] != "Bound" || f["claim"] != "apps/data-api-0" || f["reclaim"] != "Delete" {
		t.Fatalf("pv fields: %+v", f)
	}
}

func TestListIngressNetworkPolicyConfigAndSecretFields(t *testing.T) {
	immutable := true
	ingressClass := "external"
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &ingressClass,
			TLS:              []networkingv1.IngressTLS{{SecretName: "api-tls", Hosts: []string{"api.example.com"}}},
			Rules: []networkingv1.IngressRule{{
				Host: "api.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 8080}}},
				}}}},
			}},
		},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.20"}}}},
	}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "api-allow", Namespace: "apps"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "api-secret", Namespace: "apps"}, Type: corev1.SecretTypeOpaque, Immutable: &immutable, Data: map[string][]byte{"password": []byte("hidden")}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "apps"}, Immutable: &immutable, Data: map[string]string{"config.yaml": "x"}, BinaryData: map[string][]byte{"blob": []byte("x")}}
	typed := typedfake.NewSimpleClientset(ing, np, secret, cm)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	ings, _, err := c.ListInstances(context.Background(), "networking.k8s.io", "v1", "ingresses", 100, "")
	if err != nil {
		t.Fatalf("ingress list: %v", err)
	}
	if f := ings[0].Fields; f["class"] != "external" || f["hosts"] != "api.example.com" || f["address"] != "192.0.2.20" || f["tls"] != "api-tls" || f["backends"] != "api:8080" {
		t.Fatalf("ingress fields: %+v", f)
	}

	nps, _, err := c.ListInstances(context.Background(), "networking.k8s.io", "v1", "networkpolicies", 100, "")
	if err != nil {
		t.Fatalf("networkpolicy list: %v", err)
	}
	if f := nps[0].Fields; f["selector"] != "app=api" || f["policyTypes"] != "Ingress,Egress" || f["ingress"] != "1" || f["egress"] != "1" {
		t.Fatalf("network policy fields: %+v", f)
	}

	secrets, _, err := c.ListInstances(context.Background(), "", "v1", "secrets", 100, "")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if f := secrets[0].Fields; f["type"] != "Opaque" || f["keys"] != "1" || f["immutable"] != "yes" {
		t.Fatalf("secret fields: %+v", f)
	}

	configMaps, _, err := c.ListInstances(context.Background(), "", "v1", "configmaps", 100, "")
	if err != nil {
		t.Fatalf("configmap list: %v", err)
	}
	if f := configMaps[0].Fields; f["keys"] != "2" || f["immutable"] != "yes" {
		t.Fatalf("configmap fields: %+v", f)
	}
}

func TestListAutoscalingDisruptionAndBatchFields(t *testing.T) {
	min := int32(2)
	targetCPU := int32(80)
	currentCPU := int32(42)
	suspend := true
	completions := int32(3)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"},
			MinReplicas:    &min,
			MaxReplicas:    5,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
				Name:   corev1.ResourceCPU,
				Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &targetCPU},
			}}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 3,
			DesiredReplicas: 4,
			CurrentMetrics: []autoscalingv2.MetricStatus{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{
				Name:    corev1.ResourceCPU,
				Current: autoscalingv2.MetricValueStatus{AverageUtilization: &currentCPU},
			}}},
		},
	}
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"}, Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1, CurrentHealthy: 4, DesiredHealthy: 3, ExpectedPods: 5}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "apps"}, Spec: batchv1.JobSpec{Completions: &completions}, Status: batchv1.JobStatus{Active: 1, Succeeded: 2, Failed: 1}}
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "apps"}, Spec: batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &suspend}, Status: batchv1.CronJobStatus{Active: []corev1.ObjectReference{{Name: "backup-1"}}, LastScheduleTime: &metav1.Time{Time: metav1.Now().Time}}}
	typed := typedfake.NewSimpleClientset(hpa, pdb, job, cj)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	hpas, _, err := c.ListInstances(context.Background(), "autoscaling", "v2", "horizontalpodautoscalers", 100, "")
	if err != nil {
		t.Fatalf("hpa list: %v", err)
	}
	if f := hpas[0].Fields; f["target"] != "Deployment/api" || f["replicas"] != "2/3/4/5" || f["metrics"] != "cpu 42%/80%" {
		t.Fatalf("hpa fields: %+v", f)
	}

	pdbs, _, err := c.ListInstances(context.Background(), "policy", "v1", "poddisruptionbudgets", 100, "")
	if err != nil {
		t.Fatalf("pdb list: %v", err)
	}
	if f := pdbs[0].Fields; f["allowed"] != "1" || f["healthy"] != "4/3" || f["expected"] != "5" {
		t.Fatalf("pdb fields: %+v", f)
	}

	jobs, _, err := c.ListInstances(context.Background(), "batch", "v1", "jobs", 100, "")
	if err != nil {
		t.Fatalf("job list: %v", err)
	}
	if f := jobs[0].Fields; f["active"] != "1" || f["succeeded"] != "2" || f["failed"] != "1" || f["completions"] != "2/3" {
		t.Fatalf("job fields: %+v", f)
	}

	cronJobs, _, err := c.ListInstances(context.Background(), "batch", "v1", "cronjobs", 100, "")
	if err != nil {
		t.Fatalf("cronjob list: %v", err)
	}
	if f := cronJobs[0].Fields; f["schedule"] != "0 2 * * *" || f["suspended"] != "yes" || f["active"] != "1" || f["lastSchedule"] == "-" {
		t.Fatalf("cronjob fields: %+v", f)
	}
}

func TestListUnstructuredOperatorInstancesExposeFields(t *testing.T) {
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	cert := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": "api-tls", "namespace": "apps"},
		"spec": map[string]interface{}{
			"issuerRef":   map[string]interface{}{"kind": "ClusterIssuer", "name": "letsencrypt"},
			"dnsNames":    []interface{}{"api.example.com", "api.internal"},
			"renewBefore": "720h",
		},
		"status": map[string]interface{}{
			"notAfter":    "2026-09-01T12:00:00Z",
			"renewalTime": "2026-08-01T12:00:00Z",
			"conditions":  []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
		},
	}}
	cnp := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata":   map[string]interface{}{"name": "api-allow", "namespace": "apps"},
		"spec": map[string]interface{}{
			"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "api"}},
			"ingress":          []interface{}{map[string]interface{}{}},
			"ingressDeny":      []interface{}{map[string]interface{}{}},
			"egress":           []interface{}{map[string]interface{}{}, map[string]interface{}{}},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{
		certGVR: "CertificateList",
		cnpGVR:  "CiliumNetworkPolicyList",
	}, cert, cnp)
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	certs, _, err := c.ListInstances(context.Background(), "cert-manager.io", "v1", "certificates", 100, "")
	if err != nil {
		t.Fatalf("cert list: %v", err)
	}
	if f := certs[0].Fields; f["ready"] != "ready" || f["issuer"] != "ClusterIssuer/letsencrypt" || f["expires"] != "2026-09-01" || f["renew"] != "2026-08-01" || !strings.Contains(f["dns"], "api.example.com") {
		t.Fatalf("certificate fields: %+v", f)
	}

	cnps, _, err := c.ListInstances(context.Background(), "cilium.io", "v2", "ciliumnetworkpolicies", 100, "")
	if err != nil {
		t.Fatalf("cnp list: %v", err)
	}
	if f := cnps[0].Fields; f["selector"] != "app=api" || f["ingress"] != "1 +1 deny" || f["egress"] != "2" || f["scope"] != "namespace" {
		t.Fatalf("cilium policy fields: %+v", f)
	}
}

func TestGetInstanceDetail(t *testing.T) {
	wGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{wGVR: "WidgetList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata": map[string]interface{}{
			"name": "w1", "namespace": "team-a", "uid": "uid-1",
			"labels":      map[string]interface{}{"app": "w"},
			"annotations": map[string]interface{}{"owner": "platform"},
		},
		"status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "reason": "OK", "message": "ready"}}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)

	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "w1.evt", Namespace: "team-a"},
		InvolvedObject: corev1.ObjectReference{Kind: "Widget", Name: "w1", Namespace: "team-a", UID: "uid-1"},
		Type:           "Warning", Reason: "Failed", Message: "could not reconcile", Count: 3,
		LastTimestamp: metav1.Now(),
	}
	typed := typedfake.NewSimpleClientset(ev)

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "widgets", "team-a", "w1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.Kind != "Widget" || d.Name != "w1" || d.Namespace != "team-a" {
		t.Fatalf("header: %+v", d)
	}
	if len(d.Conditions) != 1 || d.Conditions[0].Type != "Ready" {
		t.Fatalf("conditions: %+v", d.Conditions)
	}
	if len(d.Events) != 1 || d.Events[0].Type != "Warning" || d.Events[0].Count != 3 {
		t.Fatalf("events: %+v", d.Events)
	}
	if !strings.Contains(d.YAML, "kind: Widget") {
		t.Fatalf("yaml: %s", d.YAML)
	}
	if d.Labels["app"] != "w" {
		t.Fatalf("labels: %+v", d.Labels)
	}
	if d.Annotations["owner"] != "platform" {
		t.Fatalf("annotations: %+v", d.Annotations)
	}
}

func TestGetInstanceDetailClusterScoped(t *testing.T) {
	nGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnodes"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{nGVR: "CiliumNodeList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNode",
		"metadata":   map[string]interface{}{"name": "node-1", "uid": "uid-n1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "cilium.io", "v2", "ciliumnodes", "", "node-1")
	if err != nil {
		t.Fatalf("cluster-scoped detail: %v", err)
	}
	if d.Kind != "CiliumNode" || d.Namespace != "" || !strings.Contains(d.YAML, "kind: CiliumNode") {
		t.Fatalf("cluster-scoped: %+v", d)
	}
}

func TestGetInstanceDetailSecretMasked(t *testing.T) {
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{secretGVR: "SecretList"}

	b64pass := "aHVudGVyMg==" // base64("hunter2")
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "app-secret", "namespace": "default", "uid": "uid-s1"},
		"data": map[string]interface{}{
			"password": b64pass,
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	typed := typedfake.NewSimpleClientset()
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "", "v1", "secrets", "default", "app-secret")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}

	// YAML must contain the key name but not the base64 value.
	if !strings.Contains(d.YAML, "password") {
		t.Fatalf("key name missing from YAML:\n%s", d.YAML)
	}
	if strings.Contains(d.YAML, b64pass) {
		t.Fatalf("base64 value must be masked in YAML:\n%s", d.YAML)
	}
	if !strings.Contains(d.YAML, "<masked>") {
		t.Fatalf("<masked> placeholder missing from YAML:\n%s", d.YAML)
	}

	// SecretKeys must list the key with correct byte length.
	if len(d.SecretKeys) != 1 || d.SecretKeys[0].Key != "password" || d.SecretKeys[0].Bytes != 7 {
		t.Fatalf("SecretKeys: %+v", d.SecretKeys)
	}
}

func TestGetInstanceDetailNonSecretUnaffected(t *testing.T) {
	wGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{wGVR: "WidgetList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w1", "namespace": "team-a", "uid": "uid-w1"},
		"spec":       map[string]interface{}{"field": "visible-value"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "widgets", "team-a", "w1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if !strings.Contains(d.YAML, "visible-value") {
		t.Fatalf("non-secret field must not be masked:\n%s", d.YAML)
	}
	if len(d.SecretKeys) != 0 {
		t.Fatalf("SecretKeys must be empty for non-secret, got %+v", d.SecretKeys)
	}
}

func TestRevealSecretKey(t *testing.T) {
	// client-go typed fake stores Secret.Data as []byte; the Secret.Data field
	// is decoded bytes, not base64.
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("hunter2"), "token": []byte("abc")},
	}
	typed := typedfake.NewSimpleClientset(sec)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	val, err := c.RevealSecretKey(context.Background(), "default", "app-secret", "password")
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if val != "hunter2" {
		t.Fatalf("want 'hunter2', got %q", val)
	}
}

func TestRevealSecretKeyMissingKey(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("abc")},
	}
	typed := typedfake.NewSimpleClientset(sec)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	_, err := c.RevealSecretKey(context.Background(), "default", "app-secret", "nonexistent")
	if err == nil {
		t.Fatal("want error for missing key, got nil")
	}
}

func TestRevealSecretKeyMissingSecret(t *testing.T) {
	typed := typedfake.NewSimpleClientset()
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	_, err := c.RevealSecretKey(context.Background(), "default", "ghost-secret", "key")
	if err == nil {
		t.Fatal("want error for missing secret, got nil")
	}
}

func TestGetInstanceDetailServiceBacking(t *testing.T) {
	serviceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{serviceGVR: "ServiceList"}
	svcObj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]interface{}{"name": "web", "namespace": "default", "uid": "uid-svc1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, svcObj)

	// Build a typed Service + EndpointSlice with one ready and one not-ready endpoint.
	readyTrue := true
	readyFalse := false
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP}},
			Selector: map[string]string{"app": "web"},
		},
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc",
			Namespace: "default",
			Labels:    map[string]string{"kubernetes.io/service-name": "web"},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{Ready: &readyTrue},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: "web-pod-1"},
			},
			{
				Addresses:  []string{"10.0.0.2"},
				Conditions: discoveryv1.EndpointConditions{Ready: &readyFalse},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: "web-pod-2"},
			},
		},
	}
	typed := typedfake.NewSimpleClientset(svc, slice)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "", "v1", "services", "default", "web")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.ServiceBacking == nil {
		t.Fatal("ServiceBacking must be non-nil for a v1 Service")
	}
	b := d.ServiceBacking
	if b.Ready != 1 {
		t.Fatalf("want Ready=1, got %d", b.Ready)
	}
	if b.NotReady != 1 {
		t.Fatalf("want NotReady=1, got %d", b.NotReady)
	}
	if len(b.Ports) != 1 || b.Ports[0].Port != 80 {
		t.Fatalf("ports: %+v", b.Ports)
	}
	if b.Selector["app"] != "web" {
		t.Fatalf("selector: %+v", b.Selector)
	}
}

func TestGetInstanceDetailNonServiceNoServiceBacking(t *testing.T) {
	wGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{wGVR: "WidgetList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w1", "namespace": "team-a", "uid": "uid-w2"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "widgets", "team-a", "w1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.ServiceBacking != nil {
		t.Fatalf("ServiceBacking must be nil for non-service, got %+v", d.ServiceBacking)
	}
}

func TestGetInstanceDetailHPAScalingPresent(t *testing.T) {
	hpaGVR := schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{hpaGVR: "HorizontalPodAutoscalerList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata":   map[string]interface{}{"name": "web-hpa", "namespace": "default", "uid": "uid-hpa1"},
		"spec": map[string]interface{}{
			"maxReplicas": int64(10),
			"minReplicas": int64(2),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "web",
			},
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "cpu",
						"target": map[string]interface{}{
							"type":               "Utilization",
							"averageUtilization": int64(70),
						},
					},
				},
			},
		},
		"status": map[string]interface{}{
			"currentReplicas": int64(4),
			"desiredReplicas": int64(4),
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "autoscaling", "v2", "horizontalpodautoscalers", "default", "web-hpa")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.HPAScaling == nil {
		t.Fatal("HPAScaling must be non-nil for an autoscaling HPA")
	}
	s := d.HPAScaling
	if s.MaxReplicas != 10 {
		t.Errorf("MaxReplicas: want 10, got %d", s.MaxReplicas)
	}
	if s.TargetKind != "Deployment" || s.TargetName != "web" {
		t.Errorf("scaleTargetRef: kind=%q name=%q", s.TargetKind, s.TargetName)
	}
}

func TestGetInstanceDetailNonHPANoHPAScaling(t *testing.T) {
	wGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{wGVR: "WidgetList"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w1", "namespace": "team-a", "uid": "uid-w3"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	d, err := c.GetInstanceDetail(context.Background(), "example.com", "v1", "widgets", "team-a", "w1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.HPAScaling != nil {
		t.Fatalf("HPAScaling must be nil for non-HPA, got %+v", d.HPAScaling)
	}
}

func partialMeta(group, version, kind, ns, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: group + "/" + version, Kind: kind},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
	}
}
