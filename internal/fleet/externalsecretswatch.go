package fleet

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/externalsecrets"
)

type esoWatch struct {
	cancel   context.CancelFunc
	esInf    cache.SharedIndexInformer
	storeInf map[externalsecrets.Kind]cache.SharedIndexInformer // SecretStore + ClusterSecretStore
	errs     *watchErrTracker
}

// esoStoreKinds is the set of ESO store CRDs Klyx watches alongside
// ExternalSecrets, so a stuck secret's root cause (a not-ready store) is
// visible without a separate API read.
var esoStoreKinds = []struct {
	kind     externalsecrets.Kind
	resource string
}{
	{externalsecrets.SecretStoreKind, "secretstores"},
	{externalsecrets.ClusterSecretStoreKind, "clustersecretstores"},
}

// OpenExternalSecrets starts (idempotently) the lazy dynamic informers on the
// ESO CRDs (ExternalSecret + SecretStore + ClusterSecretStore). Mirrors
// OpenGitOps, including the opening/wanted flags that keep a Close landing
// mid-open from leaking the freshly started factory.
func (c *ClusterConn) OpenExternalSecrets() {
	c.mu.Lock()
	c.esoWanted = true
	if c.eso != nil || c.esoOpening {
		c.mu.Unlock()
		return
	}
	c.esoOpening = true
	dyn := c.dyn
	parent := c.ctx
	c.mu.Unlock()
	if dyn == nil || parent == nil {
		c.mu.Lock()
		c.esoOpening = false
		c.mu.Unlock()
		return
	}

	ver := preferredVersion(c.discovery(), "external-secrets.io", "v1beta1")
	esGVR := schema.GroupVersionResource{Group: "external-secrets.io", Version: ver, Resource: "externalsecrets"}

	gctx, cancel := context.WithCancel(parent)
	factory := dynamicinformer.NewDynamicSharedInformerFactory(dyn, defaultResync)
	errs := &watchErrTracker{}
	esInf := factory.ForResource(esGVR).Informer()
	trackWatchErrors(esInf, errs)

	storeInf := make(map[externalsecrets.Kind]cache.SharedIndexInformer, len(esoStoreKinds))
	for _, k := range esoStoreKinds {
		gvr := schema.GroupVersionResource{Group: "external-secrets.io", Version: ver, Resource: k.resource}
		inf := factory.ForResource(gvr).Informer()
		trackWatchErrors(inf, errs)
		storeInf[k.kind] = inf
	}

	factory.Start(gctx.Done())

	c.mu.Lock()
	c.esoOpening = false
	if !c.esoWanted {
		// Closed while we were starting: don't install; stop the factory.
		c.mu.Unlock()
		cancel()
		return
	}
	c.eso = &esoWatch{cancel: cancel, esInf: esInf, storeInf: storeInf, errs: errs}
	c.mu.Unlock()
}

// ExternalSecretsWatchHealth reports whether the ExternalSecret informer has
// synced and the most recent watch error - the "permanently failing" signal
// that must render as an error, never as an empty secret list. Mirrors
// CertManagerWatchHealth.
func (c *ClusterConn) ExternalSecretsWatchHealth() (synced bool, lastErr string) {
	c.mu.RLock()
	g := c.eso
	c.mu.RUnlock()
	if g == nil {
		return false, ""
	}
	return g.esInf.HasSynced(), g.errs.lastError()
}

// CloseExternalSecrets stops the ESO informers. Clearing esoWanted also aborts
// an in-flight OpenExternalSecrets install (see OpenGitOps).
func (c *ClusterConn) CloseExternalSecrets() {
	c.mu.Lock()
	c.esoWanted = false
	g := c.eso
	c.eso = nil
	c.mu.Unlock()
	if g != nil {
		g.cancel()
	}
}

// ExternalSecretsList reads the ExternalSecret informer store and parses it.
// nil when not open. Sorted (namespace/name) so the UI list does not reshuffle.
func (c *ClusterConn) ExternalSecretsList() []externalsecrets.ExternalSecret {
	c.mu.RLock()
	g := c.eso
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	now := c.clk.Now()
	var out []externalsecrets.ExternalSecret
	for _, obj := range g.esInf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			out = append(out, externalsecrets.ParseExternalSecret(u, now))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// ExternalSecretsStores reads the SecretStore + ClusterSecretStore informer
// stores and parses them. nil when not open. Sorted (kind then namespace/name).
func (c *ClusterConn) ExternalSecretsStores() []externalsecrets.Store {
	c.mu.RLock()
	g := c.eso
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	var out []externalsecrets.Store
	for _, inf := range g.storeInf {
		for _, obj := range inf.GetStore().List() {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, externalsecrets.ParseStore(u))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}
