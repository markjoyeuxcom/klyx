// Package config loads the Klyx-owned fleet configuration. Cluster identity,
// grouping, environment tags, and metrics endpoints are declared here;
// kubeconfig is used only to resolve credentials.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Clusters []ClusterConfig `yaml:"clusters"`
	AI       *AIConfig       `yaml:"ai"`
}

// AIConfig is the global (not per-cluster) AI-assist settings (spec
// 2026-10-13 §2.2). AI is opt-in and OFF by default. Only non-secret settings
// live here: an API key is in the OS credential store (keySource keychain), an
// environment variable (keySource env), or - the pre-2026-10-13 form - in
// plain text as apiKey (keySource legacy). A protected cluster's context goes
// to an off-machine provider only with allowProtectedEgress.
type AIConfig struct {
	Provider             string `yaml:"provider"`  // off (default) | anthropic | openai | gemini | openaiCompatible | claudeCLI
	Model                string `yaml:"model"`     // anthropic defaults to claude-opus-5-5; the others must be chosen
	KeySource            string `yaml:"keySource"` // keychain | env | legacy | none
	APIKeyEnv            string `yaml:"apiKeyEnv"` // keySource env: the variable's name
	BaseURL              string `yaml:"baseUrl"`   // openaiCompatible (default localhost:11434); optional HTTPS override for a vendor
	CLIPath              string `yaml:"cliPath"`   // optional override for a CLI provider
	AllowProtectedEgress bool   `yaml:"allowProtectedEgress"`
	TLSSkipVerify        bool   `yaml:"tlsSkipVerify"`   // openaiCompatible only: a self-signed local server
	RefusalFallback      *bool  `yaml:"refusalFallback"` // anthropic: server-side refusal fallback, default on

	// The pre-2026-10-13 spelling, still read: backend hosted = anthropic,
	// local = openaiCompatible; apiKey is a plain-text key (keySource legacy).
	Backend string `yaml:"backend"`
	APIKey  string `yaml:"apiKey"`
}

// Enabled reports whether AI is configured to run (a non-nil, non-off backend).
func (a *AIConfig) Enabled() bool { return a != nil && a.Provider != "" && a.Provider != "off" }

// UsesRefusalFallback reports the anthropic refusal-fallback setting (default on).
func (a *AIConfig) UsesRefusalFallback() bool {
	return a == nil || a.RefusalFallback == nil || *a.RefusalFallback
}

type ClusterConfig struct {
	Name        string            `yaml:"name"`
	Context     string            `yaml:"context"`
	Kubeconfig  string            `yaml:"kubeconfig"`
	Tags        map[string]string `yaml:"tags"`
	Group       string            `yaml:"group"`
	Environment string            `yaml:"environment"`
	Protected   bool              `yaml:"protected"`
	Metrics     *MetricsConfig    `yaml:"metrics"`
}

type MetricsConfig struct {
	Endpoint      string             `yaml:"endpoint"`
	Token         string             `yaml:"token"`
	TLSSkipVerify bool               `yaml:"tlsSkipVerify"`
	ServiceRef    *MetricsServiceRef `yaml:"serviceRef"`
}

type MetricsServiceRef struct {
	Namespace string `yaml:"namespace"`
	Name      string `yaml:"name"`
	Port      string `yaml:"port"`
	Scheme    string `yaml:"scheme"` // http|https; default http
}

// Env returns the environment tag, or "" if unset.
func (c ClusterConfig) Env() string { return c.Tags["env"] }

// IsProtected reports the cluster's EFFECTIVE protection - what the destructive-
// verb guard enforces. A cluster is protected if it is explicitly marked
// (protected: true), or its environment / name looks like production. The
// production default is deliberate and fail-safe: a false positive only adds a
// typed-confirmation prompt, while a false negative fails open on prod - the
// costly direction for an owner running prod-bearing clusters. Notably this
// makes a cluster hot-added mid-session through Settings protected the moment it
// looks like prod, instead of unprotected until a fleet.yaml edit + restart.
func (c ClusterConfig) IsProtected() bool {
	if c.Protected {
		return true
	}
	env := c.Environment
	if env == "" {
		env = c.EffectiveTags()["env"]
	}
	return looksProd(env) || looksProd(c.Name) || looksProd(c.Context)
}

// looksProd reports whether an environment/name string carries a production
// token. It splits on non-alphanumeric separators and matches whole tokens
// (prd/prod/production) rather than substrings, so "reproducer" or "productivity"
// do not false-match while "eks-prd-we" or "environment: production" do.
func looksProd(s string) bool {
	if s == "" {
		return false
	}
	tok := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	for _, t := range tok {
		switch t {
		case "prd", "prod", "production":
			return true
		}
	}
	return false
}

// reservedTagKeys are top-level ClusterConfig field names. If one shows up as a
// `tags:` key it is almost certainly a misplaced field: YAML silently accepts it
// into the Tags map, so the intended typed field stays at its zero value (the
// classic `tags: {protected: true}` trap, where Protected never gets set).
var reservedTagKeys = map[string]bool{
	"name": true, "context": true, "kubeconfig": true, "group": true,
	"environment": true, "protected": true, "metrics": true,
}

// Warnings returns non-fatal config problems worth surfacing at startup, chiefly
// tag keys that shadow a real cluster field. Deterministically ordered.
func (c *Config) Warnings() []string {
	var w []string
	for _, cl := range c.Clusters {
		shadowed := make([]string, 0, len(cl.Tags))
		for k := range cl.Tags {
			if reservedTagKeys[k] {
				shadowed = append(shadowed, k)
			}
		}
		sort.Strings(shadowed)
		for _, k := range shadowed {
			w = append(w, fmt.Sprintf("cluster %q: tag %q shadows a cluster field and is ignored; move it out of `tags:` to a top-level key", cl.Name, k))
		}
	}
	for _, cl := range c.Clusters {
		if m := cl.Metrics; m != nil && m.Endpoint != "" && m.ServiceRef != nil {
			w = append(w, fmt.Sprintf("cluster %q: metrics.serviceRef is ignored because metrics.endpoint is set", cl.Name))
		}
	}
	return w
}

// Summary is a one-line description of the loaded fleet for the startup log, so a
// misplaced `protected` shows up as an absence the operator can notice. It lists
// EFFECTIVE protection (what the destructive-verb guard enforces), marking the
// clusters protected only by a prd/prod/production token as "(inferred)".
func (c *Config) Summary() string {
	protected := make([]string, 0)
	for _, cl := range c.Clusters {
		switch {
		case cl.Protected:
			protected = append(protected, cl.Name)
		case cl.IsProtected():
			protected = append(protected, cl.Name+" (inferred)")
		}
	}
	sort.Strings(protected)
	list := "none"
	if len(protected) > 0 {
		list = strings.Join(protected, ", ")
	}
	return fmt.Sprintf("fleet: %d cluster(s); protected: %s", len(c.Clusters), list)
}

// Load reads, parses, defaults, and validates a fleet config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Clusters {
		if c.Clusters[i].Context == "" {
			c.Clusters[i].Context = c.Clusters[i].Name
		}
		c.Clusters[i].applyDerivedDefaults()
	}
	if c.AI != nil {
		c.AI.applyDefaults()
	}
}

// ApplyDefaults fills the defaults Load applies (the legacy backend mapping,
// the key source, the local base URL), for a block built in code.
func (a *AIConfig) ApplyDefaults() { a.applyDefaults() }

func (a *AIConfig) applyDefaults() {
	if a.Provider == "" {
		switch a.Backend {
		case "hosted":
			a.Provider = "anthropic"
		case "local":
			a.Provider = "openaiCompatible"
		case "", "off":
			a.Provider = "off"
		}
	}
	if a.Provider == "openaiCompatible" && a.BaseURL == "" {
		a.BaseURL = "http://localhost:11434"
	}
	if a.KeySource == "" {
		switch {
		case a.APIKey != "":
			a.KeySource = "legacy"
		case a.Provider == "anthropic" || a.Provider == "openai" || a.Provider == "gemini" || a.Provider == "openaiCompatible":
			a.KeySource = "keychain"
		default:
			a.KeySource = "none"
		}
	}
}

// validate checks the AI block's structure. A missing key is not a config
// error - AI reports it at run time - but an unknown provider or key source,
// or a field that does not apply, is.
func (a *AIConfig) validate() error {
	switch a.Backend {
	case "", "off", "hosted", "local":
	default:
		return fmt.Errorf("ai.backend must be off, hosted, or local (got %q); new configs use ai.provider", a.Backend)
	}
	switch a.Provider {
	case "", "off", "anthropic", "openai", "gemini", "openaiCompatible", "claudeCLI":
	default:
		return fmt.Errorf("ai.provider must be off, anthropic, openai, gemini, openaiCompatible or claudeCLI (got %q)", a.Provider)
	}
	switch a.KeySource {
	case "", "keychain", "env", "legacy", "none":
	default:
		return fmt.Errorf("ai.keySource must be keychain, env, legacy or none (got %q)", a.KeySource)
	}
	if a.KeySource == "env" && a.APIKeyEnv == "" {
		return fmt.Errorf("ai.apiKeyEnv (the variable's name) is required when ai.keySource is env")
	}
	if a.KeySource == "legacy" && a.APIKey == "" && a.Provider != "openaiCompatible" {
		return fmt.Errorf("ai.apiKey is required when ai.keySource is legacy")
	}
	if a.TLSSkipVerify && a.Provider != "openaiCompatible" {
		return fmt.Errorf("ai.tlsSkipVerify applies only to ai.provider openaiCompatible")
	}
	if a.Provider == "openaiCompatible" && a.Model == "" {
		return fmt.Errorf("ai.model is required for an OpenAI-compatible server (e.g. llama3.1)")
	}
	return nil
}

// errNoClusters is the one validation failure adding clusters can repair.
var errNoClusters = errors.New("no clusters configured")

func (c *Config) validate() error {
	if len(c.Clusters) == 0 {
		return errNoClusters
	}
	if c.AI != nil {
		if err := c.AI.validate(); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(c.Clusters))
	for i, cl := range c.Clusters {
		if cl.Name == "" {
			return fmt.Errorf("cluster[%d]: name is required", i)
		}
		if seen[cl.Name] {
			return fmt.Errorf("duplicate cluster name %q", cl.Name)
		}
		seen[cl.Name] = true
		if m := cl.Metrics; m != nil {
			if m.Endpoint != "" {
				endpoint := strings.TrimRight(m.Endpoint, "/")
				if strings.HasSuffix(endpoint, "/api/v1") {
					return fmt.Errorf("cluster %q: metrics.endpoint must be the Prometheus base URL without a trailing /api/v1", cl.Name)
				}
			}
			if sr := m.ServiceRef; sr != nil {
				if sr.Namespace == "" || sr.Name == "" || sr.Port == "" {
					return fmt.Errorf("cluster %q: metrics.serviceRef requires namespace, name, and port", cl.Name)
				}
				if sr.Scheme != "" && sr.Scheme != "http" && sr.Scheme != "https" {
					return fmt.Errorf("cluster %q: metrics.serviceRef.scheme must be http or https", cl.Name)
				}
			}
		}
	}
	return nil
}
