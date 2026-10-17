// Package capability models tiered capability detection. Presence-only is
// insufficient: a tool can be installed but not working, so state is tiered.
package capability

// Tier is the three-state classification for any capability.
type Tier int

const (
	Absent   Tier = iota // CRDs/APIs not served - view hidden
	Degraded             // installed but not fully working/partial - view renders with banner
	Healthy              // installed and operational
)

func (t Tier) String() string {
	switch t {
	case Absent:
		return "Absent"
	case Degraded:
		return "Degraded"
	case Healthy:
		return "Healthy"
	default:
		return "Unknown"
	}
}

// Classify maps (present, healthy) to a Tier.
func Classify(present, healthy bool) Tier {
	switch {
	case !present:
		return Absent
	case !healthy:
		return Degraded
	default:
		return Healthy
	}
}

// Base is embedded by every capability; it carries the tier and a human reason.
type Base struct {
	Tier   Tier
	Reason string
}

type FluxInfo struct {
	Present     bool
	Version     string
	Controllers []string
	Healthy     bool
	// Verified: the controller workload was actually read, so Healthy is a
	// claim. Unverified (forbidden, unreadable, or no workload under the
	// conventional name) claims nothing; Why names the gap.
	Verified bool
	Why      string
	// Images are the image automation controllers (coverage S1b §3), read
	// with detection.
	Images ImageControllers
	// Components are source-watcher and notification-controller
	// (coverage S1c §3), each needed only for its own resources.
	Components FluxComponents
	// Synthesized: no kustomize-controller was read; Healthy and Verified
	// stand for the operator's own state (an operator-only install) or the
	// image controllers' (an image-automation-only install), so they never
	// invent a controller failure of their own.
	Synthesized bool
}

type ArgoInfo struct {
	Present  bool
	Version  string
	Healthy  bool
	Verified bool // as FluxInfo
	Why      string
}

// GitOpsCapability models both tools together because the GitOps view needs them
// in one place, including their coexistence.
type GitOpsCapability struct {
	Base
	Flux        FluxInfo
	Argo        ArgoInfo
	Coexistence bool
	// FluxOperator is the Flux Operator (coverage S1a §2): Flux is present
	// when the operator is, even before any FluxInstance installs it.
	FluxOperator FluxOperatorInfo
}

// NetworkCapability is finer-grained: Gateway API present without EnvoyProxy is
// Degraded, not Absent.
type NetworkCapability struct {
	Base
	GatewayAPIVersion  string
	HasEnvoyProxy      bool
	CiliumPresent      bool
	HasHubble          bool
	ClusterMesh        bool
	IngressControllers []string
	// GatewayUnverified marks a Degraded tier that proves nothing: the
	// GatewayClasses could not be read, or none is Accepted yet without all
	// being rejected (acceptance pending or unreported). Data-plane health
	// is unknown, not known bad.
	GatewayUnverified bool
}

// CertManagerCapability models cert-manager: presence of the cert-manager.io
// CRDs plus controller health. HasACME notes the acme.cert-manager.io group
// (the Order/Challenge issuance chain) so the lens knows whether to resolve it.
// Flattened like NetworkCapability because cert-manager is a single tool.
type CertManagerCapability struct {
	Base
	Present  bool
	Healthy  bool
	Verified bool // the controller was read, so Healthy is a claim (as FluxInfo)
	HasACME  bool
}

// ExternalSecretsCapability models the External Secrets Operator: presence of
// the external-secrets.io CRDs plus controller health. Flattened like
// CertManagerCapability because ESO is a single tool.
type ExternalSecretsCapability struct {
	Base
	Present  bool
	Healthy  bool
	Verified bool // as CertManagerCapability
}

// ReloaderCapability models Stakater Reloader. Unlike every other capability,
// Reloader serves NO CRD/API group - it is just a controller Deployment that
// watches ConfigMap/Secret annotations - so presence is detected by the
// controller workload (label app.kubernetes.io/name=reloader), not discovery.
// Its value is context: it tells the inspector whether a workload's reload
// annotations actually have an operator behind them.
type ReloaderCapability struct {
	Base
	Present bool
	Healthy bool
}

// KarpenterCapability models Karpenter: presence of the karpenter.sh CRDs
// (NodePool/NodeClaim) plus controller health. The controller Deployment's
// namespace varies by install (kube-system on newer EKS defaults, karpenter
// on the classic helm chart), so health probes both.
type KarpenterCapability struct {
	Base
	Present  bool
	Healthy  bool
	Verified bool // as CertManagerCapability
}

// Set is the full per-cluster capability snapshot handed to the view layer.
type Set struct {
	GitOps          GitOpsCapability
	Network         NetworkCapability
	CertManager     CertManagerCapability
	ExternalSecrets ExternalSecretsCapability
	Reloader        ReloaderCapability
	Karpenter       KarpenterCapability
}
