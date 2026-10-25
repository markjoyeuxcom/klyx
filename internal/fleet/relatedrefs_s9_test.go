package fleet

import (
	"context"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	discoveryfake "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func s9Conn(t *testing.T, objs ...any) *ClusterConn {
	t.Helper()
	typed := k8sfake.NewSimpleClientset()
	for _, o := range objs {
		if h, ok := o.(*autoscalingv2.HorizontalPodAutoscaler); ok {
			if _, err := typed.AutoscalingV2().HorizontalPodAutoscalers(h.Namespace).Create(context.Background(), h, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true}, {Name: "deployments/scale", Kind: "Scale", Namespaced: true}}},
		{GroupVersion: "argoproj.io/v1alpha1", APIResources: []metav1.APIResource{{Name: "rollouts", Kind: "Rollout", Namespaced: true}}},
		{GroupVersion: "keda.sh/v1alpha1", APIResources: []metav1.APIResource{
			{Name: "scaledobjects", Kind: "ScaledObject", Namespaced: true},
			{Name: "scaledjobs", Kind: "ScaledJob", Namespaced: true},
			{Name: "triggerauthentications", Kind: "TriggerAuthentication", Namespaced: true},
			{Name: "clustertriggerauthentications", Kind: "ClusterTriggerAuthentication", Namespaced: false},
		}},
	}
	return NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
}

// Coverage S9 §7: owner and target links resolve through discovery, with
// their own apiVersion; an unresolvable kind stays plain text.
func TestRefForResolvesThroughDiscovery(t *testing.T) {
	c := s9Conn(t)
	for _, tc := range []struct {
		name, api, kind string
		ok              bool
		group, plural   string
		ns              string
	}{
		{"a core kind", "apps/v1", "Deployment", true, "apps", "deployments", "team"},
		{"a CRD owner", "keda.sh/v1alpha1", "ScaledObject", true, "keda.sh", "scaledobjects", "team"},
		{"a scalable CRD target", "argoproj.io/v1alpha1", "Rollout", true, "argoproj.io", "rollouts", "team"},
		{"a cluster-scoped kind links without a namespace", "keda.sh/v1alpha1", "ClusterTriggerAuthentication", true, "keda.sh", "clustertriggerauthentications", ""},
		{"an unserved kind", "example.com/v1", "Widget", false, "", "", ""},
		{"a same-named kind in another group is never apps'", "example.com/v1", "Deployment", false, "", "", ""},
		{"a malformed apiVersion", "a/b/c", "Deployment", false, "", "", ""},
	} {
		ref, ok, err := c.refFor(context.Background(), tc.api, tc.kind, "team", "web", "owner")
		if err != nil || ok != tc.ok || ref.Group != tc.group || ref.Plural != tc.plural || ref.Namespace != tc.ns {
			t.Errorf("%s: %v %+v", tc.name, ok, ref)
		}
	}
	// discovery unavailable: only a core kind in its own group falls back
	if ref, ok := coreKindRef("apps/v1", "Deployment", "team", "web", "owner"); !ok || ref.Plural != "deployments" {
		t.Fatalf("core fallback: %v %+v", ok, ref)
	}
	if _, ok := coreKindRef("example.com/v1", "Deployment", "team", "web", "owner"); ok {
		t.Fatal("the fallback ignored the group")
	}
}

// An HPA's owner ScaledObject, a ScaledObject's target, its HPA (by
// controller UID, never the name) and its authentications.
func TestRelatedRefsKEDA(t *testing.T) {
	yes := true
	hpaOwned := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "custom-name",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "web", UID: "so-1", Controller: &yes}}}}
	hpaOther := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "keda-hpa-web"}} // the prefix proves nothing
	c := s9Conn(t, hpaOwned, hpaOther)

	so := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
		"metadata": map[string]any{"namespace": "team", "name": "web", "uid": "so-1"},
		"spec": map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "triggers": []any{
			map[string]any{"type": "prometheus", "authenticationRef": map[string]any{"name": "ta"}},
			map[string]any{"type": "prometheus", "authenticationRef": map[string]any{"name": "cta", "kind": "ClusterTriggerAuthentication"}},
		}}}}
	got := map[string]string{}
	refs, err := c.relatedRefs(context.Background(), "keda.sh", "v1alpha1", "scaledobjects", "team", "web", so)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		got[r.Relation+" "+r.Kind+" "+r.Name] = r.Namespace
	}
	for k, ns := range map[string]string{"scale target Deployment web": "team", "HPA HorizontalPodAutoscaler custom-name": "team",
		"authentication TriggerAuthentication ta": "team", "authentication ClusterTriggerAuthentication cta": ""} {
		if v, ok := got[k]; !ok || v != ns {
			t.Errorf("missing %q (namespace %q): %v", k, ns, got)
		}
	}
	if _, ok := got["HPA HorizontalPodAutoscaler keda-hpa-web"]; ok {
		t.Fatal("an HPA joined by its name")
	}

	hpa := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler",
		"metadata": map[string]any{"namespace": "team", "name": "custom-name", "ownerReferences": []any{
			map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject", "name": "web", "uid": "so-1", "controller": true}}},
		"spec": map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "name": "web"}}}}
	got = map[string]string{}
	refs, err = c.relatedRefs(context.Background(), "autoscaling", "v2", "horizontalpodautoscalers", "team", "custom-name", hpa)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		got[r.Relation+" "+r.Group+" "+r.Kind+" "+r.Name] = r.Plural
	}
	if got["owner keda.sh ScaledObject web"] != "scaledobjects" || got["scale target argoproj.io Rollout web"] != "rollouts" {
		t.Fatalf("HPA links: %v", got)
	}
}
