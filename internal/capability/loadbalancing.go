package capability

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Coverage S4 §2: MetalLB's controller workload, found by the native
// manifest's labels or the Helm chart's, and the API groups that name the
// other load-balancer controllers Klyx can see.
const (
	metallbGroup     = "metallb.io"
	awsLBCGroup      = "elbv2.k8s.aws" // the AWS Load Balancer Controller's CRDs
	metallbNotReady  = "MetalLB controller %s/%s is not ready"
	metallbNoCtl     = "MetalLB CRDs installed but no MetalLB controller found (no Deployment labelled app=metallb,component=controller or app.kubernetes.io/name=metallb,app.kubernetes.io/component=controller)"
	metallbCtlUnread = "MetalLB CRDs installed but its controller could not be read"
	metallbCtlCut    = "the MetalLB controller list was cut short (a continued list): the rest was not read"
)

// metallbSelectors are the native manifest's and the Helm chart's labels.
var metallbSelectors = []string{"app=metallb,component=controller", "app.kubernetes.io/name=metallb,app.kubernetes.io/component=controller"}

// MetalLBController is one MetalLB controller Deployment, as read.
type MetalLBController struct {
	Namespace, Name string
	Ready           bool
}

// LoadBalancingCapability models the load-balancer controllers (S4):
// MetalLB's CRDs and controller workload, and whether the AWS Load
// Balancer Controller's CRDs are served. Cilium LB IPAM, k3s ServiceLB and
// kube-vip are read from the Observer's kinds.
type LoadBalancingCapability struct {
	Base
	MetalLB  bool
	Healthy  bool
	Verified bool // a controller was read, so Healthy is a claim
	// ControllersRead: the controller list was read, so an empty one is
	// evidence.
	ControllersRead bool
	Controllers     []MetalLBController
	AWSLBC          bool
	// ServicesSeen is set by the connection from the Observer's Services
	// kind: a LoadBalancer Service exists, from a complete live read. With
	// the controllers it gates the Load balancers lens (S4 §6); ServiceLB
	// acts only on LoadBalancer Services, so they cover it.
	ServicesSeen bool
}

// Present gates the Load balancers lens: a controller detected (MetalLB,
// the AWS Load Balancer Controller, or Cilium, whose LB IPAM pools come
// with it) or a LoadBalancer Service seen.
func (c LoadBalancingCapability) Present(ciliumPresent bool) bool {
	return c.MetalLB || c.AWSLBC || ciliumPresent || c.ServicesSeen
}

// detectLoadBalancing classifies MetalLB like the other controllers: not
// ready for a found controller not ready; unknown when the list is
// unreadable, or no controller is found while the CRDs are served. Both
// selectors' reads accumulate (Codex's S4 implementation review, finding
// 4): a gap in one never discards what the other proved, and only complete
// reads of both certify health, absence, or ControllersRead.
func (d *Detector) detectLoadBalancing(ctx context.Context, served map[string]bool) LoadBalancingCapability {
	out := LoadBalancingCapability{MetalLB: served[metallbGroup], AWSLBC: served[awsLBCGroup]}
	if !out.MetalLB {
		out.Base = Base{Tier: Absent, Reason: "no MetalLB CRDs installed"}
		return out
	}
	seen := map[string]bool{}
	var gaps []string
	for _, sel := range metallbSelectors {
		deps, err := d.cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			gaps = append(gaps, metallbCtlUnread+": "+err.Error())
			continue
		}
		if deps.Continue != "" || (deps.RemainingItemCount != nil && *deps.RemainingItemCount > 0) {
			gaps = append(gaps, metallbCtlCut)
		}
		for i := range deps.Items {
			dp := &deps.Items[i]
			if key := dp.Namespace + "/" + dp.Name; !seen[key] {
				seen[key] = true
				out.Controllers = append(out.Controllers, MetalLBController{Namespace: dp.Namespace, Name: dp.Name, Ready: DeploymentReady(dp)})
			}
		}
	}
	out.ControllersRead = len(gaps) == 0
	sort.Slice(out.Controllers, func(i, j int) bool {
		if out.Controllers[i].Namespace != out.Controllers[j].Namespace {
			return out.Controllers[i].Namespace < out.Controllers[j].Namespace
		}
		return out.Controllers[i].Name < out.Controllers[j].Name
	})
	var notReady []string
	for _, c := range out.Controllers {
		if !c.Ready {
			notReady = append(notReady, fmt.Sprintf(metallbNotReady, c.Namespace, c.Name))
		}
	}
	switch {
	case len(notReady) > 0:
		// a proven failure stands beside any gap
		out.Verified = true
		out.Base.Reason = strings.Join(append(notReady, gaps...), "; ")
	case len(gaps) > 0:
		out.Base.Reason = strings.Join(gaps, "; ") // unknown: what was not read may not be ready
	case len(out.Controllers) == 0:
		out.Base.Reason = metallbNoCtl
	default:
		out.Verified, out.Healthy = true, true
	}
	out.Base.Tier = Classify(true, out.Healthy)
	return out
}
