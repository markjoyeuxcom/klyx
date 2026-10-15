package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/routemetrics"
)

// Per-rule route metrics (The Line slice 7c, slice 7 note §4): one Gateway's
// rules, each with four series, each attributed from the rule history (7b).
// A point is this rule's only after its interval's cut-off; earlier points
// are left out and the cut-off says from when. Slice 8's data path reads it.

// RuleSeriesDTO is one per-rule metric: available, absent (no series) or
// failed, apart from the others; "" when nothing was read (the rule or the
// Gateway unattributable, or the read unavailable). Points are the
// attributed ones only.
type RuleSeriesDTO struct {
	Availability string     `json:"availability"` // available | absent | failed
	Error        string     `json:"error,omitempty"`
	Points       []PointDTO `json:"points"`
}

// RuleClassesDTO is a rule's response-class shares, keyed "1xx".."5xx".
type RuleClassesDTO struct {
	Availability string                `json:"availability"`
	Error        string                `json:"error,omitempty"`
	Classes      map[string][]PointDTO `json:"classes"`
}

// RuleAttributionDTO is the rule's proven interval: Start is the proof (a
// fingerprint change, or the first sighting or after-gap when Basis is
// unknown) and Cutoff is one rate window later. Points before Cutoff are not
// this rule's. With no proven interval, no point is.
type RuleAttributionDTO struct {
	Proven bool   `json:"proven"`
	Start  string `json:"start,omitempty"`  // RFC3339
	Cutoff string `json:"cutoff,omitempty"` // RFC3339
	Basis  string `json:"basis,omitempty"`  // observed | unknown
}

// RuleBackendSeriesDTO is one backend's share of a rule, for gateways that
// expose one. Envoy Gateway does not (see BackendSplit): always empty there.
type RuleBackendSeriesDTO struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Requests  RuleSeriesDTO `json:"requests"`
}

// RuleMetricDTO is one rule of a route attached to the Gateway.
type RuleMetricDTO struct {
	Namespace      string                 `json:"namespace"`
	Name           string                 `json:"name"`
	UID            string                 `json:"uid"`
	Rule           int                    `json:"rule"`
	Fingerprint    string                 `json:"fingerprint"`
	Unattributable string                 `json:"unattributable,omitempty"` // the whole rule: nothing read
	Attribution    RuleAttributionDTO     `json:"attribution"`
	Requests       RuleSeriesDTO          `json:"requests"` // per second
	Classes        RuleClassesDTO         `json:"classes"`  // shares 0..1
	Share503       RuleSeriesDTO          `json:"share503"` // share 0..1
	Retries        RuleSeriesDTO          `json:"retries"`  // per second
	Backends       []RuleBackendSeriesDTO `json:"backends"`
}

// RuleMetricsDTO is one Gateway's per-rule read.
type RuleMetricsDTO struct {
	Status         RouteMetricsStatusDTO `json:"status"`
	Scope          string                `json:"scope"` // owning-gateway | pod | kubernetes-pod | "" (nothing scoped)
	ScopeNote      string                `json:"scopeNote,omitempty"`
	Unattributable string                `json:"unattributable,omitempty"` // Gateway-wide
	Start          string                `json:"start"`                    // RFC3339 window
	End            string                `json:"end"`
	StepSeconds    int                   `json:"stepSeconds"`
	RateSeconds    int                   `json:"rateSeconds"`
	Rules          []RuleMetricDTO       `json:"rules"`
	BackendSplit   string                `json:"backendSplit"`
	Notes          []string              `json:"notes"`
	Error          string                `json:"error,omitempty"` // the Gateway could not be read
}

// SetRuleIntervals wires the rule history (7b) that attributes per-rule
// points; without it no point is attributable. A package function, not a
// method, so it is not bound to the webview.
func SetRuleIntervals(s *GatewayService, f func(cluster, uid string, index int, fingerprint string) (RuleInterval, bool)) {
	s.ruleIntervals = f
}

// GetRuleMetrics returns one Gateway's per-rule metrics, attributed. On
// demand; the route-level metrics the topology shows are unchanged.
func (s *GatewayService) GetRuleMetrics(cluster, namespace, name string) RuleMetricsDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return RuleMetricsDTO{Status: RouteMetricsStatusDTO{Message: "cluster not connected"}, Rules: []RuleMetricDTO{}, Notes: []string{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayTimeout)
	defer cancel()
	res, err := conn.RuleMetrics(ctx, namespace, name)
	if err != nil {
		return RuleMetricsDTO{Status: RouteMetricsStatusDTO{Message: err.Error()}, Rules: []RuleMetricDTO{}, Notes: []string{}, Error: err.Error()}
	}
	return s.ruleMetricsDTO(cluster, res)
}

func (s *GatewayService) ruleMetricsDTO(cluster string, res routemetrics.GatewayRules) RuleMetricsDTO {
	dto := RuleMetricsDTO{
		Status:         RouteMetricsStatusDTO{Available: res.Status.Available, Message: res.Status.Message, UpdatedAt: rfc3339(res.Status.UpdatedAt)},
		Scope:          string(res.Scope),
		ScopeNote:      res.ScopeNote,
		Unattributable: res.Unattributable,
		Start:          rfc3339(res.Start),
		End:            rfc3339(res.End),
		StepSeconds:    int(routemetrics.RuleStep / time.Second),
		RateSeconds:    int(routemetrics.RuleRate / time.Second),
		Rules:          make([]RuleMetricDTO, 0, len(res.Rules)),
		BackendSplit:   res.BackendSplit,
		Notes:          append([]string{}, res.Notes...),
	}
	for _, r := range res.Rules {
		rd := RuleMetricDTO{Namespace: r.Namespace, Name: r.Name, UID: r.UID, Rule: r.Rule, Fingerprint: r.Fingerprint,
			Unattributable: r.Unattributable, Backends: []RuleBackendSeriesDTO{}}
		var cutoff int64
		proven := false
		if s.ruleIntervals != nil && r.UID != "" {
			if iv, ok := s.ruleIntervals(cluster, r.UID, r.Rule, r.Fingerprint); ok {
				proven = true
				rd.Attribution = RuleAttributionDTO{Proven: true, Start: rfc3339(iv.Start), Cutoff: rfc3339(iv.Cutoff), Basis: iv.Basis}
				cutoff = ceilUnix(iv.Cutoff)
			}
		}
		keep := func(ps []metrics.Point) []PointDTO {
			out := []PointDTO{}
			if !proven {
				return out
			}
			for _, p := range ps {
				if p.Unix >= cutoff {
					out = append(out, PointDTO{T: p.Unix, V: p.Value})
				}
			}
			return out
		}
		series := func(x routemetrics.Series) RuleSeriesDTO {
			return RuleSeriesDTO{Availability: string(x.Availability), Error: x.Error, Points: keep(x.Points)}
		}
		rd.Requests, rd.Share503, rd.Retries = series(r.Requests), series(r.Share503), series(r.Retries)
		rd.Classes = RuleClassesDTO{Availability: string(r.Classes.Availability), Error: r.Classes.Error, Classes: map[string][]PointDTO{}}
		for k, ps := range r.Classes.Classes {
			rd.Classes.Classes[k] = keep(ps)
		}
		dto.Rules = append(dto.Rules, rd)
	}
	return dto
}

// ceilUnix: a point at t (whole seconds) covers (t - rate window, t], so it
// is attributable when t is at or after the cut-off, rounded up.
func ceilUnix(t time.Time) int64 {
	u := t.Unix()
	if t.Nanosecond() > 0 {
		u++
	}
	return u
}
