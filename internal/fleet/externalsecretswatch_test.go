package fleet

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/externalsecrets"
)

func externalSecretUnstructured(name, ns, ready, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1beta1",
		"kind":       "ExternalSecret",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"refreshInterval": "1h",
			"secretStoreRef":  map[string]interface{}{"name": "azure-kv", "kind": "ClusterSecretStore"},
			"target":          map[string]interface{}{"name": name + "-secret"},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready, "reason": reason},
		}},
	}}
}

func clusterSecretStoreUnstructured(name, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1beta1",
		"kind":       "ClusterSecretStore",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"provider": map[string]interface{}{"azurekv": map[string]interface{}{}},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready},
		}},
	}}
}

func secretStoreUnstructured(name, ns, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1beta1",
		"kind":       "SecretStore",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"provider": map[string]interface{}{"vault": map[string]interface{}{}},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready},
		}},
	}}
}

func externalSecretsGVRToListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets"}:     "ExternalSecretList",
		{Group: "external-secrets.io", Version: "v1beta1", Resource: "secretstores"}:        "SecretStoreList",
		{Group: "external-secrets.io", Version: "v1beta1", Resource: "clustersecretstores"}: "ClusterSecretStoreList",
	}
}

func TestOpenExternalSecretsListsSecretsSorted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	// Seed out of order; the list must come back namespace/name sorted.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, externalSecretsGVRToListKinds(),
		externalSecretUnstructured("z-secret", "payments", "True", "SecretSynced"),
		externalSecretUnstructured("a-secret", "payments", "False", "SecretSyncedError"),
		clusterSecretStoreUnstructured("azure-kv", "False"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenExternalSecrets()
	defer c.CloseExternalSecrets()

	waitFor(t, 2*time.Second, func() bool { return len(c.ExternalSecretsList()) == 2 })

	secrets := c.ExternalSecretsList()
	if secrets[0].Name != "a-secret" || secrets[1].Name != "z-secret" {
		t.Fatalf("want a-secret,z-secret order, got %s,%s", secrets[0].Name, secrets[1].Name)
	}
	if secrets[0].State != externalsecrets.Failed || secrets[0].Reason != "SecretSyncedError" {
		t.Fatalf("failing secret not parsed: %+v", secrets[0])
	}
	if secrets[0].StoreKind != externalsecrets.ClusterSecretStoreKind || secrets[0].StoreName != "azure-kv" {
		t.Fatalf("store ref not parsed: %+v", secrets[0])
	}
	if secrets[0].TargetSecret != "a-secret-secret" {
		t.Fatalf("target secret not parsed: %+v", secrets[0])
	}
}

func TestExternalSecretsStoresSorted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, externalSecretsGVRToListKinds(),
		externalSecretUnstructured("app-secret", "payments", "False", "SecretSyncedError"),
		secretStoreUnstructured("local-vault", "payments", "True"),
		clusterSecretStoreUnstructured("azure-kv", "False"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenExternalSecrets()
	defer c.CloseExternalSecrets()

	waitFor(t, 2*time.Second, func() bool { return len(c.ExternalSecretsStores()) == 2 })

	stores := c.ExternalSecretsStores()
	// Sorted kind then namespace/name: ClusterSecretStore < SecretStore.
	if stores[0].Kind != externalsecrets.ClusterSecretStoreKind || stores[0].Namespace != "" || stores[0].State != externalsecrets.Failed {
		t.Fatalf("clustersecretstore: %+v", stores[0])
	}
	if stores[0].Provider != "azurekv" {
		t.Fatalf("provider not parsed: %+v", stores[0])
	}
	if stores[1].Kind != externalsecrets.SecretStoreKind || stores[1].Namespace != "payments" || stores[1].State != externalsecrets.Synced {
		t.Fatalf("secretstore: %+v", stores[1])
	}
}

// TestOpenCloseExternalSecretsInterleaved storms Open/Close from concurrent
// goroutines. Guards the opening/wanted flags: overlapping Opens must not
// double-install, a Close landing mid-open must win (final Close leaves the
// watch closed), and the whole dance must be race-clean under -race. Mirrors
// TestOpenCloseGitOpsInterleaved.
func TestOpenCloseExternalSecretsInterleaved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, externalSecretsGVRToListKinds(),
		externalSecretUnstructured("app-secret", "payments", "True", "SecretSynced"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx

	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				c.OpenExternalSecrets()
				c.CloseExternalSecrets()
			}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}

	// The storm ends with Closes: the watch must be closed and re-openable.
	c.CloseExternalSecrets()
	if es := c.ExternalSecretsList(); es != nil {
		t.Fatalf("watch still installed after final Close: %d secrets", len(es))
	}
	c.OpenExternalSecrets()
	defer c.CloseExternalSecrets()
	waitFor(t, 2*time.Second, func() bool { return len(c.ExternalSecretsList()) == 1 })
}

// Coverage is per kind: ExternalSecrets syncing must not vouch for a
// forbidden ClusterSecretStore watch, and its recovery clears the error.
func TestExternalSecretsCoverageIsPerKindAndRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), externalSecretsGVRToListKinds(),
		externalSecretUnstructured("stripe", "payments", "True", "SecretSynced"),
		clusterSecretStoreUnstructured("azure-kv", "True"))
	var forbid atomic.Bool
	forbid.Store(true)
	dyn.PrependReactor("list", "clustersecretstores", func(k8stesting.Action) (bool, runtime.Object, error) {
		if forbid.Load() {
			return true, nil, errors.New("clustersecretstores.external-secrets.io is forbidden")
		}
		return false, nil, nil
	})

	c := NewClusterConn("x", typed, nil, dyn, capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenExternalSecrets()
	defer c.CloseExternalSecrets()

	cov := func(kind string) KindCoverage {
		for _, k := range c.ExternalSecretsCoverage() {
			if k.Kind == kind {
				return k
			}
		}
		return KindCoverage{}
	}
	waitFor(t, 3*time.Second, func() bool { return cov("ExternalSecret").Synced && cov("ClusterSecretStore").Error != "" })
	if cs := cov("ClusterSecretStore"); cs.Synced || !cs.Served {
		t.Fatalf("forbidden ClusterSecretStore must be served but unsynced: %+v", cs)
	}
	if synced, err := c.ExternalSecretsWatchHealth(); synced || err == "" {
		t.Fatalf("aggregate health: %v %q", synced, err)
	}
	forbid.Store(false)
	waitFor(t, 5*time.Second, func() bool { cs := cov("ClusterSecretStore"); return cs.Synced && cs.Error == "" })
	if synced, err := c.ExternalSecretsWatchHealth(); !synced || err != "" {
		t.Fatalf("recovered: %v %q", synced, err)
	}
}
