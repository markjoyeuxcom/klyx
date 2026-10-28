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

// S3b-2 §3.3: metrics.match is read now, on a legacy block and on a
// subscription; a legacy token may stand beside a legacy match.
func TestMetricsMatchIsAdmitted(t *testing.T) {
	for _, block := range []string{
		"    metrics: {endpoint: https://mimir.example, match: {cluster: dev}}\n",
		"    metrics: {endpoint: https://mimir.example, token: abc, match: {cluster: dev}}\n",
		"    metrics: {source: m, match: {cluster: dev}}\n",
	} {
		doc := strings.Replace(kindsFleet, "    metrics: {source: m, scope: tenant}\n", block, 1)
		c, err := loadYAML(t, doc)
		if err != nil {
			t.Fatalf("%q: %v", block, err)
		}
		if m := c.Clusters[1].Metrics.Match; m["cluster"] != "dev" {
			t.Fatalf("%q: match %v", block, m)
		}
	}
}

// Two clusters reading the same metrics endpoint, one without match, are
// warned about as Alertmanager and Loki are: metrics.match is now applied
// (Settings S3b §2.1, §3.3).
func TestSharedMetricsEndpointWithoutMatchWarns(t *testing.T) {
	doc := func(a, b string) string {
		return "clusters:\n  - name: a\n    context: a\n    metrics: " + a + "\n  - name: b\n    context: b\n    metrics: " + b + "\n"
	}
	for _, tc := range []struct {
		a, b string
		warn bool
	}{
		{"{endpoint: https://mimir.example}", "{endpoint: https://MIMIR.example:443/}", true},
		{"{endpoint: https://mimir.example, match: {cluster: a}}", "{endpoint: https://mimir.example}", true},
		{"{endpoint: https://mimir.example, match: {cluster: a}}", "{endpoint: https://mimir.example, match: {cluster: b}}", false},
		{"{endpoint: https://mimir.example/a}", "{endpoint: https://mimir.example/b}", false},
	} {
		c, err := loadYAML(t, doc(tc.a, tc.b))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(strings.Join(c.Warnings(), "\n"), "clusters a, b read the same metrics endpoint, and at least one has no metrics.match")
		if got != tc.warn {
			t.Errorf("%q / %q: warned %v, want %v: %q", tc.a, tc.b, got, tc.warn, c.Warnings())
		}
	}
}

// Codex's review of S3b-2a, R3: a metrics scope never names a label
// Prometheus reserves - not in a cluster's match, a subscription's, or a
// metrics installation's member label - while Alertmanager's and Loki's
// member labels are as before.
func TestAReservedMetricsScopeLabelIsRefused(t *testing.T) {
	for _, block := range []string{
		"    metrics: {endpoint: https://mimir.example, match: {__name__: node_cpu_seconds_total}}\n",
		"    metrics: {endpoint: https://mimir.example, match: {cluster: dev, __meta_x: y}}\n",
		"    metrics: {source: m, match: {__name__: up}}\n",
	} {
		doc := strings.Replace(kindsFleet, "    metrics: {source: m, scope: tenant}\n", block, 1)
		if _, err := loadYAML(t, doc); err == nil || !strings.Contains(err.Error(), "is reserved by Prometheus") {
			t.Errorf("%q: %v", block, err)
		}
	}
	doc := "clusters:\n  - name: a\n    context: a\nsources:\n  - id: m\n    kind: metrics\n    endpoint: https://mimir.example\n    memberLabel: __name__\n    serves: [{cluster: a}]\n"
	if _, err := loadYAML(t, doc); err == nil || !strings.Contains(err.Error(), "sources.m.memberLabel") || !strings.Contains(err.Error(), "is reserved by Prometheus") {
		t.Fatalf("memberLabel: %v", err)
	}
	// the control: an Alertmanager installation's member label is as before
	doc = strings.Replace(strings.Replace(doc, "kind: metrics", "kind: alertmanager", 1), "https://mimir.example", "https://am.example", 1)
	if _, err := loadYAML(t, doc); err != nil {
		t.Fatalf("alertmanager: %v", err)
	}
}

// Codex's re-review of S3b-2a, RR3: the refusal withholds a long cluster
// name and a long reserved label, as every diagnostic of the file's values.
func TestTheReservedLabelRefusalWithholdsLongValues(t *testing.T) {
	long := strings.Repeat("pasted-value-", 12)
	label := "__" + strings.Repeat("x", 40)
	doc := "clusters:\n  - name: " + long + "\n    context: a\n    metrics: {endpoint: https://mimir.example, match: {" + label + ": up}}\n"
	_, err := loadYAML(t, doc)
	if err == nil || !strings.Contains(err.Error(), "is reserved by Prometheus") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), long) || strings.Contains(err.Error(), label) || !strings.Contains(err.Error(), "a value of 156 characters") {
		t.Fatalf("a long value echoed: %v", err)
	}
	// the loop's other refusals withhold it too
	for what, doc := range map[string]string{
		"duplicate":         "clusters:\n  - name: " + long + "\n    context: a\n  - name: " + long + "\n    context: b\n",
		"trailing /api/v1":  "clusters:\n  - name: " + long + "\n    context: a\n    metrics: {endpoint: https://mimir.example/api/v1}\n",
		"an endpoint block": "clusters:\n  - name: " + long + "\n    context: a\n    alertmanager: {serviceRef: {namespace: m}}\n",
	} {
		if _, err := loadYAML(t, doc); err == nil || strings.Contains(err.Error(), long) || !strings.Contains(err.Error(), "a value of 156 characters") {
			t.Errorf("%s: %v", what, err)
		}
	}
	// the control: a short name is said as it is
	doc = "clusters:\n  - name: dev\n    context: a\n    metrics: {endpoint: https://mimir.example, match: {__name__: up}}\n"
	if _, err := loadYAML(t, doc); err == nil || !strings.Contains(err.Error(), `cluster "dev": metrics.match: "__name__" is reserved`) {
		t.Fatalf("%v", err)
	}
}
