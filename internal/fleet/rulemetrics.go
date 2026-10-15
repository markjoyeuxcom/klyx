package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/routemetrics"
)

// The proxy pods' owner labels (Envoy Gateway's, unmerged Gateways).
const (
	labelOwningGatewayName      = "gateway.envoyproxy.io/owning-gateway-name"
	labelOwningGatewayNamespace = "gateway.envoyproxy.io/owning-gateway-namespace"
)

// RuleMetrics reads one Gateway's per-rule route metrics (The Line slice 7c):
// its attached HTTPRoutes' rules, each from its own Envoy cluster, scoped to
// this Gateway's proxies, over the last 30 minutes. On demand, like the
// topology. Only an unreadable Gateway is an error; every other gap is the
// result's status or an unattributable reason.
func (c *ClusterConn) RuleMetrics(ctx context.Context, namespace, name string) (routemetrics.GatewayRules, error) {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	q := routemetrics.RuleQuery{GatewayNamespace: namespace, GatewayName: name, End: clk.Now().Truncate(time.Second)}

	c.mu.RLock()
	hasEnvoy := c.caps.Network.HasEnvoyProxy
	c.mu.RUnlock()
	if !hasEnvoy {
		return routemetrics.Unavailable(q, "Envoy Gateway not detected"), nil
	}
	gw, err := c.dyn.Resource(c.gwGVR("gateways")).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return routemetrics.GatewayRules{}, fmt.Errorf("get gateway %s/%s: %w", namespace, name, err)
	}
	hrList, err := c.dyn.Resource(c.gwGVR("httproutes")).List(ctx, metav1.ListOptions{})
	if err != nil {
		return routemetrics.Unavailable(q, "could not list HTTPRoutes: "+err.Error()), nil
	}
	for i := range hrList.Items {
		u := &hrList.Items[i]
		n := gwapi.ParentRefCount(u, namespace, name)
		if n == 0 {
			continue
		}
		q.Routes = append(q.Routes, routemetrics.RouteRules{Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
			Fingerprints: gwapi.RuleFingerprints(u), SameGatewayTwice: n > 1})
	}
	q.Unattributable = c.envoyProxyReason(ctx, gw)

	c.metricsMu.Lock()
	c.ensureMetricsLocked(ctx, false, q.End)
	mcap := c.metricsState.cap
	transport := c.metricsState.transport
	c.metricsMu.Unlock()
	if !mcap.Available {
		msg := "metrics unavailable"
		if mcap.Reason != "" {
			msg += ": " + mcap.Reason
		}
		return routemetrics.Unavailable(q, msg), nil
	}
	q.Pods = func(ctx context.Context) ([]routemetrics.ProxyPod, error) {
		sel := labels.SelectorFromSet(labels.Set{labelOwningGatewayName: name, labelOwningGatewayNamespace: namespace})
		list, err := c.typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
		if err != nil {
			return nil, err
		}
		out := make([]routemetrics.ProxyPod, 0, len(list.Items))
		for _, p := range list.Items {
			out = append(out, routemetrics.ProxyPod{Namespace: p.Namespace, Name: p.Name})
		}
		return out, nil
	}
	res := routemetrics.NewEnvoyRuleSource(metrics.NewClient(transport)).QueryRules(ctx, q)
	res.Status.UpdatedAt = q.End
	return res, nil
}

// envoyProxyReason reads the EnvoyProxy objects that configure the Gateway -
// its own infrastructure.parametersRef and its GatewayClass's parametersRef -
// and names why its rules cannot be attributed: a setting that renames or
// shares clusters, or a referenced configuration Klyx cannot read. Either
// level counts, whatever Envoy Gateway's merge makes of them (fail closed).
// The controller's own default EnvoyProxy is not read: merged Gateways fail
// the proxy scoping, merged backends show as shared clusters on the
// proxies, and a custom stat name stops the cluster names parsing.
func (c *ClusterConn) envoyProxyReason(ctx context.Context, gw *unstructured.Unstructured) string {
	type ref struct{ from, group, kind, ns, name string }
	var refs []ref
	if m, ok, _ := unstructured.NestedMap(gw.Object, "spec", "infrastructure", "parametersRef"); ok {
		g, _ := m["group"].(string)
		k, _ := m["kind"].(string)
		n, _ := m["name"].(string)
		refs = append(refs, ref{"the Gateway", g, k, gw.GetNamespace(), n})
	}
	if className, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName"); className != "" {
		gc, err := c.dyn.Resource(c.gwGVR("gatewayclasses")).Get(ctx, className, metav1.GetOptions{})
		if err != nil {
			return fmt.Sprintf("%s: the GatewayClass %s could not be read: %v", routemetrics.ReasonSharedClusters, className, err)
		}
		if m, ok, _ := unstructured.NestedMap(gc.Object, "spec", "parametersRef"); ok {
			g, _ := m["group"].(string)
			k, _ := m["kind"].(string)
			ns, _ := m["namespace"].(string)
			n, _ := m["name"].(string)
			refs = append(refs, ref{"the GatewayClass " + className, g, k, ns, n})
		}
	}
	for _, r := range refs {
		if r.group != envoyGroup || r.kind != "EnvoyProxy" || r.ns == "" || r.name == "" {
			return fmt.Sprintf("%s: %s refers to %s %s/%s, not an EnvoyProxy Klyx can read", routemetrics.ReasonSharedClusters, r.from, r.kind, r.ns, r.name)
		}
		gvr, ok := c.servedResourceGVR(envoyGroup, "envoyproxies")
		if !ok {
			return fmt.Sprintf("%s: the EnvoyProxy %s/%s is referenced but the resource is not served", routemetrics.ReasonSharedClusters, r.ns, r.name)
		}
		ep, err := c.dyn.Resource(gvr).Namespace(r.ns).Get(ctx, r.name, metav1.GetOptions{})
		if err != nil {
			return fmt.Sprintf("%s: the EnvoyProxy %s/%s could not be read: %v", routemetrics.ReasonSharedClusters, r.ns, r.name, err)
		}
		if set := routemetrics.EnvoyProxySettings(ep.Object); len(set) > 0 {
			return fmt.Sprintf("%s: the EnvoyProxy %s/%s sets %s", routemetrics.ReasonSharedClusters, r.ns, r.name, strings.Join(set, ", "))
		}
	}
	return ""
}
