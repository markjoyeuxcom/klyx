package appbridge

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/routemetrics"
)

// AI A1 §3.4: the route templates read the existing per-rule Envoy metrics
// (The Line slice 7c) of each Gateway the HTTPRoute names: its fixed window,
// its scoping to that Gateway's proxies, and its refusals (shared or
// renamed clusters, a proxy scraped twice, several clusters). A rule's
// series is its Envoy cluster's, named httproute/<ns>/<name>/rule/<n>: by
// name. Only rules of the route Klyx read now, by its UID, are kept, so a
// rule's index is today's spec's.

const (
	chatRouteGatewaysMax = 3
	chat5xxThreshold     = 0.05 // a rule whose 5xx share reached 5%
)

var gatewayAPIGroup = "gateway.networking.k8s.io"

type gatewayRef struct{ ns, name string }

// parentGateways are the Gateways an HTTPRoute's parentRefs name, once each.
func parentGateways(route *unstructured.Unstructured) []gatewayRef {
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	seen := map[gatewayRef]bool{}
	var out []gatewayRef
	for _, p := range parents {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if g, _ := m["group"].(string); g != "" && g != gatewayAPIGroup {
			continue
		}
		if k, _ := m["kind"].(string); k != "" && k != "Gateway" {
			continue
		}
		ref := gatewayRef{route.GetNamespace(), ""}
		if ns, _ := m["namespace"].(string); ns != "" {
			ref.ns = ns
		}
		ref.name, _ = m["name"].(string)
		if ref.name != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

func (t *chatTools) routeSeries(ctx context.Context, template, ns, name, window string) ChatToolResult {
	target := template + " " + ns + "/" + name
	ks, _, err := t.conn.ChatResolveKind(ctx, "HTTPRoute", gatewayAPIGroup)
	if err != nil {
		return t.failed(toolMetrics, "api", target, err)
	}
	var found bool
	gvrIdx := -1
	for i := range ks {
		if ks[i].GVR.Group == gatewayAPIGroup && ks[i].Kind == "HTTPRoute" {
			gvrIdx, found = i, true
		}
	}
	if !found {
		return t.failed(toolMetrics, "api", target, errors.New("HTTPRoutes are not served on this cluster"))
	}
	route, err := t.conn.ChatGet(ctx, ks[gvrIdx].GVR, ns, name)
	if err != nil {
		return t.failed(toolMetrics, "api", target, err)
	}
	uid := string(route.GetUID())
	p := &projector{}
	unit := map[string]string{"route_requests": "requests per second", "route_5xx_ratio": "share of coded responses"}[template]
	expected := int(routemetrics.RuleWindow/routemetrics.RuleStep) + 1
	data := chatMetricsData{Template: template, Unit: unit, Window: routemetrics.RuleWindow.String(), Step: routemetrics.RuleStep.String(), Series: []chatSeries{},
		Note: "Route series are Envoy's per-rule clusters, by name (httproute/<namespace>/<name>/rule/<n>), scoped to each Gateway's proxies, over Envoy's last " +
			routemetrics.RuleWindow.String() + " whatever window was asked; rules are those of the route as read now."}
	if window != "" && window != "1h" {
		data.Note += " (The window asked for, " + window + ", does not apply to route series.)"
	}
	env := chatEnvelope{Tool: toolMetrics, Source: "prometheus", ReadAt: t.stamp(), Complete: true}
	gws := parentGateways(route)
	if len(gws) > chatRouteGatewaysMax {
		env.Complete = false
		data.Note += fmt.Sprintf(" The route names %d Gateways; the first %d are read.", len(gws), chatRouteGatewaysMax)
		gws = gws[:chatRouteGatewaysMax]
	}
	var checks []ChatCheck
	for _, gw := range gws {
		gwText := gw.ns + "/" + gw.name
		gr, err := t.conn.RuleMetrics(ctx, gw.ns, gw.name)
		switch {
		case err != nil:
			env.Errors = append(env.Errors, "Gateway "+gwText+": "+chatScrub(readError(err), &p.redacted))
			env.Complete = false
			continue
		case !gr.Status.Available:
			env.Errors = append(env.Errors, "Gateway "+gwText+": "+chatScrub(gr.Status.Message, &p.redacted))
			env.Complete = false
			continue
		}
		if data.Start == "" {
			data.Start, data.End = rfcOrEmpty(gr.Start), rfcOrEmpty(gr.End)
		}
		kept := 0
		for _, r := range gr.Rules {
			if r.Namespace != ns || r.Name != name {
				continue
			}
			if r.UID != uid {
				env.Errors = append(env.Errors, "Gateway "+gwText+": the route was replaced between reads; its rules were not kept")
				env.Complete = false
				break
			}
			kept++
			label := fmt.Sprintf("Gateway %s rule %d", gwText, r.Rule)
			if r.Unattributable != "" {
				env.Errors = append(env.Errors, label+": "+chatScrub(r.Unattributable, &p.redacted))
				env.Complete = false
				continue
			}
			var s routemetrics.Series
			if template == "route_requests" {
				s = r.Requests
			} else {
				s = routemetrics.Series{Availability: r.Classes.Availability, Error: r.Classes.Error, Points: r.Classes.Classes["5xx"]}
				if s.Availability == routemetrics.MetricAvailable && s.Points == nil {
					// the classes were read and none is 5xx
					data.Note += " " + label + ": no 5xx responses in the window."
					continue
				}
			}
			switch s.Availability {
			case routemetrics.MetricFailed:
				env.Errors = append(env.Errors, label+": "+chatScrub(s.Error, &p.redacted))
				env.Complete = false
				continue
			case routemetrics.MetricAbsent:
				data.Note += " " + label + ": no data in the window (this does not show that the rule took no traffic)."
				continue
			}
			cs := summarize(p.str(label, chatStringMax), s.Points, expected, false)
			if template == "route_5xx_ratio" {
				cs.Threshold = chat5xxThreshold
				for _, pt := range s.Points {
					if pt.Value >= chat5xxThreshold {
						cs.CrossedAt = rfcOrEmpty(timeOf(pt))
						break
					}
				}
				if cs.CrossedAt != "" {
					st := fmt.Sprintf("rule %d of HTTPRoute %s/%s (by name) through Gateway %s served up to %.0f%% 5xx responses in the last %s, first past %.0f%% at %s",
						r.Rule, ns, name, gwText, 100*cs.Max, routemetrics.RuleWindow, 100*chat5xxThreshold, cs.CrossedAt)
					checks = append(checks, ChatCheck{Class: checkMetric, Subject: ChatCheckSubject{Kind: "HTTPRoute", Namespace: ns, Name: name, Identity: identityName},
						Statement: st, Observed: data.End, Onset: cs.CrossedAt, Coverage: "live"})
				}
			}
			data.SeriesTotal++
			if len(data.Series) < chatSeriesMax {
				data.Series = append(data.Series, cs)
			} else {
				env.Complete = false
			}
		}
		if kept == 0 && env.Complete {
			data.Note += " Gateway " + gwText + " lists no rule of this route."
		}
	}
	if len(gws) == 0 {
		data.Note += " The route names no Gateway."
	}
	env.Total = &chatTotal{Count: data.SeriesTotal, Exact: env.Complete}
	env.Data = data
	env.Checks = capChecks(checks)
	ref := ChatRef{GVR: gvrString(ks[gvrIdx].GVR), Namespace: ns, Name: name, UID: uid}
	return t.finish(env, ChatRead{Tool: toolMetrics, Target: target, Outcome: "completed", Redacted: p.redacted, Refs: []ChatRef{ref},
		Detail: fmt.Sprintf("%d rule series through %d Gateways", data.SeriesTotal, len(gws)), ObservedFrom: data.Start, ObservedTo: data.End})
}
