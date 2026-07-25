package capability

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Official install-default namespaces and controller names.
// Non-default install paths (e.g. renamed namespaces) are not yet supported.
const (
	fluxNamespace             = "flux-system"
	fluxKustomizeController   = "kustomize-controller"
	argoNamespace             = "argocd"
	argoAppController         = "argocd-application-controller"
	certManagerNamespace      = "cert-manager"
	certManagerController     = "cert-manager"
	externalSecretsNamespace  = "external-secrets"
	externalSecretsController = "external-secrets"
	// Reloader ships no CRD; its controller carries this label in both the chart
	// ("reloader-reloader") and raw-manifest ("reloader") install layouts.
	reloaderNameSelector = "app.kubernetes.io/name=reloader"
)

// workloadKind distinguishes the two workload types the health probe supports.
type workloadKind int

const (
	deploymentWorkload  workloadKind = iota
	statefulSetWorkload workloadKind = iota
)

func desiredReplicas(r *int32) int32 {
	if r != nil {
		return *r
	}
	return 1
}

// DeploymentReady reports whether a Deployment has its desired replicas available.
func DeploymentReady(d *appsv1.Deployment) bool {
	return d.Status.AvailableReplicas >= desiredReplicas(d.Spec.Replicas)
}

// StatefulSetReady reports whether a StatefulSet has its desired replicas ready.
func StatefulSetReady(s *appsv1.StatefulSet) bool {
	return s.Status.ReadyReplicas >= desiredReplicas(s.Spec.Replicas)
}

// gitOpsTier computes the GitOps tier and reason from per-tool presence/health.
// Callers handle the all-absent case before calling this.
func gitOpsTier(flux FluxInfo, argo ArgoInfo) (Tier, string) {
	var reasons []string
	if flux.Present && !flux.Healthy {
		reasons = append(reasons, "Flux installed but "+fluxKustomizeController+" is not ready")
	}
	if argo.Present && !argo.Healthy {
		reasons = append(reasons, "Argo installed but "+argoAppController+" is not ready")
	}
	fluxOK := !flux.Present || flux.Healthy
	argoOK := !argo.Present || argo.Healthy
	return Classify(true, fluxOK && argoOK), strings.Join(reasons, "; ")
}

// Detector classifies capabilities for one cluster.
type Detector struct {
	cs kubernetes.Interface
}

func NewDetector(cs kubernetes.Interface) *Detector { return &Detector{cs: cs} }

// servedGroups returns the set of served "group/version" strings plus bare
// group names. Callers pin explicit versions where they matter.
func (d *Detector) servedGroups(ctx context.Context) (map[string]bool, error) {
	// client-go's ServerGroups() is not context-aware; _ = ctx is intentional
	// and should be revisited when upstream adds context support.
	_ = ctx
	lists, err := d.cs.Discovery().ServerGroups()
	if err != nil {
		return nil, err
	}
	served := make(map[string]bool)
	for _, g := range lists.Groups {
		for _, v := range g.Versions {
			served[v.GroupVersion] = true // e.g. "gateway.networking.k8s.io/v1"
			served[g.Name] = true         // bare group presence
		}
	}
	return served, nil
}

func (d *Detector) Detect(ctx context.Context) Set {
	served, err := d.servedGroups(ctx)
	if err != nil {
		// Error-honesty: a discovery-degraded cluster reports every capability
		// as Absent with the failure reason, never as "nothing installed".
		msg := fmt.Sprintf("capability detection failed: %v", err)
		return Set{
			GitOps:          GitOpsCapability{Base: Base{Tier: Absent, Reason: msg}},
			Network:         NetworkCapability{Base: Base{Tier: Absent, Reason: msg}},
			CertManager:     CertManagerCapability{Base: Base{Tier: Absent, Reason: msg}},
			ExternalSecrets: ExternalSecretsCapability{Base: Base{Tier: Absent, Reason: msg}},
			Reloader:        ReloaderCapability{Base: Base{Tier: Absent, Reason: msg}},
			Karpenter:       KarpenterCapability{Base: Base{Tier: Absent, Reason: msg}},
		}
	}
	return Set{
		GitOps:          d.detectGitOps(ctx, served),
		Network:         d.detectNetwork(ctx, served),
		CertManager:     d.detectCertManager(ctx, served),
		ExternalSecrets: d.detectExternalSecrets(ctx, served),
		Reloader:        d.detectReloader(ctx),
		Karpenter:       d.detectKarpenter(ctx, served),
	}
}

func (d *Detector) detectGitOps(ctx context.Context, served map[string]bool) GitOpsCapability {
	fluxPresent := served["kustomize.toolkit.fluxcd.io"]
	argoPresent := served["argoproj.io"]

	out := GitOpsCapability{}
	out.Flux.Present = fluxPresent
	out.Argo.Present = argoPresent
	out.Coexistence = fluxPresent && argoPresent

	if !fluxPresent && !argoPresent {
		out.Base = Base{Tier: Absent, Reason: "no Flux or Argo CRDs installed"}
		return out
	}

	if fluxPresent {
		out.Flux.Healthy = d.controllerReady(ctx, deploymentWorkload, fluxNamespace, fluxKustomizeController)
		if out.Flux.Healthy {
			out.Flux.Controllers = []string{fluxKustomizeController}
		}
	}
	if argoPresent {
		out.Argo.Healthy = d.controllerReady(ctx, statefulSetWorkload, argoNamespace, argoAppController)
	}

	tier, reason := gitOpsTier(out.Flux, out.Argo)
	out.Base.Tier = tier
	out.Base.Reason = reason
	return out
}

func (d *Detector) detectNetwork(ctx context.Context, served map[string]bool) NetworkCapability {
	out := NetworkCapability{}
	out.CiliumPresent = served["cilium.io"]
	if out.CiliumPresent {
		out.ClusterMesh = d.clusterMeshInstalled(ctx)
		out.HasHubble = d.hubbleInstalled(ctx)
	}

	gwVersion := ""
	switch {
	case served["gateway.networking.k8s.io/v1"]:
		gwVersion = "v1"
	case served["gateway.networking.k8s.io/v1beta1"]:
		gwVersion = "v1beta1"
	}
	out.GatewayAPIVersion = gwVersion
	out.HasEnvoyProxy = served["gateway.envoyproxy.io"]

	gwPresent := gwVersion != ""
	if !gwPresent && !out.CiliumPresent {
		out.Base = Base{Tier: Absent, Reason: "no Gateway API or Cilium CRDs installed"}
		return out
	}

	// Healthy requires Gateway API AND its data-plane operator (EnvoyProxy).
	healthy := gwPresent && out.HasEnvoyProxy
	out.Base.Tier = Classify(true, healthy)
	if !healthy {
		if gwPresent && !out.HasEnvoyProxy {
			out.Base.Reason = "Gateway API present but no EnvoyProxy (data plane) installed"
		} else if !gwPresent {
			out.Base.Reason = "Cilium present but Gateway API not installed"
		}
	}
	return out
}

// detectCertManager classifies cert-manager: present iff the cert-manager.io
// CRDs are served, healthy iff the cert-manager controller Deployment is ready.
// HasACME records whether the acme.cert-manager.io group (Order/Challenge) is
// also served, so the chain resolver knows to walk it.
func (d *Detector) detectCertManager(ctx context.Context, served map[string]bool) CertManagerCapability {
	out := CertManagerCapability{}
	out.Present = served["cert-manager.io"]
	out.HasACME = served["acme.cert-manager.io"]
	if !out.Present {
		out.Base = Base{Tier: Absent, Reason: "no cert-manager CRDs installed"}
		return out
	}
	out.Healthy = d.controllerReady(ctx, deploymentWorkload, certManagerNamespace, certManagerController)
	out.Base.Tier = Classify(true, out.Healthy)
	if !out.Healthy {
		out.Base.Reason = "cert-manager installed but " + certManagerController + " is not ready"
	}
	return out
}

// detectExternalSecrets classifies the External Secrets Operator: present iff
// the external-secrets.io CRDs are served, healthy iff the external-secrets
// controller Deployment is ready. Mirrors detectCertManager.
func (d *Detector) detectExternalSecrets(ctx context.Context, served map[string]bool) ExternalSecretsCapability {
	out := ExternalSecretsCapability{}
	out.Present = served["external-secrets.io"]
	if !out.Present {
		out.Base = Base{Tier: Absent, Reason: "no External Secrets Operator CRDs installed"}
		return out
	}
	out.Healthy = d.controllerReady(ctx, deploymentWorkload, externalSecretsNamespace, externalSecretsController)
	out.Base.Tier = Classify(true, out.Healthy)
	if !out.Healthy {
		out.Base.Reason = "External Secrets Operator installed but " + externalSecretsController + " is not ready"
	}
	return out
}

// detectKarpenter classifies Karpenter: present iff the karpenter.sh CRDs
// (NodePool/NodeClaim) are served, healthy iff the karpenter controller
// Deployment is ready in either of its conventional namespaces (kube-system
// for newer EKS installs, karpenter for the classic helm chart).
func (d *Detector) detectKarpenter(ctx context.Context, served map[string]bool) KarpenterCapability {
	out := KarpenterCapability{}
	out.Present = served["karpenter.sh"]
	if !out.Present {
		out.Base = Base{Tier: Absent, Reason: "no Karpenter CRDs installed"}
		return out
	}
	for _, ns := range []string{"kube-system", "karpenter"} {
		if d.controllerReady(ctx, deploymentWorkload, ns, "karpenter") {
			out.Healthy = true
			break
		}
	}
	out.Base.Tier = Classify(true, out.Healthy)
	if !out.Healthy {
		out.Base.Reason = "Karpenter CRDs installed but the karpenter controller is not ready (checked kube-system, karpenter)"
	}
	return out
}

// detectReloader classifies Stakater Reloader. It cannot use discovery (no served
// group) or controllerReady (namespace/name vary by install), so it lists
// Deployments cluster-wide by the reloader name label: present iff any match,
// healthy iff any matched Deployment is ready. A list error is reported as
// Absent WITH the failure reason (error honesty) - never silently "not installed".
func (d *Detector) detectReloader(ctx context.Context) ReloaderCapability {
	out := ReloaderCapability{}
	deps, err := d.cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: reloaderNameSelector})
	if err != nil {
		out.Base = Base{Tier: Absent, Reason: "could not list Deployments to detect Reloader: " + err.Error()}
		return out
	}
	if len(deps.Items) == 0 {
		out.Base = Base{Tier: Absent, Reason: "no Stakater Reloader controller found (" + reloaderNameSelector + ")"}
		return out
	}
	out.Present = true
	for i := range deps.Items {
		if DeploymentReady(&deps.Items[i]) {
			out.Healthy = true
			break
		}
	}
	out.Base.Tier = Classify(out.Present, out.Healthy)
	if !out.Healthy {
		out.Base.Reason = "Reloader controller present but not ready"
	}
	return out
}

// hubbleInstalled reports whether Hubble is enabled: the hubble-relay
// Deployment OR the hubble-peer Service in kube-system (the peer service exists
// whenever Hubble is on, relay only with the full stack). Same one-shot-Get
// shape as clusterMeshInstalled.
func (d *Detector) hubbleInstalled(ctx context.Context) bool {
	if _, err := d.cs.AppsV1().Deployments("kube-system").Get(ctx, "hubble-relay", metav1.GetOptions{}); err == nil {
		return true
	}
	if _, err := d.cs.CoreV1().Services("kube-system").Get(ctx, "hubble-peer", metav1.GetOptions{}); err == nil {
		return true
	}
	return false
}

// clusterMeshInstalled reports whether Cilium ClusterMesh is installed: the
// clustermesh-apiserver Deployment OR the cilium-clustermesh Secret in kube-system.
func (d *Detector) clusterMeshInstalled(ctx context.Context) bool {
	if _, err := d.cs.AppsV1().Deployments("kube-system").Get(ctx, "clustermesh-apiserver", metav1.GetOptions{}); err == nil {
		return true
	}
	if _, err := d.cs.CoreV1().Secrets("kube-system").Get(ctx, "cilium-clustermesh", metav1.GetOptions{}); err == nil {
		return true
	}
	return false
}

// controllerReady reports whether a controller workload has its desired replicas
// ready. Deployments check AvailableReplicas; StatefulSets check ReadyReplicas.
func (d *Detector) controllerReady(ctx context.Context, kind workloadKind, ns, name string) bool {
	switch kind {
	case statefulSetWorkload:
		sts, err := d.cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return StatefulSetReady(sts)
	default: // deploymentWorkload
		dep, err := d.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return DeploymentReady(dep)
	}
}
