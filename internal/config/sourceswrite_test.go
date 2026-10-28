package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Settings S3a §5: an Observability plan's edits build one disk candidate
// - the sources block and each cluster's alertmanager block - which loads
// strictly, changes only those blocks, and is written once against the
// planned file version.

func editSources(t *testing.T, doc string, e SourcesEdit) (string, error) {
	t.Helper()
	path := writeFleet(t, doc)
	v, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EditSourcesAt(path, v, e)
	return readFleet(t, path), err
}

const sourcesDoc = `# my fleet
clusters:
  # the prd cluster hosts the shared Alertmanager
  - name: prd # inline
    context: prd-ctx
    group: web
  - name: dev
    context: dev-ctx   # where it runs
    alertmanager:
      endpoint: https://am.example # its own
      match:
        cluster: dev
    tags:
      team: core
ai:
  provider: off
`

func lgtmSource() SourceConfig {
	return SourceConfig{ID: "lgtm-we", Kind: SourceKindAlertmanager, Name: "LGTM west", Host: "prd",
		ServiceRef: &MetricsServiceRef{Namespace: "monitoring", Name: "am", Port: "web"}, MemberLabel: "cluster",
		Serves: []ServeConfig{{Group: "web"}, {Cluster: "dev"}}}
}

// amBlock names a cluster's Alertmanager block.
func amBlock(cluster string) BlockRef {
	return BlockRef{Cluster: cluster, Kind: EndpointAlertmanager}
}

func TestASourcesPlanWritesItsBlocksOnly(t *testing.T) {
	sources := []SourceConfig{lgtmSource()}
	got, err := editSources(t, sourcesDoc, SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{
		amBlock("dev"): {Source: "lgtm-we", Match: map[string]string{"team": "core"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `# my fleet
clusters:
  # the prd cluster hosts the shared Alertmanager
  - name: prd # inline
    context: prd-ctx
    group: web
  - name: dev
    context: dev-ctx   # where it runs
    alertmanager:
      source: lgtm-we
      match:
        team: core
    tags:
      team: core
ai:
  provider: off
sources:
  - id: lgtm-we
    kind: alertmanager
    name: LGTM west
    host: prd
    serviceRef:
      namespace: monitoring
      name: am
      port: web
    memberLabel: cluster
    serves:
      - group: web
      - cluster: dev
`
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	cfg, err := Load(writeFleet(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := ResolveAlertmanager(cfg.Sources, cfg.Clusters); r["dev"].Mode != AMSubscribed || r["prd"].Mode != AMSubscribed {
		t.Fatalf("resolution: %+v", r)
	}
}

// An existing sources block is replaced; an empty list removes it, and a
// block removed takes the cluster back to resolution.
func TestASourcesPlanReplacesAndRemoves(t *testing.T) {
	sources := []SourceConfig{lgtmSource()}
	written, err := editSources(t, sourcesDoc, SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{amBlock("dev"): {Off: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(written, "    alertmanager:\n      off: true\n    tags:") {
		t.Fatalf("off:\n%s", written)
	}
	none := []SourceConfig{}
	got, err := editSources(t, written, SourcesEdit{Sources: &none, Blocks: map[BlockRef]*EndpointConfig{amBlock("dev"): nil}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "sources:") || strings.Contains(got, "alertmanager") || !strings.Contains(got, "    context: dev-ctx   # where it runs\n    tags:") {
		t.Fatalf("removed:\n%s", got)
	}
}

// A cluster with no block gets one after its content, never inside a
// nested block; line endings are kept.
func TestASubscriptionBlockIsAddedAfterTheEntry(t *testing.T) {
	sources := []SourceConfig{lgtmSource()}
	doc := strings.ReplaceAll(sourcesDoc, "\n", "\r\n")
	got, err := editSources(t, doc, SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{
		amBlock("prd"): {Source: "lgtm-we", Scope: ScopeTenant, Tenant: "ops"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "    group: web\r\n    alertmanager:\r\n      source: lgtm-we\r\n      scope: tenant\r\n      tenant: ops\r\n  - name: dev") {
		t.Fatalf("added:\n%q", got)
	}
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Fatal("a line ending changed")
	}
}

// What the writer will not do: a block it cannot edit line by line, a
// legacy block written through Settings, and a candidate that does not
// validate. Nothing is written.
func TestASourcesPlanRefusesWhatItCannotWrite(t *testing.T) {
	sources := []SourceConfig{lgtmSource()}
	dash := strings.Replace(sourcesDoc, "  - name: dev\n    context: dev-ctx   # where it runs\n    alertmanager:", "  - alertmanager:", 1)
	dash = strings.Replace(dash, "    tags:\n      team: core\n", "    name: dev\n    context: dev-ctx\n", 1)
	for name, tc := range map[string]struct {
		doc  string
		edit SourcesEdit
		want string
	}{
		"a block on the dash's line": {dash, SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{amBlock("dev"): {Source: "lgtm-we"}}}, "starts on the list dash's line"},
		"a legacy block":             {sourcesDoc, SourcesEdit{Blocks: map[BlockRef]*EndpointConfig{amBlock("dev"): {Endpoint: "https://am2.example"}}}, "only a subscription block"},
		"an unknown installation":    {sourcesDoc, SourcesEdit{Blocks: map[BlockRef]*EndpointConfig{amBlock("dev"): {Source: "nope"}}}, "failed validation"},
		"an unknown host":            {sourcesDoc, SourcesEdit{Sources: &[]SourceConfig{{ID: "x", Kind: SourceKindAlertmanager, Host: "nope", Discover: true, Serves: []ServeConfig{{Cluster: "dev"}}}}}, "failed validation"},
		"an unknown cluster":         {sourcesDoc, SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{amBlock("lab"): {Source: "lgtm-we"}}}, "not in fleet.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := editSources(t, tc.doc, tc.edit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v", err)
			}
			if got != tc.doc {
				t.Fatal("a refused plan was written")
			}
		})
	}
}

func TestASourcesPlanTakesOnlyItsPlannedVersion(t *testing.T) {
	path := writeFleet(t, sourcesDoc)
	sources := []SourceConfig{lgtmSource()}
	if _, err := EditSourcesAt(path, "", SourcesEdit{Sources: &sources}); err == nil {
		t.Fatal("a write without a planned version")
	}
	v, _ := FileVersion(path)
	if err := os.WriteFile(path, []byte(sourcesDoc+"# a hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EditSourcesAt(path, v, SourcesEdit{Sources: &sources}); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("a hand edit since the plan: %v", err)
	}
	v, _ = FileVersion(path)
	nv, err := EditSourcesAt(path, v, SourcesEdit{Sources: &sources})
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := FileVersion(path); now != nv {
		t.Fatal("the version returned is not the file's")
	}
}

// The decoded check behind the builders: a candidate that changes anything
// but the plan's blocks is refused.
func TestOnlyThePlansBlocksMayChange(t *testing.T) {
	sources := []SourceConfig{lgtmSource()}
	e := SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{amBlock("dev"): {Source: "lgtm-we"}}}
	good, _, err := BuildSourcesCandidate([]byte(sourcesDoc), e)
	if err != nil {
		t.Fatal(err)
	}
	if err := onlySourcesChanged([]byte(sourcesDoc), good, e); err != nil {
		t.Fatalf("the plan's own candidate: %v", err)
	}
	for name, bad := range map[string]string{
		"another field":   strings.Replace(string(good), "group: web", "group: api", 1),
		"another block":   strings.Replace(string(good), "    context: prd-ctx\n", "    context: prd-ctx\n    alertmanager: {off: true}\n", 1),
		"another source":  strings.Replace(string(good), "memberLabel: cluster", "memberLabel: team", 1),
		"a block missing": strings.Replace(string(good), "    alertmanager:\n      source: lgtm-we\n", "", 1),
	} {
		if bad == string(good) {
			t.Fatalf("%s: fixture unchanged", name)
		}
		if err := onlySourcesChanged([]byte(sourcesDoc), []byte(bad), e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Settings S3b §5: a plan writes a cluster's loki or metrics block as it
// writes its alertmanager block; a metrics subscription replaces a legacy
// block whole, its token with it, and the other kinds' blocks stay.
func TestASourcesPlanWritesEveryKindsBlock(t *testing.T) {
	doc := strings.Replace(sourcesDoc, "    tags:\n      team: core\n", "    metrics:\n      endpoint: https://mimir.example\n      token: S3B-LEGACY-TOKEN\n    tags:\n      team: core\n", 1)
	logs := SourceConfig{ID: "lgtm-we-logs", Kind: SourceKindLoki, Host: "prd", ServiceRef: &MetricsServiceRef{Namespace: "monitoring", Name: "loki-gateway", Port: "http"},
		MemberLabel: "k8s_cluster_name"}
	met := SourceConfig{ID: "lgtm-we-metrics", Kind: SourceKindMetrics, Host: "prd", Discover: true, MemberLabel: "cluster"}
	sources := []SourceConfig{logs, met}
	got, err := editSources(t, doc, SourcesEdit{Sources: &sources, Blocks: map[BlockRef]*EndpointConfig{
		{Cluster: "dev", Kind: EndpointLoki}:    {Source: "lgtm-we-logs", Scope: ScopeTenant},
		{Cluster: "dev", Kind: EndpointMetrics}: {Source: "lgtm-we-metrics", Match: map[string]string{"cluster": "dev-eu"}},
		{Cluster: "prd", Kind: EndpointLoki}:    {Off: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"    group: web\n    loki:\n      off: true\n",
		"    metrics:\n      source: lgtm-we-metrics\n      match:\n        cluster: dev-eu\n",
		"    loki:\n      source: lgtm-we-logs\n      scope: tenant\n",
		"    alertmanager:\n      endpoint: https://am.example # its own\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "S3B-LEGACY-TOKEN") || strings.Contains(got, "mimir.example") {
		t.Fatalf("the legacy metrics block stayed:\n%s", got)
	}
	cfg, err := ParseFleet([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	if m := cfg.Clusters[1].Metrics; m == nil || m.Source != "lgtm-we-metrics" || m.Token != "" {
		t.Fatalf("dev's metrics: %+v", m)
	}
	// removed: the block goes, the others stay
	none := []SourceConfig{logs, met}
	again, err := editSources(t, got, SourcesEdit{Sources: &none, Blocks: map[BlockRef]*EndpointConfig{{Cluster: "dev", Kind: EndpointLoki}: nil}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again, "source: lgtm-we-logs") || !strings.Contains(again, "source: lgtm-we-metrics") || !strings.Contains(again, "    loki:\n      off: true\n") {
		t.Fatalf("removal:\n%s", again)
	}
	if _, err := editSources(t, sourcesDoc, SourcesEdit{Blocks: map[BlockRef]*EndpointConfig{{Cluster: "dev", Kind: "traces"}: {Off: true}}}); err == nil || !strings.Contains(err.Error(), "is not a kind of observability block") {
		t.Fatalf("an unknown kind: %v", err)
	}
}
