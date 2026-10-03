package capability

import (
	"context"
	"encoding/json"
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

	// gatewayClasses reports how many GatewayClasses exist at the given
	// Gateway API version and how many a controller has Accepted. nil selects
	// the default (a read through the discovery REST client); tests inject it.
	gatewayClasses func(ctx context.Context, version string) (total, accepted int, err error)
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

	// Only a FAULT degrades the tier (review 2026-10-03 F; "only faults
	// glow"). Cilium without Gateway API is a configuration choice, not a fault,
	// and which data plane serves Gateway API is not Klyx's to assume: Envoy
	// Gateway, Cilium, Istio and others all implement it. The implementation-
	// agnostic signal is the GatewayClass contract - a controller that serves a
	// class sets Accepted=True on it. Degraded means classes exist but no
	// controller accepted any of them (a missing or broken data plane).
	if !gwPresent {
		out.Base.Tier = Healthy
		return out
	}
	probe := d.gatewayClasses
	if probe == nil {
		probe = d.readGatewayClasses
	}
	total, accepted, err := probe(ctx, gwVersion)
	switch {
	case err != nil:
		// Could not read GatewayClasses: acceptance is UNKNOWN, and CRD presence
		// (Envoy Gateway, Cilium) cannot establish it. Say so rather than claim
		// Healthy (Codex batch-2 review R4).
		out.Base = Base{Tier: Degraded, Reason: "Gateway API installed but GatewayClasses could not be read (" + err.Error() + ") - data-plane health unknown"}
	case total > 0 && accepted == 0:
		out.Base = Base{Tier: Degraded, Reason: fmt.Sprintf("%d GatewayClass(es) exist but no controller has Accepted any - no working Gateway API data plane", total)}
	default:
		// Accepted classes, or Gateway API CRDs with no classes yet (not in use).
		out.Base.Tier = Healthy
	}
	return out
}

// readGatewayClasses lists GatewayClasses through the discovery REST client
// (the typed clientset has no Gateway API types) and counts the Accepted ones.
func (d *Detector) readGatewayClasses(ctx context.Context, version string) (int, int, error) {
	rc := d.cs.Discovery().RESTClient()
	if rc == nil {
		return 0, 0, fmt.Errorf("no REST client for gatewayclasses")
	}
	raw, err := rc.Get().AbsPath("/apis/gateway.networking.k8s.io", version, "gatewayclasses").Do(ctx).Raw()
	if err != nil {
		return 0, 0, err
	}
	return countAcceptedGatewayClasses(raw)
}

// countAcceptedGatewayClasses parses a GatewayClassList and counts the classes
// whose Accepted condition is True FOR THE CURRENT SPEC: a condition whose
// observedGeneration lags metadata.generation describes an older spec the
// controller has not yet re-evaluated, so it does not count (Codex batch-2
// review R4). A condition without observedGeneration is taken as current.
func countAcceptedGatewayClasses(raw []byte) (int, int, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Generation int64 `json:"generation"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type               string `json:"type"`
					Status             string `json:"status"`
					ObservedGeneration int64  `json:"observedGeneration"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, 0, fmt.Errorf("parse gatewayclasses: %w", err)
	}
	accepted := 0
	for _, it := range list.Items {
		gen := it.Metadata.Generation
		for _, c := range it.Status.Conditions {
			if c.Type != "Accepted" || c.Status != "True" {
				continue
			}
			if c.ObservedGeneration == 0 || gen == 0 || c.ObservedGeneration >= gen {
				accepted++
			}
			break
		}
	}
	return len(list.Items), accepted, nil
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
