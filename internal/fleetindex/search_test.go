package fleetindex

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// searchFixture indexes two clusters from fake Observers: pods and
// configmaps (the latter acquired) on each, a same-name pod on both.
func searchFixture(t *testing.T) (*Index, *fakeObs, *fakeObs) {
	t.Helper()
	api := newFakeAPI(t)
	api.items["configmaps"] = []string{obj("cm-a", "shop", "web-config"), obj("cm-b", "shop", "checkout-config")}
	a, b := newFakeObs(1), newFakeObs(2)
	a.set("pods", "v1/pods", observe.Live, meta1("pa-web", "shop", "web", nil), meta1("pa-web2", "shop", "web-7d9f", nil), meta1("pa-api", "shop", "api", nil), meta1("pa-x", "team-a", "reweb", nil))
	b.set("pods", "v1/pods", observe.Live, meta1("pb-web", "shop", "web", nil))
	a.mu.Lock()
	a.roles["pods/pa-web"] = observe.RoleView{State: observe.RoleCurrent, Role: "fault", Label: "CrashLoopBackOff"}
	a.roles["pods/pa-web2"] = observe.RoleView{State: observe.RoleLastObserved, Role: "ok"}
	a.mu.Unlock()
	ix := New(testSettings(refPods, refConfigMaps))
	t.Cleanup(ix.Stop)
	ix.SetClusters([]string{"prd", "stg", "dev"})
	ix.Attach("prd", a, api.acquire())
	ix.Attach("stg", b, api.acquire())
	ix.SetConnection("prd", "connected")
	ix.SetConnection("stg", "connected")
	eventually(t, "indexed", func() bool {
		return kindStatus(ix, "prd", "/configmaps").State == StateLive && kindStatus(ix, "stg", "/configmaps").State == StateLive &&
			kindStatus(ix, "prd", "/pods").State == StateLive && kindStatus(ix, "stg", "/pods").State == StateLive
	})
	return ix, a, b
}

func names(hs []Hit) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Cluster+":"+h.Namespace+"/"+h.Name)
	}
	return strings.Join(out, " ")
}

func TestSearchRanksDeterministically(t *testing.T) {
	ix, _, _ := searchFixture(t)
	res, err := ix.Search(context.Background(), "web", 50)
	if err != nil {
		t.Fatal(err)
	}
	// exact names first (fleet order), then prefixes, then substrings
	want := "prd:shop/web stg:shop/web prd:shop/web-7d9f prd:shop/web-config stg:shop/web-config prd:team-a/reweb"
	if got := names(res.Hits); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if res.Total != 6 {
		t.Fatalf("total %d", res.Total)
	}
	// same-name objects on two clusters are two hits, each with its UID
	if res.Hits[0].UID != "pa-web" || res.Hits[1].UID != "pb-web" || res.Hits[0].Kind != "Pod" || res.Hits[0].GVR != "v1/pods" {
		t.Fatalf("hits %+v", res.Hits[:2])
	}
	for i := 0; i < 5; i++ {
		again, _ := ix.Search(context.Background(), "web", 50)
		if names(again.Hits) != want {
			t.Fatal("not deterministic")
		}
	}
	// coverage names the cluster never attached
	if len(res.Coverage) != 3 || res.Coverage[2].Cluster != "dev" || res.Coverage[2].Indexed {
		t.Fatalf("coverage %+v", res.Coverage)
	}
}

func TestSearchRolesFencedAndUnverified(t *testing.T) {
	ix, a, _ := searchFixture(t)
	res, _ := ix.Search(context.Background(), "web", 50)
	role := map[string]string{}
	for _, h := range res.Hits {
		role[h.Cluster+":"+h.Name] = h.Role.State + ":" + h.Role.Role + h.Role.Reason
	}
	want := map[string]string{
		"prd:web":        "current:fault",
		"prd:web-7d9f":   "lastObserved:ok",
		"stg:web":        "unverified:not observed",
		"prd:web-config": "unverified:no ledger for this kind", // ConfigMaps: not evaluated - health unverified
		"prd:reweb":      "unverified:not observed",
	}
	for k, w := range want {
		if role[k] != w {
			t.Errorf("%s: %q, want %q", k, role[k], w)
		}
	}
	// a new incarnation between indexing and the role read: unverified
	ix.mu.Lock()
	c := ix.clusters["prd"]
	ix.mu.Unlock()
	hits := []Hit{{Cluster: "prd", UID: "pa-web", observer: "pods", Incarnation: a.inc + 1}}
	if err := ix.roles(context.Background(), map[string]*coord{"prd": c}, hits); err != nil || hits[0].Role.State != observe.RoleUnverified || !strings.Contains(hits[0].Role.Reason, "changed") {
		t.Fatalf("incarnation fence: %+v %v", hits[0].Role, err)
	}
}

func TestSearchQueriesAndBounds(t *testing.T) {
	ix, _, _ := searchFixture(t)
	q := func(s string, limit int) Result {
		r, err := ix.Search(context.Background(), s, limit)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := q("w", 50); len(r.Hits) != 0 || len(r.Coverage) == 0 {
		t.Fatal("a one-character query searches nothing, but says what it would search")
	}
	if got := names(q("pod web", 50).Hits); got != "prd:shop/web stg:shop/web prd:shop/web-7d9f prd:team-a/reweb" {
		t.Fatalf("kind restriction: %s", got)
	}
	if got := names(q("shop/web", 50).Hits); !strings.HasPrefix(got, "prd:shop/web stg:shop/web") {
		t.Fatalf("namespace/name: %s", got)
	}
	if got := names(q("  WEB  ", 50).Hits); !strings.HasPrefix(got, "prd:shop/web ") {
		t.Fatalf("case and spaces: %s", got)
	}
	if r := q("configmap", 50); r.Total != 4 || r.Hits[0].Kind != "ConfigMap" {
		t.Fatalf("a kind alone lists its objects: %d %s", r.Total, names(r.Hits))
	}
	r := q("web", 2)
	if len(r.Hits) != 2 || r.Total != 6 || names(r.Hits) != "prd:shop/web stg:shop/web" {
		t.Fatalf("top-K: %d of %d: %s", len(r.Hits), r.Total, names(r.Hits))
	}
	if r := q("web", 10_000); len(r.Hits) != 6 {
		t.Fatal("limit capped at MaxHits")
	}
}

// A cancelled search returns at once with the context's error.
func TestSearchCancels(t *testing.T) {
	ix := New(Settings{Kinds: []KindRef{refPods}, Limits: Limits{ClusterEntries: 1e6, ClusterBytes: 1 << 40, FleetEntries: 1e6, FleetBytes: 1 << 40, PodPercent: 100}})
	defer ix.Stop()
	c := ix.coordFor("a")
	c.store.begin(1)
	visitOnce(t, c, 1, bigObs(50_000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := ix.Search(ctx, "web", 50); err != context.Canceled {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	// and a full search over 50,000 entries is quick
	start = time.Now()
	r, _ := ix.Search(context.Background(), "web-49999", 50)
	t.Logf("50,000 entries searched in %v", time.Since(start))
	if len(r.Hits) == 0 || r.Hits[0].Name != fmt.Sprintf("web-%d-7d9f8c6b5-x2k4p", 49999) {
		t.Fatalf("hits %s", names(r.Hits))
	}
}
