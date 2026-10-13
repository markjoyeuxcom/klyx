package fleet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// discoveryServer answers /api, /apis and /api/v1 the way an API server does.
func discoveryReply(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/apis":
		_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`))
	case "/api":
		_, _ = w.Write([]byte(`{"kind":"APIVersions","apiVersion":"v1","versions":["v1"],"serverAddressByClientCIDRs":[]}`))
	case "/api/v1":
		_, _ = w.Write([]byte(`{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"pods","kind":"Pod","namespaced":true,"verbs":["get","list","create","delete"]}]}`))
	default:
		http.NotFound(w, r)
	}
}

// Codex review 2026-10-13 (slice A): the context-aware discovery the health
// probes warm shares one cache with the sweep. It must carry the core group
// and read core resources at /api/v1, or a warmed cache silently drops Pods.
func TestAdhocInventoryAfterProbeWarmedDiscoveryListsCore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(discoveryReply))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c, dyn := adhocConn(t)
	c.typed = cs
	pod := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]interface{}{
		"name": "dbg", "namespace": "scratch", "uid": "pod-uid", "labels": map[string]interface{}{AdhocLabel: "true"},
	}}}
	if _, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("scratch").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Warm the shared cache through the probes' context-aware path first.
	if _, err := c.probeDiscovery().ServerGroupsCtx(context.Background()); err != nil {
		t.Fatal(err)
	}
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 1 || inv.Objects[0].Name != "dbg" {
		t.Fatalf("a probe-warmed cache must still list core scratch objects: %+v %v", inv, err)
	}
}

// The sweep's discovery honours the caller's cancellation: a blocked
// discovery request ends when the caller goes away.
func TestAdhocInventoryDiscoveryHonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			discoveryReply(w, r)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := adhocConn(t)
	c.typed = cs
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ListAdhocObjects(ctx)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled sweep must report that the inventory was not read")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep's discovery is still blocked after the caller cancelled")
	}
}

// recordingDyn records the ListOptions each List call sends, which the fake's
// reactors cannot see (client-go hands them a copy without ListOptions).
type recordingDyn struct {
	dynamic.Interface
	mu   *sync.Mutex
	sent map[string][]metav1.ListOptions
}

func (r recordingDyn) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return recordingRes{r.Interface.Resource(gvr), r, gvr.Resource}
}

type recordingRes struct {
	dynamic.NamespaceableResourceInterface
	rec      recordingDyn
	resource string
}

func (r recordingRes) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	r.rec.mu.Lock()
	r.rec.sent[r.resource] = append(r.rec.sent[r.resource], opts)
	r.rec.mu.Unlock()
	return r.NamespaceableResourceInterface.List(ctx, opts)
}

// The sweep sends the continue token it was given, with the same selector.
func TestAdhocInventorySendsTheContinueToken(t *testing.T) {
	c, dyn := adhocConn(t)
	calls := 0
	dyn.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		l := &unstructured.UnstructuredList{}
		u := unstructured.Unstructured{}
		u.SetName("cm-" + string(rune('a'+calls)))
		u.SetNamespace(ScratchNamespace)
		u.SetUID(types.UID("uid-" + string(rune('a'+calls))))
		u.SetLabels(map[string]string{AdhocLabel: "true"})
		l.Items = append(l.Items, u)
		if calls == 1 {
			l.SetContinue("page-2-token")
		}
		return true, l, nil
	})
	rec := recordingDyn{Interface: dyn, mu: &sync.Mutex{}, sent: map[string][]metav1.ListOptions{}}
	c.dyn = rec
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 2 {
		t.Fatalf("want both pages: %+v %v", inv, err)
	}
	sent := rec.sent["configmaps"]
	if len(sent) != 2 || sent[0].Continue != "" || sent[1].Continue != "page-2-token" || sent[1].LabelSelector != AdhocLabel+"=true" || sent[1].Limit != adhocListPage {
		t.Fatalf("the second page must carry the token and the selector: %+v", sent)
	}
}

func statusReply(w http.ResponseWriter, code int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":%q,"message":"discovery denied or unavailable","code":%d}`, reason, code)
}

// Codex review 2026-10-13 (slice A, b3c2c84): a discovery root that fails is
// an error, never a successful list with groups missing - for the scratch
// sweep and for the health probes' capability check alike. Only client-go's
// own toleration holds: a 404 on the legacy /api root.
func TestDiscoveryRootFailureIsNotAbsence(t *testing.T) {
	for _, tc := range []struct {
		path   string
		code   int
		reason string
	}{{"/api", 403, "Forbidden"}, {"/apis", 403, "Forbidden"}, {"/apis", 404, "NotFound"}, {"/apis", 500, "InternalError"}} {
		t.Run(fmt.Sprintf("%s %d", tc.path, tc.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					statusReply(w, tc.code, tc.reason)
					return
				}
				discoveryReply(w, r)
			}))
			defer srv.Close()
			cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := adhocConn(t)
			c.typed = cs
			if inv, err := c.ListAdhocObjects(context.Background()); err == nil {
				t.Fatalf("a failed discovery root must fail the sweep, not read as complete: %+v", inv)
			}
			if p, err := c.crdServed(context.Background(), "cert-manager.io", "certificates", "v1"); err == nil {
				t.Fatalf("a failed discovery root must not confirm a capability answer: %+v", p)
			}
		})
	}
}

// The legacy /api root may 404 (aggregated API servers do): the named groups
// still count, as client-go has it.
func TestDiscoveryToleratesALegacyRoot404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			statusReply(w, 404, "NotFound")
		case "/apis":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"rbac.authorization.k8s.io","versions":[{"groupVersion":"rbac.authorization.k8s.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"rbac.authorization.k8s.io/v1","version":"v1"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := adhocConn(t)
	c.typed = cs
	g, err := c.probeDiscovery().ServerGroupsCtx(context.Background())
	if err != nil || len(g.Groups) != 1 || g.Groups[0].Name != "rbac.authorization.k8s.io" {
		t.Fatalf("a legacy-root 404 keeps the named groups: %+v %v", g, err)
	}
}

// A failed discovery is never cached: once the server answers again, the
// next sweep reads the real groups.
func TestDiscoveryFailureIsNotCachedAsAbsence(t *testing.T) {
	var denied atomic.Bool
	denied.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if denied.Load() && r.URL.Path == "/apis" {
			statusReply(w, 403, "Forbidden")
			return
		}
		discoveryReply(w, r)
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c, dyn := adhocConn(t)
	c.typed = cs
	pod := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]interface{}{
		"name": "dbg", "namespace": "scratch", "uid": "pod-uid", "labels": map[string]interface{}{AdhocLabel: "true"},
	}}}
	if _, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("scratch").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListAdhocObjects(context.Background()); err == nil {
		t.Fatal("a denied /apis must fail the sweep")
	}
	denied.Store(false)
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 1 {
		t.Fatalf("after recovery the sweep reads the real groups: %+v %v", inv, err)
	}
}
