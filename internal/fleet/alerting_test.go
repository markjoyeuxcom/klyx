package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// amFake is an Alertmanager answering /api/v2/alerts, /alerts/groups and
// /status, recording what it was asked.
type amFake struct {
	mu          sync.Mutex
	alerts      string
	groups      string
	status      string
	code        int
	queries     map[string][]url.Values
	auth        []string
	tenant      []string
	inFlight    int
	maxInFlight int
	hold        chan struct{} // when set, /alerts waits for it
	entered     chan struct{}
}

func newAMFake() *amFake {
	return &amFake{alerts: `[]`, groups: `[]`, status: `{"versionInfo":{"version":"0.34.1"},"cluster":{"status":"disabled","peers":[]}}`, queries: map[string][]url.Values{}}
}

func (f *amFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	path := r.URL.Path
	f.queries[path] = append(f.queries[path], r.URL.Query())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.tenant = append(f.tenant, r.Header.Get("X-Scope-OrgID"))
	hold, entered, code := f.hold, f.entered, f.code
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inFlight--; f.mu.Unlock() }()
	if strings.HasSuffix(path, "/api/v2/alerts") && hold != nil {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		<-hold
	}
	if code != 0 {
		w.WriteHeader(code)
		return
	}
	switch {
	case strings.HasSuffix(path, "/api/v2/alerts/groups"):
		_, _ = io.WriteString(w, f.groups)
	case strings.HasSuffix(path, "/api/v2/alerts"):
		_, _ = io.WriteString(w, f.alerts)
	case strings.HasSuffix(path, "/api/v2/status"):
		_, _ = io.WriteString(w, f.status)
	default:
		http.NotFound(w, r)
	}
}

const amAlerts = `[
 {"fingerprint":"f1","labels":{"alertname":"PodCrash","severity":"critical","cluster":"prd","namespace":"team","pod":"api-1"},
  "annotations":{"summary":"api-1 crash-looping; token C5-AM-TOKEN"},"status":{"state":"active","silencedBy":[],"inhibitedBy":[],"mutedBy":[]},
  "receivers":[{"name":"lab"}],"startsAt":"2026-10-19T10:00:00Z"},
 {"fingerprint":"f2","labels":{"alertname":"Elsewhere","severity":"critical","cluster":"stg"},"status":{"state":"active"},"receivers":[{"name":"lab"}]},
 {"fingerprint":"f3","labels":{"alertname":"Muted","severity":"warning","cluster":"prd"},"status":{"state":"active"},"receivers":[{"name":"quiet"}]}
]`

const amGroups = `[
 {"labels":{},"receiver":{"name":"lab"},"alerts":[{"fingerprint":"f1","status":{"state":"active"}},{"fingerprint":"f2","status":{"state":"active"}}]},
 {"labels":{},"receiver":{"name":"quiet"},"alerts":[{"fingerprint":"f3","status":{"state":"suppressed","mutedBy":["always"]}}]}
]`

type amEnv struct {
	c     *ClusterConn
	reg   *endpointcred.Registry
	store *secretstore.Memory
	mu    sync.Mutex
	keys  []string
}

func (e *amEnv) rekeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.keys...)
}

func newAMEnv(t *testing.T, cfg *config.EndpointConfig, typed *typedfake.Clientset, dyn *dynamicfake.FakeDynamicClient) *amEnv {
	t.Helper()
	if typed == nil {
		typed = typedfake.NewSimpleClientset()
	}
	e := &amEnv{reg: endpointcred.NewRegistry(), store: &secretstore.Memory{}}
	e.c = NewClusterConn("prd", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	if dyn != nil {
		e.c.dyn = dyn
	}
	e.c.WithCredentials(Credentials{Store: e.store, Revisions: e.reg})
	e.c.WithAlertmanager(cfg)
	e.c.setAlertsRekey(func(k string) { e.mu.Lock(); e.keys = append(e.keys, k); e.mu.Unlock() })
	return e
}

// read resolves the source (the first read re-keys) and reads it.
func (e *amEnv) read(t *testing.T) alertmanager.Read {
	t.Helper()
	key := e.c.AlertsIdentity()
	r, err := e.c.ReadAlerts(context.Background(), key)
	if errors.Is(err, ErrSourceChanged) {
		r, err = e.c.ReadAlerts(context.Background(), r.Identity)
	}
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return r
}

func storeAM(t *testing.T, s *secretstore.Memory, origin, secret string) {
	t.Helper()
	v, _ := endpointcred.Record{V: 1, Origin: origin, Type: "bearer", Secret: secret}.Encode()
	if err := s.Set(endpointcred.Account("prd", config.EndpointAlertmanager), v); err != nil {
		t.Fatal(err)
	}
}

// S8a §3: one read is /alerts then /alerts/groups with match as filter;
// match is re-checked; Reduce decides mutes; the credential and tenant are
// sent and the credential never reaches a display value.
func TestReadAlertsDirect(t *testing.T) {
	f := newAMFake()
	f.alerts, f.groups = amAlerts, amGroups
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := &config.EndpointConfig{Endpoint: srv.URL + "/am", Auth: &config.EndpointAuth{Type: "bearer"}, Tenant: "team-a", Match: map[string]string{"cluster": "prd"}}
	e := newAMEnv(t, cfg, nil, nil)
	origin, _ := safehttp.OriginOf(srv.URL)
	storeAM(t, e.store, origin, "C5-AM-TOKEN")
	r := e.read(t)
	if len(r.Alerts) != 2 {
		t.Fatalf("match re-check: %+v", r.Alerts)
	}
	if !strings.HasPrefix(r.Identity, "endpoint|"+origin+"/am|tenant=team-a|match=cluster=\"prd\"") {
		t.Fatalf("identity %q", r.Identity)
	}
	q := f.queries["/am/api/v2/alerts"][0]
	gq := f.queries["/am/api/v2/alerts/groups"][0]
	if q.Get("filter") != `cluster="prd"` || q.Get("unprocessed") != "true" || gq.Get("muted") != "true" || gq.Get("silenced") != "false" {
		t.Fatalf("queries: %v %v", q, gq)
	}
	if f.auth[0] != "Bearer C5-AM-TOKEN" || f.tenant[0] != "team-a" {
		t.Fatalf("sent %q %q", f.auth, f.tenant)
	}
	crash, muted := r.Alerts[0], r.Alerts[1]
	if !crash.Counted || crash.Subject.Kind != "Pod" || crash.Subject.Name != "api-1" || strings.Contains(crash.Display.Summary, "C5-AM-TOKEN") {
		t.Fatalf("crash: %+v", crash)
	}
	if muted.Counted || muted.Display.State != "suppressed" || len(muted.Display.MutedBy) != 1 {
		t.Fatalf("muted: %+v", muted.Display)
	}
}

// A non-2xx answer, a 410 and a malformed body are failed reads; the groups
// cap fails the read; past the entry cap the read is truncated.
func TestReadAlertsBounds(t *testing.T) {
	f := newAMFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	e := newAMEnv(t, &config.EndpointConfig{Endpoint: srv.URL}, nil, nil)
	key := e.c.AlertsIdentity()
	if _, err := e.c.ReadAlerts(context.Background(), key); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("first read keys the source: %v", err)
	}
	key = e.c.AlertsIdentity()
	for code, want := range map[int]string{http.StatusServiceUnavailable: "HTTP 503", http.StatusGone: "v1 API is gone"} {
		f.mu.Lock()
		f.code = code
		f.mu.Unlock()
		if _, err := e.c.ReadAlerts(context.Background(), key); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%d: %v", code, err)
		}
	}
	f.mu.Lock()
	f.code, f.alerts = 0, `{"not":"a list"}`
	f.mu.Unlock()
	if _, err := e.c.ReadAlerts(context.Background(), key); err == nil {
		t.Fatal("a malformed body read")
	}
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < amMaxAlerts+3; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"fingerprint":"f%d","labels":{"alertname":"A"},"status":{"state":"active"},"receivers":[{"name":"r"}]}`, i)
	}
	b.WriteString("]")
	f.mu.Lock()
	f.alerts, f.groups = b.String(), `[]`
	f.mu.Unlock()
	r, err := e.c.ReadAlerts(context.Background(), key)
	if err != nil || !r.Truncated || len(r.Alerts) != amMaxAlerts {
		t.Fatalf("entry cap: %d %v %v", len(r.Alerts), r.Truncated, err)
	}
	var g strings.Builder
	g.WriteString(`[{"labels":{},"receiver":{"name":"r"},"alerts":[`)
	for i := 0; i < amMaxAppearances+1; i++ {
		if i > 0 {
			g.WriteString(",")
		}
		fmt.Fprintf(&g, `{"fingerprint":"g%d","status":{"state":"active"}}`, i)
	}
	g.WriteString("]}]")
	f.mu.Lock()
	f.alerts, f.groups = `[]`, g.String()
	f.mu.Unlock()
	if _, err := e.c.ReadAlerts(context.Background(), key); err == nil || !strings.Contains(err.Error(), "appearances") {
		t.Fatalf("groups cap: %v", err)
	}
}

// A credential moved during the read drops it (C5 §7).
func TestReadAlertsCredentialFence(t *testing.T) {
	f := newAMFake()
	f.hold, f.entered = make(chan struct{}), make(chan struct{}, 1)
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := &config.EndpointConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}}
	e := newAMEnv(t, cfg, nil, nil)
	origin, _ := safehttp.OriginOf(srv.URL)
	storeAM(t, e.store, origin, "C5-AM-TOKEN")
	_, _ = e.c.ReadAlerts(context.Background(), "")
	key := e.c.AlertsIdentity()
	done := make(chan error, 1)
	go func() { _, err := e.c.ReadAlerts(context.Background(), key); done <- err }()
	<-f.entered
	e.reg.Revoke("prd", config.EndpointAlertmanager)
	close(f.hold)
	if err := <-done; err == nil {
		t.Fatal("a read across a credential move was kept")
	}
}

// S8a §4: an identity change re-keys; a credential-only change does not; a
// configuration that depends on discovery re-keys to a pending collection;
// a read keyed to another identity is refused.
func TestAlertsIdentityAndRekey(t *testing.T) {
	cfg := &config.EndpointConfig{Endpoint: "https://am.example/one", Match: map[string]string{"cluster": "a"}}
	e := newAMEnv(t, cfg, nil, nil)
	e.c.SetAlertmanagerConfig(cfg)
	first := e.c.AlertsIdentity()
	if !strings.HasPrefix(first, "endpoint|https://am.example:443/one|") {
		t.Fatalf("identity %q", first)
	}
	for name, next := range map[string]*config.EndpointConfig{
		"path":   {Endpoint: "https://am.example/two", Match: map[string]string{"cluster": "a"}},
		"match":  {Endpoint: "https://am.example/one", Match: map[string]string{"cluster": "b"}},
		"tenant": {Endpoint: "https://am.example/one", Match: map[string]string{"cluster": "a"}, Tenant: "t"},
	} {
		e.c.SetAlertmanagerConfig(cfg)
		before := len(e.rekeys())
		e.c.SetAlertmanagerConfig(next)
		if got := e.rekeys(); len(got) != before+1 || got[len(got)-1] == first {
			t.Errorf("%s: no re-key: %q", name, got)
		}
	}
	e.c.SetAlertmanagerConfig(cfg)
	before := len(e.rekeys())
	if err := e.c.ChangeAlertmanagerCredential(cfg, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(e.rekeys()) != before {
		t.Fatal("a credential-only change re-keyed")
	}
	e.c.SetAlertmanagerConfig(&config.EndpointConfig{Tenant: "t"})
	if got := e.rekeys(); got[len(got)-1] != amPendingIdentity {
		t.Fatalf("discovery-dependent: %q", got)
	}
	e2 := newAMEnv(t, &config.EndpointConfig{Endpoint: "https://am.example"}, nil, nil)
	if r, err := e2.c.ReadAlerts(context.Background(), "some-other-identity"); !errors.Is(err, ErrSourceChanged) || r.Identity == "" {
		t.Fatalf("a mismatched key: %v", err)
	}
}

// One read per cluster at a time, across re-keys; at most four fleet-wide.
func TestReadAlertsSingleFlightAndFleetLimit(t *testing.T) {
	f := newAMFake()
	f.hold = make(chan struct{})
	srv := httptest.NewServer(f)
	defer srv.Close()
	var envs []*amEnv
	for i := 0; i < 6; i++ {
		e := newAMEnv(t, &config.EndpointConfig{Endpoint: srv.URL}, nil, nil)
		_, _ = e.c.ReadAlerts(context.Background(), "")
		envs = append(envs, e)
	}
	var wg sync.WaitGroup
	for _, e := range envs {
		for j := 0; j < 2; j++ { // two reads per cluster
			wg.Add(1)
			go func(e *amEnv) {
				defer wg.Done()
				_, _ = e.c.ReadAlerts(context.Background(), e.c.AlertsIdentity())
			}(e)
		}
	}
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	inFlight := f.inFlight
	f.mu.Unlock()
	close(f.hold)
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if inFlight > amMaxInFlight || f.maxInFlight > amMaxInFlight {
		t.Fatalf("in flight %d, max %d", inFlight, f.maxInFlight)
	}
	if inFlight != amMaxInFlight {
		t.Fatalf("the limit was not reached: %d", inFlight)
	}
}

// S8a §2: serviceRef.path through the API server's service proxy.
func TestReadAlertsServiceRefPath(t *testing.T) {
	f := newAMFake()
	f.alerts = `[{"fingerprint":"f1","labels":{"alertname":"A"},"status":{"state":"active"},"receivers":[{"name":"r"}]}]`
	api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/mimir/services/http:alertmanager:8080/proxy", f))
	defer api.Close()
	e := newAMEnv(t, &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "mimir", Name: "alertmanager", Port: "8080", Path: "/alertmanager"}, Tenant: "t"}, nil, nil)
	e.c.restConfig = &rest.Config{Host: api.URL}
	r := e.read(t)
	if len(r.Alerts) != 1 || !strings.HasPrefix(r.Identity, "service|mimir/alertmanager:8080/alertmanager|tenant=t") {
		t.Fatalf("read: %+v", r)
	}
	if len(f.queries["/alertmanager/api/v2/alerts"]) != 1 || f.tenant[0] != "t" || f.auth[0] != "" {
		t.Fatalf("proxied: %v %q %q", f.queries, f.tenant, f.auth)
	}
}

// discovery fixtures, as Prometheus Operator v0.94.1 creates them

var (
	amUID  = types.UID("am-uid")
	stsUID = types.UID("sts-uid")
)

func amResource(ns, name string, uid types.UID, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "monitoring.coreos.com/v1", "kind": "Alertmanager",
		"metadata": map[string]any{"namespace": ns, "name": name, "uid": string(uid)}, "spec": spec}}
	return u
}

func amDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{amResourceGVR: "AlertmanagerList"}, objs...)
}

func servedAM(typed *typedfake.Clientset) {
	typed.Resources = []*metav1.APIResourceList{{GroupVersion: "monitoring.coreos.com/v1", APIResources: []metav1.APIResource{{Name: "alertmanagers", Namespaced: true, Kind: "Alertmanager"}}}}
}

func governed(pods ...string) []runtime.Object {
	ctl := true
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "alertmanager-main", UID: stsUID,
		OwnerReferences: []metav1.OwnerReference{{Kind: "Alertmanager", Name: "main", UID: amUID, Controller: &ctl}}}}
	objs := []runtime.Object{sts,
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "alertmanager-operated"},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app.kubernetes.io/name": "alertmanager"}, Ports: []corev1.ServicePort{{Name: "web", Port: 9093}, {Name: "tcp-mesh", Port: 9094}}}}}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "alertmanager-operated-x", Labels: map[string]string{"kubernetes.io/service-name": "alertmanager-operated"}}}
	for _, p := range pods {
		objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: p,
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "alertmanager-main", UID: stsUID, Controller: &ctl}}}})
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: p}})
	}
	return append(objs, slice)
}

func TestAlertmanagerDiscovery(t *testing.T) {
	ctx := context.Background()
	discover := func(typed *typedfake.Clientset, dyn *dynamicfake.FakeDynamicClient) *amDiscovery {
		e := newAMEnv(t, nil, typed, dyn)
		return e.c.discoverAlertmanager(ctx)
	}
	// not served: none
	if d := discover(typedfake.NewSimpleClientset(), amDyn()); !d.none {
		t.Fatalf("not served: %+v", d)
	}
	// served, no resource: none
	typed := typedfake.NewSimpleClientset()
	servedAM(typed)
	if d := discover(typed, amDyn()); !d.none {
		t.Fatalf("no resource: %+v", d)
	}
	// a forbidden list is a failure, never none
	typed = typedfake.NewSimpleClientset()
	servedAM(typed)
	dyn := amDyn()
	dyn.PrependReactor("list", "alertmanagers", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "monitoring.coreos.com", Resource: "alertmanagers"}, "", errors.New("rbac"))
	})
	if d := discover(typed, dyn); d.err == nil || d.none {
		t.Fatalf("forbidden: %+v", d)
	}
	// two resources sharing one governing Service: ambiguous
	typed = typedfake.NewSimpleClientset(governed("alertmanager-main-0")...)
	servedAM(typed)
	if d := discover(typed, amDyn(amResource("monitoring", "main", amUID, nil), amResource("monitoring", "second", "uid-2", nil))); d.ambiguous == "" {
		t.Fatalf("two resources: %+v", d)
	}
	// an instance Service is preferred, with the route prefix as the path
	inst := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "kps-alertmanager"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"alertmanager": "main", "app.kubernetes.io/name": "alertmanager"}, Ports: []corev1.ServicePort{{Name: "http-web", Port: 9093}}}}
	typed = typedfake.NewSimpleClientset(append(governed("alertmanager-main-0"), inst)...)
	servedAM(typed)
	d := discover(typed, amDyn(amResource("monitoring", "main", amUID, map[string]any{"routePrefix": "/am"})))
	if d.svc.Name != "kps-alertmanager" || d.svc.Port != "http-web" || d.svc.Path != "/am" || d.resource != "monitoring/main" {
		t.Fatalf("instance Service: %+v", d)
	}
	// the governing Service, proven by ownership
	typed = typedfake.NewSimpleClientset(governed("alertmanager-main-0", "alertmanager-main-1")...)
	servedAM(typed)
	if d := discover(typed, amDyn(amResource("monitoring", "main", amUID, nil))); d.svc.Name != "alertmanager-operated" || d.svc.Port != "web" || d.ambiguous != "" || d.err != nil {
		t.Fatalf("governing proven: %+v", d)
	}
	// unproven: an endpoint without a targetRef, a Pod of another owner, a
	// StatefulSet not owned by the resource, or no endpoints at all
	ctl := true
	for name, mutate := range map[string]func([]runtime.Object) []runtime.Object{
		"sts owned by another Alertmanager": func(o []runtime.Object) []runtime.Object {
			o[0].(*appsv1.StatefulSet).OwnerReferences = []metav1.OwnerReference{{Kind: "Alertmanager", Name: "main", UID: "an-earlier-uid", Controller: &ctl}}
			return o
		},
		"another owner": func(o []runtime.Object) []runtime.Object {
			o[2].(*corev1.Pod).OwnerReferences = []metav1.OwnerReference{{Kind: "StatefulSet", Name: "other", UID: "other-uid", Controller: &ctl}}
			return o
		},
		"sts not owned": func(o []runtime.Object) []runtime.Object {
			o[0].(*appsv1.StatefulSet).OwnerReferences = nil
			return o
		},
		"no endpoints": func(o []runtime.Object) []runtime.Object {
			o[len(o)-1].(*discoveryv1.EndpointSlice).Endpoints = nil
			return o
		},
	} {
		typed = typedfake.NewSimpleClientset(mutate(governed("alertmanager-main-0"))...)
		servedAM(typed)
		if d := discover(typed, amDyn(amResource("monitoring", "main", amUID, nil))); d.ambiguous == "" || d.svc.Name != "" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// one endpoint without a targetRef beside a proven one is not proof
	objs := governed("alertmanager-main-0", "alertmanager-main-1")
	objs[len(objs)-1].(*discoveryv1.EndpointSlice).Endpoints[0].TargetRef = nil
	typed = typedfake.NewSimpleClientset(objs...)
	servedAM(typed)
	if d := discover(typed, amDyn(amResource("monitoring", "main", amUID, nil))); d.ambiguous == "" {
		t.Errorf("a missing targetRef beside a proven one: %+v", d)
	}
	// a Service pinning another instance is never chosen
	other := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "aaa-other"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"alertmanager": "other"}, Ports: []corev1.ServicePort{{Name: "web", Port: 9093}}}}
	typed = typedfake.NewSimpleClientset(append(governed("alertmanager-main-0"), inst, other)...)
	servedAM(typed)
	if d := discover(typed, amDyn(amResource("monitoring", "main", amUID, nil))); d.svc.Name != "kps-alertmanager" {
		t.Errorf("another instance's Service: %+v", d)
	}
}

// A failed discovery reads "could not be read", and only a complete empty
// discovery is ErrNoAlertmanager.
func TestReadAlertsDiscoveryOutcomes(t *testing.T) {
	typed := typedfake.NewSimpleClientset()
	servedAM(typed)
	dyn := amDyn()
	dyn.PrependReactor("list", "alertmanagers", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server is unavailable")
	})
	e := newAMEnv(t, nil, typed, dyn)
	_, err := e.c.ReadAlerts(context.Background(), e.c.AlertsIdentity())
	if err == nil || errors.Is(err, ErrNoAlertmanager) || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("failed discovery: %v", err)
	}
	e2 := newAMEnv(t, nil, typedfake.NewSimpleClientset(), amDyn())
	r, err := e2.c.ReadAlerts(context.Background(), e2.c.AlertsIdentity())
	if errors.Is(err, ErrSourceChanged) {
		_, err = e2.c.ReadAlerts(context.Background(), r.Identity)
	}
	if !errors.Is(err, ErrNoAlertmanager) {
		t.Fatalf("nothing found: %v", err)
	}
}

// Settings' Test reads /api/v2/status through the source as configured.
func TestAlertmanagerStatus(t *testing.T) {
	f := newAMFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	e := newAMEnv(t, &config.EndpointConfig{Endpoint: srv.URL}, nil, nil)
	st, src, err := e.c.AlertmanagerStatus(context.Background())
	if err != nil || st.Version != "0.34.1" || st.ClusterStatus != "disabled" || src == "" {
		t.Fatalf("status: %+v %q %v", st, src, err)
	}
	if len(f.queries["/api/v2/alerts"]) != 0 {
		t.Fatal("Test read the alerts")
	}
}

// The fleet fence drops a read across a revision move where the transport
// has no gate of its own: the service proxy carries no credential.
func TestReadAlertsFenceOnTheProxyPath(t *testing.T) {
	f := newAMFake()
	f.hold, f.entered = make(chan struct{}), make(chan struct{}, 1)
	api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/m/services/http:am:web/proxy", f))
	defer api.Close()
	e := newAMEnv(t, &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "m", Name: "am", Port: "web"}}, nil, nil)
	e.c.restConfig = &rest.Config{Host: api.URL}
	_, _ = e.c.ReadAlerts(context.Background(), "")
	key := e.c.AlertsIdentity()
	done := make(chan error, 1)
	go func() { _, err := e.c.ReadAlerts(context.Background(), key); done <- err }()
	<-f.entered
	e.reg.Bump("prd", config.EndpointAlertmanager, false)
	close(f.hold)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "changed during the read") {
		t.Fatalf("a read across a revision move: %v", err)
	}
}

// One cluster never has two alert reads in flight.
func TestReadAlertsOnePerCluster(t *testing.T) {
	f := newAMFake()
	f.hold = make(chan struct{})
	srv := httptest.NewServer(f)
	defer srv.Close()
	e := newAMEnv(t, &config.EndpointConfig{Endpoint: srv.URL}, nil, nil)
	_, _ = e.c.ReadAlerts(context.Background(), "")
	key := e.c.AlertsIdentity()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.c.ReadAlerts(context.Background(), key) }()
	}
	time.Sleep(150 * time.Millisecond)
	close(f.hold)
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxInFlight != 1 {
		t.Fatalf("in flight at once: %d", f.maxInFlight)
	}
}

// A credential installed with a changed configuration adopts it, in one step.
func TestChangeAlertmanagerCredentialAdoptsTheConfiguration(t *testing.T) {
	cfg := &config.EndpointConfig{Endpoint: "https://am.example/one", Auth: &config.EndpointAuth{Type: "bearer"}}
	e := newAMEnv(t, cfg, nil, nil)
	e.c.SetAlertmanagerConfig(cfg)
	next := &config.EndpointConfig{Endpoint: "https://am.example/two", Auth: &config.EndpointAuth{Type: "bearer"}, Match: map[string]string{"cluster": "a"}}
	before := len(e.rekeys())
	if err := e.c.ChangeAlertmanagerCredential(next, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := e.c.AlertmanagerConfig(); got.Endpoint != next.Endpoint || got.Match["cluster"] != "a" || len(e.rekeys()) != before+1 {
		t.Fatalf("adopted %+v, re-keys %q", got, e.rekeys())
	}
	if err := e.c.ChangeAlertmanagerCredential(cfg, func() error { return errors.New("keychain") }); err == nil || e.c.AlertmanagerConfig().Endpoint != next.Endpoint {
		t.Fatal("a failed install changed the configuration")
	}
}

type timingOutDiscovery struct{}

func (timingOutDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return nil, errors.New("discovery timed out")
}
func (timingOutDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, errors.New("discovery timed out")
}

// A discovery error other than "not found" is a failure, never "none".
func TestAlertmanagerDiscoveryErrorIsNotNone(t *testing.T) {
	e := newAMEnv(t, nil, typedfake.NewSimpleClientset(), amDyn())
	e.c.disc = timingOutDiscovery{}
	if d := e.c.discoverAlertmanager(context.Background()); d.err == nil || d.none {
		t.Fatalf("discovery error: %+v", d)
	}
}

// An Observer restarted with the empty key re-keys to the identity the
// connection already holds - a read never stays refused because the
// identity itself did not change.
func TestAReadKeyedOtherwiseAlwaysRekeys(t *testing.T) {
	cfg := &config.EndpointConfig{Endpoint: "https://am.example"}
	e := newAMEnv(t, cfg, nil, nil)
	e.c.SetAlertmanagerConfig(cfg)
	id := e.c.AlertsIdentity()
	before := len(e.rekeys())
	if _, err := e.c.ReadAlerts(context.Background(), ""); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("read: %v", err)
	}
	if got := e.rekeys(); len(got) != before+1 || got[len(got)-1] != id {
		t.Fatalf("re-keys %q, want %q appended", got, id)
	}
}

// S8a §4: the Alerting capability is the snapshot's own evidence - present
// when configured, or after a keyed read resolved an Alertmanager; absent
// after a read that completed discovery and found none.
func TestAlertingCapabilityIsTheReadsEvidence(t *testing.T) {
	cfg := &config.EndpointConfig{Endpoint: "https://am.example"}
	if e := newAMEnv(t, cfg, nil, nil); !e.c.alertsPresent() {
		t.Fatal("configured is present")
	}
	e := newAMEnv(t, nil, typedfake.NewSimpleClientset(), amDyn())
	if e.c.alertsPresent() {
		t.Fatal("unread and unconfigured is not present")
	}
	r, err := e.c.ReadAlerts(context.Background(), e.c.AlertsIdentity())
	if errors.Is(err, ErrSourceChanged) {
		_, err = e.c.ReadAlerts(context.Background(), r.Identity)
	}
	if !errors.Is(err, ErrNoAlertmanager) || e.c.alertsPresent() {
		t.Fatalf("none found: %v present=%v", err, e.c.alertsPresent())
	}
	typed := typedfake.NewSimpleClientset(governed("alertmanager-main-0")...)
	servedAM(typed)
	e2 := newAMEnv(t, nil, typed, amDyn(amResource("monitoring", "main", amUID, nil)))
	r, err = e2.c.ReadAlerts(context.Background(), e2.c.AlertsIdentity())
	if errors.Is(err, ErrSourceChanged) {
		_, _ = e2.c.ReadAlerts(context.Background(), r.Identity)
	}
	if !e2.c.alertsPresent() {
		t.Fatal("a discovered Alertmanager is present")
	}
}

// The connection's snapshot carries the capability the cluster DTO reads.
func TestSnapshotCarriesTheAlertingCapability(t *testing.T) {
	e := newAMEnv(t, &config.EndpointConfig{Endpoint: "https://am.example"}, nil, nil)
	if !e.c.Snapshot().Capabilities.Alerting.Present {
		t.Fatal("the snapshot dropped the Alerting capability")
	}
}
