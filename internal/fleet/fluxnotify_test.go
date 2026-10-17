package fleet

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// Coverage S1c §3: Providers and Alerts are read on demand at v1beta3, a
// kind not served reads absent, another version is named, and only the
// Alerts' event reasons are kept.
func TestFluxNotifications(t *testing.T) {
	const group = "notification.toolkit.fluxcd.io"
	gvr := func(res string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: group, Version: "v1beta3", Resource: res}
	}
	obj := func(kind, name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1beta3", "kind": kind,
			"metadata": map[string]any{"namespace": "flux-system", "name": name}}}
	}
	conn := func(served []*metav1.APIResourceList, events ...runtime.Object) *ClusterConn {
		typed := fake.NewSimpleClientset(events...)
		typed.Resources = served
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{gvr("providers"): "ProviderList", gvr("alerts"): "AlertList"},
			obj("Provider", "slack"), obj("Alert", "on-call"))
		return NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	}
	both := []*metav1.APIResourceList{{GroupVersion: group + "/v1beta3", APIResources: []metav1.APIResource{
		{Name: "providers", Namespaced: true, Kind: "Provider"}, {Name: "alerts", Namespaced: true, Kind: "Alert"}}}}
	ev := func(kind, name, uid, reason string) *corev1.Event {
		return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "flux-system", Name: name + "." + reason + uid},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: "flux-system", Name: name, UID: types.UID(uid)}, Reason: reason, Message: "secret text"}
	}
	r := conn(both, ev("Alert", "on-call", "uid-a", "NotificationDispatchFailed"), ev("Kustomization", "on-call", "uid-k", "BuildFailed"),
		ev("Alert", "on-call", "", "InvalidConfig")).FluxNotifications(context.Background())
	if len(r.Providers.Items) != 1 || len(r.Alerts.Items) != 1 || !r.Providers.Complete() || r.EventsError != "" {
		t.Fatalf("the read: %+v", r)
	}
	// by the involved Alert's UID; an event naming no UID is nobody's
	// (Codex's S1c implementation review, finding 3)
	if got := r.AlertReasons["uid-a"]; len(got) != 1 || got[0] != "NotificationDispatchFailed" || len(r.AlertReasons) != 1 {
		t.Fatalf("only the Alert's reasons, by UID: %v", r.AlertReasons)
	}
	// alerts served at another version; providers not at all
	other := []*metav1.APIResourceList{{GroupVersion: group + "/v1beta2", APIResources: []metav1.APIResource{{Name: "alerts", Namespaced: true, Kind: "Alert"}}},
		{GroupVersion: group + "/v1", APIResources: []metav1.APIResource{{Name: "receivers", Namespaced: true, Kind: "Receiver"}}}}
	r = conn(other).FluxNotifications(context.Background())
	if !r.Providers.Absent || !strings.Contains(r.Alerts.Error, "unsupported version v1beta2 for alerts") || r.Alerts.Complete() {
		t.Fatalf("absent and another version: %+v %+v", r.Providers, r.Alerts)
	}
}
