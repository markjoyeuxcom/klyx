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
// this rule's. With no proven interval, no point is. Start is when Klyx
// observed the rule in the API; when Envoy took it up is not verified.
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

// RouteUnitDTO is the Flux unit a route's labels say manages it now, with
// its applied value (slice 8d's revision proximity). It claims nothing about
// when the unit began to manage the route.
type RouteUnitDTO struct {
	Kind      string      `json:"kind"`
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	UID       string      `json:"uid,omitempty"` // "" when the rows do not name a single unit
	Applied   *AppliedDTO `json:"applied,omitempty"`
}

// AppliedDTO is a unit's typed applied value and when its change was
// observed: observedAt only for a known value that changed to another in
// Klyx's sight (basis observed); liveSince is the coverage span's start.
type AppliedDTO struct {
	Kind       string `json:"kind"` // git | artifact | chart
	Value      string `json:"value"`
	ObservedAt string `json:"observedAt,omitempty"`
	Basis      string `json:"basis"` // observed | unknown
	LiveSince  string `json:"liveSince"`
}

// RuleRouteDTO is one route the read covered.
type RuleRouteDTO struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	UID       string        `json:"uid"`
	Unit      *RouteUnitDTO `json:"unit,omitempty"`
}

// RuleMetricsDTO is one Gateway's per-rule read.
type RuleMetricsDTO struct {
	Status         RouteMetricsStatusDTO `json:"status"`
	NoEndpoint     bool                  `json:"noEndpoint"` // no metrics endpoint configured or discovered
	Scope          string                `json:"scope"`      // owning-gateway | pod | kubernetes-pod | "" (nothing scoped)
	ScopeNote      string                `json:"scopeNote,omitempty"`
	Unattributable string                `json:"unattributable,omitempty"` // Gateway-wide
	Start          string                `json:"start"`                    // RFC3339 window
	End            string                `json:"end"`
	StepSeconds    int                   `json:"stepSeconds"`
	RateSeconds    int                   `json:"rateSeconds"`
	Routes         []RuleRouteDTO        `json:"routes"`
	Rules          []RuleMetricDTO       `json:"rules"`
	BackendSplit   string                `json:"backendSplit"`
	Notes          []string              `json:"notes"`
	Error          string                `json:"error,omitempty"` // the Gateway could not be read
}

// appliedSource is what the data path reads from the applied history (8d).
type appliedSource interface {
	UnitFor(cluster, kind, ns, name string) (string, bool)
	Change(cluster, uid string) (AppliedChange, bool)
}

// SetAppliedChanges wires the applied-revision history (slice 8d). A package
// function, not a method, so it is not bound to the webview.
func SetAppliedChanges(s *GatewayService, h appliedSource) {
	s.applied = h
}

// SetRuleIntervals wires the rule history (7b) that attributes per-rule
// points; without it no point is attributable. A package function, not a
// method, so it is not bound to the webview.
func SetRuleIntervals(s *GatewayService, f func(cluster, uid string, index int, fingerprint string) (RuleInterval, bool)) {
	s.ruleIntervals = f
}

// GetRuleMetrics returns one Gateway's per-rule metrics, attributed. On
// demand; the route-level metrics the topology shows are unchanged.
func (s *GatewayService) GetRuleMetrics(ctx context.Context, cluster, namespace, name string) RuleMetricsDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return RuleMetricsDTO{Status: RouteMetricsStatusDTO{Message: "cluster not connected"}, Routes: []RuleRouteDTO{}, Rules: []RuleMetricDTO{}, Notes: []string{}}
	}
	// the caller's context: cancelling the generated call stops the reads
	ctx, cancel := context.WithTimeout(ctx, gatewayTimeout)
	defer cancel()
	res, err := conn.RuleMetrics(ctx, namespace, name)
	if err != nil {
		return RuleMetricsDTO{Status: RouteMetricsStatusDTO{Message: err.Error()}, Routes: []RuleRouteDTO{}, Rules: []RuleMetricDTO{}, Notes: []string{}, Error: err.Error()}
	}
	return s.ruleMetricsDTO(cluster, res)
}

func (s *GatewayService) ruleMetricsDTO(cluster string, res routemetrics.GatewayRules) RuleMetricsDTO {
	dto := RuleMetricsDTO{
		Status:         RouteMetricsStatusDTO{Available: res.Status.Available, Message: res.Status.Message, UpdatedAt: rfc3339(res.Status.UpdatedAt), Sentence: res.Status.Sentence, Lineage: res.Status.Lineage},
		NoEndpoint:     res.Status.NoEndpoint,
		Scope:          string(res.Scope),
		ScopeNote:      res.ScopeNote,
		Unattributable: res.Unattributable,
		Start:          rfc3339(res.Start),
		End:            rfc3339(res.End),
		StepSeconds:    int(routemetrics.RuleStep / time.Second),
		RateSeconds:    int(routemetrics.RuleRate / time.Second),
		Routes:         make([]RuleRouteDTO, 0, len(res.Routes)),
		Rules:          make([]RuleMetricDTO, 0, len(res.Rules)),
		BackendSplit:   res.BackendSplit,
		Notes:          append([]string{}, res.Notes...),
	}
	for _, r := range res.Routes {
		rd := RuleRouteDTO{Namespace: r.Namespace, Name: r.Name, UID: r.UID}
		if u := r.Unit; u != nil {
			ud := &RouteUnitDTO{Kind: u.Kind, Namespace: u.Namespace, Name: u.Name}
			if s.applied != nil {
				if uid, ok := s.applied.UnitFor(cluster, u.Kind, u.Namespace, u.Name); ok {
					ud.UID = uid
					if c, ok := s.applied.Change(cluster, uid); ok {
						ad := &AppliedDTO{Kind: c.Kind, Value: c.Value, Basis: c.Basis, LiveSince: rfc3339(c.LiveSince)}
						if c.ObservedAt != nil {
							ad.ObservedAt = rfc3339(*c.ObservedAt)
						}
						ud.Applied = ad
					}
				}
			}
			rd.Unit = ud
		}
		dto.Routes = append(dto.Routes, rd)
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
