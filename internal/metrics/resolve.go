package metrics

import (
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
