package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWarningsFlagsShadowedTagKey(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{
		{Name: "homelab", Tags: map[string]string{"env": "homelab", "protected": "true"}},
	}}
	w := c.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "protected") || !strings.Contains(w[0], "homelab") {
		t.Fatalf("want one warning about protected on homelab, got %v", w)
	}
}

func TestWarningsCleanConfigNone(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{
		{Name: "dev", Tags: map[string]string{"env": "dev", "region": "we"}},
	}}
	if w := c.Warnings(); len(w) != 0 {
		t.Fatalf("want no warnings, got %v", w)
	}
}

func TestSummaryListsProtected(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{
		{Name: "prd", Protected: true}, {Name: "dev"},
	}}
	s := c.Summary()
	if !strings.Contains(s, "2 cluster") || !strings.Contains(s, "prd") {
		t.Fatalf("summary: %q", s)
	}
}

func TestLoadValidConfig(t *testing.T) {
	c, err := Load(filepath.Join("testdata", "fleet.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Clusters) != 2 {
		t.Fatalf("want 2 clusters, got %d", len(c.Clusters))
	}
	first := c.Clusters[0]
	if first.Context != "prd-we" {
		t.Fatalf("want context prd-we, got %q", first.Context)
	}
	if first.Tags["env"] != "prd" {
		t.Fatalf("want env prd, got %q", first.Tags["env"])
	}
	if first.Metrics == nil || first.Metrics.Endpoint == "" {
		t.Fatalf("want metrics endpoint set")
	}
	// Context defaults to Name when omitted.
	if c.Clusters[1].Context != "vimadaboda-k3s" {
		t.Fatalf("want defaulted context, got %q", c.Clusters[1].Context)
	}
}

func TestLoadParsesEnvironmentAndProtected(t *testing.T) {
	cfg, err := Load("testdata/protected.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byName := map[string]ClusterConfig{}
	for _, c := range cfg.Clusters {
		byName[c.Name] = c
	}
	if byName["prd-we"].Environment != "prd" || !byName["prd-we"].Protected {
		t.Fatalf("prd-we: %+v", byName["prd-we"])
	}
	if byName["dev-ne"].Environment != "dev" || byName["dev-ne"].Protected {
		t.Fatalf("dev-ne: %+v", byName["dev-ne"])
	}
}

func TestValidateRejectsDuplicateNames(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{{Name: "a"}, {Name: "a"}}}
	if err := c.validate(); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestValidateRejectsEmptyName(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{{Name: ""}}}
	if err := c.validate(); err == nil {
		t.Fatal("expected empty-name error")
	}
}

func TestValidateRejectsNoClusters(t *testing.T) {
	c := &Config{}
	if err := c.validate(); err == nil {
		t.Fatal("expected no-clusters error")
	}
}

func TestAIConfigDefaultsAndValidation(t *testing.T) {
	// nil AI is fine (off) and Enabled() is false.
	var none *AIConfig
	if none.Enabled() {
		t.Fatal("nil AIConfig must not be Enabled")
	}

	// hosted backend fills BaseURL + Model defaults.
	c := &Config{Clusters: []ClusterConfig{{Name: "a"}}, AI: &AIConfig{Backend: "hosted", APIKey: "sk"}}
	c.applyDefaults()
	if c.AI.BaseURL != "https://api.anthropic.com" || c.AI.Model != "claude-sonnet-5" {
		t.Fatalf("hosted defaults not applied: %+v", c.AI)
	}
	if !c.AI.Enabled() {
		t.Fatal("hosted backend must be Enabled")
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid hosted config rejected: %v", err)
	}

	// empty backend defaults to off (not enabled).
	off := &Config{Clusters: []ClusterConfig{{Name: "a"}}, AI: &AIConfig{}}
	off.applyDefaults()
	if off.AI.Backend != "off" || off.AI.Enabled() {
		t.Fatalf("empty backend must default off, got %+v", off.AI)
	}

	// hosted without an API key is rejected.
	noKey := &Config{Clusters: []ClusterConfig{{Name: "a"}}, AI: &AIConfig{Backend: "hosted"}}
	noKey.applyDefaults()
	if err := noKey.validate(); err == nil {
		t.Fatal("hosted backend without apiKey must be rejected")
	}

	// unknown backend is rejected.
	bad := &Config{Clusters: []ClusterConfig{{Name: "a"}}, AI: &AIConfig{Backend: "wat"}}
	if err := bad.validate(); err == nil {
		t.Fatal("unknown ai.backend must be rejected")
	}

	// local backend defaults the Ollama base URL and needs no key.
	local := &Config{Clusters: []ClusterConfig{{Name: "a"}}, AI: &AIConfig{Backend: "local", Model: "llama3.1"}}
	local.applyDefaults()
	if local.AI.BaseURL != "http://localhost:11434" {
		t.Fatalf("local base URL default not applied: %+v", local.AI)
	}
	if err := local.validate(); err != nil {
		t.Fatalf("valid local config rejected: %v", err)
	}

	// local backend without a model is rejected (there is no local model default).
	localNoModel := &Config{Clusters: []ClusterConfig{{Name: "a"}}, AI: &AIConfig{Backend: "local"}}
	localNoModel.applyDefaults()
	if err := localNoModel.validate(); err == nil {
		t.Fatal("local backend without a model must be rejected")
	}
}

func TestServiceRefValidation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "serviceRef missing port",
			yaml:    "clusters:\n  - name: a\n    metrics:\n      serviceRef:\n        namespace: monitoring\n        name: prometheus-operated\n",
			wantErr: "serviceRef",
		},
		{
			name:    "endpoint ending in /api/v1 rejected",
			yaml:    "clusters:\n  - name: a\n    metrics:\n      endpoint: https://host/prometheus/api/v1\n",
			wantErr: "/api/v1",
		},
		{
			name:    "bad scheme",
			yaml:    "clusters:\n  - name: a\n    metrics:\n      serviceRef:\n        namespace: monitoring\n        name: p\n        port: \"9090\"\n        scheme: ftp\n",
			wantErr: "scheme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f.yaml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadParsesServiceRef(t *testing.T) {
	yaml := "clusters:\n" +
		"  - name: a\n" +
		"    metrics:\n" +
		"      serviceRef:\n" +
		"        namespace: monitoring\n" +
		"        name: prometheus-operated\n" +
		"        port: \"9090\"\n" +
		"        scheme: https\n" +
		"  - name: b\n" +
		"    metrics:\n" +
		"      endpoint: https://prom.example/prometheus\n"
	p := filepath.Join(t.TempDir(), "f.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr := cfg.Clusters[0].Metrics.ServiceRef
	if sr == nil {
		t.Fatal("serviceRef is nil")
	}
	if sr.Namespace != "monitoring" || sr.Name != "prometheus-operated" || sr.Port != "9090" || sr.Scheme != "https" {
		t.Fatalf("serviceRef fields wrong: %+v", sr)
	}
	if cfg.Clusters[1].Metrics.Endpoint != "https://prom.example/prometheus" {
		t.Fatalf("endpoint wrong: %q", cfg.Clusters[1].Metrics.Endpoint)
	}
}

func TestEndpointAndServiceRefWarning(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{{
		Name:    "a",
		Metrics: &MetricsConfig{Endpoint: "https://host", ServiceRef: &MetricsServiceRef{Namespace: "m", Name: "p", Port: "9090"}},
	}}}
	got := strings.Join(c.Warnings(), "|")
	if !strings.Contains(got, "serviceRef") || !strings.Contains(got, "endpoint") {
		t.Fatalf("want endpoint/serviceRef warning, got %q", got)
	}
}

func TestIsProtectedInfersProduction(t *testing.T) {
	cases := []struct {
		name string
		cc   ClusterConfig
		want bool
	}{
		{"explicit-protected", ClusterConfig{Name: "anything", Protected: true}, true},
		{"environment-field-prd", ClusterConfig{Name: "c", Environment: "prd"}, true},
		{"env-tag-production", ClusterConfig{Name: "c", Tags: map[string]string{"env": "production"}}, true},
		{"name-token-prd", ClusterConfig{Name: "eks-prd-we"}, true},
		{"context-arn-prod", ClusterConfig{Name: "friendly", Context: "arn:aws:eks:eu-west-1:1:cluster/app-prod-1"}, true},
		{"dev", ClusterConfig{Name: "eks-dev-we", Environment: "dev"}, false},
		{"substring-not-token", ClusterConfig{Name: "reproducer-lab"}, false},
		{"homelab", ClusterConfig{Name: "homelab"}, false},
	}
	for _, tc := range cases {
		if got := tc.cc.IsProtected(); got != tc.want {
			t.Errorf("%s: IsProtected()=%v want %v", tc.name, got, tc.want)
		}
	}
}

// The startup summary lists EFFECTIVE protection, marking token-inferred
// clusters, so an operator can see why a cluster is locked.
func TestSummaryListsInferredProtection(t *testing.T) {
	c := &Config{Clusters: []ClusterConfig{
		{Name: "core", Protected: true}, {Name: "aks-prd-we"}, {Name: "dev"},
	}}
	s := c.Summary()
	if !strings.Contains(s, "aks-prd-we (inferred)") || !strings.Contains(s, "core") || strings.Contains(s, "dev") {
		t.Fatalf("summary: %q", s)
	}
}
