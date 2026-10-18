package fleet

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	metafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/clock"

	"github.com/moomora/klyx/internal/admission"
)

func binding(name string, paramRef map[string]any) *unstructured.Unstructured {
	spec := map[string]any{"policyName": "p", "validationActions": []any{"Deny"}}
	if paramRef != nil {
		spec["paramRef"] = paramRef
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding",
		"metadata": map[string]any{"name": name, "uid": "u-" + name}, "spec": spec}}
}

// The on-demand param check (S3a §6): metadata only; the request-namespace
// case is not checked; forbidden is never "not found".
func TestCheckAdmissionParams(t *testing.T) {
	bgvr := schema.GroupVersionResource{Group: admission.Group, Version: admission.APIVersion, Resource: admission.ResourceValidatingBindings}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{bgvr: "ValidatingAdmissionPolicyBindingList"})
	for _, b := range []*unstructured.Unstructured{
		binding("none", nil),
		binding("request-ns", map[string]any{"name": "limits"}),
		binding("found", map[string]any{"name": "limits", "namespace": "team", "parameterNotFoundAction": "Deny"}),
		binding("missing", map[string]any{"name": "gone", "namespace": "team", "parameterNotFoundAction": "Deny"}),
		binding("selected", map[string]any{"namespace": "team", "selector": map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}),
		binding("forbidden", map[string]any{"name": "limits", "namespace": "secret-ns"}),
	} {
		if _, err := dyn.Resource(bgvr).Create(context.Background(), b, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pgvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "limits"}
	param := func(name string, labels map[string]string) *metav1.PartialObjectMetadata {
		return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "example.com/v1", Kind: "Limit"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", Labels: labels}}
	}
	meta := metafake.NewSimpleMetadataClient(scheme, param("limits", nil), param("gold", map[string]string{"tier": "gold"}))
	meta.PrependReactor("get", "limits", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "secret-ns" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "limits"}, "limits", nil)
		}
		return false, nil, nil
	})
	c := &ClusterConn{dyn: dyn, meta: meta, clk: clock.RealClock{}}
	res, err := c.CheckAdmissionParams(context.Background(), admission.Validating, "p", pgvr, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"none": ParamCheckNone, "request-ns": ParamCheckRequestNamespace, "found": ParamCheckFound,
		"missing": ParamCheckNotFound, "selected": ParamCheckFound, "forbidden": ParamCheckUnreadable}
	if len(res.Checks) != len(want) {
		t.Fatalf("%+v", res.Checks)
	}
	for _, chk := range res.Checks {
		if chk.State != want[chk.Binding] {
			t.Errorf("%s: %s, want %s", chk.Binding, chk.State, want[chk.Binding])
		}
	}
	// a cluster-scoped kind: a namespace is a misconfiguration
	res, _ = c.CheckAdmissionParams(context.Background(), admission.Validating, "p", pgvr, false)
	for _, chk := range res.Checks {
		if chk.Binding == "found" && chk.State != ParamCheckMisconfigured {
			t.Fatalf("cluster-scoped with a namespace: %+v", chk)
		}
	}
}
