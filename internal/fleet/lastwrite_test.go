package fleet

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// The Line slice 7a: the last recorded managed-field update, one metadata
// GET per reference, UID-pinned; a failed read is never "no record".
func TestObjectWritesReadsManagedFields(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			{Name: "namespaces", Kind: "Namespace"},
			{Name: "secrets", Kind: "Secret", Namespaced: true},
			{Name: "pods/status", Kind: "Pod", Namespaced: true},
		}},
	}
	when := metav1.NewTime(time.Date(2026, 10, 15, 20, 31, 0, 0, time.UTC))
	cm := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "shop", UID: "u1", ManagedFields: []metav1.ManagedFieldsEntry{
			{Manager: "kustomize-controller", Operation: metav1.ManagedFieldsOperationApply, Time: &when},
		}}}
	ns := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "n1"}}
	sec := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "shop", UID: "s1"}}
	scheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(scheme)
	mclient := metadatafake.NewSimpleMetadataClient(scheme, cm, ns, sec)
	mclient.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "token", nil)
	})
	c := NewClusterConn("x", typed, mclient, nil, capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{})

	refs := []ObjectRef{
		{Version: "v1", Kind: "ConfigMap", Namespace: "shop", Name: "settings", UID: "u1"},
		{Kind: "Namespace", Name: "shop", UID: "n1"},
		{Version: "v1", Kind: "ConfigMap", Namespace: "shop", Name: "settings", UID: "older-uid"},
		{Version: "v1", Kind: "ConfigMap", Namespace: "shop", Name: "gone"},
		{Version: "v1", Kind: "Secret", Namespace: "shop", Name: "token", UID: "s1"},
		{Group: "example.test", Version: "v1", Kind: "Widget", Namespace: "shop", Name: "w"},
		{Version: "v1", Kind: "Pod", Namespace: "shop", Name: "p"}, // only a subresource is served
	}
	got, err := c.ObjectWrites(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{WritesOK, WritesNone, WritesReplaced, WritesGone, WritesError, WritesError, WritesError}
	for i, w := range want {
		if got[i].State != w {
			t.Errorf("%+v: %s (%s), want %s", refs[i], got[i].State, got[i].Error, w)
		}
	}
	if l := got[0].Writes.LastSpec; l == nil || l.Manager != "kustomize-controller" || !l.Time.Equal(when.Time) {
		t.Fatalf("the ConfigMap's record %+v", got[0].Writes)
	}
	if _, err := c.ObjectWrites(context.Background(), make([]ObjectRef, MaxWriteRefs+1)); err == nil {
		t.Fatal("more than the bound was read")
	}
}

// TestLabObjectWrites reads managed fields on the kind lab, read-only.
//
//	KLYX_LAB_CONTEXT=kind-klyx go test ./internal/fleet -run TestLabObjectWrites -v -count=1
func TestLabObjectWrites(t *testing.T) {
	c := labConn(t)
	got, err := c.ObjectWrites(context.Background(), []ObjectRef{
		{Version: "v1", Kind: "ConfigMap", Namespace: "default", Name: "kube-root-ca.crt"},
		{Kind: "Namespace", Name: "default"},
		{Version: "v1", Kind: "ConfigMap", Namespace: "default", Name: "klyx-no-such-object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != WritesOK || got[0].Writes.LastSpec == nil || got[0].Writes.LastSpec.Manager == "" {
		t.Errorf("kube-root-ca.crt: %+v", got[0])
	}
	if got[1].State != WritesOK && got[1].State != WritesNone {
		t.Errorf("the default namespace: %+v", got[1])
	}
	if got[2].State != WritesGone {
		t.Errorf("a missing object: %+v", got[2])
	}
	t.Logf("kube-root-ca.crt last spec write: %+v", got[0].Writes.LastSpec)
}

// labConn is the production connection to a kind lab context, or a skip.
func labConn(t *testing.T) Conn {
	t.Helper()
	ctxName := os.Getenv("KLYX_LAB_CONTEXT")
	if ctxName == "" {
		t.Skip("set KLYX_LAB_CONTEXT=kind-klyx")
	}
	if !strings.HasPrefix(ctxName, "kind-") {
		t.Fatalf("refusing %q: the lab runs only against kind-* contexts", ctxName)
	}
	conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: ctxName, Context: ctxName})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
