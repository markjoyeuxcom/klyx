package appbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/obslog"
	"github.com/moomora/klyx/internal/routemetrics"
)

// The Line slice 7c: per-rule points attributed from the rule history - a
// point is the rule's only at or after its interval's cut-off.

type fakeRuleConn struct {
	fakeGatewayConn
	res routemetrics.GatewayRules
	err error
	ctx context.Context
}

func (f *fakeRuleConn) RuleMetrics(ctx context.Context, _, _ string) (routemetrics.GatewayRules, error) {
	f.ctx = ctx
	return f.res, f.err
}

// Design review of 1078084: the caller's context reaches the read, so
// cancelling the generated call stops it; a missing endpoint is flagged.
func TestGetRuleMetricsCallerContext(t *testing.T) {
	conn := &fakeRuleConn{res: routemetrics.GatewayRules{Status: routemetrics.Status{NoEndpoint: true}}}
	s := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	ctx, cancel := context.WithCancel(context.Background())
	dto := s.GetRuleMetrics(ctx, "prd", "infra", "eg")
	cancel()
	if conn.ctx.Err() == nil {
		t.Fatal("the caller's cancellation does not reach the read")
	}
	if !dto.NoEndpoint {
		t.Fatal("noEndpoint lost")
	}
}

func rulePts(ts ...int64) []metrics.Point {
	out := make([]metrics.Point, len(ts))
	for i, t := range ts {
		out[i] = metrics.Point{Unix: t, Value: float64(i + 1)}
	}
	return out
}

func TestGetRuleMetricsAttribution(t *testing.T) {
	end := time.Unix(1792080000, 0)
	avail := func(ps []metrics.Point) routemetrics.Series {
		return routemetrics.Series{Availability: routemetrics.MetricAvailable, Points: ps}
	}
	pts := rulePts(1792079910, 1792079940, 1792079970, 1792080000)
	res := routemetrics.GatewayRules{Status: routemetrics.Status{Available: true, UpdatedAt: end}, Scope: routemetrics.ScopeOwningGateway,
		Start: end.Add(-30 * time.Minute), End: end, BackendSplit: routemetrics.NoteBackendSplit, Notes: []string{routemetrics.NoteDirectResponses},
		Rules: []routemetrics.RuleSeries{
			{Namespace: "apps", Name: "web", UID: "u1", Rule: 0, Fingerprint: "fp0", Requests: avail(pts), Retries: avail(pts), Share503: avail(pts),
				Classes: routemetrics.ClassSeries{Availability: routemetrics.MetricAvailable, Classes: map[string][]metrics.Point{"2xx": pts}}},
			{Namespace: "apps", Name: "web", UID: "u1", Rule: 1, Fingerprint: "fp1", Requests: avail(pts)},
			{Namespace: "apps", Name: "twice", UID: "u2", Rule: 0, Fingerprint: "fp2", Unattributable: routemetrics.ReasonSameGateway},
		}}
	conn := &fakeRuleConn{res: res}
	s := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	var asked []string
	SetRuleIntervals(s, func(cluster, uid string, index int, fp string) (RuleInterval, bool) {
		asked = append(asked, cluster+"/"+uid+"/"+fp)
		if uid == "u1" && index == 0 && fp == "fp0" {
			// The cut-off falls between points, with sub-second precision:
			// the point at :40 is before it, the one at :70 after.
			start := time.Unix(1792079880, 250_000_000)
			return RuleInterval{Start: start, Cutoff: start.Add(time.Minute), Basis: obslog.ProofObserved}, true
		}
		return RuleInterval{}, false
	})
	dto := s.GetRuleMetrics(context.Background(), "prd", "infra", "eg")
	if !dto.Status.Available || dto.Scope != "owning-gateway" || dto.StepSeconds != 30 || dto.RateSeconds != 60 || len(dto.Rules) != 3 {
		t.Fatalf("dto: %+v", dto)
	}
	if dto.Start != rfc3339(end.Add(-30*time.Minute)) || dto.End != rfc3339(end) || dto.Status.UpdatedAt != rfc3339(end) {
		t.Fatalf("window %q..%q updated %q", dto.Start, dto.End, dto.Status.UpdatedAt)
	}
	r0 := dto.Rules[0]
	if !r0.Attribution.Proven || r0.Attribution.Basis != obslog.ProofObserved || r0.Attribution.Cutoff == "" {
		t.Fatalf("attribution: %+v", r0.Attribution)
	}
	if len(r0.Requests.Points) != 2 || r0.Requests.Points[0].T != 1792079970 {
		t.Fatalf("requests kept: %+v", r0.Requests.Points)
	}
	if len(r0.Classes.Classes["2xx"]) != 2 || len(r0.Retries.Points) != 2 || len(r0.Share503.Points) != 2 {
		t.Fatalf("every series attributed alike: %+v", r0)
	}
	if r0.Backends == nil || len(r0.Backends) != 0 {
		t.Fatalf("backends: %+v", r0.Backends)
	}
	// No proven interval: available, but no point is this rule's.
	r1 := dto.Rules[1]
	if r1.Attribution.Proven || r1.Requests.Availability != "available" || r1.Requests.Points == nil || len(r1.Requests.Points) != 0 {
		t.Fatalf("unproven: %+v", r1)
	}
	if r2 := dto.Rules[2]; r2.Unattributable != routemetrics.ReasonSameGateway || r2.Requests.Availability != "" {
		t.Fatalf("unattributable: %+v", r2)
	}
	if len(asked) != 3 || asked[0] != "prd/u1/fp0" {
		t.Fatalf("intervals asked: %v", asked)
	}

	// Without the history wired, nothing is attributable.
	s2 := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	if d := s2.GetRuleMetrics(context.Background(), "prd", "infra", "eg"); d.Rules[0].Attribution.Proven || len(d.Rules[0].Requests.Points) != 0 {
		t.Fatalf("no history: %+v", d.Rules[0])
	}
	// An unreadable Gateway, and an unknown cluster.
	conn.err = errors.New("get gateway infra/eg: not found")
	if d := s.GetRuleMetrics(context.Background(), "prd", "infra", "eg"); d.Error == "" || d.Status.Available || d.Rules == nil {
		t.Fatalf("error: %+v", d)
	}
	s3 := NewGatewayService(func(string) (GatewayConn, bool) { return nil, false })
	if d := s3.GetRuleMetrics(context.Background(), "x", "infra", "eg"); d.Status.Message != "cluster not connected" {
		t.Fatalf("not connected: %+v", d)
	}
}

func TestCeilUnix(t *testing.T) {
	if ceilUnix(time.Unix(100, 0)) != 100 || ceilUnix(time.Unix(100, 1)) != 101 {
		t.Fatal("ceilUnix")
	}
}

// The Line slice 8d: each route carries the unit its labels name now, with
// that unit's applied value when the rows name a single unit.
type fakeApplied struct{}

func (fakeApplied) UnitFor(cluster, kind, ns, name string) (string, bool) {
	if name == "apps" {
		return "u-apps", true
	}
	return "", false
}

func (fakeApplied) Change(cluster, uid string) (AppliedChange, bool) {
	at := time.Unix(1792079000, 0)
	return AppliedChange{Kind: "git", Value: strings.Repeat("a", 40), ObservedAt: &at, Basis: obslog.ProofObserved, LiveSince: at.Add(-time.Hour)}, true
}

func TestRuleMetricsRouteUnits(t *testing.T) {
	res := routemetrics.GatewayRules{Status: routemetrics.Status{Available: true}, Routes: []routemetrics.RouteRules{
		{Namespace: "shop", Name: "web", UID: "r1", Unit: &routemetrics.UnitRef{Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}},
		{Namespace: "shop", Name: "api", UID: "r2", Unit: &routemetrics.UnitRef{Kind: "Kustomization", Namespace: "flux-system", Name: "twice"}},
		{Namespace: "shop", Name: "raw", UID: "r3"},
	}}
	conn := &fakeRuleConn{res: res}
	s := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	SetAppliedChanges(s, fakeApplied{})
	dto := s.GetRuleMetrics(context.Background(), "prd", "infra", "eg")
	if len(dto.Routes) != 3 {
		t.Fatalf("routes: %+v", dto.Routes)
	}
	web := dto.Routes[0].Unit
	if web == nil || web.UID != "u-apps" || web.Applied == nil || web.Applied.Kind != "git" || web.Applied.Basis != obslog.ProofObserved || web.Applied.ObservedAt == "" || web.Applied.LiveSince == "" {
		t.Fatalf("web's unit: %+v", web)
	}
	if api := dto.Routes[1].Unit; api == nil || api.UID != "" || api.Applied != nil {
		t.Fatalf("a unit the rows do not name once: %+v", api)
	}
	if dto.Routes[2].Unit != nil {
		t.Fatalf("a route with no Flux labels: %+v", dto.Routes[2].Unit)
	}
	// Without the history wired, the unit is named and nothing more.
	s2 := NewGatewayService(func(string) (GatewayConn, bool) { return conn, true })
	if u := s2.GetRuleMetrics(context.Background(), "prd", "infra", "eg").Routes[0].Unit; u == nil || u.UID != "" || u.Applied != nil {
		t.Fatalf("no history: %+v", u)
	}
}
