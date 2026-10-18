package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"

	"github.com/moomora/klyx/internal/admission"
)

type paramDisc map[string]any // group/version -> *APIResourceList or error

func (d paramDisc) ServerGroups() (*metav1.APIGroupList, error) { return &metav1.APIGroupList{}, nil }
func (d paramDisc) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	switch v := d[gv].(type) {
	case *metav1.APIResourceList:
		return v, nil
	case error:
		return nil, v
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: gv}, "")
}

// The param-kind fact (S3a §6): only a completed discovery is evidence.
func TestResolveParamKinds(t *testing.T) {
	disc := paramDisc{
		"example.com/v1beta1": &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "limits", Kind: "Limits", Namespaced: true}, {Name: "limits/status", Kind: "Limits"}}},
		"example.com/v1":      &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "quotas", Kind: "Quota"}}},
		"metrics.k8s.io/v1":   errors.New("the server is currently unable to handle the request"),
		"v1":                  &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "configmaps", Kind: "ConfigMap", Namespaced: true}}},
	}
	refs := map[string][3]string{}
	for _, r := range [][3]string{{"example.com", "v1beta1", "Limits"}, {"example.com", "v1", "Limits"}, {"metrics.k8s.io", "v1", "Usage"},
		{"gone.example.com", "v1", "X"}, {"", "v1", "ConfigMap"}} {
		refs[admission.KeyOf(r[0], r[1], r[2])] = r
	}
	at := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	got := resolveParamKindsWith(context.Background(), refs, newCachedDiscovery(disc, clock.RealClock{}), func() time.Time { return at })
	want := map[string]string{
		admission.KeyOf("example.com", "v1beta1", "Limits"): admission.ParamServed,   // a non-preferred served version
		admission.KeyOf("example.com", "v1", "Limits"):      admission.ParamNotServed, // the version is served, the kind is not
		admission.KeyOf("metrics.k8s.io", "v1", "Usage"):    admission.ParamUnknown,   // an unavailable aggregated group
		admission.KeyOf("gone.example.com", "v1", "X"):      admission.ParamNotServed, // NotFound
		admission.KeyOf("", "v1", "ConfigMap"):              admission.ParamServed,
	}
	for k, w := range want {
		if got[k].State != w || !got[k].At.Equal(at) {
			t.Errorf("%s: %+v, want %s", k, got[k], w)
		}
	}
	if !got[admission.KeyOf("example.com", "v1beta1", "Limits")].Namespaced || got[admission.KeyOf("metrics.k8s.io", "v1", "Usage")].Error == "" {
		t.Fatalf("scope and error: %+v", got)
	}
}
