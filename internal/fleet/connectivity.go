package fleet

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/netpol"
)

// ConnectivityReport is the connectivity can-i answer plus the honesty
// counters: how many CNI-native policies (invisible to the simulation) exist
// in scope.
type ConnectivityReport struct {
	Verdict        netpol.Verdict
	SrcLabels      map[string]string
	DstLabels      map[string]string
	IngressExplain string
	EgressExplain  string
	// CiliumPolicies counts CiliumNetworkPolicies in the two namespaces plus
	// CiliumClusterwideNetworkPolicies. >0 means the static verdict is
	// PARTIAL - Cilium policies are not simulated.
	CiliumPolicies int
}

var (
	connCNPGVR  = schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	connCCNPGVR = schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
)

// ConnectivityCanI answers "can pod src reach pod dst on port P?" by statically
// evaluating the NetworkPolicies of both namespaces (netpol.Evaluate mirrors
// the API semantics). Pods are resolved by name to their live labels - the
// question is asked about real pods, not hypothetical label sets. Namespace
// labels come from a full namespace list (needed for namespaceSelector).
func (c *ClusterConn) ConnectivityCanI(ctx context.Context, srcNS, srcPod, dstNS, dstPod string, port int, protocol string) (ConnectivityReport, error) {
	if c.typed == nil {
		return ConnectivityReport{}, fmt.Errorf("cluster client not available")
	}
	src, err := c.typed.CoreV1().Pods(srcNS).Get(ctx, srcPod, metav1.GetOptions{})
	if err != nil {
		return ConnectivityReport{}, fmt.Errorf("source pod %s/%s: %w", srcNS, srcPod, err)
	}
	dst, err := c.typed.CoreV1().Pods(dstNS).Get(ctx, dstPod, metav1.GetOptions{})
	if err != nil {
		return ConnectivityReport{}, fmt.Errorf("destination pod %s/%s: %w", dstNS, dstPod, err)
	}

	nsList, err := c.typed.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return ConnectivityReport{}, fmt.Errorf("list namespaces: %w", err)
	}
	nsLabels := netpol.NamespaceLabels{}
	for i := range nsList.Items {
		nsLabels[nsList.Items[i].Name] = nsList.Items[i].Labels
	}

	polNamespaces := []string{srcNS}
	if dstNS != srcNS {
		polNamespaces = append(polNamespaces, dstNS)
	}
	var all []networkingv1.NetworkPolicy
	for _, ns := range polNamespaces {
		pl, err := c.typed.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return ConnectivityReport{}, fmt.Errorf("list networkpolicies in %s: %w", ns, err)
		}
		for i := range pl.Items {
			all = append(all, pl.Items[i])
		}
	}

	q := netpol.Query{
		Src:  netpol.Endpoint{Namespace: srcNS, PodLabels: src.Labels},
		Dst:  netpol.Endpoint{Namespace: dstNS, PodLabels: dst.Labels},
		Port: port, Protocol: protocol,
	}
	verdict := netpol.Evaluate(q, all, nsLabels)

	report := ConnectivityReport{
		Verdict:        verdict,
		SrcLabels:      src.Labels,
		DstLabels:      dst.Labels,
		IngressExplain: netpol.Explain(verdict.Ingress, "ingress"),
		EgressExplain:  netpol.Explain(verdict.Egress, "egress"),
	}

	// CNI-native policy honesty: count, best-effort (absent CRDs → 0).
	if c.dyn != nil {
		for _, ns := range polNamespaces {
			if l, cerr := c.dyn.Resource(connCNPGVR).Namespace(ns).List(ctx, metav1.ListOptions{}); cerr == nil && l != nil {
				report.CiliumPolicies += len(l.Items)
			}
		}
		if l, cerr := c.dyn.Resource(connCCNPGVR).List(ctx, metav1.ListOptions{}); cerr == nil && l != nil {
			report.CiliumPolicies += len(l.Items)
		}
	}
	return report, nil
}
