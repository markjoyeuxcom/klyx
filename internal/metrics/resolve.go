package metrics

import (
	"net/url"
	"strings"

	"github.com/moomora/klyx/internal/config"
)

func schemeOr(s string) string {
	if s == "" {
		return "http"
	}
	return s
}

func sourceStr(c ServiceCandidate) string {
	return c.Namespace + "/" + c.Name + ":" + c.Port
}

// Resolve applies the 4-tier priority: endpoint -> serviceRef -> discovery ->
// unavailable. disco is the single reduced discovery outcome.
func Resolve(cfg config.MetricsConfig, disco DiscoveryResult, tf TransportFactory) Resolution {
	if cfg.Endpoint != "" {
		// an endpoint URL carrying credentials is refused for use, and
		// never shown with them (coverage C5 §2)
		if p := config.UserinfoProblem(cfg.Endpoint); p != "" {
			return Resolution{Mode: ModeUnavailable, Source: withoutUserinfo(cfg.Endpoint), Reason: p}
		}
		base := strings.TrimRight(cfg.Endpoint, "/")
		warn := ""
		if cfg.ServiceRef != nil {
			warn = "serviceRef ignored because endpoint is set"
		}
		return Resolution{Mode: ModeExplicitEndpoint, Source: base, Transport: tf.Direct(base, cfg.Token, cfg.TLSSkipVerify), Warning: warn}
	}
	if sr := cfg.ServiceRef; sr != nil {
		c := ServiceCandidate{Namespace: sr.Namespace, Name: sr.Name, Port: sr.Port}
		c.Scheme = schemeOr(sr.Scheme)
		return Resolution{Mode: ModeExplicitService, Source: sourceStr(c), Transport: tf.Proxy(c)}
	}
	if disco.MultiMatch {
		return Resolution{Mode: ModeUnavailable, Reason: "multiple candidate Services found, set metrics.serviceRef"}
	}
	if disco.Chosen != nil {
		c := *disco.Chosen
		c.Scheme = schemeOr(c.Scheme)
		return Resolution{Mode: ModeDiscovered, Source: sourceStr(c), Transport: tf.Proxy(c)}
	}
	return Resolution{Mode: ModeUnavailable, Reason: ReasonNoService}
}

// ReasonNoService: no endpoint or Service is configured and discovery found
// none - the one case where the remedy is to configure a metrics endpoint.
const ReasonNoService = "no Prometheus Service found"

// withoutUserinfo is an endpoint URL with any credentials it carries removed.
func withoutUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return strings.TrimRight(u.String(), "/")
}
