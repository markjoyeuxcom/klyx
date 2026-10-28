// Package hubble reduces Cilium Hubble's PROMETHEUS metrics into a flow/drop
// triage readout. Deliberately metrics-based, not a Hubble Relay gRPC client:
// the owner's LGTM stack already scrapes hubble-metrics (:9965), Klyx already
// carries the Prometheus transport, and the routemetrics Source seam documented
// this as the Cilium slot. Flow-level tailing (hubble observe) stays a CLI
// escape hatch.
//
// Every Hubble metric set is OPT-IN in the Cilium config (hubble.metrics:
// flow, drop, dns, http, ...), so each section renders only when its series
// exist - capability detection over assumption - and "nothing at all" produces
// an honest enable-hint, never a silent empty panel.
package hubble

import (
	"context"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/metrics"
)

// Hubble label names, as the registered templates read them (Settings S3b
// §3.3: the queries are metrics' templates). Names match Cilium's hubble
// metrics exporter.
const (
	lVerdict = metrics.HubbleVerdict
	lReason  = metrics.HubbleReason
	lProto   = metrics.HubbleProto
	lRcode   = metrics.HubbleRcode
	lStatus  = metrics.HubbleStatus

	verdictForwarded = "FORWARDED"
	verdictDropped   = "DROPPED"
	dnsRcodeOK       = "No Error"
)

// ReasonRate is one drop reason's rate (flows/s over the rate window).
type ReasonRate struct {
	Reason   string
	Protocol string
	Rate     float64
}

// FlowStats is the triage readout. Nil pointers mean "no usable series" (that
// metric set is not enabled/scraped) and render as absent - never a fabricated
// zero. Rates are flows- or requests-per-second over the rate window.
type FlowStats struct {
	ForwardRate   *float64
	DropRate      *float64
	DropReasons   []ReasonRate
	DNSErrorRate  *float64
	DNSTotalRate  *float64
	HTTPErrorRate *float64
	HTTPTotalRate *float64
}

// Empty reports whether no Hubble series produced any section.
func (f FlowStats) Empty() bool {
	return f.ForwardRate == nil && f.DropRate == nil && len(f.DropReasons) == 0 &&
		f.DNSTotalRate == nil && f.HTTPTotalRate == nil
}

// Status mirrors the routemetrics status discipline: Available=false carries
// the unavailable reason; Available=true may carry an informational note.
type Status struct {
	Available bool
	Message   string
	UpdatedAt time.Time
	// Sentence says where the metrics came from and what makes them this
	// cluster's; Lineage keys that source (Settings S3b §3.2, §3.4)
	Sentence string
	Lineage  string
	// NoSeries: the read answered with no series at all (S3b §3.3)
	NoSeries bool
}

// Source reduces Hubble metrics from a Prometheus client.
type Source struct{ client *metrics.Client }

func NewSource(c *metrics.Client) *Source { return &Source{client: c} }

// QueryFlowStats fetches the four metric sets. A transport-level error fails
// the whole query (degraded, not empty); an individual EMPTY result just leaves
// its section nil (that metric set is not enabled).
func (s *Source) QueryFlowStats(ctx context.Context) (FlowStats, Status, error) {
	out := FlowStats{}

	verdicts, err := s.client.InstantVector(ctx, metrics.HubbleVerdicts.With(metrics.None{}))
	if err != nil {
		return FlowStats{}, Status{}, err
	}
	for _, v := range verdicts {
		val := v.Value
		switch v.Labels[lVerdict] {
		case verdictForwarded:
			out.ForwardRate = &val
		case verdictDropped:
			out.DropRate = &val
		}
	}

	drops, err := s.client.InstantVector(ctx, metrics.HubbleDropReasons.With(metrics.None{}))
	if err != nil {
		return FlowStats{}, Status{}, err
	}
	for _, d := range drops {
		if d.Value <= 0 {
			continue // a zero-rate reason is settled history, not triage signal
		}
		out.DropReasons = append(out.DropReasons, ReasonRate{
			// shown as written, less any credential the endpoint echoed
			Reason: s.client.Display(d.Labels[lReason]), Protocol: s.client.Display(d.Labels[lProto]), Rate: d.Value,
		})
	}
	sort.Slice(out.DropReasons, func(i, j int) bool { return out.DropReasons[i].Rate > out.DropReasons[j].Rate })

	dns, err := s.client.InstantVector(ctx, metrics.HubbleDNSRcodes.With(metrics.None{}))
	if err != nil {
		return FlowStats{}, Status{}, err
	}
	if len(dns) > 0 {
		total, errs := 0.0, 0.0
		for _, d := range dns {
			total += d.Value
			if d.Labels[lRcode] != dnsRcodeOK {
				errs += d.Value
			}
		}
		out.DNSTotalRate, out.DNSErrorRate = &total, &errs
	}

	http, err := s.client.InstantVector(ctx, metrics.HubbleHTTPStatus.With(metrics.None{}))
	if err != nil {
		return FlowStats{}, Status{}, err
	}
	if len(http) > 0 {
		total, errs := 0.0, 0.0
		for _, h := range http {
			total += h.Value
			if len(h.Labels[lStatus]) > 0 && h.Labels[lStatus][0] == '5' {
				errs += h.Value
			}
		}
		out.HTTPTotalRate, out.HTTPErrorRate = &total, &errs
	}

	if out.Empty() {
		return out, Status{Available: true, NoSeries: true, Message: "no Hubble metric series found - enable hubble.metrics (drop, flow, dns, http) in the Cilium config and scrape hubble-metrics (:9965)"}, nil
	}
	return out, Status{Available: true}, nil
}
