// Package config loads the Klyx-owned fleet configuration. Cluster identity,
// grouping, environment tags, and metrics endpoints are declared here;
// kubeconfig is used only to resolve credentials.
package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Config struct {
	Clusters []ClusterConfig `yaml:"clusters"`
	AI       *AIConfig       `yaml:"ai"`
	Line     *LineConfig     `yaml:"line"`
	Git      *GitConfig      `yaml:"git"`
	Index    *IndexConfig    `yaml:"index"`
	// Notifications are the health notifications (The Line slice 10): only
	// Settings > Notifications writes them.
	Notifications *NotificationsConfig `yaml:"notifications"`
	// Sources are the observability installations (Settings S3a §2.1).
	Sources []SourceConfig `yaml:"sources"`

	// unknownTop are the unknown top-level keys, for Warnings (strict.go)
	unknownTop []string
}

// NotificationsConfig is the notifications block. Absent, notifications
// are off (parent §14.9); once enabled, protected clusters only and no
// recoveries unless chosen.
type NotificationsConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Scope      string `yaml:"scope,omitempty"` // "protected" (the default) or "all"
	Recoveries bool   `yaml:"recoveries,omitempty"`
}

// IndexConfig configures the fleet metadata index (The Line slice 9 note
// §2.2). Kinds replaces the default kinds: absent (nil) keeps them, an
// explicit empty list indexes none. Each entry is group/resource or
// group/version/resource. A zero limit keeps its default.
type IndexConfig struct {
	Kinds             []string `yaml:"kinds"`
	EntriesPerCluster int      `yaml:"entriesPerCluster"`
	MiBPerCluster     int      `yaml:"mibPerCluster"`
	EntriesFleet      int      `yaml:"entriesFleet"`
	MiBFleet          int      `yaml:"mibFleet"`
}

// GitConfig is The Line's read-only Git access (parent spec §7, slice 6
// note §5): only Settings > Git writes it. Klyx fetches only from these
// approved URLs, exactly as written; a URL read from a cluster is only
// matched to one, never executed.
type GitConfig struct {
	Repos []GitRepoConfig `yaml:"repos"`
}

// GitRepoConfig is one approved repository.
type GitRepoConfig struct {
	URL         string   `yaml:"url"`         // executed exactly as approved: https or ssh, never a credential
	Web         string   `yaml:"web"`         // optional https base for "Open in Git" on hosts Klyx does not know
	RemoteBases bool     `yaml:"remoteBases"` // the cache diff (6e): remote Kustomize bases from approved repos
	Matches     []string `yaml:"matches"`     // normalised repository keys this entry also serves (an SSH alias)
}

// LineConfig configures The Line (spec 2026-10-14 §3.2): Source overrides the
// comparison key, which otherwise is the Git key most stations share.
type LineConfig struct {
	Source *LineSourceConfig `yaml:"source"`
}

// LineSourceConfig names a Git comparison key: a repository URL and a ref
// in Flux's terms (branch, tag, semver, commit or name).
type LineSourceConfig struct {
	URL string `yaml:"url"`
	Ref struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	} `yaml:"ref"`
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
	// ProposeActions lets an investigation propose day-2 verbs for the
	// operator to run (AI A3 §6), default on
	ProposeActions *bool `yaml:"proposeActions"`

	// The pre-2026-10-13 spelling, still read: backend hosted = anthropic,
	// local = openaiCompatible; apiKey is a plain-text key (keySource legacy).
	Backend string `yaml:"backend"`
	APIKey  string `yaml:"apiKey"`
}

// Enabled reports whether AI is configured to run (a non-nil, non-off backend).
func (a *AIConfig) Enabled() bool { return a != nil && a.Provider != "" && a.Provider != "off" }

// UsesProposeActions reports whether investigations may propose actions
// (AI A3 §6, default on).
func (a *AIConfig) UsesProposeActions() bool {
	return a == nil || a.ProposeActions == nil || *a.ProposeActions
}

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
	// Alertmanager and Loki (coverage C5 §2): parsed, validated and shown
	// with their credential state; S8 reads them.
	Alertmanager *EndpointConfig `yaml:"alertmanager"`
	Loki         *EndpointConfig `yaml:"loki"`
}

type MetricsConfig struct {
	Endpoint string `yaml:"endpoint"`
	// Token is the legacy plaintext bearer token (coverage C5 §6): still
	// read until Settings > Endpoints moves it into the keychain.
	Token         string             `yaml:"token"`
	TLSSkipVerify bool               `yaml:"tlsSkipVerify"`
	ServiceRef    *MetricsServiceRef `yaml:"serviceRef"`
	Auth          *EndpointAuth      `yaml:"auth"`   // C5 §2: the secret is in the keychain
	Tenant        string             `yaml:"tenant"` // C5 §2: X-Scope-OrgID
	// Match scopes every query to this cluster's series in a shared tenant;
	// Source subscribes the cluster to a metrics installation of sources,
	// Scope "tenant" takes everything in its tenant, Off reads no metrics
	// (Settings S3b §2.1, §3.3).
	Match  map[string]string `yaml:"match"`
	Source string            `yaml:"source"`
	Scope  string            `yaml:"scope"`
	Off    bool              `yaml:"off"`

	// keys are the keys the block wrote (as EndpointConfig's)
	keys []string
}

type MetricsServiceRef struct {
	Namespace string `yaml:"namespace"`
	Name      string `yaml:"name"`
	Port      string `yaml:"port"`
	Scheme    string `yaml:"scheme"` // http|https; default http
	// Path is the prefix the Service serves its API under (coverage S8a §2):
	// Mimir's Alertmanager under /alertmanager, its Prometheus API under
	// /prometheus. "" for none.
	Path string `yaml:"path"`
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

// Effective is the entry as Load reads it (Settings S2 §4.2): the context
// defaulted to the name, and the tags its context derives. A plan compares
// effective entries; it writes raw ones.
func (c ClusterConfig) Effective() ClusterConfig {
	if c.Context == "" {
		c.Context = c.Name
	}
	c.Tags = copyTags(c.Tags)
	c.applyDerivedDefaults()
	return c
}

// ProtectedBy says what makes the entry protected, "" when nothing does:
// protected: true, or the environment, the name or the context that looks
// like production (Settings S2 §4.3). The inputs are ORed; this names the
// first that holds.
func (c ClusterConfig) ProtectedBy() string {
	e := c.Effective()
	env, src := e.Environment, "the environment "
	if env == "" {
		env, src = e.Tags["env"], "the env tag "
	}
	switch {
	case e.Protected:
		return "protected: true"
	case looksProd(env):
		return src + env
	case looksProd(e.Name):
		return "the name " + e.Name
	case looksProd(e.Context):
		return "the context " + e.Context
	}
	return ""
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
	"environment": true, "protected": true, "metrics": true, "alertmanager": true, "loki": true,
}

// Warnings returns non-fatal config problems worth surfacing at startup, chiefly
// tag keys that shadow a real cluster field. Deterministically ordered.
func (c *Config) Warnings() []string {
	w := append([]string(nil), c.unknownTop...)
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
		for _, e := range cl.Endpoints() {
			w = append(w, endpointWarnings(cl.Name, e.Kind, e.Spec)...)
		}
	}
	w = append(w, sharedWithoutMatch(c.Clusters)...)
	w = append(w, c.sourceWarnings()...)
	_, notes := c.ApprovedGitRepos()
	return append(w, notes...)
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
	raw, err := readFleetFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	c, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return c, nil
}

// LoadVersioned is Load with the file version of the bytes it decoded
// (FileVersion): a plan reads its block and the version it writes against
// from one read (Settings slice F §5). The version is set whenever the file
// was read, so a file that does not decode still has one.
func LoadVersioned(path string) (*Config, string, error) {
	raw, err := readFleetFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read config %q: %w", path, err)
	}
	version := versionOf(raw)
	c, err := decode(raw)
	if err != nil {
		return nil, version, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return c, version, nil
}

// LoadRaw is LoadVersioned with each cluster's entry as written, before any
// default (Settings S2 §3): what a plan's edit is computed against. The
// file must load; raw is keyed by cluster name.
func LoadRaw(path string) (*Config, map[string]ClusterConfig, string, error) {
	b, err := readFleetFile(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read config %q: %w", path, err)
	}
	version := versionOf(b)
	c, err := decode(b)
	if err != nil {
		return nil, nil, version, fmt.Errorf("invalid config %q: %w", path, err)
	}
	var r struct {
		Clusters []ClusterConfig `yaml:"clusters"`
	}
	if err := unmarshalFleet(b, &r); err != nil {
		return nil, nil, version, fmt.Errorf("invalid config %q: %w", path, shown(err))
	}
	raw := make(map[string]ClusterConfig, len(r.Clusters))
	for _, cl := range r.Clusters {
		raw[cl.Name] = cl
	}
	return c, raw, version, nil
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
		}
		for _, e := range cl.Endpoints() {
			spec := e.Spec
			if err := validateSubscription(cl.Name, e.Kind, &spec, e.LegacyToken); err != nil {
				return err
			}
			if err := validateEndpoint(e.Kind, e.Spec, e.LegacyToken); err != nil {
				return fmt.Errorf("cluster %q: %w", cl.Name, err)
			}
			if e.Kind == EndpointMetrics {
				if k := reservedMetricsLabel(e.Spec.Match); k != "" {
					return fmt.Errorf("cluster %q: metrics.match: %s is reserved by Prometheus: a scope matches series by their labels, and each query names its own metric", cl.Name, describeKey(k))
				}
			}
		}
	}
	// Settings S3a §2: the installations, then every cluster's resolution
	// and scope, so a conflict is a load error naming its place
	if err := c.validateSources(); err != nil {
		return err
	}
	return ResolveAll(c.Sources, c.Clusters)
}
