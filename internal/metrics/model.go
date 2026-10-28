// Package metrics owns the Prometheus query data path: PromQL instant queries,
// response parsing, transport selection, endpoint resolution, and
// probe-confirmed capability. Pure of client-go except the proxy transport.
package metrics

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/safehttp"
)

// Sample is one scalar value from an instant query. Absent reports "no data".
type Sample struct {
	Value  float64
	Absent bool
}

// Querier executes a PromQL instant query and returns the HTTP status and body.
// Transports implement it; the Client parses. Outside this package a
// Querier is only implemented (a test's fake), never sent to: transports
// travel as a Transport, which only a Client sends through (Settings S3b
// §3.3).
type Querier interface {
	InstantQuery(ctx context.Context, promql string) (status int, body []byte, err error)
}

// Transport is a transport, opaque: its requests take PromQL text, so only
// a Client sends through it, rendering a registered template under its own
// scope (Settings S3b §3.3). The zero Transport is none.
type Transport struct{ q Querier }

// TransportOf makes q a Transport: a test's fake. No production file
// outside this package calls it, and none writes the Prometheus query
// API's paths, so a Querier written elsewhere never reaches Prometheus
// (TestNoOtherPackageWritesThePrometheusQueryAPI; Codex's re-review of
// S3b-2a, RR2).
func TransportOf(q Querier) Transport { return Transport{q: q} }

// FailedTransport is a transport whose every request fails with err,
// sending nothing: a connection that could not build its proxy client
// reads as unavailable with the reason, never through an unguarded one.
func FailedTransport(err error) Transport { return Transport{q: failed{err: err}} }

type failed struct{ err error }

func (f failed) InstantQuery(context.Context, string) (int, []byte, error) { return 0, nil, f.err }

func (f failed) RangeQuery(context.Context, string, time.Time, time.Time, time.Duration) (int, []byte, error) {
	return 0, nil, f.err
}

// IsZero: no transport.
func (t Transport) IsZero() bool { return t.q == nil }

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
	Transport Transport
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
	Direct(base string, auth Auth, tlsSkipVerify bool) Transport
	Proxy(c ServiceCandidate, tenant string) Transport
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
