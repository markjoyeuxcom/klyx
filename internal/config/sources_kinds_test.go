package config

import (
	"reflect"
	"strings"
	"testing"
)

// Settings S3b §2: Loki and metrics installations and subscriptions,
// resolved per kind on S3a's model.

const kindsFleet = `clusters:
  - name: prd
    context: prd
    group: we
  - name: dev
    context: dev
    group: we
    metrics: {source: m, scope: tenant}
  - name: lab
    context: lab
    loki: {off: true}
sources:
  - id: am
    kind: alertmanager
    host: prd
    discover: true
    memberLabel: cluster
    serves: [{group: we}]
  - id: logs
    kind: loki
    host: prd
    serviceRef: {namespace: monitoring, name: loki-gateway, port: http}
    memberLabel: k8s_cluster_name
    serves: [{group: we}]
  - id: m
    kind: metrics
    host: prd
    discover: true
    tenant: t
`

func TestEachKindResolvesItsOwnInstallations(t *testing.T) {
	c, err := loadYAML(t, kindsFleet)
	if err != nil {
		t.Fatal(err) // an Alertmanager and a Loki installation serving one group is no conflict
	}
	am, _ := Resolve(EndpointAlertmanager, c.Sources, c.Clusters)
	logs, _ := Resolve(EndpointLoki, c.Sources, c.Clusters)
	met, _ := Resolve(EndpointMetrics, c.Sources, c.Clusters)
	if am["dev"].Source != "am" || am["lab"].Mode != AMDiscovered {
		t.Fatalf("alerts: %+v", am)
	}
	if logs["dev"].Source != "logs" || !reflect.DeepEqual(logs["dev"].Match, map[string]string{"k8s_cluster_name": "dev"}) || logs["lab"].Mode != AMOff {
		t.Fatalf("logs: %+v", logs)
	}
	if met["dev"].Source != "m" || !met["dev"].ScopeTenant || met["dev"].Tenant != "t" || met["prd"].Mode != AMDiscovered {
		t.Fatalf("metrics: %+v", met)
	}
	// Loki is never discovered: a cluster nothing serves, or whose block
	// names nowhere, has none
	c2, err := loadYAML(t, "clusters:\n  - name: a\n    context: a\n  - name: b\n    context: b\n    loki: {tenant: x}\n")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := Resolve(EndpointLoki, c2.Sources, c2.Clusters)
	if r["a"].Mode != AMNone || r["b"].Mode != AMNone {
		t.Fatalf("loki without a locator: %+v", r)
	}
}

func TestKindLoadErrors(t *testing.T) {
	for _, tc := range []struct{ old, new, want string }{
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {source: logs}\n", `cluster "dev": metrics.source "logs" is not a metrics installation: it is of kind "loki"`},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {source: m, match: {cluster: dev}}\n", `cluster "dev": metrics.match is read from a later version of Klyx`},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {endpoint: https://mimir.example, match: {cluster: dev}}\n", "metrics.match is read from a later version"},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {source: m, scope: tenant, token: abc}\n", "metrics.token belongs to the cluster's own endpoint, so it is not allowed beside source or off"},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {off: true, token: abc}\n", "metrics.token belongs to the cluster's own endpoint"},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {off: false}\n", "metrics.off is true, or left out"},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {source: \"\"}\n", "metrics.source names an installation in sources: it cannot be empty"},
		{"    metrics: {source: m, scope: tenant}\n", "    metrics: {source: m}\n", `the installation "m" has no memberLabel, so its subscription needs a scope: give the cluster's metrics block`},
		{"    serves: [{group: we}]\n  - id: m\n", "    serves: [{group: we}]\n  - id: logs2\n    kind: loki\n    endpoint: https://loki.example\n    serves: [{group: we}]\n  - id: m\n", `served by both "logs" and "logs2" for loki`},
	} {
		doc := strings.Replace(kindsFleet, tc.old, tc.new, 1)
		if doc == kindsFleet {
			t.Fatalf("fixture: %q not found", tc.old)
		}
		_, err := loadYAML(t, doc)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.new, err, tc.want)
		}
	}
	// a legacy token beside a legacy endpoint stays valid
	doc := strings.Replace(kindsFleet, "    metrics: {source: m, scope: tenant}\n", "    metrics: {endpoint: https://mimir.example, token: abc}\n", 1)
	if _, err := loadYAML(t, doc); err != nil {
		t.Fatalf("a legacy token: %v", err)
	}
}

func TestLogsSharingATenantMustBeToldApart(t *testing.T) {
	doc := strings.Replace(kindsFleet, "    group: we\n    metrics: {source: m, scope: tenant}\n", "    group: we\n    metrics: {source: m, scope: tenant}\n    loki: {source: logs, match: {k8s_cluster_name: prd}}\n", 1)
	_, err := loadYAML(t, doc)
	if err == nil || !strings.Contains(err.Error(), "so their logs cannot be told apart") {
		t.Fatalf("%v", err)
	}
}

func TestAConnectionTakesOnlyItsKindsInstallation(t *testing.T) {
	c, err := loadYAML(t, kindsFleet)
	if err != nil {
		t.Fatal(err)
	}
	prd := c.Clusters[0]
	if b := ConnectionAlertmanager(c.Sources, prd); b == nil || b.Source != "am" {
		t.Fatalf("alerts: %+v", b)
	}
	if b := ConnectionLoki(c.Sources, prd); b == nil || b.Source != "logs" {
		t.Fatalf("logs: %+v", b)
	}
	if m := ConnectionMetrics(c.Sources, prd); m != nil {
		t.Fatalf("prd's metrics are discovered, not %+v", m)
	}
	dev := c.Clusters[1]
	if m := ConnectionMetrics(c.Sources, dev); m == nil || m.Source != "m" || m.Scope != ScopeTenant {
		t.Fatalf("dev's own metrics block: %+v", m)
	}
	// an installation of another kind implies nothing
	only := []SourceConfig{{ID: "logs", Kind: SourceKindLoki, Host: "prd", Serves: []ServeConfig{{Group: "we"}}}}
	if ConnectionAlertmanager(only, prd) != nil || ConnectionMetrics(only, prd) != nil {
		t.Fatal("a Loki installation implied an Alertmanager or metrics subscription")
	}
	if b := ConnectionBlock(EndpointMetrics, c.Sources, dev); b == nil || b.Source != "m" {
		t.Fatalf("ConnectionBlock metrics: %+v", b)
	}
}

func TestALokiSubscriptionOrOffIsNotWarnedAsUnlocated(t *testing.T) {
	c, err := loadYAML(t, kindsFleet)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range c.Warnings() {
		if strings.Contains(w, "loki has no endpoint or serviceRef") {
			t.Fatalf("warned: %s", w)
		}
	}
	c2, err := loadYAML(t, "clusters:\n  - name: a\n    context: a\n    loki: {tenant: x}\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c2.Warnings(), "\n"), "loki has no endpoint or serviceRef") {
		t.Fatal("the control: an unlocated Loki block is warned")
	}
}

// Codex's review of S3b-1, R1: a token written empty or null, directly or
// through a merge, is refused beside source or off as a non-empty one is;
// an empty token beside a legacy endpoint stays valid, and no credential.
func TestATokenWrittenAtAllIsRefusedBesideSourceOrOff(t *testing.T) {
	block := "    metrics: {source: m, scope: tenant}\n"
	for _, written := range []string{
		"    metrics: {off: true, token: \"\"}\n",
		"    metrics: {off: true, token: null}\n",
		"    metrics: {source: m, scope: tenant, token: \"\"}\n",
		"    metrics: {source: m, scope: tenant, token: null}\n",
		"    metrics:\n      <<: {token: \"\"}\n      off: true\n",
		"    metrics:\n      <<: {token: null}\n      source: m\n      scope: tenant\n",
	} {
		doc := strings.Replace(kindsFleet, block, written, 1)
		if _, err := loadYAML(t, doc); err == nil || !strings.Contains(err.Error(), "metrics.token belongs to the cluster's own endpoint") {
			t.Errorf("%q: %v", written, err)
		}
	}
	doc := strings.Replace(kindsFleet, block, "    metrics: {endpoint: https://mimir.example, token: \"\"}\n", 1)
	c, err := loadYAML(t, doc)
	if err != nil {
		t.Fatalf("an empty token beside a legacy endpoint: %v", err)
	}
	for _, e := range c.Clusters[1].Endpoints() {
		if e.Kind == EndpointMetrics && e.LegacyToken {
			t.Fatal("an empty token is advertised as a credential")
		}
	}
}
