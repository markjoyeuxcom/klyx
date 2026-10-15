package routemetrics

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/metrics"
)

// Per-rule route metrics (The Line slice 7c, slice 7 note §4): one Gateway's
// rules, each from its own Envoy cluster (httproute/<ns>/<name>/rule/<n>),
// read only from that Gateway's proxies, over the last 30 minutes. Nothing is
// summed across rules; whether a rule's points are its own is attributed later
// from the rule history (slice 7b), not here.

const (
	mRq       = "envoy_cluster_upstream_rq"
	mRqRetry  = "envoy_cluster_upstream_rq_retry"
	lRespCode = "envoy_response_code"

	// The proxy pods' owner labels as a Prometheus labelmap of the pod's
	// labels writes them (gateway.envoyproxy.io/owning-gateway-name ...).
	lOwnGwName = "gateway_envoyproxy_io_owning_gateway_name"
	lOwnGwNS   = "gateway_envoyproxy_io_owning_gateway_namespace"

	// RuleWindow, RuleStep and RuleRate shape every per-rule range query.
	RuleWindow = 30 * time.Minute
	RuleStep   = 30 * time.Second
	RuleRate   = time.Minute
	ruleRate   = "1m"
	ruleWindow = "30m"

	// mergedCluster is the name Envoy Gateway gives a backend cluster shared
	// across routes (mergeBackends): <kind>/<ns>/<name>/<port>[/<protocol>],
	// for the mergeable kinds. Only merging produces it.
	mergedCluster = "^(service|serviceimport|backend)/[^/]+/[^/]+/[0-9]+(/[a-z0-9]+)?$"
)

// ScopeForm is the label pair the series were scoped by.
type ScopeForm string

const (
	ScopeOwningGateway ScopeForm = "owning-gateway" // the proxies' owner labels
	ScopePod           ScopeForm = "pod"            // pod with namespace
	ScopeKubernetesPod ScopeForm = "kubernetes-pod" // kubernetes_pod_name with kubernetes_namespace
)

// scopePairs, in preference order: each names one pod (or one Gateway's
// proxies) only as a pair; a pod name alone is never enough.
var scopePairs = []struct {
	form     ScopeForm
	name, ns string
}{
	{ScopeOwningGateway, lOwnGwName, lOwnGwNS},
	{ScopePod, "pod", "namespace"},
	{ScopeKubernetesPod, "kubernetes_pod_name", "kubernetes_namespace"},
}

// Availability of one metric, apart from the others.
type Availability string

const (
	MetricAvailable Availability = "available"
	MetricAbsent    Availability = "absent" // no series
	MetricFailed    Availability = "failed"
)

// Fixed reasons and notes, worded once.
const (
	ReasonCannotScope     = "the series cannot be scoped to this Gateway's proxies"
	ReasonSharedClusters  = "shared or custom cluster names"
	ReasonSameGateway     = "the route attaches to this Gateway more than once; one cluster per rule serves every listener it names"
	ReasonScrapedTwice    = ReasonCannotScope + ": the same proxy's series are scraped more than once"
	ReasonSeveralClusters = ReasonCannotScope + ": the series come from more than one cluster"
	NoteDirectResponses   = "Direct and local responses (a directResponse, a redirect, a 404 with no route) never reach an upstream cluster, so they are not in these series."
	NoteBackendSplit      = "unavailable: a rule's backends share its one Envoy cluster"
	NoteCurrentPods       = "Only the proxy pods running now are counted: samples from proxies that have since gone are left out."
)

// RouteRules is one HTTPRoute attached to the Gateway: its rules by index.
type RouteRules struct {
	Namespace, Name, UID string
	Fingerprints         []string // by rule index (gwapi.RuleFingerprints)
	SameGatewayTwice     bool     // its parentRefs name this Gateway more than once
}

// ProxyPod is one of the Gateway's proxy pods.
type ProxyPod struct{ Namespace, Name string }

// RuleQuery is one Gateway's per-rule read. Pods lists the Gateway's proxy
// pods on demand: only a pod-name scope needs them.
type RuleQuery struct {
	GatewayNamespace, GatewayName string
	Routes                        []RouteRules
	Pods                          func(context.Context) ([]ProxyPod, error)
	End                           time.Time
	// Unattributable, when set, is a Gateway-wide reason (the EnvoyProxy that
	// configures it): no series are read, every rule carries the reason.
	Unattributable string
}

// Series is one per-rule metric over the window.
type Series struct {
	Availability Availability
	Error        string
	Points       []metrics.Point
}

// ClassSeries is a rule's response-class shares, keyed "1xx".."5xx".
type ClassSeries struct {
	Availability Availability
	Error        string
	Classes      map[string][]metrics.Point
}

// RuleSeries is one rule's four metrics. Requests and Retries are per
// second; the class and 503 shares are fractions of the requests that got a
// response code. Points are not yet attributed.
type RuleSeries struct {
	Namespace, Name, UID string
	Rule                 int
	Fingerprint          string
	Unattributable       string // the rule cannot be attributed at all: no series read
	Requests             Series
	Classes              ClassSeries
	Share503             Series
	Retries              Series
}

// GatewayRules is one Gateway's per-rule read.
type GatewayRules struct {
	Status         Status
	Scope          ScopeForm // "" when nothing needed scoping
	ScopeNote      string
	Unattributable string // Gateway-wide reason
	Start, End     time.Time
	Rules          []RuleSeries
	BackendSplit   string
	Notes          []string
}

// RuleCluster names a rule's Envoy cluster.
func RuleCluster(ns, name string, rule int) string {
	return "httproute/" + ns + "/" + name + "/rule/" + strconv.Itoa(rule)
}

// ParseRuleCluster reads (route, rule) from httproute/<ns>/<name>/rule/<n>.
// Every other shape is skipped, never guessed.
func ParseRuleCluster(name string) (ns, route string, rule int, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 5 || parts[0] != "httproute" || parts[3] != "rule" || parts[1] == "" || parts[2] == "" || !isAllDigits(parts[4]) {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[4])
	if err != nil {
		return "", "", 0, false
	}
	return parts[1], parts[2], n, true
}

// EnvoyRuleSource reads per-rule metrics from Envoy's cluster series.
type EnvoyRuleSource struct{ client *metrics.Client }

func NewEnvoyRuleSource(c *metrics.Client) *EnvoyRuleSource { return &EnvoyRuleSource{client: c} }

// base lists the query's rules, with no series read: the rules a reader
// names, and the cluster names of those it can read.
func base(q RuleQuery) (GatewayRules, []string) {
	out := GatewayRules{Status: Status{Available: true}, Start: q.End.Add(-RuleWindow), End: q.End,
		BackendSplit: NoteBackendSplit, Notes: []string{NoteDirectResponses}, Unattributable: q.Unattributable}
	var names []string
	for _, r := range q.Routes {
		for i, fp := range r.Fingerprints {
			rs := RuleSeries{Namespace: r.Namespace, Name: r.Name, UID: r.UID, Rule: i, Fingerprint: fp}
			switch {
			case q.Unattributable != "":
				rs.Unattributable = q.Unattributable
			case r.SameGatewayTwice:
				rs.Unattributable = ReasonSameGateway
			default:
				names = append(names, RuleCluster(r.Namespace, r.Name, i))
			}
			out.Rules = append(out.Rules, rs)
		}
	}
	return out, names
}

// Unavailable is the query's rules with nothing read, and why.
func Unavailable(q RuleQuery, msg string) GatewayRules {
	out, _ := base(q)
	out.Status = Status{Available: false, Message: msg}
	return out
}

// EnvoyProxySettings names the settings of an EnvoyProxy (its object) that
// make a rule's cluster series unattributable: a custom stat name, merged
// Gateways or merged backends.
func EnvoyProxySettings(obj map[string]any) []string {
	var set []string
	if v, _, _ := unstructured.NestedString(obj, "spec", "telemetry", "metrics", "clusterStatName"); v != "" {
		set = append(set, "spec.telemetry.metrics.clusterStatName")
	}
	if v, ok, _ := unstructured.NestedBool(obj, "spec", "mergeGateways"); ok && v {
		set = append(set, "spec.mergeGateways")
	}
	if v, ok, _ := unstructured.NestedFieldNoCopy(obj, "spec", "mergeBackends"); ok && v != nil {
		set = append(set, "spec.mergeBackends")
	}
	return set
}

// QueryRules reads the Gateway's rules. A failure that leaves nothing
// readable is the Status; a failure of one metric is that metric's.
func (s *EnvoyRuleSource) QueryRules(ctx context.Context, q RuleQuery) GatewayRules {
	out, names := base(q)
	if len(names) == 0 {
		return out
	}
	sel := metrics.MatchAnyOf(lClusterName, "", names, "")
	pods := onceList(q.Pods)

	// A shared (merged) cluster on this Gateway's proxies: a rule's own
	// cluster may carry only part of its traffic, or none of it. Checked
	// first, since a fully merged rule has no series of its own.
	if reason := s.sharedClusters(ctx, q, pods); reason != "" {
		out.Unattributable = reason
		for i := range out.Rules {
			if out.Rules[i].Unattributable == "" {
				out.Rules[i].Unattributable = reason
			}
		}
		return out
	}

	// Which labels the series carry, over the whole window: the scope.
	probe, err := s.client.InstantVector(ctx, fmt.Sprintf("last_over_time(%s{%s}[%s])", mRqTotal, sel, ruleWindow))
	if err != nil {
		out.Status = Status{Available: false, Message: "route metrics query failed: " + err.Error()}
		return out
	}
	if len(probe) == 0 {
		absentAll(&out)
		return out
	}
	form, ok := detectForm(probe)
	if !ok {
		out.Status = Status{Available: false, Message: ReasonCannotScope}
		return out
	}
	scopes, note, reason := scopesFor(ctx, q, form, pods)
	if reason == "" {
		reason = scopedOnce(ctx, probe, form, q, pods)
	}
	if reason != "" {
		out.Status = Status{Available: false, Message: reason}
		return out
	}
	out.Scope, out.ScopeNote = form, note

	rangeOf := func(metric, extra, by string) ([]metrics.LabeledSeries, error) {
		inner := orEach(scopes, func(sc string) string {
			return fmt.Sprintf("rate(%s{%s,%s%s}[%s])", metric, sel, sc, extra, ruleRate)
		})
		return s.client.RangeMatrix(ctx, fmt.Sprintf("sum by (%s%s) (%s)", lClusterName, by, inner), out.Start, out.End, RuleStep)
	}
	reqs, reqErr := rangeOf(mRqTotal, "", "")
	classes, clsErr := rangeOf(mRqXX, "", ", "+lRespClass)
	r503, r503Err := rangeOf(mRq, ","+metrics.Equals(lRespCode, "503"), "")
	retries, retryErr := rangeOf(mRqRetry, "", "")
	reduceRules(&out, reqs, reqErr, classes, clsErr, r503, r503Err, retries, retryErr)
	return out
}

// detectForm picks the first label pair every probed series carries.
func detectForm(probe []metrics.LabeledSample) (ScopeForm, bool) {
	for _, p := range scopePairs {
		all := true
		for _, s := range probe {
			if s.Labels[p.name] == "" || s.Labels[p.ns] == "" {
				all = false
				break
			}
		}
		if all {
			return p.form, true
		}
	}
	return "", false
}

// scopesFor builds one matcher set per pod namespace (one in all for the
// owner labels), joined by "or" in every query.
func scopesFor(ctx context.Context, q RuleQuery, form ScopeForm, pods func(context.Context) ([]ProxyPod, error)) (sel []string, note, reason string) {
	if form == ScopeOwningGateway {
		return []string{metrics.Equals(lOwnGwName, q.GatewayName) + "," + metrics.Equals(lOwnGwNS, q.GatewayNamespace)}, "", ""
	}
	list, err := pods(ctx)
	if err != nil {
		return nil, "", ReasonCannotScope + ": " + err.Error()
	}
	if len(list) == 0 {
		return nil, "", ReasonCannotScope + ": no proxy pods found for this Gateway"
	}
	nameL, nsL := "pod", "namespace"
	if form == ScopeKubernetesPod {
		nameL, nsL = "kubernetes_pod_name", "kubernetes_namespace"
	}
	byNS := map[string][]string{}
	for _, p := range list {
		byNS[p.Namespace] = append(byNS[p.Namespace], p.Name)
	}
	nss := make([]string, 0, len(byNS))
	for ns := range byNS {
		nss = append(nss, ns)
	}
	sort.Strings(nss)
	for _, ns := range nss {
		names := byNS[ns]
		sort.Strings(names)
		sel = append(sel, metrics.Equals(nsL, ns)+","+metrics.MatchAnyOf(nameL, "", names, ""))
	}
	return sel, NoteCurrentPods, ""
}

// scopedOnce checks the probed series that fall in the scope: each proxy's
// series for a rule must be one series (a pod scraped by two jobs would be
// counted twice), and they must not span clusters (a store shared across
// clusters, told apart by a cluster label). Either is ambiguous: fail closed.
func scopedOnce(ctx context.Context, probe []metrics.LabeledSample, form ScopeForm, q RuleQuery, pods func(context.Context) ([]ProxyPod, error)) string {
	var ours map[ProxyPod]bool
	if form != ScopeOwningGateway {
		list, _ := pods(ctx) // listed already: scopesFor needed them
		ours = map[ProxyPod]bool{}
		for _, p := range list {
			ours[p] = true
		}
	}
	seen := map[string]bool{}
	clusters := map[string]bool{}
	for _, smp := range probe {
		l := smp.Labels
		var id string
		switch form {
		case ScopeOwningGateway:
			if l[lOwnGwName] != q.GatewayName || l[lOwnGwNS] != q.GatewayNamespace {
				continue
			}
			id = proxyIdentity(l)
		default:
			nameL, nsL := "pod", "namespace"
			if form == ScopeKubernetesPod {
				nameL, nsL = "kubernetes_pod_name", "kubernetes_namespace"
			}
			if !ours[ProxyPod{Namespace: l[nsL], Name: l[nameL]}] {
				continue
			}
			id = l[nsL] + "/" + l[nameL]
		}
		key := l[lClusterName] + "\x00" + id
		if seen[key] {
			return ReasonScrapedTwice
		}
		seen[key] = true
		if c := l["cluster"]; c != "" {
			clusters[c] = true
		}
	}
	if len(clusters) > 1 {
		return ReasonSeveralClusters
	}
	return ""
}

// proxyIdentity names the proxy a series came from: its pod pair when the
// scrape wrote one, else its scrape target.
func proxyIdentity(l map[string]string) string {
	switch {
	case l["pod"] != "" && l["namespace"] != "":
		return l["namespace"] + "/" + l["pod"]
	case l["kubernetes_pod_name"] != "" && l["kubernetes_namespace"] != "":
		return l["kubernetes_namespace"] + "/" + l["kubernetes_pod_name"]
	}
	return "instance " + l["instance"]
}

// sharedClusters reports whether a merged cluster served this Gateway's
// proxies in the window: one sample per scraped pod that carried one, matched
// to the Gateway by any pair it has. A sample with no pair, or a check that
// fails, cannot be ruled out, so it counts.
func (s *EnvoyRuleSource) sharedClusters(ctx context.Context, q RuleQuery, pods func(context.Context) ([]ProxyPod, error)) string {
	by := make([]string, 0, 2*len(scopePairs))
	for _, p := range scopePairs {
		by = append(by, p.name, p.ns)
	}
	samples, err := s.client.InstantVector(ctx, fmt.Sprintf("count by (%s) (last_over_time(%s{%s=~%s}[%s]))",
		strings.Join(by, ", "), mRqTotal, lClusterName, strconv.Quote(mergedCluster), ruleWindow))
	if err != nil {
		return "could not check for shared clusters: " + err.Error()
	}
	var ours map[ProxyPod]bool
	for _, smp := range samples {
		l := smp.Labels
		if l[lOwnGwName] != "" && l[lOwnGwNS] != "" {
			if l[lOwnGwName] == q.GatewayName && l[lOwnGwNS] == q.GatewayNamespace {
				return ReasonSharedClusters
			}
			continue
		}
		var pod ProxyPod
		switch {
		case l["pod"] != "" && l["namespace"] != "":
			pod = ProxyPod{Namespace: l["namespace"], Name: l["pod"]}
		case l["kubernetes_pod_name"] != "" && l["kubernetes_namespace"] != "":
			pod = ProxyPod{Namespace: l["kubernetes_namespace"], Name: l["kubernetes_pod_name"]}
		default:
			return ReasonSharedClusters + " (a shared cluster's series cannot be scoped)"
		}
		if ours == nil {
			list, err := pods(ctx)
			if err != nil {
				return "could not check for shared clusters: " + err.Error()
			}
			ours = map[ProxyPod]bool{}
			for _, p := range list {
				ours[p] = true
			}
		}
		if ours[pod] {
			return ReasonSharedClusters
		}
	}
	return ""
}

// onceList lists the proxy pods at most once per read.
func onceList(f func(context.Context) ([]ProxyPod, error)) func(context.Context) ([]ProxyPod, error) {
	var (
		done bool
		list []ProxyPod
		err  error
	)
	return func(ctx context.Context) ([]ProxyPod, error) {
		if !done {
			done = true
			if f == nil {
				err = fmt.Errorf("the proxy pods are unknown")
			} else {
				list, err = f(ctx)
				if err != nil {
					err = fmt.Errorf("the proxy pods could not be listed: %w", err)
				}
			}
		}
		return list, err
	}
}

// orEach joins one expression per scope with "or": the scopes' label sets
// differ, so nothing collides.
func orEach(scopes []string, f func(string) string) string {
	parts := make([]string, len(scopes))
	for i, sc := range scopes {
		parts[i] = f(sc)
	}
	return strings.Join(parts, " or ")
}

func absentAll(out *GatewayRules) {
	for i := range out.Rules {
		r := &out.Rules[i]
		if r.Unattributable != "" {
			continue
		}
		r.Requests = Series{Availability: MetricAbsent}
		r.Classes = ClassSeries{Availability: MetricAbsent}
		r.Share503 = Series{Availability: MetricAbsent}
		r.Retries = Series{Availability: MetricAbsent}
	}
}

// byCluster indexes a matrix by its rule cluster name; other shapes are skipped.
func byCluster(series []metrics.LabeledSeries) map[string][]metrics.LabeledSeries {
	m := map[string][]metrics.LabeledSeries{}
	for _, s := range series {
		name := s.Labels[lClusterName]
		if _, _, _, ok := ParseRuleCluster(name); ok {
			m[name] = append(m[name], s)
		}
	}
	return m
}

func reduceRules(out *GatewayRules, reqs []metrics.LabeledSeries, reqErr error, classes []metrics.LabeledSeries, clsErr error,
	r503 []metrics.LabeledSeries, r503Err error, retries []metrics.LabeledSeries, retryErr error) {
	reqM, clsM, r503M, retryM := byCluster(reqs), byCluster(classes), byCluster(r503), byCluster(retries)
	for i := range out.Rules {
		r := &out.Rules[i]
		if r.Unattributable != "" {
			continue
		}
		name := RuleCluster(r.Namespace, r.Name, r.Rule)
		r.Requests = single(reqM[name], reqErr)
		r.Retries = single(retryM[name], retryErr)
		r.Classes, r.Share503 = shares(clsM[name], clsErr, r503M[name], r503Err)
	}
}

// single is a per-rule series: the query summed by cluster, so one at most.
func single(s []metrics.LabeledSeries, err error) Series {
	switch {
	case err != nil:
		return Series{Availability: MetricFailed, Error: err.Error()}
	case len(s) == 0:
		return Series{Availability: MetricAbsent}
	case len(s) > 1:
		return Series{Availability: MetricFailed, Error: fmt.Sprintf("expected one series per rule, got %d", len(s))}
	}
	return Series{Availability: MetricAvailable, Points: s[0].Points}
}

// shares turns the per-class rates into each class's share of all coded
// responses, per point, and the 503 rate into its share of the same total.
// A class or the 503 counter is created at its first response, so a missing
// one at a point where the total exists is zero. A point whose total is zero
// has no share (not meaningful).
func shares(cls []metrics.LabeledSeries, clsErr error, r503 []metrics.LabeledSeries, r503Err error) (ClassSeries, Series) {
	if clsErr != nil {
		return ClassSeries{Availability: MetricFailed, Error: clsErr.Error()}, Series{Availability: MetricFailed, Error: "the response classes could not be read: " + clsErr.Error()}
	}
	if len(cls) == 0 {
		return ClassSeries{Availability: MetricAbsent}, Series{Availability: MetricAbsent}
	}
	total := map[int64]float64{}
	byClass := map[string]map[int64]float64{}
	for _, s := range cls {
		c := s.Labels[lRespClass]
		if len(c) != 1 || c[0] < '1' || c[0] > '5' {
			continue // not a response class Envoy writes
		}
		key := c + "xx"
		if byClass[key] == nil {
			byClass[key] = map[int64]float64{}
		}
		for _, p := range s.Points {
			byClass[key][p.Unix] += p.Value
			total[p.Unix] += p.Value
		}
	}
	ts := make([]int64, 0, len(total))
	for t, v := range total {
		if v > 0 {
			ts = append(ts, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	cs := ClassSeries{Availability: MetricAvailable, Classes: map[string][]metrics.Point{}}
	for key, pts := range byClass {
		series := make([]metrics.Point, 0, len(ts))
		for _, t := range ts {
			series = append(series, metrics.Point{Unix: t, Value: clamp01(pts[t] / total[t])})
		}
		cs.Classes[key] = series
	}
	var s503 Series
	switch {
	case r503Err != nil:
		s503 = Series{Availability: MetricFailed, Error: r503Err.Error()}
	case len(r503) > 1:
		s503 = Series{Availability: MetricFailed, Error: fmt.Sprintf("expected one 503 series per rule, got %d", len(r503))}
	default:
		at := map[int64]float64{}
		if len(r503) == 1 {
			for _, p := range r503[0].Points {
				at[p.Unix] = p.Value
			}
		}
		pts := make([]metrics.Point, 0, len(ts))
		for _, t := range ts {
			pts = append(pts, metrics.Point{Unix: t, Value: clamp01(at[t] / total[t])})
		}
		s503 = Series{Availability: MetricAvailable, Points: pts}
	}
	return cs, s503
}

// clamp01: rates of counters scraped apart can momentarily disagree.
func clamp01(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}
