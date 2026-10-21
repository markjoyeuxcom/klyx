package appbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
)

// AI A1 §3.4: get_metrics runs Klyx's own templates, never the model's
// PromQL. Label values are escaped with metrics.Equals and MatchAnyOf. A
// window's step keeps every series at most 60 points, the inclusive end
// counted; each series travels as a summary. Series are labelled by pod
// name: a workload's are selected by the names of the pods it has now
// (listed by controller UID), which is not historical ownership - a pod of
// the same name recreated inside the window is included, and one gone
// before the read is not. An empty result is "no data for this selection
// and window", never proof that a source is absent.

const (
	toolMetrics        = "get_metrics"
	chatSeriesMax      = 8
	chatSeriesPoints   = 60
	chatThresholdShare = 0.9 // a container at or past 90% of its limit
	chatMetricPodsMax  = 20  // pod names listed in the result
)

var chatMetricWindows = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour}

var chatMetricTemplates = map[string]string{
	"pod_cpu": "cores", "pod_memory": "bytes", "workload_cpu": "cores", "workload_memory": "bytes", "restarts": "restarts",
	"route_requests": "requests per second", "route_5xx_ratio": "share of coded responses",
}

func timeOf(p metrics.Point) time.Time { return time.Unix(p.Unix, 0).UTC() }

// chatMetricWorkloads are the kinds a workload template or restarts takes.
var chatMetricWorkloads = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

// stepFor keeps a window at most chatSeriesPoints points, the inclusive end
// counted, in whole seconds.
func stepFor(w time.Duration) time.Duration {
	step := (w/(chatSeriesPoints-1) + time.Second - 1) / time.Second * time.Second
	for int(w/step)+1 > chatSeriesPoints {
		step += time.Second
	}
	return step
}

type chatSeries struct {
	Label     string  `json:"label"` // the container, the pod, or "total"
	Points    int     `json:"points"`
	Expected  int     `json:"expected"`
	Gaps      int     `json:"gaps"`
	First     float64 `json:"first"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	Last      float64 `json:"last"`
	Resets    int     `json:"resets,omitempty"`   // a counter that went down
	Increase  float64 `json:"increase,omitempty"` // a counter's rise, resets counted
	Limit     string  `json:"limit,omitempty"`    // the container's limit, as written
	Threshold float64 `json:"threshold,omitempty"`
	CrossedAt string  `json:"crossedAt,omitempty"`
	RoseAt    string  `json:"roseAt,omitempty"`
}

type chatMetricsData struct {
	Template    string       `json:"template"`
	Unit        string       `json:"unit"`
	Window      string       `json:"window"`
	Start       string       `json:"start"`
	End         string       `json:"end"`
	Step        string       `json:"step"`
	Pods        []string     `json:"pods"`
	PodsTotal   int          `json:"podsTotal"`
	Series      []chatSeries `json:"series"`
	SeriesTotal int          `json:"seriesTotal"`
	Note        string       `json:"note"`
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// summarize reads one series: its extremes, gaps against the steps the
// window holds, and, for a counter, its resets and rise.
func summarize(label string, pts []metrics.Point, expected int, counter bool) chatSeries {
	s := chatSeries{Label: label, Points: len(pts), Expected: expected, Gaps: max(expected-len(pts), 0)}
	for i, p := range pts {
		v := p.Value
		if i == 0 {
			s.First, s.Min, s.Max = v, v, v
		}
		s.Min, s.Max, s.Last = math.Min(s.Min, v), math.Max(s.Max, v), v
		if !counter || i == 0 {
			continue
		}
		prev := pts[i-1].Value
		switch {
		case v < prev:
			s.Resets++
			s.Increase += v
		case v > prev:
			s.Increase += v - prev
		}
		if v != prev && s.RoseAt == "" && s.Increase > 0 {
			s.RoseAt = time.Unix(p.Unix, 0).UTC().Format(time.RFC3339)
		}
	}
	s.First, s.Min, s.Max, s.Last, s.Increase = round3(s.First), round3(s.Min), round3(s.Max), round3(s.Last), round3(s.Increase)
	return s
}

// containerLimits are a pod's containers' limits for one resource.
func containerLimits(pod *unstructured.Unstructured, res string) map[string]resource.Quantity {
	out := map[string]resource.Quantity{}
	cs, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
	for _, c := range cs {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		v, _, _ := unstructured.NestedString(m, "resources", "limits", res)
		if q, err := resource.ParseQuantity(v); err == nil && name != "" && v != "" {
			out[name] = q
		}
	}
	return out
}

func (t *chatTools) metricSeries(ctx context.Context, template, kind, ns, name, window string) ChatToolResult {
	if strings.HasPrefix(template, "route_") {
		if kind != "" && kind != "HTTPRoute" {
			return t.refuse(toolMetrics, template, fmt.Errorf("%s reads an HTTPRoute: kind must be HTTPRoute or omitted", template))
		}
		return t.routeSeries(ctx, template, ns, name, window)
	}
	target := template + " " + ns + "/" + name
	unit := chatMetricTemplates[template]
	w := chatMetricWindows[window]
	podTarget := template == "pod_cpu" || template == "pod_memory" || (template == "restarts" && (kind == "" || kind == "Pod"))
	p := &projector{}
	var pods []string
	var pod *unstructured.Unstructured
	note := ""
	if podTarget {
		if kind != "" && kind != "Pod" {
			return t.refuse(toolMetrics, target, fmt.Errorf("%s reads a pod: kind must be Pod or omitted", template))
		}
		u, err := t.conn.ChatGet(ctx, podsGVRv1, ns, name)
		if err != nil {
			return t.failed(toolMetrics, "api", target, err)
		}
		pod, pods = u, []string{name}
	} else {
		if !chatMetricWorkloads[kind] {
			return t.refuse(toolMetrics, target, fmt.Errorf("%s reads a workload: kind must be Deployment, StatefulSet or DaemonSet", template))
		}
		k, err := t.resolve(ctx, kind, "apps")
		if err != nil {
			return t.refuse(toolMetrics, target, err)
		}
		wl, err := t.workloadPods(ctx, k, ns, name)
		if err != nil {
			return t.failed(toolMetrics, "api", target, err)
		}
		for _, pr := range wl.Pods {
			pods = append(pods, pr.Name)
		}
		if wl.Partial {
			note += " The workload's pod list was capped: some pods may be missing."
		}
	}
	sort.Strings(pods)
	end := t.now().UTC().Truncate(time.Second)
	start, step := end.Add(-w), stepFor(w)
	data := chatMetricsData{Template: template, Unit: unit, Window: window, Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339),
		Step: step.String(), PodsTotal: len(pods), Series: []chatSeries{}}
	for i, n := range pods {
		if i == chatMetricPodsMax {
			break
		}
		data.Pods = append(data.Pods, p.str(n, chatStringMax))
	}
	env := chatEnvelope{Tool: toolMetrics, Source: "prometheus", ReadAt: t.stamp(), Complete: true}
	data.Note = "Series are selected by the names of the pods the target has now (by name): a pod of the same name recreated inside the window is included, and a pod gone before this read is not." + note
	if len(pods) == 0 {
		data.Note += " The workload has no pods now: no series was read."
		env.Data = data
		return t.finish(env, ChatRead{Tool: toolMetrics, Target: target, Outcome: "completed", Detail: "no pods", About: metricsAbout(kind, ns, name, nil)})
	}
	sel := metrics.Equals("namespace", ns) + "," + metrics.MatchAnyOf("pod", "", pods, "")
	ctr := sel + `,container!="",container!="POD"`
	var q, by string
	switch template {
	case "pod_cpu":
		q, by = fmt.Sprintf(`sum by (container) (rate(container_cpu_usage_seconds_total{%s}[5m]))`, ctr), "container"
	case "pod_memory":
		q, by = fmt.Sprintf(`sum by (container) (container_memory_working_set_bytes{%s})`, ctr), "container"
	case "workload_cpu":
		q = fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s}[5m]))`, ctr)
	case "workload_memory":
		q = fmt.Sprintf(`sum(container_memory_working_set_bytes{%s})`, ctr)
	case "restarts":
		q, by = fmt.Sprintf(`sum by (pod) (kube_pod_container_status_restarts_total{%s})`, sel), "pod"
	}
	series, err := t.conn.ChatMetricsRange(ctx, q, start, end, step)
	if err != nil {
		if errors.Is(err, fleet.ErrChatMetricsUnavailable) {
			return t.failed(toolMetrics, "prometheus", target, err)
		}
		return t.failed(toolMetrics, "prometheus", target, fmt.Errorf("the query failed: %w", err))
	}
	var limits map[string]resource.Quantity
	if pod != nil && template != "restarts" {
		limits = containerLimits(pod, map[string]string{"pod_cpu": "cpu", "pod_memory": "memory"}[template])
		if len(limits) > 0 {
			data.Note += " Limits are the pod's as read now; a limit changed inside the window is not reflected."
		}
	}
	expected := int(w/step) + 1
	sort.Slice(series, func(i, j int) bool { return series[i].Labels[by] < series[j].Labels[by] })
	data.SeriesTotal = len(series)
	var checks []ChatCheck
	for _, s := range series {
		if len(data.Series) == chatSeriesMax {
			env.Complete = false
			break
		}
		label := "total"
		if by != "" {
			label = s.Labels[by]
		}
		cs := summarize(p.str(label, chatStringMax), s.Points, expected, template == "restarts")
		if lim, ok := limits[label]; ok {
			cs.Limit = lim.String()
			limit := float64(lim.Value())
			if template == "pod_cpu" {
				limit = float64(lim.MilliValue()) / 1000
			}
			cs.Threshold = round3(limit * chatThresholdShare)
			for _, pt := range s.Points {
				if pt.Value >= cs.Threshold {
					cs.CrossedAt = time.Unix(pt.Unix, 0).UTC().Format(time.RFC3339)
					break
				}
			}
			if cs.CrossedAt != "" {
				what := map[string]string{"pod_cpu": "CPU", "pod_memory": "memory"}[template]
				st := fmt.Sprintf("container %s of the pod currently named %s/%s (by name) reached %.0f%% of its %s limit (%s) in the last %s, first past %.0f%% at %s",
					cs.Label, ns, name, 100*cs.Max/limit, what, cs.Limit, window, 100*chatThresholdShare, cs.CrossedAt)
				checks = append(checks, ChatCheck{Class: checkMetric, Subject: ChatCheckSubject{Kind: "Pod", Namespace: ns, Name: name, Identity: identityName},
					Statement: st, Observed: data.End, Onset: cs.CrossedAt, Coverage: "live"})
			}
		}
		if template == "restarts" && cs.Increase > 0 {
			st := fmt.Sprintf("restarts of the pod currently named %s/%s (by name) rose by %v in the last %s", ns, cs.Label, cs.Increase, window)
			switch {
			case cs.Resets == 1:
				st += " (the counter reset once)"
			case cs.Resets > 1:
				st += fmt.Sprintf(" (the counter reset %d times)", cs.Resets)
			}
			checks = append(checks, ChatCheck{Class: checkMetric, Subject: ChatCheckSubject{Kind: "Pod", Namespace: ns, Name: cs.Label, Identity: identityName},
				Statement: st, Observed: data.End, Onset: cs.RoseAt, Coverage: "live"})
		}
		data.Series = append(data.Series, cs)
	}
	if len(series) == 0 {
		data.Note += " No data for this selection and window: this does not show that a source such as kube-state-metrics or cAdvisor is absent."
	}
	env.Total = &chatTotal{Count: data.SeriesTotal, Exact: true}
	env.Data = data
	env.Checks = capChecks(checks)
	return t.finish(env, ChatRead{Tool: toolMetrics, Target: target, Outcome: "completed", Redacted: p.redacted,
		Detail: fmt.Sprintf("%d series over %d pods, %s", data.SeriesTotal, len(pods), window), About: metricsAbout(kind, ns, name, pods)})
}

// metricsAbout is a metrics read's subjects: series are selected by pod
// name, so the target and its pods are held by name.
func metricsAbout(kind, ns, name string, pods []string) []ChatCheckSubject {
	if kind == "" {
		kind = "Pod"
	}
	out := []ChatCheckSubject{byName(kind, ns, name)}
	for _, n := range pods {
		if len(out) == chatReadAboutMax {
			break
		}
		if kind != "Pod" || n != name {
			out = append(out, byName("Pod", ns, n))
		}
	}
	return out
}
