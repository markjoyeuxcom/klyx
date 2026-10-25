package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadYAML(t *testing.T, y string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

// Coverage C5 §2: the three endpoint blocks parse in one shape.
func TestEndpointBlocksParse(t *testing.T) {
	cfg, err := loadYAML(t, `clusters:
  - name: prd
    metrics:
      endpoint: https://mimir.example/prometheus
      auth: {type: bearer}
      tenant: platform
    alertmanager:
      serviceRef: {namespace: monitoring, name: alertmanager-operated, port: "9093"}
    loki:
      endpoint: https://loki.example
      auth: {type: basic, username: klyx-reader}
      tenant: platform
`)
	if err != nil {
		t.Fatal(err)
	}
	eps := cfg.Clusters[0].Endpoints()
	if len(eps) != 3 || eps[0].Kind != EndpointMetrics || eps[1].Kind != EndpointAlertmanager || eps[2].Kind != EndpointLoki {
		t.Fatalf("endpoints: %+v", eps)
	}
	if a := eps[0].Spec.Auth; a == nil || a.Type != "bearer" || eps[0].Spec.Tenant != "platform" || eps[0].LegacyToken {
		t.Fatalf("metrics: %+v", eps[0])
	}
	if sr := eps[1].Spec.ServiceRef; sr == nil || sr.Name != "alertmanager-operated" || sr.Port != "9093" {
		t.Fatalf("alertmanager: %+v", eps[1])
	}
	if a := eps[2].Spec.Auth; a == nil || a.Type != "basic" || a.Username != "klyx-reader" {
		t.Fatalf("loki: %+v", eps[2])
	}
}

// The legacy plaintext token is still read (C5 §6).
func TestLegacyMetricsTokenStillRead(t *testing.T) {
	cfg, err := loadYAML(t, "clusters:\n  - name: a\n    metrics:\n      endpoint: https://prom.example\n      token: tok-123\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clusters[0].Metrics.Token != "tok-123" || !cfg.Clusters[0].Endpoints()[0].LegacyToken {
		t.Fatalf("legacy token: %+v", cfg.Clusters[0].Metrics)
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		{"auth type", "    loki:\n      endpoint: https://l\n      auth: {type: digest}\n", "loki.auth.type must be bearer or basic"},
		{"basic needs a username", "    loki:\n      endpoint: https://l\n      auth: {type: basic}\n", "loki.auth.username is required"},
		{"bearer has no username", "    alertmanager:\n      endpoint: https://a\n      auth: {type: bearer, username: x}\n", "alertmanager.auth.username is for basic auth only"},
		{"a legacy token is bearer", "    metrics:\n      endpoint: https://m\n      token: t\n      auth: {type: basic, username: u}\n", "metrics.token is a bearer token"},
		{"tenant header injection", "    loki:\n      endpoint: https://l\n      tenant: \"a\\r\\nX-Evil: 1\"\n", "loki.tenant must not contain control characters"},
		{"serviceRef", "    alertmanager:\n      serviceRef: {namespace: m, name: a}\n", "alertmanager.serviceRef requires namespace, name, and port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAML(t, "clusters:\n  - name: a\n"+tc.block)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// Userinfo is refused for use (a warning, not a load failure), auth on a
// proxied endpoint is said not to be sent, endpoint beside serviceRef says
// which wins - for every kind.
func TestEndpointWarnings(t *testing.T) {
	cfg, err := loadYAML(t, `clusters:
  - name: a
    metrics:
      endpoint: https://user:pass@prom.example
    alertmanager:
      serviceRef: {namespace: m, name: am, port: "9093"}
      auth: {type: bearer}
    loki:
      endpoint: https://loki.example
      serviceRef: {namespace: m, name: loki, port: "3100"}
`)
	if err != nil {
		t.Fatalf("userinfo is not a load failure: %v", err)
	}
	w := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{
		`cluster "a": metrics: the endpoint URL carries credentials; set them in Settings > Observability`,
		`cluster "a": alertmanager.auth is not sent to a proxied or discovered endpoint`,
		`cluster "a": loki.serviceRef is ignored because loki.endpoint is set`,
	} {
		if !strings.Contains(w, want) {
			t.Errorf("missing warning %q in:\n%s", want, w)
		}
	}
	if strings.Contains(w, "pass") {
		t.Fatalf("a warning shows the password: %s", w)
	}
	if EndpointURLProblem("https://prom.example") != "" || EndpointURLProblem("") != "" {
		t.Fatal("no userinfo, no problem")
	}
}

// A URL that does not parse is refused for use with a fixed message.
func TestUnparseableEndpointURL(t *testing.T) {
	p := EndpointURLProblem("https://user:C5-URL-SECRET@bad%host")
	if p == "" || strings.Contains(p, "C5-URL-SECRET") {
		t.Fatalf("problem: %q", p)
	}
}

// Coverage S8a §2: match is equality matchers on label names, and
// serviceRef.path a plain path prefix.
func TestMatchAndServicePathValidation(t *testing.T) {
	base := "clusters:\n  - name: a\n    context: a\n    alertmanager:\n"
	for _, tc := range []struct{ block, want string }{
		{"      endpoint: https://am.example\n      match: {cluster: aks-prd}\n", ""},
		{"      endpoint: https://am.example\n      match: {\"a-b\": x}\n", "is not a label name"},
		{"      endpoint: https://am.example\n      match: {cluster: \"\"}\n", "non-empty value"},
		{"      endpoint: https://am.example\n      match: {cluster: \"a\\nb\"}\n", "control characters"},
		{"      serviceRef: {namespace: m, name: am, port: web, path: /alertmanager}\n", ""},
		{"      serviceRef: {namespace: m, name: am, port: web, path: alertmanager/v2/}\n", ""},
		{"      serviceRef: {namespace: m, name: am, port: web, path: /a/../b}\n", "plain path prefix"},
		{"      serviceRef: {namespace: m, name: am, port: web, path: \"/a?x=1\"}\n", "plain path prefix"},
		{"      serviceRef: {namespace: m, name: am, port: web, path: \"/a#f\"}\n", "plain path prefix"},
	} {
		_, err := loadYAML(t, base+tc.block)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%q: %v, want %q", tc.block, err, tc.want)
		}
	}
	// the metrics serviceRef takes the same path
	if _, err := loadYAML(t, "clusters:\n  - name: a\n    context: a\n    metrics:\n      serviceRef: {namespace: m, name: mimir, port: \"8080\", path: /prometheus}\n"); err != nil {
		t.Fatal(err)
	}
}

// Two clusters reading the same Alertmanager, one without match, are warned
// about by name; the origin is compared canonically and the path as written.
func TestSharedEndpointWithoutMatchWarns(t *testing.T) {
	doc := func(a, b string) string {
		return "clusters:\n  - name: a\n    context: a\n    alertmanager:\n" + a + "  - name: b\n    context: b\n    alertmanager:\n" + b
	}
	shared := func(w []string) bool {
		for _, s := range w {
			if strings.Contains(s, "clusters a, b read the same alertmanager endpoint") {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		a, b string
		warn bool
	}{
		{"      endpoint: https://am.example\n", "      endpoint: https://AM.example:443/\n", true},
		{"      endpoint: https://am.example\n      match: {cluster: a}\n", "      endpoint: https://am.example\n", true},
		{"      endpoint: https://am.example\n      match: {cluster: a}\n", "      endpoint: https://am.example\n      match: {cluster: b}\n", false},
		{"      endpoint: https://am.example/one\n", "      endpoint: https://am.example/two\n", false},
	} {
		c, err := loadYAML(t, doc(tc.a, tc.b))
		if err != nil {
			t.Fatal(err)
		}
		if got := shared(c.Warnings()); got != tc.warn {
			t.Errorf("%q / %q: warned %v, want %v: %q", tc.a, tc.b, got, tc.warn, c.Warnings())
		}
	}
}
