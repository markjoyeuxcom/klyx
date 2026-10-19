// Package metrics owns the Prometheus query data path: PromQL instant queries,
// response parsing, transport selection, endpoint resolution, and
// probe-confirmed capability. Pure of client-go except the proxy transport.
package metrics

import (
	"context"

	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/safehttp"
)

// Sample is one scalar value from an instant query. Absent reports "no data".
type Sample struct {
	Value  float64
	Absent bool
}

// Querier executes a PromQL instant query and returns the HTTP status and body.
// Transports implement it; the Client parses.
type Querier interface {
	InstantQuery(ctx context.Context, promql string) (status int, body []byte, err error)
}

// Mode is how the connection was resolved.
type Mode string

const (
	ModeExplicitEndpoint Mode = "explicit-endpoint"
	ModeExplicitService  Mode = "explicit-service-ref"
	ModeDiscovered       Mode = "discovered-service"
	ModeUnavailable      Mode = "unavailable"
)

// ServiceCandidate is an in-cluster Prometheus Service to proxy to.
type ServiceCandidate struct {
	Namespace, Name, Port, Scheme string
	// Path is the prefix the Service serves the API under (S8a §2).
	Path string
}

// DiscoveryResult is the single reduced outcome of in-cluster discovery: at
// most one chosen candidate, or a multi-match signal (label fallback only).
type DiscoveryResult struct {
	Chosen     *ServiceCandidate
	MultiMatch bool
}

// Resolution is the resolved connection. Transport is nil when unavailable.
type Resolution struct {
	Mode      Mode
	Source    string // URL, or "ns/name:port" for service modes
	Transport Querier
	Warning   string // non-fatal context on a working connection
	Reason    string // why unavailable
}

// MetricsCapability is the probe-confirmed connection status handed to the UI.
type MetricsCapability struct {
	Available bool
	Mode      Mode
	Source    string
	Warning   string
	Reason    string
}

// ClusterMetrics is the proof-of-life readout. Nil pointers mean "no data",
// distinct from a real 0.
type ClusterMetrics struct {
	CPUFraction *float64
	MemFraction *float64
}

// TransportFactory builds transports. The fleet layer supplies the real one
// (it owns the cluster REST client); tests supply a fake.
type TransportFactory interface {
	Direct(base string, auth Auth, tlsSkipVerify bool) Querier
	Proxy(c ServiceCandidate, tenant string) Querier
}

// Auth is what a direct request to an endpoint carries (coverage C5 §3-§7);
// endpointhttp owns it for every endpoint kind.
type Auth = endpointhttp.Auth

// BearerAuth is a plain bearer token, redacted (tests, and the legacy path).
func BearerAuth(token string) Auth {
	a := Auth{Redactor: safehttp.NewRedactor(safehttp.Bearer(token))}
	if token != "" {
		a.Header = "Bearer " + token
	}
	return a
}
