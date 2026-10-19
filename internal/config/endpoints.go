package config

import (
	"fmt"
	"net/url"
	"strings"
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
}

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
		return "the endpoint URL carries credentials; set them in Settings > External endpoints"
	}
	return ""
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
	if strings.IndexFunc(e.Tenant, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
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
	return w
}
