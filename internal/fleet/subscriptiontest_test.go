package fleet

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/moomora/klyx/internal/config"
)

// Settings S3b §7: a member's Loki subscription Test reads the label names
// of its streams over the last hour under the scope it resolves to, never
// a line; the member and the terms are pinned from admission to
// settlement, and a move of either discards the result.

func subscriptionTestRig(t *testing.T, onHostPage func(n int)) (*srcRig, *Registry, context.CancelFunc, *int) {
	t.Helper()
	r := newSrcRig(t, []config.SourceConfig{lokiInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	lctx, end := context.WithCancel(context.Background())
	t.Cleanup(end)
	released := 0
	var mu sync.Mutex
	pages := 0
	r.b.leaser = func(host, what string) (Conn, context.Context, func(), error) {
		if host == "dev" {
			return r.conns["dev"], lctx, func() { released++ }, nil
		}
		mu.Lock()
		pages++
		n := pages
		mu.Unlock()
		if onHostPage != nil {
			onHostPage(n)
		}
		return r.host, context.Background(), func() {}, nil
	}
	return r, &Registry{retired: map[ConnID]bool{}, alerts: r.b}, end, &released
}

func TestALokiSubscriptionsTestReadsItsMembersScope(t *testing.T) {
	r, reg, _, released := subscriptionTestRig(t, nil)
	sub, _ := r.sources.SubscriptionOf(config.EndpointLoki, "dev")
	got, err := reg.TestLokiSubscription(context.Background(), "dev", sub.InstRev, sub.SubRev)
	if err != nil || got.Version != "3.1.0" || !got.Data || got.Sentence != "from lgtm-we-logs, hosted by prd, tenant ops, filtered by k8s_cluster_name=dev" || *released != 1 {
		t.Fatalf("%+v %v, released %d", got, err, *released)
	}
	queries, tenants, _ := r.fake.sent()
	r.fake.mu.Lock()
	paths := append([]string(nil), r.fake.paths...)
	r.fake.mu.Unlock()
	if len(paths) != 2 || !strings.HasSuffix(paths[1], "/loki/api/v1/labels") || queries[1] != `{k8s_cluster_name="dev"}` || tenants[1] != "ops" {
		t.Fatalf("sent %v %v %v", paths, queries, tenants)
	}
	for _, p := range paths {
		if strings.Contains(p, "query_range") {
			t.Fatalf("a line was read: %s", p)
		}
	}
	if _, err := reg.TestLokiSubscription(context.Background(), "dev", sub.InstRev, sub.SubRev+1); err == nil || !strings.Contains(err.Error(), "changed since it was shown") {
		t.Fatalf("a stale revision: %v", err)
	}
	if _, err := reg.TestLokiSubscription(context.Background(), "prd", 0, 0); err == nil || !strings.Contains(err.Error(), "has no Loki subscription") {
		t.Fatalf("no subscription: %v", err)
	}
}

// A move of the terms during the Test, or the member's reconnection,
// discards what it read; the member's lease is released once.
func TestALokiSubscriptionsTestDiscardsAMove(t *testing.T) {
	for _, move := range []string{"the terms", "the member"} {
		t.Run(move, func(t *testing.T) {
			var r *srcRig
			var end context.CancelFunc
			r, reg, end, released := subscriptionTestRig(t, func(n int) {
				if n != 2 { // the labels page, after the build info
					return
				}
				if move == "the terms" {
					r.sources.Denied("lgtm-we-logs")
				} else {
					end()
				}
			})
			sub, _ := r.sources.SubscriptionOf(config.EndpointLoki, "dev")
			got, err := reg.TestLokiSubscription(context.Background(), "dev", sub.InstRev, sub.SubRev)
			want := map[string]string{"the terms": "changed during the test", "the member": "reconnected or removed during the test"}[move]
			if err == nil || !strings.Contains(err.Error(), want) || got.Version != "" || *released != 1 {
				t.Fatalf("%+v %v, released %d", got, err, *released)
			}
		})
	}
}
