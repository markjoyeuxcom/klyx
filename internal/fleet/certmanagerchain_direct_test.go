package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// The chain's direct reads (1c slice 3 design §5b): no lazy store, the
// served version, UID identity, and NotFound told apart from unreadable.

func chainConn(t *testing.T, objs ...runtime.Object) (*ClusterConn, *dynamicfake.FakeDynamicClient, *fake.Clientset) {
	t.Helper()
	typed := fake.NewSimpleClientset()
	typed.Resources = certManagerDiscovery()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), chainGVRToListKinds(), objs...)
	return NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{}), dyn, typed
}

func withUID(u *unstructured.Unstructured, uid string) *unstructured.Unstructured {
	u.SetUID(types.UID(uid))
	return u
}

func TestChainCarriesTheUIDSoARecreationIsAnotherObject(t *testing.T) {
	ctx := context.Background()
	c, dyn, _ := chainConn(t, withUID(certUnstructured("web", "payments", "True", "Ready"), "u1"), clusterIssuerUnstructured("lab-acme-staging", "True"))
	first, err := c.GetCertificateChain(ctx, "payments", "web")
	if err != nil || first.Certificate.UID != "u1" || first.AsOf.IsZero() {
		t.Fatalf("first read: %+v %v", first.Certificate, err)
	}
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	if err := dyn.Resource(gvr).Namespace("payments").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(gvr).Namespace("payments").Create(ctx, withUID(certUnstructured("web", "payments", "False", "Pending"), "u2"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := c.GetCertificateChain(ctx, "payments", "web")
	if err != nil || second.Certificate.UID != "u2" {
		t.Fatalf("a same-name recreation is a different UID: %+v %v", second.Certificate, err)
	}
}

func TestChainNamesWhyItsIssuerIsMissing(t *testing.T) {
	ctx := context.Background()
	external := certUnstructured("ext", "payments", "False", "Pending")
	_ = unstructured.SetNestedField(external.Object, "AWSPCAIssuer", "spec", "issuerRef", "kind")
	c, _, _ := chainConn(t, certUnstructured("missing", "payments", "False", "Pending"), external)
	ch, err := c.GetCertificateChain(ctx, "payments", "missing")
	if err != nil || ch.Issuer != nil || len(ch.Warnings) == 0 || !strings.Contains(ch.Warnings[0], "was not found") {
		t.Fatalf("a missing issuer says not found: %+v %v", ch.Warnings, err)
	}
	ch, _ = c.GetCertificateChain(ctx, "payments", "ext")
	if len(ch.Warnings) == 0 || !strings.Contains(ch.Warnings[0], "external issuer kind") {
		t.Fatalf("an external issuer kind says so: %+v", ch.Warnings)
	}

	c2, dyn2, _ := chainConn(t, certUnstructured("web", "payments", "True", "Ready"), clusterIssuerUnstructured("lab-acme-staging", "True"))
	dyn2.PrependReactor("get", "clusterissuers", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "cert-manager.io", Resource: "clusterissuers"}, "lab-acme-staging", errors.New("rbac"))
	})
	ch, _ = c2.GetCertificateChain(ctx, "payments", "web")
	if ch.Issuer != nil || len(ch.Warnings) == 0 || !strings.Contains(ch.Warnings[0], "unverified") || strings.Contains(ch.Warnings[0], "not found") {
		t.Fatalf("an unreadable issuer is unverified, never not found: %+v", ch.Warnings)
	}
}

func TestChainCertificateNotFoundIsNotForbidden(t *testing.T) {
	ctx := context.Background()
	c, dyn, _ := chainConn(t)
	if _, err := c.GetCertificateChain(ctx, "payments", "gone"); !apierrors.IsNotFound(err) {
		t.Fatalf("a missing certificate is NotFound: %v", err)
	}
	dyn.PrependReactor("get", "certificates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "web", errors.New("rbac"))
	})
	_, err := c.GetCertificateChain(ctx, "payments", "web")
	if err == nil || apierrors.IsNotFound(err) || !apierrors.IsForbidden(err) {
		t.Fatalf("a forbidden read stays forbidden: %v", err)
	}
}

// The served version moved since the cached discovery: a NotFound at the
// cached version re-probes once and reads at the new one.
func TestChainReprobesTheVersionOnceOnNotFound(t *testing.T) {
	ctx := context.Background()
	kinds := chainGVRToListKinds()
	kinds[schema.GroupVersionResource{Group: "cert-manager.io", Version: "v2", Resource: "certificates"}] = "CertificateList"
	kinds[schema.GroupVersionResource{Group: "cert-manager.io", Version: "v2", Resource: "certificaterequests"}] = "CertificateRequestList"
	typed := fake.NewSimpleClientset()
	typed.Resources = certManagerDiscovery() // v1, cached by the first probe
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds)
	v2 := certUnstructured("web", "payments", "True", "Ready")
	v2.SetAPIVersion("cert-manager.io/v2")
	if _, err := dyn.Resource(schema.GroupVersionResource{Group: "cert-manager.io", Version: "v2", Resource: "certificates"}).Namespace("payments").Create(ctx, v2, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	if _, err := c.crdServed(ctx, "cert-manager.io", "certificates", "v1"); err != nil { // warm the cache at v1
		t.Fatal(err)
	}
	typed.Resources = []*metav1.APIResourceList{{GroupVersion: "cert-manager.io/v2", APIResources: []metav1.APIResource{{Name: "certificates", Namespaced: true, Kind: "Certificate"}}}}
	ch, err := c.GetCertificateChain(ctx, "payments", "web")
	if err != nil || ch.Certificate.Name != "web" {
		t.Fatalf("the moved version is found by one re-probe: %+v %v", ch.Certificate, err)
	}
}

// Codex review #4: after a NotFound, a re-probe that cannot be read leaves
// the certificate unverified - its error, never a confirmed NotFound.
func TestChainReprobeFailureIsNotAConfirmedNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "discovery"}, "", errors.New("rbac"))},
		{"transport", errors.New("dial tcp 10.0.0.1:443: connect: connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, typed := chainConn(t) // the certificate is absent at v1
			// discovery answers the first probe, then fails: the failure
			// lands exactly on the re-probe after the NotFound
			c.disc = newCachedDiscovery(&flakyDiscovery{inner: typed.Discovery(), err: tc.err, okCalls: 2}, nil)
			_, err := c.GetCertificateChain(context.Background(), "payments", "gone")
			if err == nil || apierrors.IsNotFound(err) || !strings.Contains(err.Error(), "re-checking the served version") {
				t.Fatalf("a failed re-probe must not confirm NotFound: %v", err)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		c, _, _ := chainConn(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.GetCertificateChain(ctx, "payments", "gone"); err == nil || apierrors.IsNotFound(err) {
			t.Fatalf("a cancelled read is not a confirmed NotFound: %v", err)
		}
	})
}

// flakyDiscovery answers its first okCalls reads, then fails with err.
type flakyDiscovery struct {
	inner   discoveryLike
	err     error
	okCalls int
	calls   int
}

func (f *flakyDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	if f.calls++; f.calls > f.okCalls {
		return nil, f.err
	}
	return f.inner.ServerGroups()
}
func (f *flakyDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if f.calls++; f.calls > f.okCalls {
		return nil, f.err
	}
	return f.inner.ServerResourcesForGroupVersion(gv)
}
