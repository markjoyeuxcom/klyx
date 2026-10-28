package metrics

import (
	"slices"
	"strconv"
	"strings"
)

// The registered templates (Settings S3b §3.3): every data query Klyx sends.
// Each writes every selector through Sel, so the reader's scope is in each;
// with an empty scope each renders the query Klyx sent before scopes, byte
// for byte. Callers pass values, never PromQL.

// Metric and label names the templates read, shared with the reducers that
// parse their results.
const (
	EnvoyRqTotal     = "envoy_cluster_upstream_rq_total"
	EnvoyRqTime      = "envoy_cluster_upstream_rq_time_bucket"
	EnvoyRqXX        = "envoy_cluster_upstream_rq_xx"
	EnvoyRq          = "envoy_cluster_upstream_rq"
	EnvoyRqRetry     = "envoy_cluster_upstream_rq_retry"
	EnvoyClusterName = "envoy_cluster_name"
	EnvoyRespClass   = "envoy_response_code_class"
	EnvoyRespCode    = "envoy_response_code"

	// the proxy pods' owner labels, as a labelmap of their labels writes them
	EnvoyOwnGatewayName      = "gateway_envoyproxy_io_owning_gateway_name"
	EnvoyOwnGatewayNamespace = "gateway_envoyproxy_io_owning_gateway_namespace"

	// RuleWindowText and RuleRateText are the per-rule window and rate
	RuleWindowText = "30m"
	RuleRateText   = "1m"
	// EnvoyMergedCluster is the name Envoy Gateway gives a backend cluster
	// shared across routes (mergeBackends): <kind>/<ns>/<name>/<port>
	// [/<protocol>], for the mergeable kinds. Only merging produces it.
	EnvoyMergedCluster = "^(service|serviceimport|backend)/[^/]+/[^/]+/[0-9]+(/[a-z0-9]+)?$"

	HubbleFlows   = "hubble_flows_processed_total"
	HubbleDrops   = "hubble_drop_total"
	HubbleDNS     = "hubble_dns_responses_total"
	HubbleHTTP    = "hubble_http_requests_total"
	HubbleVerdict = "verdict"
	HubbleReason  = "reason"
	HubbleProto   = "protocol"
	HubbleRcode   = "rcode"
	HubbleStatus  = "status"

	cpuSeries      = "container_cpu_usage_seconds_total"
	memSeries      = "container_memory_working_set_bytes"
	restartSeries  = "kube_pod_container_status_restarts_total"
	realContainers = `container!="",container!="POD"`
	shortRate      = "5m"
)

// None is a template's parameters when it has none.
type None struct{}

// PodSet is a namespace and some of its pods.
type PodSet struct {
	Namespace string
	Pods      []string
}

func (p PodSet) matchers() string {
	return Equals("namespace", p.Namespace) + "," + MatchAnyOf("pod", "", p.Pods, "")
}

func (p PodSet) clone() PodSet { p.Pods = slices.Clone(p.Pods); return p }

// Pod is one pod of a namespace.
type Pod struct{ Namespace, Name string }

// Namespace is one namespace, or every one when empty.
type Namespace struct{ Name string }

func (n Namespace) matcher() string {
	if n.Name == "" {
		return ""
	}
	return Equals("namespace", n.Name) + ","
}

func (n Namespace) shape() string { return map[bool]string{true: "every", false: "one"}[n.Name == ""] }

// Routes are HTTPRoute keys, <namespace>/<name>, read from their rule
// clusters.
type Routes struct{ Keys []string }

func (r Routes) clone() Routes { r.Keys = slices.Clone(r.Keys); return r }

func (r Routes) matcher() string {
	return MatchAnyOf(EnvoyClusterName, "httproute/", r.Keys, "/rule/[0-9]+")
}

// The cluster's utilisation (the Overview readout and its sparklines).
var (
	ClusterCPU = define("cluster-cpu", func(_ None, sc string) string {
		return `1 - avg(rate(` + Sel("node_cpu_seconds_total", `mode="idle"`, sc) + `[5m]))`
	}, nil, nil, None{})
	ClusterMem = define("cluster-memory", func(_ None, sc string) string {
		return `1 - sum(` + Sel("node_memory_MemAvailable_bytes", "", sc) + `) / sum(` + Sel("node_memory_MemTotal_bytes", "", sc) + `)`
	}, nil, nil, None{})
)

// Usage by pod, cluster-wide (workloads and nodes), and by container.
var (
	PodCPU = define("pod-cpu", func(n Namespace, sc string) string {
		return `sum by (namespace,pod) (rate(` + Sel(cpuSeries, n.matcher()+realContainers, sc) + `[` + shortRate + `]))`
	}, Namespace.shape, nil, Namespace{}, Namespace{Name: "shop"})
	PodMem = define("pod-memory", func(n Namespace, sc string) string {
		return `sum by (namespace,pod) (` + Sel(memSeries, n.matcher()+realContainers, sc) + `)`
	}, Namespace.shape, nil, Namespace{}, Namespace{Name: "shop"})
	ContainerCPU = define("container-cpu", func(p Pod, sc string) string {
		return `sum by (container) (rate(` + Sel(cpuSeries, Equals("namespace", p.Namespace)+","+Equals("pod", p.Name)+","+realContainers, sc) + `[` + shortRate + `]))`
	}, nil, nil, Pod{Namespace: "shop", Name: "web-0"})
	ContainerMem = define("container-memory", func(p Pod, sc string) string {
		return `sum by (container) (` + Sel(memSeries, Equals("namespace", p.Namespace)+","+Equals("pod", p.Name)+","+realContainers, sc) + `)`
	}, nil, nil, Pod{Namespace: "shop", Name: "web-0"})
)

// A workload's pods: their sum (sparklines, chat), by container, and their
// restarts (chat).
var (
	WorkloadCPU = define("workload-cpu", func(p PodSet, sc string) string {
		return `sum(rate(` + Sel(cpuSeries, p.matchers()+","+realContainers, sc) + `[` + shortRate + `]))`
	}, nil, PodSet.clone, PodSet{Namespace: "shop", Pods: []string{"web-0", "web-1"}})
	WorkloadMem = define("workload-memory", func(p PodSet, sc string) string {
		return `sum(` + Sel(memSeries, p.matchers()+","+realContainers, sc) + `)`
	}, nil, PodSet.clone, PodSet{Namespace: "shop", Pods: []string{"web-0", "web-1"}})
	PodsCPUByContainer = define("pods-cpu-by-container", func(p PodSet, sc string) string {
		return `sum by (container) (rate(` + Sel(cpuSeries, p.matchers()+","+realContainers, sc) + `[` + shortRate + `]))`
	}, nil, PodSet.clone, PodSet{Namespace: "shop", Pods: []string{"web-0"}})
	PodsMemByContainer = define("pods-memory-by-container", func(p PodSet, sc string) string {
		return `sum by (container) (` + Sel(memSeries, p.matchers()+","+realContainers, sc) + `)`
	}, nil, PodSet.clone, PodSet{Namespace: "shop", Pods: []string{"web-0"}})
	PodsRestarts = define("pods-restarts", func(p PodSet, sc string) string {
		return `sum by (pod) (` + Sel(restartSeries, p.matchers(), sc) + `)`
	}, nil, PodSet.clone, PodSet{Namespace: "shop", Pods: []string{"web-0"}})
)

// An HTTPRoute's Envoy metrics (the topology's route readout).
var (
	RouteRPS = define("route-rps", func(r Routes, sc string) string {
		return `sum by (` + EnvoyClusterName + `)(rate(` + Sel(EnvoyRqTotal, r.matcher(), sc) + `[` + shortRate + `]))`
	}, nil, Routes.clone, Routes{Keys: []string{"shop/web"}})
	RouteP50 = define("route-p50", func(r Routes, sc string) string {
		return `histogram_quantile(0.50, sum by (` + EnvoyClusterName + `, le)(rate(` + Sel(EnvoyRqTime, r.matcher(), sc) + `[` + shortRate + `])))`
	}, nil, Routes.clone, Routes{Keys: []string{"shop/web"}})
	RouteP99 = define("route-p99", func(r Routes, sc string) string {
		return `histogram_quantile(0.99, sum by (` + EnvoyClusterName + `, le)(rate(` + Sel(EnvoyRqTime, r.matcher(), sc) + `[` + shortRate + `])))`
	}, nil, Routes.clone, Routes{Keys: []string{"shop/web"}})
	Route5xx = define("route-5xx", func(r Routes, sc string) string {
		return `sum by (` + EnvoyClusterName + `)(rate(` + Sel(EnvoyRqXX, r.matcher()+","+EnvoyRespClass+`="5"`, sc) + `[` + shortRate + `]))`
	}, nil, Routes.clone, Routes{Keys: []string{"shop/web"}})
	RouteAll = define("route-all", func(r Routes, sc string) string {
		return `sum by (` + EnvoyClusterName + `)(rate(` + Sel(EnvoyRqXX, r.matcher(), sc) + `[` + shortRate + `]))`
	}, nil, Routes.clone, Routes{Keys: []string{"shop/web"}})
	// EnvoyScraped asks whether any Envoy series exists under the scope
	EnvoyScraped = define("envoy-scraped", func(_ None, sc string) string {
		return `count(` + Sel(EnvoyRqTotal, "", sc) + `)`
	}, nil, nil, None{})
)

// The rule families a Gateway's per-rule reads cover, in order.
var RuleFamilies = []string{EnvoyRqTotal, EnvoyRqXX, EnvoyRq, EnvoyRqRetry}

// RuleProbe is one family's series for some rule clusters over the window.
type RuleProbe struct {
	Family   string // one of RuleFamilies
	Clusters []string
}

func (p RuleProbe) clone() RuleProbe { p.Clusters = slices.Clone(p.Clusters); return p }

// RuleRange is one family's rate for some rule clusters, from some of the
// Gateway's proxies: each Proxies entry scopes one operand, joined by "or".
type RuleRange struct {
	// Family: "total", "xx" (by response class), "503" or "retry"
	Family   string
	Clusters []string
	Proxies  []ProxySet
}

// ProxySet names some of one Gateway's proxies by value, never as PromQL:
// an equality on each Equal pair and, with Names, the pods named under
// NameLabel.
type ProxySet struct {
	Equal     [][2]string
	NameLabel string
	Names     []string
}

func (r RuleRange) clone() RuleRange {
	r.Clusters = slices.Clone(r.Clusters)
	proxies := make([]ProxySet, len(r.Proxies))
	for i, p := range r.Proxies {
		proxies[i] = ProxySet{Equal: slices.Clone(p.Equal), NameLabel: p.NameLabel, Names: slices.Clone(p.Names)}
	}
	r.Proxies = proxies
	return r
}

func (p ProxySet) matchers() string {
	parts := make([]string, 0, len(p.Equal)+1)
	for _, e := range p.Equal {
		parts = append(parts, Equals(e[0], e[1]))
	}
	if p.NameLabel != "" {
		parts = append(parts, MatchAnyOf(p.NameLabel, "", p.Names, ""))
	}
	return strings.Join(parts, ",")
}

// valid: the set names something, under label names.
func (p ProxySet) valid() bool {
	if len(p.Equal) == 0 && p.NameLabel == "" || (p.NameLabel == "") != (len(p.Names) == 0) {
		return false
	}
	for _, e := range p.Equal {
		if !isLabelName(e[0]) {
			return false
		}
	}
	return p.NameLabel == "" || isLabelName(p.NameLabel)
}

var ruleRangeMetric = map[string]string{"total": EnvoyRqTotal, "xx": EnvoyRqXX, "503": EnvoyRq, "retry": EnvoyRqRetry}

// ruleScopeLabels are the label pairs that name one Gateway's proxies, in
// the shared-cluster check's order.
var ruleScopeLabels = []string{EnvoyOwnGatewayName, EnvoyOwnGatewayNamespace, "pod", "namespace", "kubernetes_pod_name", "kubernetes_namespace"}

// The per-rule reads (the data path).
var (
	RuleProbeQuery = define("rule-probe", func(p RuleProbe, sc string) string {
		return `last_over_time(` + Sel(p.Family, MatchAnyOf(EnvoyClusterName, "", p.Clusters, ""), sc) + `[` + RuleWindowText + `])`
	}, func(p RuleProbe) string { return p.Family }, RuleProbe.clone,
		RuleProbe{Family: EnvoyRqTotal, Clusters: []string{"httproute/shop/web/rule/0"}},
		RuleProbe{Family: EnvoyRqXX, Clusters: []string{"httproute/shop/web/rule/0"}},
		RuleProbe{Family: EnvoyRq, Clusters: []string{"httproute/shop/web/rule/0"}},
		RuleProbe{Family: EnvoyRqRetry, Clusters: []string{"httproute/shop/web/rule/0"}})
	RuleRangeQuery = define("rule-range", func(p RuleRange, sc string) string {
		extra, by := "", ""
		switch p.Family {
		case "xx":
			by = ", " + EnvoyRespClass
		case "503":
			extra = "," + Equals(EnvoyRespCode, "503")
		}
		parts := make([]string, len(p.Proxies))
		for i, proxy := range p.Proxies {
			parts[i] = `rate(` + Sel(ruleRangeMetric[p.Family], MatchAnyOf(EnvoyClusterName, "", p.Clusters, "")+","+proxy.matchers()+extra, sc) + `[` + RuleRateText + `])`
		}
		return `sum by (` + EnvoyClusterName + by + `) (` + strings.Join(parts, " or ") + `)`
	}, func(p RuleRange) string {
		if _, ok := ruleRangeMetric[p.Family]; !ok || len(p.Proxies) == 0 {
			return "invalid"
		}
		for _, proxy := range p.Proxies {
			if !proxy.valid() {
				return "invalid"
			}
		}
		return p.Family + map[bool]string{true: "/one", false: "/or"}[len(p.Proxies) == 1]
	}, RuleRange.clone, ruleRangeCases()...)
	// RuleShared asks whether a merged cluster served the proxies, every
	// family "or"-ed so one filtered out never hides it
	RuleShared = define("rule-shared", func(_ None, sc string) string {
		parts := make([]string, len(RuleFamilies))
		for i, m := range RuleFamilies {
			parts[i] = `last_over_time(` + Sel(m, EnvoyClusterName+`=~`+strconv.Quote(EnvoyMergedCluster), sc) + `[` + RuleWindowText + `])`
		}
		return `count by (` + strings.Join(ruleScopeLabels, ", ") + `) (` + strings.Join(parts, " or ") + `)`
	}, nil, nil, None{})
)

// ruleRangeCases are RuleRange's shapes: every family, from one proxy
// scope and from several.
func ruleRangeCases() []RuleRange {
	var out []RuleRange
	one := ProxySet{Equal: [][2]string{{"namespace", "envoy-gateway-system"}}, NameLabel: "pod", Names: []string{"envoy-0"}}
	two := ProxySet{Equal: [][2]string{{"namespace", "edge"}}, NameLabel: "pod", Names: []string{"envoy-1"}}
	owning := ProxySet{Equal: [][2]string{{EnvoyOwnGatewayName, "eg"}, {EnvoyOwnGatewayNamespace, "envoy-gateway-system"}}}
	for _, fam := range []string{"total", "xx", "503", "retry"} {
		out = append(out,
			RuleRange{Family: fam, Clusters: []string{"httproute/shop/web/rule/0"}, Proxies: []ProxySet{one}},
			RuleRange{Family: fam, Clusters: []string{"httproute/shop/web/rule/0"}, Proxies: []ProxySet{one, two}},
			RuleRange{Family: fam, Clusters: []string{"httproute/shop/web/rule/0"}, Proxies: []ProxySet{owning}})
	}
	return out
}

// Hubble's flow metrics.
var (
	HubbleVerdicts = define("hubble-verdicts", func(_ None, sc string) string {
		return `sum by (` + HubbleVerdict + `)(rate(` + Sel(HubbleFlows, "", sc) + `[` + shortRate + `]))`
	}, nil, nil, None{})
	HubbleDropReasons = define("hubble-drops", func(_ None, sc string) string {
		return `sum by (` + HubbleReason + `, ` + HubbleProto + `)(rate(` + Sel(HubbleDrops, "", sc) + `[` + shortRate + `]))`
	}, nil, nil, None{})
	HubbleDNSRcodes = define("hubble-dns", func(_ None, sc string) string {
		return `sum by (` + HubbleRcode + `)(rate(` + Sel(HubbleDNS, "", sc) + `[` + shortRate + `]))`
	}, nil, nil, None{})
	HubbleHTTPStatus = define("hubble-http", func(_ None, sc string) string {
		return `sum by (` + HubbleStatus + `)(rate(` + Sel(HubbleHTTP, "", sc) + `[` + shortRate + `]))`
	}, nil, nil, None{})
)
