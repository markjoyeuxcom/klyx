package healthsrc

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/observe"
)

func admObj(kind, name, uid string, body map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "uid": uid, "generation": int64(1), "creationTimestamp": time.Now().UTC().Format(time.RFC3339)}}
	for k, v := range body {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func svcHook(name, service string, fail bool) map[string]any {
	fp := "Fail"
	if !fail {
		fp = "Ignore"
	}
	return map[string]any{"name": name, "failurePolicy": fp, "sideEffects": "None", "admissionReviewVersions": []any{"v1"},
		"clientConfig": map[string]any{"service": map[string]any{"namespace": "sys", "name": service, "port": int64(443)}},
		"rules":        []any{map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{"lab.klyx.dev"}, "apiVersions": []any{"v1"}, "resources": []any{"widgets"}}}}
}

func hookSvc(name string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "sys", Name: name, UID: types.UID("svc-" + name)},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": name}, Ports: []corev1.ServicePort{{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP}}}}
}

func hookSlice(service string) *discoveryv1.EndpointSlice {
	name, port, proto := "https", int32(8443), corev1.ProtocolTCP
	return &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "sys", Name: service + "-1", Labels: map[string]string{discoveryv1.LabelServiceName: service}},
		AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &name, Port: &port, Protocol: &proto}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.9"}}}}
}

var admGVR = func(r string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: admission.Group, Version: admission.APIVersion, Resource: r}
}

func startAdmission(t *testing.T, typed *fake.Clientset, objs ...*unstructured.Unstructured) (*sink, *observe.Observer, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	lists := map[schema.GroupVersionResource]string{}
	for _, r := range []string{KindValidatingWebhooks, KindMutatingWebhooks, KindValidatingPolicies, KindMutatingPolicies, KindValidatingBindings, KindMutatingBindings} {
		lists[admGVR(r)] = map[string]string{KindValidatingWebhooks: "ValidatingWebhookConfigurationList", KindMutatingWebhooks: "MutatingWebhookConfigurationList",
			KindValidatingPolicies: "ValidatingAdmissionPolicyList", KindMutatingPolicies: "MutatingAdmissionPolicyList",
			KindValidatingBindings: "ValidatingAdmissionPolicyBindingList", KindMutatingBindings: "MutatingAdmissionPolicyBindingList"}[r]
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists)
	res := map[string]string{"ValidatingWebhookConfiguration": KindValidatingWebhooks, "MutatingWebhookConfiguration": KindMutatingWebhooks,
		"ValidatingAdmissionPolicy": KindValidatingPolicies, "MutatingAdmissionPolicy": KindMutatingPolicies,
		"ValidatingAdmissionPolicyBinding": KindValidatingBindings, "MutatingAdmissionPolicyBinding": KindMutatingBindings}
	for _, o := range objs {
		if _, err := dyn.Resource(admGVR(res[o.GetKind()])).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		if group == admission.Group {
			return observe.Probe{Served: true, Version: "v1"}, nil
		}
		return observe.Probe{Served: false}, nil
	}
	nd := NativeDeps{Cluster: "c1", Dyn: dyn, Served: served, Typed: typed}
	kinds, domains := Admission(nd)
	kinds = append(SharedNetworkKinds(nd), kinds...)
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: 10 * time.Millisecond, ReprobeEvery: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	o.SubscribeRows(TaskAdmission, "webhooks", observe.Owner{Kind: "lens", ID: "t"})
	o.SubscribeRows(TaskAdmission, "policies", observe.Owner{Kind: "lens", ID: "t"})
	o.SetFact(FactParamKinds, admission.ParamKinds{admission.KeyOf("example.com", "v1", "Limits"): {State: admission.ParamNotServed}})
	return s, o, dyn
}

func configRows(o *observe.Observer) map[string]Row[AdmissionConfigRow] {
	rows, _ := o.RowsNow(TaskAdmission, "webhooks")
	out := map[string]Row[AdmissionConfigRow]{}
	if set, ok := rows.Rows.(RowSet[AdmissionConfigRow]); ok {
		for _, r := range set.Rows {
			out[r.Data.Config.Name] = r
		}
	}
	return out
}

// Coverage S3a §4-§6 through the Observer.
func TestAdmissionParts(t *testing.T) {
	typed := fake.NewClientset(hookSvc("ok"), hookSlice("ok"))
	s, o, dyn := startAdmission(t, typed,
		admObj("ValidatingWebhookConfiguration", "healthy", "u-healthy", map[string]any{"webhooks": []any{svcHook("a.lab", "ok", true)}}),
		admObj("ValidatingWebhookConfiguration", "broken", "u-broken", map[string]any{"webhooks": []any{svcHook("a.lab", "ok", true), svcHook("b.lab", "late", true)}}),
		admObj("MutatingWebhookConfiguration", "open", "u-open", map[string]any{"webhooks": []any{svcHook("c.lab", "missing", false)}}),
		admObj("ValidatingAdmissionPolicy", "unbound", "u-p1", map[string]any{"spec": map[string]any{"validations": []any{map[string]any{"expression": "true"}}}}),
		admObj("ValidatingAdmissionPolicy", "params", "u-p2", map[string]any{"spec": map[string]any{"paramKind": map[string]any{"apiVersion": "example.com/v1", "kind": "Limits"},
			"validations": []any{map[string]any{"expression": "true"}}}}),
		admObj("ValidatingAdmissionPolicyBinding", "params-b", "u-b1", map[string]any{"spec": map[string]any{"policyName": "params", "validationActions": []any{"Deny"},
			"paramRef": map[string]any{"name": "x", "namespace": "team", "parameterNotFoundAction": "Deny"}}}),
		admObj("ValidatingAdmissionPolicyBinding", "dangling", "u-b2", map[string]any{"spec": map[string]any{"policyName": "gone", "validationActions": []any{"Deny"}}}),
	)
	waitFor(t, "both parts counted", func() bool {
		w, ok1 := nativePart(s, TaskAdmission, "webhooks")
		p, ok2 := nativePart(s, TaskAdmission, "policies")
		return ok1 && ok2 && w.Native != nil && p.Native != nil && w.Native.Total == 3 && w.Native.Included == 2 && p.Native.Total == 2 && p.Native.Included == 1
	})
	rows := configRows(o)
	if r := rows["healthy"]; r.Eval.Role.String() != "ok" {
		t.Fatalf("healthy: %+v", r.Eval)
	}
	if r := rows["broken"]; r.Eval.Role.String() != "fault" || !strings.Contains(r.Eval.Reason, "b.lab: Service sys/late not found") || r.Ref.UID != "u-broken" {
		t.Fatalf("broken: %+v %+v", r.Eval, r.Ref)
	}
	if r := rows["open"]; r.Eval.Role.String() != "degraded" || !strings.Contains(r.Eval.Reason, "skip this webhook") {
		t.Fatalf("fail-open: %+v", r.Eval)
	}
	prows, _ := o.RowsNow(TaskAdmission, "policies")
	var facts, verdicts int
	for _, r := range prows.Rows.(RowSet[AdmissionPolicyRow]).Rows {
		switch {
		case r.Data.FactOnly():
			facts++
			if r.Data.Binding.Name == "dangling" && r.Data.PolicyState != "not found" {
				t.Fatalf("dangling binding: %+v", r.Data)
			}
			if r.Eval.Label != "" {
				t.Fatalf("a binding's row carries no evaluation: %+v", r.Eval)
			}
		case r.Data.Policy.Name == "unbound":
			verdicts++
			if r.Eval.Label != "Not bound" {
				t.Fatalf("unbound: %+v", r.Eval)
			}
		case r.Data.Policy.Name == "params":
			verdicts++
			if r.Eval.Role.String() != "fault" || r.Eval.Label != "paramKind not served" || r.Data.ParamKind == nil {
				t.Fatalf("params: %+v", r.Eval)
			}
		}
	}
	if facts != 2 || verdicts != 2 {
		t.Fatalf("facts %d verdicts %d", facts, verdicts)
	}

	// a Service appearing late heals the configuration with no
	// configuration event
	if _, err := typed.CoreV1().Services("sys").Create(context.Background(), hookSvc("late"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := typed.DiscoveryV1().EndpointSlices("sys").Create(context.Background(), hookSlice("late"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the late Service heals it", func() bool { return configRows(o)["broken"].Eval.Role.String() == "ok" })

	// replaced under its name: a new UID
	if err := dyn.Resource(admGVR(KindValidatingWebhooks)).Delete(context.Background(), "healthy", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(admGVR(KindValidatingWebhooks)).Create(context.Background(),
		admObj("ValidatingWebhookConfiguration", "healthy", "u-healthy-2", map[string]any{"webhooks": []any{svcHook("a.lab", "ok", true)}}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the replacement's UID", func() bool { return configRows(o)["healthy"].Ref.UID == "u-healthy-2" })
}
