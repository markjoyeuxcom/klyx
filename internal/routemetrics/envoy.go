package routemetrics

import (
	"context"

	"github.com/moomora/klyx/internal/metrics"
)

// Envoy metric and label names, as the registered templates read them
// (Settings S3b §3.3: the queries are metrics' templates).
const (
	mRqTotal     = metrics.EnvoyRqTotal
	mRqXX        = metrics.EnvoyRqXX
	lClusterName = metrics.EnvoyClusterName
	lRespClass   = metrics.EnvoyRespClass
)

// EnvoyClusterSource builds Envoy-cluster PromQL and reduces the result into
// per-route metrics. Pure of clocks; the fleet layer stamps Status.UpdatedAt.
type EnvoyClusterSource struct{ client *metrics.Client }

func NewEnvoyClusterSource(c *metrics.Client) *EnvoyClusterSource {
	return &EnvoyClusterSource{client: c}
}

func (s *EnvoyClusterSource) QueryRouteMetrics(ctx context.Context, routeKeys []string) (map[string]RouteMetrics, Status, error) {
	if len(routeKeys) == 0 {
		return map[string]RouteMetrics{}, Status{Available: true}, nil
	}
	routes := metrics.Routes{Keys: routeKeys}

	// seen counts the series the answers held, a non-finite one included,
	// and usable those with a value: an answer whose series all lack one is
	// not an answer with none (Settings S3b §3.3)
	seen, usable := 0, 0
	vector := func(q metrics.Query) ([]metrics.LabeledSample, error) {
		v, n, err := s.client.InstantVectorSeen(ctx, q)
		seen, usable = seen+n, usable+len(v)
		return v, err
	}
	rps, err := vector(metrics.RouteRPS.With(routes))
	if err != nil {
		return nil, Status{}, err
	}
	p50, err := vector(metrics.RouteP50.With(routes))
	if err != nil {
		return nil, Status{}, err
	}
	p99, err := vector(metrics.RouteP99.With(routes))
	if err != nil {
		return nil, Status{}, err
	}
	rq5xx, err := vector(metrics.Route5xx.With(routes))
	if err != nil {
		return nil, Status{}, err
	}
	rqall, err := vector(metrics.RouteAll.With(routes))
	if err != nil {
		return nil, Status{}, err
	}

	out := reduce(rps, p50, p99, rq5xx, rqall)
	if len(out) > 0 {
		return out, Status{Available: true}, nil
	}
	if seen > 0 && usable == 0 {
		return out, Status{Available: true, Message: msgNoUsableRouteValue}, nil
	}

	// No matched series: distinguish "Envoy not scraped at all" from "these
	// routes are just idle / mislabeled". Reuse the verified rq-total constant.
	// The existence probe counts every Envoy rq_total series, not just
	// httproute's, under the reader's scope (Settings S3b §3.3): it answers
	// "is Envoy scraped for this cluster at all"; its wording under a scope
	// is S3b-2c's.
	exist, err := s.client.InstantScalar(ctx, metrics.EnvoyScraped.With(metrics.None{}))
	if err != nil {
		return nil, Status{}, err
	}
	// Real Prometheus count() over no series returns an empty vector
	// (-> Empty); the ==0 check is defensive against a malformed response. A
	// count with no usable value proves no absence (S3b §3.3).
	switch {
	case exist.Empty || (!exist.Absent && exist.Value == 0):
		return out, Status{Available: false, NoSeries: true, Message: "no envoy_cluster_* series found"}, nil
	case exist.Absent:
		return out, Status{Available: true, Message: msgNoUsableEnvoyCount}, nil
	}
	return out, Status{Available: true, Message: "no route series matched this topology"}, nil
}

// What a read whose series have no usable value says: never that it found
// none (Settings S3b §3.3).
const (
	msgNoUsableRouteValue = "the route series have no usable value (NaN or Inf)"
	msgNoUsableEnvoyCount = "no route series matched this topology, and the check for Envoy's series has no usable value (NaN or Inf)"
)

type acc struct {
	rps     float64
	rpsHas  bool
	p50     float64
	p50Has  bool
	p99     float64
	p99Has  bool
	f5xx    float64
	fall    float64
	fallHas bool
}

// reduce maps per-rule vector elements to per-route metrics: rps summed across
// rules; p50/p99 max across rules (worst-rule tail); err = sum(5xx)/sum(all)
// divided ONCE per route (never averaging per-rule fractions). A route is
// "measured" iff it has an rps element (the counter exists even at 0 traffic).
func reduce(rps, p50, p99, rq5xx, rqall []metrics.LabeledSample) map[string]RouteMetrics {
	accs := map[string]*acc{}
	get := func(k string) *acc {
		a := accs[k]
		if a == nil {
			a = &acc{}
			accs[k] = a
		}
		return a
	}
	for _, s := range rps {
		if k, ok := parseClusterName(s.Labels[lClusterName]); ok {
			a := get(k)
			a.rps += s.Value
			a.rpsHas = true
		}
	}
	// p50/p99 are max across a route's rules (worst-rule tail). NOTE: because
	// the two quantiles are reduced independently, a multi-rule route can report
	// p50 > p99 when the high-p50 rule differs from the high-p99 rule. Single-rule
	// routes (the common case) can never invert. A bucket-level re-aggregation
	// (route-level histogram_quantile) would fix it but needs a query redesign.
	for _, s := range p50 {
		if k, ok := parseClusterName(s.Labels[lClusterName]); ok {
			a := get(k)
			if !a.p50Has || s.Value > a.p50 {
				a.p50 = s.Value
				a.p50Has = true
			}
		}
	}
	for _, s := range p99 {
		if k, ok := parseClusterName(s.Labels[lClusterName]); ok {
			a := get(k)
			if !a.p99Has || s.Value > a.p99 {
				a.p99 = s.Value
				a.p99Has = true
			}
		}
	}
	for _, s := range rq5xx {
		if k, ok := parseClusterName(s.Labels[lClusterName]); ok {
			get(k).f5xx += s.Value
		}
	}
	for _, s := range rqall {
		if k, ok := parseClusterName(s.Labels[lClusterName]); ok {
			a := get(k)
			a.fall += s.Value
			a.fallHas = true
		}
	}

	out := make(map[string]RouteMetrics, len(accs))
	for k, a := range accs {
		if !a.rpsHas {
			continue // not measured without an rps series
		}
		rm := RouteMetrics{}
		v := a.rps
		rm.RPS = &v
		if a.p50Has {
			p := a.p50
			rm.P50 = &p
		}
		if a.p99Has {
			p := a.p99
			rm.P99 = &p
		}
		if a.fallHas && a.fall > 0 {
			e := a.f5xx / a.fall
			if e > 1 { // 5xx rate can momentarily exceed total under scrape skew / counter resets
				e = 1
			}
			rm.ErrRate = &e
		}
		out[k] = rm
	}
	return out
}
