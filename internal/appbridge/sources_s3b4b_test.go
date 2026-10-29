package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
)

// Settings S3b §6, §7: Detect for metrics proposes the one Service it
// found and names several; Loki is never detected; a member's Loki
// subscription Test is resolved by the backend from the revisions its row
// showed; and the migration offer is made for every kind, a legacy
// metrics token never carried.

func TestDetectForMetricsProposesTheOneService(t *testing.T) {
	r := newSourcesRig(t)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	cand := fleet.MetricsCandidate{ServiceCandidate: metrics.ServiceCandidate{Namespace: "monitoring", Name: "prometheus-operated", Port: "9090", Scheme: "http"}, ByName: true}
	r.svc.d.DetectMetrics = func(_ context.Context, cluster string, names []string) fleet.MetricsDetect {
		switch cluster {
		case "prd":
			return fleet.MetricsDetect{Cluster: "prd", Candidates: []fleet.MetricsCandidate{cand}, At: at, Sample: &metrics.MetadataSample{
				Start: at.Add(-time.Hour), End: at, Names: []string{"cluster", "job"}, Queried: []string{"cluster", "k8s_cluster_name", "cluster_name", "kubernetes_cluster", "job"},
				Members: map[string][]string{"cluster": {"dev", "prd"}}, Cut: []string{"job"}, NotQueried: 3}}
		default:
			other := cand
			other.Name, other.ByName, other.BySelector = "prometheus-server", false, "app.kubernetes.io/name=prometheus"
			return fleet.MetricsDetect{Cluster: cluster, Candidates: []fleet.MetricsCandidate{cand, other}}
		}
	}
	got := r.svc.Detect(DetectScopeDTO{Kind: config.EndpointMetrics, Cluster: "prd"})
	if got.Error != "" || len(got.Proposals) != 1 || got.Clusters[0].Outcome != "found" || got.Clusters[0].Detail != "Service monitoring/prometheus-operated:9090 (found by its name)" {
		t.Fatalf("%+v", got)
	}
	p := got.Proposals[0]
	if p.MemberLabel != "cluster" || strings.Join(p.MembersSeen, ",") != "dev,prd" || p.SampledAt != "2026-10-28T12:00:00Z" ||
		p.Sample != "label metadata for the hour to 2026-10-28T12:00:00Z: 2 label names, the values of 5 labels, at most 2,000 values and 1 MiB each" {
		t.Fatalf("%+v", p)
	}
	notes := strings.Join(p.Notes, " ")
	for _, want := range []string{metadataNote, metricsUnseenNote, "The values of job were cut", "The values of 3 more labels were not asked for"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q: %v", want, p.Notes)
		}
	}
	d := p.Draft
	if d.Kind != config.EndpointMetrics || d.Installation.Source.ID != "metrics-prd" || d.Installation.Source.ServiceRef.Name != "prometheus-operated" ||
		d.Installation.Source.MemberLabel != "cluster" || strings.Join(d.Installation.Source.ServesClusters, ",") != "dev,prd" {
		t.Fatalf("draft: %+v", d)
	}
	// the proposal is an ordinary metrics plan
	if plan := r.svc.PlanSources(d); plan.Refused != "" {
		t.Fatalf("the proposal does not plan: %s", plan.Refused)
	}
	// several Services: each named, none proposed
	got = r.svc.Detect(DetectScopeDTO{Kind: config.EndpointMetrics, Cluster: "dev"})
	if len(got.Proposals) != 0 || got.Clusters[0].Outcome != "ambiguous" || !strings.Contains(got.Clusters[0].Detail, "prometheus-server:9090 (found by the selector app.kubernetes.io/name=prometheus)") {
		t.Fatalf("several: %+v", got)
	}
}

func TestDetectForMetricsNeverTakesAnUnreadableDiscoveryAsAll(t *testing.T) {
	r := newSourcesRig(t)
	cand := fleet.MetricsCandidate{ServiceCandidate: metrics.ServiceCandidate{Namespace: "monitoring", Name: "prom", Port: "9090"}, BySelector: "app.kubernetes.io/name=prometheus"}
	for name, d := range map[string]fleet.MetricsDetect{
		"unreadable":    {Unreadable: []string{"the Service monitoring/prometheus-operated could not be read: forbidden"}, Candidates: []fleet.MetricsCandidate{cand}},
		"an error":      {Err: "prd is not connected"},
		"none":          {},
		"sample unread": {Candidates: []fleet.MetricsCandidate{cand}, Unread: "the label names could not be read: Prometheus answered HTTP 401"},
	} {
		d := d
		d.Cluster = "prd"
		r.svc.d.DetectMetrics = func(context.Context, string, []string) fleet.MetricsDetect { return d }
		got := r.svc.Detect(DetectScopeDTO{Kind: config.EndpointMetrics, Cluster: "prd"})
		row := got.Clusters[0]
		switch name {
		case "unreadable":
			if row.Outcome != "unreadable" || len(got.Proposals) != 0 || !strings.Contains(row.Detail, "found so far, which may not be all: Service monitoring/prom:9090") {
				t.Fatalf("%s: %+v", name, got)
			}
		case "an error":
			if row.Outcome != "unreadable" || row.Detail != "prd is not connected" || len(got.Proposals) != 0 {
				t.Fatalf("%s: %+v", name, got)
			}
		case "none":
			if row.Outcome != "none" || len(got.Proposals) != 0 {
				t.Fatalf("%s: %+v", name, got)
			}
		case "sample unread":
			if len(got.Proposals) != 1 || got.Proposals[0].MemberLabel != "" || !strings.HasPrefix(got.Proposals[0].Sample, "not read: the label names") ||
				!slices.Contains(got.Proposals[0].Notes, metricsUnseenNote) {
				t.Fatalf("%s: %+v", name, got)
			}
		}
	}
	if got := r.svc.Detect(DetectScopeDTO{Kind: config.EndpointLoki, Cluster: "prd"}); !strings.Contains(got.Error, "Loki is never discovered") {
		t.Fatalf("loki: %+v", got)
	}
	if got := r.svc.Detect(DetectScopeDTO{Kind: "traces", Cluster: "prd"}); !strings.Contains(got.Error, "is not a kind of source") {
		t.Fatalf("unknown: %+v", got)
	}
}

// A member's Loki subscription Test: its row shows the revisions, and the
// page sends those and the member's name only.
func TestALokiSubscriptionIsTestedFromItsRow(t *testing.T) {
	logs := lokiOn("lgtm-we-logs", "prd", "nowhere")
	logs.Serves = []config.ServeConfig{{Cluster: "dev"}}
	r := newSourcesRig(t, logs)
	var row SubscriptionRowDTO
	for _, s := range r.svc.GetSources(config.EndpointLoki).Subscriptions {
		if s.Cluster == "dev" {
			row = s
		}
	}
	sub, _ := r.active.SubscriptionOf(config.EndpointLoki, "dev")
	if row.Mode != config.AMSubscribed || row.InstRevision == "" || row.SubRevision == "" {
		t.Fatalf("row: %+v", row)
	}
	var asked []uint64
	r.svc.d.TestLoki = func(_ context.Context, cluster string, inst, s uint64) (fleet.SubscriptionTest, error) {
		asked = append(asked, inst, s)
		if cluster != "dev" {
			return fleet.SubscriptionTest{}, errors.New("prd's logs: S3B-DENIED (401)")
		}
		res := fleet.SubscriptionTest{Sentence: "from lgtm-we-logs, hosted by prd, filtered by k8s_cluster_name=dev"}
		res.Version, res.Data = "3.4.2", len(asked) == 2
		return res, nil
	}
	got := r.svc.TestSubscription("dev", config.EndpointLoki, row.InstRevision, row.SubRevision)
	if !got.OK || got.Message != "Available: Loki 3.4.2. dev's logs, read from lgtm-we-logs, hosted by prd, filtered by k8s_cluster_name=dev: streams answered in the last hour." ||
		asked[0] != sub.InstRev || asked[1] != sub.SubRev {
		t.Fatalf("%+v %v", got, asked)
	}
	if got := r.svc.TestSubscription("dev", config.EndpointLoki, row.InstRevision, row.SubRevision); !got.OK || !strings.Contains(got.Message, "no stream answered in the last hour") {
		t.Fatalf("no data: %+v", got)
	}
	if got := r.svc.TestSubscription("prd", config.EndpointLoki, "1", "1"); got.OK || !strings.HasPrefix(got.Message, "Unavailable: prd's logs") {
		t.Fatalf("each member's own: %+v", got)
	}
	if got := r.svc.TestSubscription("dev", config.EndpointLoki, "x", row.SubRevision); got.OK || !strings.Contains(got.Message, "changed since it was shown") {
		t.Fatalf("a revision that is not one: %+v", got)
	}
	for _, kind := range []string{config.EndpointMetrics, ""} {
		if got := r.svc.TestSubscription("dev", kind, row.InstRevision, row.SubRevision); got.OK || !strings.Contains(got.Message, "test this member's installation instead") {
			t.Fatalf("%q: %+v", kind, got)
		}
	}
	// a legacy row shows no revisions
	for _, s := range r.svc.GetSources("").Subscriptions {
		if s.InstRevision != "" || s.SubRevision != "" {
			t.Fatalf("an unsubscribed row: %+v", s)
		}
	}
}

// The offer for Loki and metrics: one direct URL on several clusters'
// blocks of the kind. A legacy metrics token is never carried: a token
// without auth is bearer auth's shape, and the offer says the
// installation needs its own credential.
func TestTheMigrationOfferIsEveryKinds(t *testing.T) {
	r := newSourcesRig(t)
	for _, c := range []string{"prd", "dev"} {
		SetRunningEndpoint(r.cs, c, config.EndpointLoki, nil, &config.EndpointConfig{Endpoint: "https://loki.example/", Tenant: c + "-logs", Match: map[string]string{"k8s_cluster_name": c}})
		SetRunningEndpoint(r.cs, c, config.EndpointMetrics, &config.MetricsConfig{Endpoint: "https://mimir.example/prometheus", Token: "S3B-TOKEN-" + c, Match: map[string]string{"cluster": c}}, nil)
	}
	if offers := r.svc.MigrationOffers(""); len(offers) != 0 {
		t.Fatalf("the alerts panel offers another kind's: %+v", offers)
	}
	logs := r.svc.MigrationOffers(config.EndpointLoki)
	if len(logs) != 1 || logs[0].Kind != config.EndpointLoki || logs[0].Draft.Kind != config.EndpointLoki || logs[0].Draft.Installation.Source.ID != "logs-loki" {
		t.Fatalf("%+v", logs)
	}
	if sc := logs[0].Scopes[0]; sc.Before != "from its own endpoint at https://loki.example:443, tenant prd-logs, filtered by k8s_cluster_name=prd" ||
		sc.After != "from logs-loki at https://loki.example:443, tenant prd-logs, filtered by k8s_cluster_name=prd" || sc.Change != "the same" || len(logs[0].Choices) != 0 {
		t.Fatalf("logs scope: %+v %v", sc, logs[0].Choices)
	}
	// a block without a match says so in its kind's words
	unmatched := []config.ClusterConfig{{Name: "prd", Loki: &config.EndpointConfig{Endpoint: "https://loki.example"}}, {Name: "dev", Loki: &config.EndpointConfig{Endpoint: "https://loki.example"}}}
	if o := r.svc.offerFor(config.EndpointLoki, "https://loki.example:443", "https://loki.example", unmatched, nil, r.cs.fleetClusters(), map[string]bool{}); o.Scopes[0].Before != "from its own endpoint at https://loki.example:443, unfiltered: every stream there is read as this cluster's" {
		t.Fatalf("unmatched: %+v", o.Scopes[0])
	}
	met := r.svc.MigrationOffers(config.EndpointMetrics)
	if len(met) != 1 {
		t.Fatalf("%+v", met)
	}
	o := met[0]
	raw, _ := json.Marshal(o)
	if strings.Contains(string(raw), "S3B-TOKEN") {
		t.Fatalf("a token in the offer: %s", raw)
	}
	if o.Draft.Installation.Source.AuthType != "bearer" || len(o.Conflicts) != 0 || !strings.Contains(o.Credential, "metrics tokens are not carried over") {
		t.Fatalf("%+v %q", o.Draft.Installation.Source, o.Credential)
	}
	// one tenant shared: the person chooses the member label, then the
	// draft applies, each cluster's legacy block, its token with it,
	// replaced
	if len(o.Choices) != 1 {
		t.Fatalf("the shared tenant's choice: %v", o.Choices)
	}
	o.Draft.Installation.Source.MemberLabel = "cluster"
	if res := r.applySources(r.svc.PlanSources(o.Draft)); !res.OK {
		t.Fatal(res.Error)
	}
	if run, _ := r.cs.runningEntry("dev"); run.Metrics == nil || run.Metrics.Source != o.Draft.Installation.Source.ID || run.Metrics.Token != "" {
		t.Fatalf("dev's running metrics: %+v", run.Metrics)
	}
	// a cluster with an explicit auth beside a token-only one: a conflict
	SetRunningEndpoint(r.cs, "prd", config.EndpointMetrics, &config.MetricsConfig{Endpoint: "https://thanos.example", Auth: &config.EndpointAuth{Type: "basic", Username: "k"}}, nil)
	SetRunningEndpoint(r.cs, "dev", config.EndpointMetrics, &config.MetricsConfig{Endpoint: "https://thanos.example", Token: "S3B-TOKEN"}, nil)
	if o := r.svc.MigrationOffers(config.EndpointMetrics); len(o) != 1 || !strings.Contains(o[0].Conflicts[0], "prd basic, dev bearer") || o[0].Draft.Installation.Source.AuthType != "" {
		t.Fatalf("%+v", o)
	}
}
