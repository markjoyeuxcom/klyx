package appbridge

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
)

// Settings S3b in the plans and the page: a running connection follows its
// Loki and metrics to another source, each move listed (S3b-2b, §3.5), and
// Observability's plans and credentials are every kind's (S3b-4).

func lokiOn(id, host, group string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindLoki, Host: host,
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "loki-gateway", Port: "http"}, MemberLabel: "k8s_cluster_name", Serves: []config.ServeConfig{{Group: group}}}
}

func metricsOn(id, host, group string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindMetrics, Host: host, Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: group}}}
}

func TestAPlacementThatMovesLogsOrMetricsListsEach(t *testing.T) {
	r := newFleetRig(t, true)
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "prd", "web"), metricsOn("lgtm-we-metrics", "prd", "web")}
	r.mu.Unlock()
	d := devDraft()
	d.Group = "web"
	p := r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" || !hasChange(p, "fleet.metrics", "discovery in the cluster", "the installation lgtm-we-metrics") ||
		!hasChange(p, "fleet.alerts", "discovery in the cluster", "the installation lgtm-we") {
		t.Fatalf("metrics moved: %+v", p)
	}
	// the control: an installation of another group moves nothing
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "prd", "web"), metricsOn("lgtm-ne-metrics", "prd", "elsewhere"), lokiOn("lgtm-ne-logs", "prd", "elsewhere")}
	r.mu.Unlock()
	p = r.cs.PlanClusterEdit("dev", d)
	for _, c := range p.Changes {
		if c.ID == "fleet.metrics" || c.ID == "fleet.logs" {
			t.Fatalf("control: %+v", p)
		}
	}
	if p.Refused != "" || !hasChange(p, "fleet.alerts", "discovery in the cluster", "the installation lgtm-we") {
		t.Fatalf("control: %+v", p)
	}
	// a Loki installation serving the new group: listed, from no log
	// history, never from discovery
	r.mu.Lock()
	r.sources = []config.SourceConfig{lokiOn("lgtm-we-logs", "prd", "web")}
	r.mu.Unlock()
	if p := r.cs.PlanClusterEdit("dev", d); p.Refused != "" || !hasChange(p, "fleet.logs", "no log history (Loki is never discovered)", "the installation lgtm-we-logs") {
		t.Fatalf("logs moved: %+v", p)
	}
}

// Settings S3b §5: a panel plans its own kind. The alerts panel neither
// edits nor removes a Loki installation, nor offers its hand edit; each
// kind's "Use the file's values" adopts that kind only.
func TestAPanelPlansItsOwnKind(t *testing.T) {
	am := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
	logs := lokiOn("logs", "prd", "nowhere")
	r := newSourcesRig(t, am, logs)
	if st := r.svc.GetSources(""); st.Kind != config.EndpointAlertmanager || len(st.Installations) != 1 || st.Installations[0].ID != "shared" || st.FileDiffers {
		t.Fatalf("alerts: %+v", st)
	}
	if st := r.svc.GetSources(config.EndpointLoki); st.Kind != config.EndpointLoki || len(st.Installations) != 1 || st.Installations[0].ID != "logs" || st.FileDiffers {
		t.Fatalf("logs: %+v", st)
	}
	for op, want := range map[string]string{installEdit: "logs is a loki installation: its kind does not change", installRemove: "logs is a loki installation, removed from its own panel"} {
		if p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: op, Source: sourceDraftOf(logs)}}); !strings.Contains(p.Refused, want) {
			t.Fatalf("%s: %+v", op, p)
		}
	}
	// a hand edit of the Loki installation: the logs panel's, never the
	// alerts panel's
	raw := r.file()
	edited := strings.Replace(raw, "loki-gateway", "loki-read", 1)
	if edited == raw {
		t.Fatal("fixture: no loki-gateway in the file")
	}
	if err := os.WriteFile(r.path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := r.svc.GetSources(""); st.FileDiffers {
		t.Fatal("a Loki installation's hand edit is offered as the alert sources'")
	}
	if p := r.svc.PlanSourcesFromFile(""); !strings.Contains(p.Refused, "alerts sources are the ones in force") {
		t.Fatalf("alerts from file: %+v", p)
	}
	if st := r.svc.GetSources(config.EndpointLoki); !st.FileDiffers {
		t.Fatal("the logs panel does not offer its hand edit")
	}
	// the alerts panel adopts the Alertmanager installation's own edit and
	// leaves the Loki one as it is in force
	edited2 := strings.Replace(edited, "id: shared", "id: shared\n    tenant: ops", 1)
	if edited2 == edited {
		t.Fatal("fixture: no shared in the file")
	}
	if err := os.WriteFile(r.path, []byte(edited2), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := r.applySources(r.svc.PlanSourcesFromFile("")); !res.OK {
		t.Fatalf("alerts apply: %+v", res)
	}
	for _, s := range r.active.Sources() {
		if s.ID == "logs" && !reflect.DeepEqual(s, logs) {
			t.Fatalf("the Loki installation in force changed: %+v", s)
		}
		if s.ID == "shared" && s.Tenant != "ops" {
			t.Fatalf("the file's Alertmanager installation was not adopted: %+v", s)
		}
	}
	// the logs panel adopts its own, and nothing of the alerts'
	p := r.svc.PlanSourcesFromFile(config.EndpointLoki)
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	for _, c := range p.Changes {
		if strings.HasPrefix(c.ID, "alerts.") || c.ID == "sources.shared" {
			t.Fatalf("the logs panel's plan moves alerts: %+v", c)
		}
	}
	if res := r.applySources(p); !res.OK {
		t.Fatalf("logs apply: %+v", res)
	}
	if s, _, _ := r.active.Installation("logs"); s.ServiceRef == nil || s.ServiceRef.Name != "loki-read" {
		t.Fatalf("the file's Loki installation was not adopted: %+v", s)
	}
	if st := r.svc.GetSources(config.EndpointLoki); st.FileDiffers {
		t.Fatal("the logs panel still differs after adopting")
	}
	// an unknown kind is refused everywhere
	if st := r.svc.GetSources("traces"); !strings.Contains(st.FileError, "is not a kind of source") {
		t.Fatalf("state: %+v", st)
	}
	if p := r.svc.PlanSourcesFromFile("traces"); !strings.Contains(p.Refused, "is not a kind of source") {
		t.Fatalf("from file: %+v", p)
	}
	if p := r.svc.PlanSources(SourcesDraftDTO{Kind: "traces", Subscriptions: []SubscriptionEditDTO{{Cluster: "dev"}}}); !strings.Contains(p.Refused, "is not a kind of source") {
		t.Fatalf("draft: %+v", p)
	}
}

// Settings S3b §5: a Loki installation added with a cluster's
// subscription. The plan says the block and the member's logs before and
// after, ends its log views and the work holding its logs, and nothing of
// its alerts; Apply writes the loki block and puts it in force.
func TestALokiPlanSubscribesAClusterAndEndsItsLogViewsOnly(t *testing.T) {
	r := newSourcesRig(t, hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"}))
	logs := lokiOn("lgtm-we-logs", "prd", "nowhere")
	p := r.svc.PlanSources(SourcesDraftDTO{Kind: config.EndpointLoki,
		Installation:  &InstallationEditDTO{Op: installAdd, Source: sourceDraftOf(logs)},
		Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: &SubscriptionDTO{Source: "lgtm-we-logs"}}},
	})
	if p.Refused != "" || !p.Writes {
		t.Fatalf("%+v", p)
	}
	if c, ok := change(p, "clusters.dev.loki"); !ok || c.From != "none: by name or group" || c.To != "subscribed to lgtm-we-logs" {
		t.Fatalf("block: %+v", p.Changes)
	}
	if c, ok := change(p, "logs.dev"); !ok || c.From != "none" || c.To != "from lgtm-we-logs, hosted by prd, filtered by k8s_cluster_name=dev" {
		t.Fatalf("dev's logs: %+v", p.Changes)
	}
	ends := map[string]string{}
	for _, w := range p.Ends {
		ends[w.ID] = w.What
	}
	if ends["logs:dev"] != "dev's log views start again under the new terms" || ends["conv:loki:dev"] == "" {
		t.Fatalf("ends: %+v", p.Ends)
	}
	for id := range ends {
		if strings.HasPrefix(id, "alerts:") || strings.Contains(id, "alertmanager") || strings.Contains(id, "metrics") {
			t.Fatalf("a logs plan ends %s: %+v", id, p.Ends)
		}
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if file := r.file(); !strings.Contains(file, "    loki:\n      source: lgtm-we-logs\n") || !strings.Contains(file, "  - id: lgtm-we-logs\n    kind: loki\n") {
		t.Fatalf("written:\n%s", file)
	}
	if run, _ := r.cs.runningEntry("dev"); run.Loki == nil || run.Loki.Source != "lgtm-we-logs" || run.Alertmanager != nil {
		t.Fatalf("dev's running blocks: %+v %+v", run.Loki, run.Alertmanager)
	}
	if sub, ok := r.active.SubscriptionOf(config.EndpointLoki, "dev"); !ok || sub.Source.ID != "lgtm-we-logs" {
		t.Fatalf("dev's logs are not subscribed in force: %+v", sub)
	}
	if sub, ok := r.active.Subscription("dev"); !ok || sub.Source.ID != "shared" {
		t.Fatalf("dev's alerts moved: %+v", sub)
	}
	st := r.svc.GetSources(config.EndpointLoki)
	if st.Sentence != "Logs: 1 cluster from 1 installation, 1 with none" || len(st.Installations) != 1 || !reflect.DeepEqual(st.Installations[0].Subscribers, []string{"dev"}) || st.FileDiffers {
		t.Fatalf("state: %+v", st)
	}
	// the installation edited from its panel: its subscriber's logs move,
	// nothing else
	d := sourceDraftOf(logs)
	d.Tenant = "stg-logs"
	p = r.svc.PlanSources(SourcesDraftDTO{Kind: config.EndpointLoki, Installation: &InstallationEditDTO{Op: installEdit, Source: d}})
	if c, ok := change(p, "logs.dev"); p.Refused != "" || !ok || c.To != "from lgtm-we-logs, hosted by prd, tenant stg-logs, filtered by k8s_cluster_name=dev" {
		t.Fatalf("edit: %+v", p)
	}
	for _, w := range p.Ends {
		if w.Cluster != "dev" || !strings.Contains(w.ID, "loki") && !strings.HasPrefix(w.ID, "logs:") {
			t.Fatalf("an edit of the logs installation ends %+v", w)
		}
	}
	// a subscription to another kind's installation is refused
	if p := r.svc.PlanSources(SourcesDraftDTO{Kind: config.EndpointLoki, Subscriptions: []SubscriptionEditDTO{{Cluster: "prd", Block: &SubscriptionDTO{Source: "shared"}}}}); !strings.Contains(p.Refused, "do not resolve") {
		t.Fatalf("a cross-kind subscription: %+v", p)
	}
}

// Settings S3b §5: a metrics subscription with its own match. The plan
// says the member's metrics before (discovery) and after, ends its metrics
// cache; Apply writes the metrics block and the running entry takes it.
func TestAMetricsPlanSubscribesAClusterWithItsMatch(t *testing.T) {
	r := newSourcesRig(t)
	met := metricsOn("lgtm-we-metrics", "prd", "nowhere")
	p := r.svc.PlanSources(SourcesDraftDTO{Kind: config.EndpointMetrics,
		Installation:  &InstallationEditDTO{Op: installAdd, Source: sourceDraftOf(met)},
		Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: &SubscriptionDTO{Source: "lgtm-we-metrics", Match: map[string]string{"cluster": "dev-eu"}}}},
	})
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	if c, ok := change(p, "clusters.dev.metrics"); !ok || c.From != "none: by name, group or discovery" || c.To != "subscribed to lgtm-we-metrics, match cluster=dev-eu" {
		t.Fatalf("block: %+v", p.Changes)
	}
	if c, ok := change(p, "metrics.dev"); !ok || c.From != "discovered in this cluster, unfiltered: everything there is read as this cluster's" || c.To != "from lgtm-we-metrics, hosted by prd, filtered by cluster=dev-eu" {
		t.Fatalf("dev's metrics: %+v", p.Changes)
	}
	found := false
	for _, w := range p.Ends {
		if w.ID == "metrics:dev" && w.What == "dev's metrics are read again under the new terms: its cached readings are dropped" {
			found = true
		}
		if strings.HasPrefix(w.ID, "alerts:") || strings.HasPrefix(w.ID, "logs:") {
			t.Fatalf("a metrics plan ends %s", w.ID)
		}
	}
	if !found {
		t.Fatalf("ends: %+v", p.Ends)
	}
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if file := r.file(); !strings.Contains(file, "    metrics:\n      source: lgtm-we-metrics\n      match:\n        cluster: dev-eu\n") {
		t.Fatalf("written:\n%s", file)
	}
	if run, _ := r.cs.runningEntry("dev"); run.Metrics == nil || run.Metrics.Source != "lgtm-we-metrics" || run.Metrics.Match["cluster"] != "dev-eu" {
		t.Fatalf("dev's running metrics: %+v", run.Metrics)
	}
	// the subscription removed: dev reads by discovery again
	p = r.svc.PlanSources(SourcesDraftDTO{Kind: config.EndpointMetrics, Subscriptions: []SubscriptionEditDTO{{Cluster: "dev", Block: nil}}})
	if res := r.applySources(p); !res.OK {
		t.Fatal(res.Error)
	}
	if run, _ := r.cs.runningEntry("dev"); run.Metrics != nil {
		t.Fatalf("dev's running metrics stayed: %+v", run.Metrics)
	}
	if strings.Contains(r.file(), "metrics:") {
		t.Fatalf("the block stayed:\n%s", r.file())
	}
}

// A legacy metrics block is the file's: "Use the file's values" never
// adopts one, though the file's differs from what runs.
func TestAFilesLegacyMetricsBlockIsNotAdopted(t *testing.T) {
	r := newSourcesRig(t)
	raw := r.file()
	edited := strings.Replace(raw, "  - name: dev\n    context: dev-ctx\n", "  - name: dev\n    context: dev-ctx\n    metrics:\n      endpoint: https://mimir.example\n      match:\n        cluster: dev\n", 1)
	if edited == raw {
		t.Fatal("fixture: no dev entry")
	}
	if err := os.WriteFile(r.path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := r.svc.GetSources(config.EndpointMetrics); st.FileDiffers || st.FileError != "" {
		t.Fatalf("a legacy block is offered: %+v", st)
	}
	if p := r.svc.PlanSourcesFromFile(config.EndpointMetrics); !strings.Contains(p.Refused, "metrics sources are the ones in force") {
		t.Fatalf("%+v", p)
	}
}

func TestAnOffRowSaysItsKind(t *testing.T) {
	for kind, want := range map[string]string{
		config.EndpointAlertmanager: "off in fleet.yaml: no alerts are read for this cluster",
		config.EndpointLoki:         "off in fleet.yaml: no log history is read for this cluster",
		config.EndpointMetrics:      "off in fleet.yaml: no metrics are read for this cluster",
	} {
		if got := endpointSource(kind, config.EndpointConfig{Off: true}); got != want {
			t.Errorf("%s: %q", kind, got)
		}
	}
}

// Settings S3b §3.4, §5: each kind's words. A cluster's own Loki reads
// every stream as its own; a cluster without Loki has none, never
// discovery; a running metrics block is the page's row.
func TestEachKindSaysItsWords(t *testing.T) {
	svc := &config.MetricsServiceRef{Namespace: "monitoring", Name: "loki-gateway", Port: "http"}
	for _, tc := range []struct {
		kind, mode string
		block      *config.EndpointConfig
		want       string
	}{
		{config.EndpointLoki, config.AMService, &config.EndpointConfig{ServiceRef: svc}, "from the Service monitoring/loki-gateway:http on this cluster, unfiltered: every stream there is read as this cluster's"},
		{config.EndpointLoki, config.AMNone, nil, "none"},
		{config.EndpointMetrics, config.AMDirect, &config.EndpointConfig{Endpoint: "https://mimir.example/prometheus", Match: map[string]string{"cluster": "dev"}}, "from its own endpoint at https://mimir.example:443, filtered by cluster=dev"},
		{config.EndpointMetrics, config.AMDiscovered, nil, "discovered in this cluster, unfiltered: everything there is read as this cluster's"},
	} {
		if got := scopeSentence(tc.kind, config.AMResolution{Mode: tc.mode}, config.SourceConfig{}, tc.block); got != tc.want {
			t.Errorf("%s %s: %q", tc.kind, tc.mode, got)
		}
	}
	for _, tc := range []struct {
		kind  string
		block *config.EndpointConfig
		want  string
	}{
		{config.EndpointLoki, nil, "none: by name or group"},
		{config.EndpointLoki, &config.EndpointConfig{ServiceRef: svc}, "its own Service"},
		{config.EndpointMetrics, nil, "none: by name, group or discovery"},
		{config.EndpointMetrics, &config.EndpointConfig{ServiceRef: svc}, "its own Service or discovery"},
		{config.EndpointMetrics, &config.EndpointConfig{Endpoint: "https://mimir.example"}, "its own endpoint"},
	} {
		if got := blockWords(tc.kind, tc.block); got != tc.want {
			t.Errorf("%s %+v: %q", tc.kind, tc.block, got)
		}
	}
	res := map[string]config.AMResolution{"prd": {Mode: config.AMNone}, "dev": {Mode: config.AMService}}
	if got := sourcesSentence(config.EndpointLoki, res, []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}); got != "Logs: 1 with their own endpoint or Service, 1 with none" {
		t.Errorf("%q", got)
	}
	r := newSourcesRig(t)
	SetRunningEndpoint(r.cs, "dev", config.EndpointMetrics, &config.MetricsConfig{Endpoint: "https://mimir.example", Match: map[string]string{"cluster": "dev"}}, nil)
	for _, row := range r.svc.GetSources(config.EndpointMetrics).Subscriptions {
		if row.Cluster == "dev" && (!row.Legacy || row.Sentence != "from its own endpoint at https://mimir.example:443, filtered by cluster=dev") {
			t.Fatalf("dev's row: %+v", row)
		}
	}
}

// A running Explain holding alerts is named in an Apply that moves its
// cluster's alerts, never in one that moves only its logs or metrics, or
// another cluster's alerts (S3b §13).
func TestAnExplainIsNamedForTheKindItHolds(t *testing.T) {
	ctx, _, done := withLineages(context.Background(), "dev", func() error { return nil })
	defer done()
	if err := recordLineage(ctx, alertmanager.Lineage{Mode: config.AMSubscribed, Installation: "shared", Identity: "read"}); err != nil {
		t.Fatal(err)
	}
	if w := explainWork(map[string][]string{config.EndpointAlertmanager: {"dev"}}); len(w) != 1 || !strings.Contains(w[0].What, "holding alerts read under the old terms") {
		t.Fatalf("%+v", w)
	}
	for _, moved := range []map[string][]string{{config.EndpointLoki: {"dev"}, config.EndpointMetrics: {"dev"}}, {config.EndpointAlertmanager: {"prd"}}} {
		if w := explainWork(moved); len(w) != 0 {
			t.Fatalf("%v: %+v", moved, w)
		}
	}
}

// A Loki installation's credential state is its kind's: one revoked in
// memory, its keychain delete having failed, is not said to be stored.
func TestALokiCredentialStateIsItsKinds(t *testing.T) {
	logs := lokiOn("logs", "", "nowhere")
	logs.Host, logs.ServiceRef, logs.Endpoint, logs.Auth = "", nil, "https://loki.example", &config.EndpointAuth{Type: "bearer"}
	r := newSourcesRig(t, logs)
	v, err := endpointcred.Record{V: 1, Origin: "https://loki.example:443", Type: "bearer", Secret: "S3B-SECRET"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.Set(endpointcred.SourceAccount("logs"), v); err != nil {
		t.Fatal(err)
	}
	if st := r.svc.GetSources(config.EndpointLoki); len(st.Installations) != 1 || st.Installations[0].Credential != "stored" {
		t.Fatalf("%+v", st.Installations)
	}
	r.revs.Revoke(endpointcred.SourceAccount("logs"), config.EndpointLoki)
	if st := r.svc.GetSources(config.EndpointLoki); st.Installations[0].Credential == "stored" {
		t.Fatalf("a revoked credential is stored: %+v", st.Installations)
	}
}
