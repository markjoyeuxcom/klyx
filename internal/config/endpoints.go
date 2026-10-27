package config

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/safehttp"
	"gopkg.in/yaml.v3"
)

// Coverage C5 §2: one endpoint shape for metrics, Alertmanager and Loki.
// The endpoint is found by its URL, a serviceRef through the API server's
// service proxy, or discovery; its credential (auth) lives in the OS
// keychain, never here, bound to the endpoint's origin; its tenant is
// configuration, sent as X-Scope-OrgID.

// Endpoint kinds, as the keychain account and Settings name them.
const (
	EndpointMetrics      = "metrics"
	EndpointAlertmanager = "alertmanager"
	EndpointLoki         = "loki"
)

// EndpointKinds is every endpoint kind.
var EndpointKinds = []string{EndpointMetrics, EndpointAlertmanager, EndpointLoki}

// EndpointAuth says how an endpoint authenticates: a bearer token, or basic
// with this username. The secret is in the keychain (C5 §3).
type EndpointAuth struct {
	Type     string `yaml:"type"`     // bearer | basic
	Username string `yaml:"username"` // basic only; configuration, not a secret
}

// EndpointConfig is an Alertmanager or Loki endpoint (C5 §2). Metrics keeps
// its own MetricsConfig, which also carries the legacy plaintext token.
type EndpointConfig struct {
	Endpoint      string             `yaml:"endpoint"`
	TLSSkipVerify bool               `yaml:"tlsSkipVerify"`
	ServiceRef    *MetricsServiceRef `yaml:"serviceRef"`
	Auth          *EndpointAuth      `yaml:"auth"`
	Tenant        string             `yaml:"tenant"`
	// Match are the labels that make an alert (or a log stream) this
	// cluster's in a shared Alertmanager or Loki (coverage S8a §2):
	// equality matchers, sent as the endpoint's filter and checked again on
	// what it returns. None means everything there is this cluster's.
	Match map[string]string `yaml:"match"`
	// Source subscribes the cluster to an installation of sources, its
	// tenant and match over the installation's defaults; Scope "tenant"
	// takes everything in the tenant; Off reads no alerts (Settings S3a
	// §2.1). Alertmanager only until S3b: on Loki each is a load error.
	Source string `yaml:"source"`
	Scope  string `yaml:"scope"`
	Off    bool   `yaml:"off"`

	// keys are the keys the block wrote, so a key written with a zero
	// value is told apart from one left out (Codex's review of S3a-1, R1)
	keys []string
}

// UnmarshalYAML decodes the block and records the keys it wrote.
func (e *EndpointConfig) UnmarshalYAML(n *yaml.Node) error {
	type plain EndpointConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*e = EndpointConfig(p)
	e.keys = nil
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			e.keys = append(e.keys, n.Content[i].Value)
		}
	}
	return nil
}

// wrote reports whether the block wrote key, whatever its value.
func (e EndpointConfig) wrote(key string) bool { return slices.Contains(e.keys, key) }

// Located says the endpoint names where it is: a URL or a Service. An
// Alertmanager or metrics block without either is discovered in the
// cluster; Loki is never discovered, so its block needs one (Settings
// review slice 0).
func (e EndpointConfig) Located() bool { return e.Endpoint != "" || e.ServiceRef != nil }

// Spec is the metrics endpoint in the shared shape.
func (m MetricsConfig) Spec() EndpointConfig {
	return EndpointConfig{Endpoint: m.Endpoint, TLSSkipVerify: m.TLSSkipVerify, ServiceRef: m.ServiceRef, Auth: m.Auth, Tenant: m.Tenant}
}

// Endpoints are a cluster's configured endpoints by kind, in a fixed order.
func (c ClusterConfig) Endpoints() []NamedEndpoint {
	var out []NamedEndpoint
	if c.Metrics != nil {
		out = append(out, NamedEndpoint{Kind: EndpointMetrics, Spec: c.Metrics.Spec(), LegacyToken: c.Metrics.Token != ""})
	}
	if c.Alertmanager != nil {
		out = append(out, NamedEndpoint{Kind: EndpointAlertmanager, Spec: *c.Alertmanager})
	}
	if c.Loki != nil {
		out = append(out, NamedEndpoint{Kind: EndpointLoki, Spec: *c.Loki})
	}
	return out
}

// NamedEndpoint is one configured endpoint of a cluster.
type NamedEndpoint struct {
	Kind        string
	Spec        EndpointConfig
	LegacyToken bool // a plaintext metrics.token is in the file (C5 §6)
}

// EndpointURLProblem says why an endpoint URL cannot be used (C5 §2): it
// carries credentials, or it does not parse - in which case nothing in it
// can be trusted not to be a credential. Such an endpoint is unavailable,
// never sent to, and its text is never echoed: the message is fixed. ""
// when the URL is usable.
func EndpointURLProblem(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	switch {
	case err != nil:
		return "the endpoint URL does not parse; fix it in fleet.yaml"
	case u.User != nil:
		return "the endpoint URL carries credentials; set them in Settings > Observability"
	}
	return ""
}

// labelName is a label name Klyx can send as an equality matcher.
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// servicePath is a path prefix: path characters only, no query or fragment.
var servicePath = regexp.MustCompile(`^/?[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// validateEndpoint checks one endpoint block; prefix names it in errors.
func validateEndpoint(prefix string, e EndpointConfig, legacyToken bool) error {
	if sr := e.ServiceRef; sr != nil {
		if sr.Namespace == "" || sr.Name == "" || sr.Port == "" {
			return fmt.Errorf("%s.serviceRef requires namespace, name, and port", prefix)
		}
		if sr.Scheme != "" && sr.Scheme != "http" && sr.Scheme != "https" {
			return fmt.Errorf("%s.serviceRef.scheme must be http or https", prefix)
		}
		if !servicePath.MatchString(sr.Path) || strings.Contains("/"+sr.Path+"/", "/../") || strings.Contains("/"+sr.Path+"/", "/./") {
			return fmt.Errorf("%s.serviceRef.path must be a plain path prefix such as /alertmanager", prefix)
		}
	}
	names := make([]string, 0, len(e.Match))
	for k := range e.Match {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !labelName.MatchString(k) {
			return fmt.Errorf("%s.match: %s is not a label name", prefix, describeKey(k))
		}
		if v := e.Match[k]; v == "" || hasControl(v) {
			return fmt.Errorf("%s.match: %s needs a non-empty value without control characters", prefix, describeKey(k))
		}
	}
	if a := e.Auth; a != nil {
		switch a.Type {
		case "bearer":
			if a.Username != "" {
				return fmt.Errorf("%s.auth.username is for basic auth only", prefix)
			}
		case "basic":
			if a.Username == "" {
				return fmt.Errorf("%s.auth.username is required for basic auth", prefix)
			}
			if legacyToken {
				return fmt.Errorf("%s.token is a bearer token; it cannot be used with basic auth", prefix)
			}
		default:
			return fmt.Errorf("%s.auth.type must be bearer or basic", prefix)
		}
	}
	// a header value: no line breaks or other control characters
	if hasControl(e.Tenant) {
		return fmt.Errorf("%s.tenant must not contain control characters", prefix)
	}
	return nil
}

// endpointWarnings are an endpoint's non-fatal problems (C5 §2).
func endpointWarnings(cluster, prefix string, e EndpointConfig) []string {
	var w []string
	if e.Endpoint != "" && e.ServiceRef != nil {
		w = append(w, fmt.Sprintf("cluster %q: %s.serviceRef is ignored because %s.endpoint is set", cluster, prefix, prefix))
	}
	if e.Endpoint == "" && e.Auth != nil {
		w = append(w, fmt.Sprintf("cluster %q: %s.auth is not sent to a proxied or discovered endpoint; external credentials never ride the Kubernetes API server's authentication", cluster, prefix))
	}
	if p := EndpointURLProblem(e.Endpoint); p != "" {
		w = append(w, fmt.Sprintf("cluster %q: %s: %s", cluster, prefix, p))
	}
	if prefix == EndpointLoki && !e.Located() {
		w = append(w, fmt.Sprintf("cluster %q: loki has no endpoint or serviceRef: Klyx does not discover Loki, so it reads no log history for this cluster", cluster))
	}
	return w
}

// sharedWithoutMatch warns when two clusters read the same Alertmanager or
// Loki endpoint and one of them has no match (coverage S8a §2): without
// match, every alert or stream there is read as that cluster's own.
func sharedWithoutMatch(clusters []ClusterConfig) []string {
	type key struct{ kind, at string }
	users := map[key][]string{}
	unmatched := map[key]bool{}
	var order []key
	for _, cl := range clusters {
		for _, e := range cl.Endpoints() {
			if e.Kind == EndpointMetrics || e.Spec.Endpoint == "" || EndpointURLProblem(e.Spec.Endpoint) != "" {
				continue
			}
			u, err := url.Parse(strings.TrimRight(e.Spec.Endpoint, "/"))
			if err != nil {
				continue
			}
			origin, err := safehttp.CanonicalOrigin(u)
			if err != nil {
				continue
			}
			k := key{e.Kind, origin + u.Path} // the origin canonical, the path as written
			if _, seen := users[k]; !seen {
				order = append(order, k)
			}
			users[k] = append(users[k], cl.Name)
			if len(e.Spec.Match) == 0 {
				unmatched[k] = true
			}
		}
	}
	var w []string
	for _, k := range order {
		if len(users[k]) > 1 && unmatched[k] {
			w = append(w, fmt.Sprintf("clusters %s read the same %s endpoint, and at least one has no %s.match: without it, everything there is read as that cluster's own", strings.Join(users[k], ", "), k.kind, k.kind))
		}
	}
	return w
}
