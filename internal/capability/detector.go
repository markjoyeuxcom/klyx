package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
// FluxIssue and ArgoIssue name each GitOps tool's controller problem on its
// own ("" when present and ready, or absent): the health summary reports
// them per task, where the tier joins them. An unverified controller says
// so - never "not ready".
func (g GitOpsCapability) FluxIssue() string {
	f, gaps := g.FluxProblems()
	return strings.Join(append(f, gaps...), "; ")
}

// FluxProblems splits Flux's controller problems into proven failures and
// evidence gaps, aggregated across every read (Codex's S1a implementation
// review 3, finding 2): the operator's own (S1a §2), kustomize-controller's
// when it was actually read, and the image controllers' (S1b §3). One
// read's gap never hides another's failure; a gap alone is unknown, never
// "not ready".
func (g GitOpsCapability) FluxProblems() (failures, gaps []string) {
	failures, gaps = g.FluxOperator.Problems()
	switch {
	case !g.Flux.Present || g.Flux.Healthy || g.Flux.Synthesized:
	case !g.Flux.Verified:
		gaps = append(gaps, "Flux installed but "+fluxKustomizeController+" health is unverified ("+g.Flux.Why+")")
	default:
		failures = append(failures, "Flux installed but "+fluxKustomizeController+" is not ready")
	}
	f, gp := g.Flux.Images.Problems()
	return append(failures, f...), append(gaps, gp...)
}

func (g GitOpsCapability) ArgoIssue() string {
	switch {
	case !g.Argo.Present || g.Argo.Healthy:
		return ""
	case !g.Argo.Verified:
		return "Argo installed but " + argoAppController + " health is unverified (" + g.Argo.Why + ")"
	default:
		return "Argo installed but " + argoAppController + " is not ready"
	}
}

// applyRead sets a GitOps tool's health from a controller read.
func applyRead(r ControllerRead, healthy, verified *bool, why *string) {
	*healthy, *verified, *why = r.Health == ControllerReady, r.Health != ControllerUnverified, ""
	if r.Health == ControllerUnverified {
		*why = r.Why
	}
}

func gitOpsTier(flux FluxInfo, argo ArgoInfo) (Tier, string) {
	var reasons []string
	g := GitOpsCapability{Flux: flux, Argo: argo}
	if r := g.FluxIssue(); r != "" {
		reasons = append(reasons, r)
	}
	if r := g.ArgoIssue(); r != "" {
		reasons = append(reasons, r)
	}
	fluxOK := !flux.Present || (flux.Healthy && flux.Images.Issue() == "")
	argoOK := !argo.Present || argo.Healthy
	return Classify(true, fluxOK && argoOK), strings.Join(reasons, "; ")
}

// Detector classifies capabilities for one cluster.
type Detector struct {
	cs kubernetes.Interface

	// gatewayClasses reports how many GatewayClasses exist at the given
	// Gateway API version and how many a controller has Accepted. nil selects
	// the default (a read through the discovery REST client); tests inject it.
	gatewayClasses func(ctx context.Context, version string) (gatewayClassCounts, error)
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
	set, _ := d.DetectErr(ctx)
	return set
}

// DetectErr is Detect plus the discovery error, so a periodic re-probe can
// tell a failed probe (which confirms nothing) from a real absence.
func (d *Detector) DetectErr(ctx context.Context) (Set, error) {
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
		}, err
	}
	return Set{
		GitOps:          d.detectGitOps(ctx, served),
		Network:         d.detectNetwork(ctx, served),
		CertManager:     d.detectCertManager(ctx, served),
		ExternalSecrets: d.detectExternalSecrets(ctx, served),
		Reloader:        d.detectReloader(ctx),
		Karpenter:       d.detectKarpenter(ctx, served),
	}, nil
}

func (d *Detector) detectGitOps(ctx context.Context, served map[string]bool) GitOpsCapability {
	kustomize := served["kustomize.toolkit.fluxcd.io"]
	argoPresent := served["argoproj.io"]

	out := GitOpsCapability{}
	out.FluxOperator = d.detectFluxOperator(ctx, served)
	// image automation is Flux too, alone or not (Codex's S1b
	// implementation review, finding 3)
	fluxPresent := kustomize || out.FluxOperator.Present || served[fluxImageGroup]
	out.Flux.Present = fluxPresent
	out.Argo.Present = argoPresent
	out.Coexistence = fluxPresent && argoPresent

	if !fluxPresent && !argoPresent {
		out.Base = Base{Tier: Absent, Reason: "no Flux or Argo CRDs installed"}
		return out
	}

	// An operator-managed install runs its controllers in the operator's
	// runtime namespace; one resolved namespace is probed, otherwise the
	// conventional one (S1a §2).
	ns := fluxNamespace
	if rt := out.FluxOperator.RuntimeNamespaces(); len(rt) == 1 {
		ns = rt[0]
	}
	if fluxPresent {
		out.Flux.Images = d.detectImageControllers(ctx, served, ns)
	}
	switch {
	case kustomize:
		applyRead(d.controllerProbe(ctx, deploymentWorkload, ns, fluxKustomizeController), &out.Flux.Healthy, &out.Flux.Verified, &out.Flux.Why)
		if out.Flux.Healthy {
			out.Flux.Controllers = []string{fluxKustomizeController}
		}
	case out.FluxOperator.Present:
		// the operator alone, before a FluxInstance installs Flux: its own
		// health stands for the installation's (FluxOperatorInfo.Issue)
		out.Flux.Verified, out.Flux.Why = out.FluxOperator.Verified, out.FluxOperator.Why
		out.Flux.Healthy = out.FluxOperator.Verified && out.FluxOperator.Issue() == ""
		out.Flux.Synthesized = true
	case fluxPresent:
		// image automation alone: its controllers stand for the
		// installation; no kustomize-controller is expected
		f, g := out.Flux.Images.Problems()
		out.Flux.Verified, out.Flux.Healthy, out.Flux.Synthesized = len(g) == 0, len(f)+len(g) == 0, true
	}
	if argoPresent {
		applyRead(d.controllerProbe(ctx, statefulSetWorkload, argoNamespace, argoAppController), &out.Argo.Healthy, &out.Argo.Verified, &out.Argo.Why)
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
	gc, err := probe(ctx, gwVersion)
	switch {
	case err != nil:
		// Could not read GatewayClasses: acceptance is UNKNOWN, and CRD presence
		// (Envoy Gateway, Cilium) cannot establish it. Say so rather than claim
		// Healthy (Codex batch-2 review R4).
		out.Base = Base{Tier: Degraded, Reason: "Gateway API installed but GatewayClasses could not be read (" + err.Error() + ") - data-plane health unknown"}
		out.GatewayUnverified = true
	case gc.Total > 0 && gc.Accepted == 0 && gc.Rejected == gc.Total:
		out.Base = Base{Tier: Degraded, Reason: fmt.Sprintf("%d GatewayClass(es) exist and their controllers rejected every one (Accepted=False) - no working Gateway API data plane", gc.Total)}
	case gc.Total > 0 && gc.Accepted == 0:
		// Pending or unreported acceptance proves nothing about the data
		// plane: no controller may be running, or it has not got to it yet.
		out.Base = Base{Tier: Degraded, Reason: fmt.Sprintf("%d GatewayClass(es) exist but none is Accepted yet (%d rejected, %d pending or unreported) - data-plane health unverified", gc.Total, gc.Rejected, gc.Total-gc.Rejected)}
		out.GatewayUnverified = true
	default:
		// Accepted classes, or Gateway API CRDs with no classes yet (not in use).
		out.Base.Tier = Healthy
	}
	return out
}

// readGatewayClasses lists GatewayClasses through the discovery REST client
// (the typed clientset has no Gateway API types) and counts the Accepted ones.
func (d *Detector) readGatewayClasses(ctx context.Context, version string) (gatewayClassCounts, error) {
	rc := d.cs.Discovery().RESTClient()
	if rc == nil {
		return gatewayClassCounts{}, fmt.Errorf("no REST client for gatewayclasses")
	}
	raw, err := rc.Get().AbsPath("/apis/gateway.networking.k8s.io", version, "gatewayclasses").Do(ctx).Raw()
	if err != nil {
		return gatewayClassCounts{}, err
	}
	return countAcceptedGatewayClasses(raw)
}

// WithGatewayClassReader replaces how the GatewayClassList is read (its raw
// JSON for a served version); counting stays the detector's. Tests and
// tools use it where no discovery REST client exists.
func (d *Detector) WithGatewayClassReader(read func(ctx context.Context, version string) ([]byte, error)) *Detector {
	d.gatewayClasses = func(ctx context.Context, version string) (gatewayClassCounts, error) {
		raw, err := read(ctx, version)
		if err != nil {
			return gatewayClassCounts{}, err
		}
		return countAcceptedGatewayClasses(raw)
	}
	return d
}

// gatewayClassCounts: Accepted and Rejected are current-spec verdicts
// (Accepted True or False for the class's generation); the rest are pending
// or unreported.
type gatewayClassCounts struct{ Total, Accepted, Rejected int }

// countAcceptedGatewayClasses parses a GatewayClassList and counts the
// classes whose Accepted condition is True, or False, FOR THE CURRENT SPEC.
// A condition whose observedGeneration is absent or lags metadata.generation
// describes no current decision (Codex batch-2 review R4; 2c review): the
// class is pending or unreported. Only a class with no generation is taken
// as current, as gwapi.condState does.
func countAcceptedGatewayClasses(raw []byte) (gatewayClassCounts, error) {
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
		return gatewayClassCounts{}, fmt.Errorf("parse gatewayclasses: %w", err)
	}
	out := gatewayClassCounts{Total: len(list.Items)}
	for _, it := range list.Items {
		gen := it.Metadata.Generation
		for _, c := range it.Status.Conditions {
			if c.Type != "Accepted" {
				continue
			}
			// Current only when it observed this spec (gwapi.condState): a
			// condition with no or an older observedGeneration on a class with
			// a known generation is pending or unreported, never a verdict.
			if gen == 0 || c.ObservedGeneration >= gen {
				switch c.Status {
				case "True":
					out.Accepted++
				case "False":
					out.Rejected++
				}
			}
			break
		}
	}
	return out, nil
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
	out.Healthy, out.Verified, out.Base.Reason = controllerVerdict(d.controllerProbe(ctx, deploymentWorkload, certManagerNamespace, certManagerController),
		"cert-manager installed but "+certManagerController+" is not ready",
		"cert-manager installed but "+certManagerController+" health is unverified")
	out.Base.Tier = Classify(true, out.Healthy)
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
	out.Healthy, out.Verified, out.Base.Reason = controllerVerdict(d.controllerProbe(ctx, deploymentWorkload, externalSecretsNamespace, externalSecretsController),
		"External Secrets Operator installed but "+externalSecretsController+" is not ready",
		"External Secrets Operator installed but "+externalSecretsController+" health is unverified")
	out.Base.Tier = Classify(true, out.Healthy)
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
	// The first Deployment actually read decides (as the Karpenter lens
	// does); none read in either namespace is unverified, never an outage.
	read := ControllerRead{Health: ControllerUnverified}
	var gaps []string
	for _, ns := range []string{"kube-system", "karpenter"} {
		r := d.controllerProbe(ctx, deploymentWorkload, ns, "karpenter")
		if r.Health != ControllerUnverified {
			read = r
			break
		}
		gaps = append(gaps, r.Why)
	}
	if read.Health == ControllerUnverified {
		read.Why = strings.Join(gaps, "; ")
	}
	out.Healthy, out.Verified, out.Base.Reason = controllerVerdict(read,
		"Karpenter CRDs installed but the karpenter controller is not ready",
		"Karpenter CRDs installed but the karpenter controller's health is unverified")
	out.Base.Tier = Classify(true, out.Healthy)
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

// ControllerHealth is what reading a controller workload proved.
type ControllerHealth int

const (
	// ControllerUnverified: the workload could not be read (forbidden, a
	// transport failure) or none exists under the conventional name - no
	// claim either way.
	ControllerUnverified ControllerHealth = iota
	ControllerNotReady                    // read: fewer ready replicas than desired
	ControllerReady                       // read: its desired replicas are ready
)

// ControllerRead is one controller read: its health and, when unverified,
// why. Only a workload actually read is evidence.
type ControllerRead struct {
	Health ControllerHealth
	Why    string
}

// controllerProbe reads a controller workload. Deployments check
// AvailableReplicas; StatefulSets check ReadyReplicas.
func (d *Detector) controllerProbe(ctx context.Context, kind workloadKind, ns, name string) ControllerRead {
	var ready bool
	var err error
	kindName := "Deployment"
	switch kind {
	case statefulSetWorkload:
		kindName = "StatefulSet"
		var sts *appsv1.StatefulSet
		if sts, err = d.cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			ready = StatefulSetReady(sts)
		}
	default: // deploymentWorkload
		var dep *appsv1.Deployment
		if dep, err = d.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			ready = DeploymentReady(dep)
		}
	}
	switch {
	case apierrors.IsNotFound(err):
		return ControllerRead{Health: ControllerUnverified, Why: fmt.Sprintf("no %s %s/%s", kindName, ns, name)}
	case err != nil:
		return ControllerRead{Health: ControllerUnverified, Why: fmt.Sprintf("%s %s/%s unreadable: %v", kindName, ns, name, err)}
	case ready:
		return ControllerRead{Health: ControllerReady}
	default:
		return ControllerRead{Health: ControllerNotReady}
	}
}

// controllerVerdict turns a read into (healthy, verified) and the reason a
// capability states: not ready only from a workload actually read.
func controllerVerdict(r ControllerRead, notReady, unverified string) (healthy, verified bool, reason string) {
	switch r.Health {
	case ControllerReady:
		return true, true, ""
	case ControllerNotReady:
		return false, true, notReady
	default:
		return false, false, unverified + " (" + r.Why + ")"
	}
}
