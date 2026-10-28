package appbridge

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/config"
)

// Settings S3b-1 in the plans and the page: a running connection cannot
// follow its Loki or metrics to another source yet, and Observability's
// plans, page and credentials are Alertmanager's only in this stage.

func lokiOn(id, host, group string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindLoki, Host: host,
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "loki-gateway", Port: "http"}, MemberLabel: "k8s_cluster_name", Serves: []config.ServeConfig{{Group: group}}}
}

func metricsOn(id, host, group string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindMetrics, Host: host, Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: group}}}
}

func TestAPlacementThatWouldMoveLogsOrMetricsIsRefused(t *testing.T) {
	r := newFleetRig(t, true)
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "prd", "web"), metricsOn("lgtm-we-metrics", "prd", "web")}
	r.mu.Unlock()
	d := devDraft()
	d.Group = "web"
	p := r.cs.PlanClusterEdit("dev", d)
	if !strings.Contains(p.Refused, "dev's metrics would come from the installation lgtm-we-metrics instead of discovery in the cluster") || !strings.Contains(p.Refused, "until it reads through installations") {
		t.Fatalf("metrics moved: %+v", p)
	}
	// the control: an installation of another group moves nothing, and the
	// alerts' move is listed as before
	r.mu.Lock()
	r.sources = []config.SourceConfig{servesGroup("lgtm-we", "prd", "web"), metricsOn("lgtm-ne-metrics", "prd", "elsewhere"), lokiOn("lgtm-ne-logs", "prd", "elsewhere")}
	r.mu.Unlock()
	p = r.cs.PlanClusterEdit("dev", d)
	if p.Refused != "" || !hasChange(p, "fleet.alerts", "discovery in the cluster", "the installation lgtm-we") {
		t.Fatalf("control: %+v", p)
	}
	// a Loki installation serving the new group: refused too
	r.mu.Lock()
	r.sources = []config.SourceConfig{lokiOn("lgtm-we-logs", "prd", "web")}
	r.mu.Unlock()
	if p := r.cs.PlanClusterEdit("dev", d); !strings.Contains(p.Refused, "dev's log history would come from the installation lgtm-we-logs instead of no log history (Loki is never discovered)") || strings.Contains(p.Refused, "discovery in the cluster") {
		t.Fatalf("logs moved: %+v", p)
	}
}

func TestObservabilityIsAlertmanagersOnlyInThisStage(t *testing.T) {
	am := hostedOnPrd("shared", config.ServeConfig{Cluster: "dev"})
	logs := lokiOn("logs", "prd", "nowhere")
	r := newSourcesRig(t, am, logs)
	st := r.svc.GetSources()
	if len(st.Installations) != 1 || st.Installations[0].ID != "shared" || st.FileDiffers {
		t.Fatalf("state: %+v", st)
	}
	for _, op := range []string{installEdit, installRemove} {
		d := sourceDraftOf(logs)
		p := r.svc.PlanSources(SourcesDraftDTO{Installation: &InstallationEditDTO{Op: op, Source: d}})
		if !strings.Contains(p.Refused, "logs is a loki installation") {
			t.Fatalf("%s: %+v", op, p)
		}
	}
	// a hand edit of the Loki installation alone: not offered, and the
	// file's Alertmanager values, adopted, leave it as it is in force
	raw := r.file()
	edited := strings.Replace(raw, "loki-gateway", "loki-read", 1)
	if edited == raw {
		t.Fatal("fixture: no loki-gateway in the file")
	}
	if err := os.WriteFile(r.path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := r.svc.GetSources(); st.FileDiffers {
		t.Fatal("a Loki installation's hand edit is offered as the alert sources'")
	}
	if p := r.svc.PlanSourcesFromFile(); !strings.Contains(p.Refused, "the ones in force") {
		t.Fatalf("from file: %+v", p)
	}
	edited2 := strings.Replace(edited, "id: shared", "id: shared\n    tenant: ops", 1)
	if edited2 == edited {
		t.Fatal("fixture: no shared in the file")
	}
	if err := os.WriteFile(r.path, []byte(edited2), 0o600); err != nil {
		t.Fatal(err)
	}
	p := r.svc.PlanSourcesFromFile()
	if res := r.applySources(p); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	for _, s := range r.active.Sources() {
		if s.ID == "logs" && !reflect.DeepEqual(s, logs) {
			t.Fatalf("the Loki installation in force changed: %+v", s)
		}
		if s.ID == "shared" && s.Tenant != "ops" {
			t.Fatalf("the file's Alertmanager installation was not adopted: %+v", s)
		}
	}
}

func TestALokiInstallationsCredentialWaits(t *testing.T) {
	logs := lokiOn("logs", "", "nowhere")
	logs.Host, logs.ServiceRef, logs.Endpoint, logs.Auth = "", nil, "https://loki.example", &config.EndpointAuth{Type: "bearer"}
	es, a, _, _ := sourceCredRig(t, logs)
	if res := es.SetSourceCredential("logs", revisionOf(a, "logs"), "S3B-SECRET"); res.OK || !strings.Contains(res.Message, "logs is a loki installation, which this version of Klyx does not read yet") {
		t.Fatalf("%+v", res)
	}
	if res := es.TestSource("logs", revisionOf(a, "logs")); res.OK || !strings.Contains(res.Message, "does not read yet") {
		t.Fatalf("test: %+v", res)
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
