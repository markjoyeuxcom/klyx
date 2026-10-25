package fleetindex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/observe"
)

// ---- a fake Observer ----

type fakeKind struct {
	gvr  string
	st   observe.KindState
	objs []any
}

type fakeObs struct {
	mu       sync.Mutex
	inc      uint64
	kinds    map[string]*fakeKind
	watchers map[string][]func(observe.KindState)
	visits   map[string]int
	roles    map[string]observe.RoleView // source/uid
	done     chan struct{}
}

func newFakeObs(inc uint64) *fakeObs {
	return &fakeObs{inc: inc, kinds: map[string]*fakeKind{}, watchers: map[string][]func(observe.KindState){}, visits: map[string]int{}, roles: map[string]observe.RoleView{}, done: make(chan struct{})}
}

func meta1(uid, ns, name string, labels map[string]string) any {
	return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid), Namespace: ns, Name: name, Labels: labels, CreationTimestamp: metav1.NewTime(t0)}}
}

// set replaces a kind's model and coverage and notifies its watchers.
func (f *fakeObs) set(name, gvr string, cov observe.Coverage, objs ...any) {
	f.mu.Lock()
	k := f.kinds[name]
	if k == nil {
		k = &fakeKind{gvr: gvr}
		f.kinds[name] = k
	}
	k.objs = objs
	k.st.Seq++
	k.st.Kind, k.st.Coverage, k.st.GVR = name, cov, gvr
	k.st.CountKnown = cov == observe.Live || cov == observe.Stale
	k.st.Objects = len(objs)
	st, ws := k.st, append([]func(observe.KindState){}, f.watchers[name]...)
	f.mu.Unlock()
	for _, w := range ws {
		go w(st)
	}
}

func (f *fakeObs) Incarnation() uint64 { return f.inc }

// RolesNow answers from roles set by the test; any other UID is unverified.
func (f *fakeObs) RolesNow(ctx context.Context, qs []observe.RoleQuery) ([]observe.RoleView, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]observe.RoleView, len(qs))
	for i, q := range qs {
		v, ok := f.roles[q.Source+"/"+q.UID]
		if !ok {
			v = observe.RoleView{State: observe.RoleUnverified, Reason: observe.ReasonNotObserved}
		}
		out[i] = v
	}
	return out, true
}
func (f *fakeObs) Done() <-chan struct{} { return f.done }
func (f *fakeObs) Kinds() []observe.KindInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []observe.KindInfo
	for n, k := range f.kinds {
		out = append(out, observe.KindInfo{Name: n, GVR: k.gvr})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (f *fakeObs) WatchKind(kind string, fn func(observe.KindState)) func() {
	f.mu.Lock()
	f.watchers[kind] = append(f.watchers[kind], fn)
	st := f.kinds[kind].st
	f.mu.Unlock()
	go fn(st)
	return func() {}
}
func (f *fakeObs) VisitKind(kind string, visit func(any) bool) (observe.KindState, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.kinds[kind]
	if k == nil {
		return observe.KindState{}, false, false
	}
	if !k.st.CountKnown {
		return k.st, false, true
	}
	f.visits[kind]++
	for _, o := range k.objs {
		if !visit(o) {
			return k.st, false, true
		}
	}
	return k.st, true, true
}

// ---- a fake API server ----

type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	items       map[string][]string // resource -> item JSON
	listStatus  map[string]int
	watchStatus map[string]int
	lists       []string // resources, in the order listed
	listing     int
	maxListing  int
	watching    map[string]int
	events      map[string]chan string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	a := &fakeAPI{t: t, items: map[string][]string{}, listStatus: map[string]int{}, watchStatus: map[string]int{}, watching: map[string]int{}, events: map[string]chan string{}}
	a.srv = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	return a
}

func obj(uid, ns, name string) string {
	return fmt.Sprintf(`{"kind":"PartialObjectMetadata","metadata":{"uid":%q,"namespace":%q,"name":%q,"resourceVersion":"5","creationTimestamp":"2026-10-15T12:00:00Z","labels":{"app":"x"}}}`, uid, ns, name)
}

func (a *fakeAPI) ch(res string) chan string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.events[res] == nil {
		a.events[res] = make(chan string, 64)
	}
	return a.events[res]
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	res := path.Base(r.URL.Path)
	if r.URL.Query().Get("watch") == "1" {
		a.mu.Lock()
		code := a.watchStatus[res]
		a.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		a.mu.Lock()
		a.watching[res]++
		a.mu.Unlock()
		defer func() { a.mu.Lock(); a.watching[res]--; a.mu.Unlock() }()
		w.Header().Set("Content-Type", acceptWatch)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		ev := a.ch(res)
		for {
			select {
			case <-r.Context().Done():
				return
			case line := <-ev:
				if line == "close" {
					return
				}
				fmt.Fprintln(w, line)
				w.(http.Flusher).Flush()
			}
		}
	}
	a.mu.Lock()
	a.lists = append(a.lists, res)
	a.listing++
	a.maxListing = max(a.maxListing, a.listing)
	code := a.listStatus[res]
	items := append([]string(nil), a.items[res]...)
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.listing--; a.mu.Unlock() }()
	time.Sleep(5 * time.Millisecond) // overlap would show
	if code != 0 {
		w.WriteHeader(code)
		return
	}
	w.Header().Set("Content-Type", acceptList)
	fmt.Fprintf(w, `{"kind":"PartialObjectMetadataList","metadata":{"resourceVersion":"9"},"items":[%s]}`, strings.Join(items, ","))
}

func (a *fakeAPI) acquire(notServed ...string) Acquire {
	return Acquire{
		Served: func(_ context.Context, group, resource, prefer string) (string, bool, error) {
			for _, n := range notServed {
				if n == resource {
					return "", false, nil
				}
			}
			return prefer, true, nil
		},
		Reader: &Reader{Client: a.srv.Client(), Host: a.srv.URL},
	}
}

func (a *fakeAPI) listed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.lists...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testSettings(kinds ...KindRef) Settings {
	return Settings{Kinds: kinds, Clock: func() time.Time { return t0 }, VisitEvery: time.Millisecond, BackoffMin: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond, ReprobeEvery: 20 * time.Millisecond, WatchTimeout: time.Minute,
		Gaps: map[string]Gap{"namespaces": {NoLabels: true, NoCreated: true}}}
}

var (
	refPods       = KindRef{"", "v1", "pods", "Pod"}
	refNamespaces = KindRef{"", "v1", "namespaces", "Namespace"}
	refServices   = KindRef{"", "v1", "services", "Service"}
	refConfigMaps = KindRef{"", "v1", "configmaps", "ConfigMap"}
	refSecrets    = KindRef{"", "v1", "secrets", "Secret"}
)

func kindStatus(ix *Index, cluster, key string) KindStatus {
	for _, cc := range ix.Coverage() {
		if cc.Cluster != cluster {
			continue
		}
		for _, k := range cc.Kinds {
			if k.Key == key {
				return k.KindStatus
			}
		}
	}
	return KindStatus{}
}

func entryUIDs(ix *Index, cluster, key string) []string {
	ix.mu.Lock()
	c := ix.clusters[cluster]
	ix.mu.Unlock()
	if c == nil {
		return nil
	}
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	k := c.store.kinds[key]
	if k == nil {
		return nil
	}
	var out []string
	for u := range k.entries {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// §2.1, §2.3, §2.4: Observer kinds are visited, never watched again; the
// other kinds are listed one at a time in configuration order, Pods last.
func TestCoordReadsObserverKindsWithoutASecondWatch(t *testing.T) {
	api := newFakeAPI(t)
	api.items["configmaps"] = []string{obj("c1", "a", "cm1"), obj("c2", "a", "cm2")}
	api.items["secrets"] = []string{obj("s1", "a", "tls")}
	obs := newFakeObs(1)
	obs.set("pods", "v1/pods", observe.Live, meta1("p1", "a", "web-1", map[string]string{"app": "web"}), meta1("p2", "a", "web-2", nil))
	obs.set("namespaces", "v1/namespaces", observe.Live, meta1("n1", "", "a", map[string]string{"x": "y"}))
	ix := New(testSettings(refPods, refConfigMaps, refNamespaces, refSecrets))
	defer ix.Stop()
	ix.SetClusters([]string{"a", "never"})
	ix.Attach("a", obs, api.acquire())
	ix.SetConnection("a", "connected")
	eventually(t, "every kind live", func() bool {
		for _, k := range []string{"/pods", "/configmaps", "/namespaces", "/secrets"} {
			if kindStatus(ix, "a", k).State != StateLive {
				return false
			}
		}
		return true
	})
	if got := strings.Join(api.listed(), ","); got != "configmaps,secrets" {
		t.Fatalf("listed %q: Observer kinds must not be listed, the others in order", got)
	}
	if api.maxListing != 1 {
		t.Fatalf("%d lists at once", api.maxListing)
	}
	if fmt.Sprint(entryUIDs(ix, "a", "/pods")) != "[p1 p2]" || fmt.Sprint(entryUIDs(ix, "a", "/configmaps")) != "[c1 c2]" {
		t.Fatal("entries")
	}
	// the projection's gaps: namespaces keep neither labels nor creation
	ix.mu.Lock()
	ns := ix.clusters["a"].store.kinds["/namespaces"].entries["n1"]
	ix.mu.Unlock()
	if ns.Labels != nil || !ns.NoLabels || !ns.NoCreated {
		t.Fatalf("namespace entry %+v", ns)
	}
	// a cluster that never became ready is named, not indexed
	cov := ix.Coverage()
	if len(cov) != 2 || cov[1].Cluster != "never" || cov[1].Indexed {
		t.Fatalf("coverage %+v", cov)
	}
	// the visit feed follows the Observer's model
	obs.set("pods", "v1/pods", observe.Live, meta1("p2", "a", "web-2", nil), meta1("p3", "a", "web-3", nil))
	eventually(t, "the visit feed", func() bool { return fmt.Sprint(entryUIDs(ix, "a", "/pods")) == "[p2 p3]" })
	// a watch event on an acquired kind
	api.ch("configmaps") <- `{"type":"ADDED","object":` + obj("c3", "b", "cm3") + `}`
	api.ch("configmaps") <- `{"type":"DELETED","object":` + obj("c1", "a", "cm1") + `}`
	eventually(t, "watch events", func() bool { return fmt.Sprint(entryUIDs(ix, "a", "/configmaps")) == "[c2 c3]" })
}

// §2.7: a down connection cancels the generation, keeps last-good entries
// stale and drops late results; recovery in the same incarnation lists
// afresh; Final releases.
func TestCoordConnectionLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	api.items["configmaps"] = []string{obj("c1", "a", "cm1")}
	obs := newFakeObs(1)
	obs.set("pods", "v1/pods", observe.Live, meta1("p1", "a", "web-1", nil))
	ix := New(testSettings(refPods, refConfigMaps))
	defer ix.Stop()
	ix.Attach("a", obs, api.acquire())
	ix.SetConnection("a", "connecting")
	if cov := ix.Coverage(); cov[0].Indexed {
		t.Fatal("indexed before the connection was usable")
	}
	ix.SetConnection("a", "connected")
	eventually(t, "live", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return kindStatus(ix, "a", "/configmaps").State == StateLive && api.watching["configmaps"] == 1
	})
	ix.SetConnection("a", "stale")
	st := kindStatus(ix, "a", "/configmaps")
	if st.State != StateStale || st.Entries != 1 || st.AsOf == nil {
		t.Fatalf("down: %+v", st)
	}
	eventually(t, "the watch closed", func() bool { api.mu.Lock(); defer api.mu.Unlock(); return api.watching["configmaps"] == 0 })
	// a late event of the cancelled generation is never applied
	ix.mu.Lock()
	c := ix.clusters["a"]
	ix.mu.Unlock()
	if c.store.put(1, "/configmaps", 0, Source{UID: "late", Name: "late"}) != putStale {
		t.Fatal("a late result applied")
	}
	api.mu.Lock()
	api.items["configmaps"] = []string{obj("c2", "a", "cm2")}
	api.mu.Unlock()
	ix.SetConnection("a", "connected · flux degraded")
	eventually(t, "a fresh list in place", func() bool {
		return kindStatus(ix, "a", "/configmaps").State == StateLive && fmt.Sprint(entryUIDs(ix, "a", "/configmaps")) == "[c2]"
	})
	if n := len(api.listed()); n != 2 {
		t.Fatalf("%d lists", n)
	}
	// Final: the Observer ends
	close(obs.done)
	eventually(t, "released", func() bool { return !ix.Coverage()[0].Indexed })
	if e, _, _, _ := ix.budget.Used("a"); e != 0 {
		t.Fatalf("%d entries held after Final", e)
	}
}

// A new incarnation starts a new generation; the old Observer's end is
// ignored.
func TestCoordNewIncarnation(t *testing.T) {
	api := newFakeAPI(t)
	old := newFakeObs(1)
	old.set("pods", "v1/pods", observe.Live, meta1("p1", "a", "a", nil))
	ix := New(testSettings(refPods))
	defer ix.Stop()
	ix.Attach("a", old, api.acquire())
	ix.SetConnection("a", "connected")
	eventually(t, "old", func() bool { return fmt.Sprint(entryUIDs(ix, "a", "/pods")) == "[p1]" })
	next := newFakeObs(2)
	next.set("pods", "v1/pods", observe.Live, meta1("p9", "a", "b", nil))
	ix.Attach("a", next, api.acquire())
	eventually(t, "new", func() bool { return fmt.Sprint(entryUIDs(ix, "a", "/pods")) == "[p9]" })
	close(old.done)
	time.Sleep(20 * time.Millisecond)
	if cov := ix.Coverage()[0]; !cov.Indexed || cov.Incarnation != 2 {
		t.Fatalf("the old Observer's end released the new one: %+v", cov)
	}
}

// §2.1: Services follow the Gateway API - acquired by the index while the
// Observer's kind is absent, handed over when it is served.
func TestCoordServicesHandOver(t *testing.T) {
	api := newFakeAPI(t)
	api.items["services"] = []string{obj("v1", "a", "svc")}
	obs := newFakeObs(1)
	obs.set("gatewayservices", "v1/services", observe.Absent)
	ix := New(testSettings(refServices))
	defer ix.Stop()
	ix.Attach("a", obs, api.acquire())
	ix.SetConnection("a", "connected")
	eventually(t, "acquired", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return kindStatus(ix, "a", "/services").State == StateLive && api.watching["services"] == 1
	})
	obs.set("gatewayservices", "v1/services", observe.Live, meta1("v1", "a", "svc", nil), meta1("v2", "a", "svc2", nil))
	eventually(t, "handed over", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return fmt.Sprint(entryUIDs(ix, "a", "/services")) == "[v1 v2]" && api.watching["services"] == 0
	})
}

// §2.8: refusals, unsupported metadata and oversize objects are said; the
// kinds around them go on.
func TestCoordKindStates(t *testing.T) {
	api := newFakeAPI(t)
	api.listStatus["secrets"] = 403
	api.listStatus["configmaps"] = 406
	api.items["persistentvolumeclaims"] = []string{obj("v1", "a", "data"), `{"metadata":{"uid":"big","name":"` + strings.Repeat("x", 2000) + `"}}`}
	api.items["jobs"] = []string{obj("j1", "a", "job")}
	api.watchStatus["jobs"] = 403
	obs := newFakeObs(1)
	refPVC := KindRef{"", "v1", "persistentvolumeclaims", "PersistentVolumeClaim"}
	refJobs := KindRef{"batch", "v1", "jobs", "Job"}
	refGRPC := KindRef{"gateway.networking.k8s.io", "v1", "grpcroutes", "GRPCRoute"}
	ix := New(testSettings(refSecrets, refConfigMaps, refPVC, refJobs, refGRPC))
	defer ix.Stop()
	ix.Attach("a", obs, api.acquire("grpcroutes"))
	ix.SetConnection("a", "connected")
	want := map[string]string{"/secrets": StateDenied, "/configmaps": StateUnsupported, "/persistentvolumeclaims": StateOversize, "batch/jobs": StateWatchDenied, "gateway.networking.k8s.io/grpcroutes": StateNotServed}
	eventually(t, "states", func() bool {
		for k, s := range want {
			if kindStatus(ix, "a", k).State != s {
				return false
			}
		}
		return true
	})
	if st := kindStatus(ix, "a", "/persistentvolumeclaims"); st.Entries != 1 || st.AsOf == nil {
		t.Fatalf("oversize keeps its evidence: %+v", st)
	}
	if st := kindStatus(ix, "a", "batch/jobs"); st.Entries != 1 || st.AsOf == nil {
		t.Fatalf("watch denied keeps the list, stale: %+v", st)
	}
}

// §2.5: an expired watch relists in place.
func TestCoordRelistsOnExpiredWatch(t *testing.T) {
	api := newFakeAPI(t)
	api.items["configmaps"] = []string{obj("c1", "a", "cm1")}
	ix := New(testSettings(refConfigMaps))
	defer ix.Stop()
	ix.Attach("a", newFakeObs(1), api.acquire())
	ix.SetConnection("a", "connected")
	eventually(t, "watching", func() bool { api.mu.Lock(); defer api.mu.Unlock(); return api.watching["configmaps"] == 1 })
	api.mu.Lock()
	api.items["configmaps"] = []string{obj("c2", "a", "cm2")}
	api.mu.Unlock()
	api.ch("configmaps") <- `{"type":"ERROR","object":{"kind":"Status","code":410,"reason":"Expired"}}`
	eventually(t, "relisted", func() bool { return fmt.Sprint(entryUIDs(ix, "a", "/configmaps")) == "[c2]" })
}

// Settings S1: the index says what it runs with, after the defaults.
func TestConfiguredIsWhatRuns(t *testing.T) {
	ix := New(Settings{})
	defer ix.Stop()
	kinds, l := ix.Configured()
	if len(kinds) != len(DefaultKinds) || l != DefaultLimits {
		t.Fatalf("%v %+v", kinds, l)
	}
}
