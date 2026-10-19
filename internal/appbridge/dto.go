// Package appbridge is the only Wails-aware Go layer. It projects the pure
// fleet data layer into JSON-friendly DTOs and pushes updates to the frontend.
package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// ClusterDTO is the JSON-friendly per-cluster shape the frontend consumes.
type ClusterDTO struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	NodesReady int    `json:"nodesReady"`
	NodesTotal int    `json:"nodesTotal"`
	Pods       int    `json:"pods"`
	// PodsKnown: a complete pod read has happened (Pods is evidence, an
	// observed zero included); false is unknown, never zero. PodsStale: Pods
	// is the last good count.
	PodsKnown    bool   `json:"podsKnown"`
	PodsStale    bool   `json:"podsStale"`
	Version      string `json:"version"`
	GitopsTier   string `json:"gitopsTier"`
	GitopsReason string `json:"gitopsReason"`
	FluxPresent  bool   `json:"fluxPresent"`
	// FluxOperatorPresent: the Flux Operator's API is served (coverage S1a).
	FluxOperatorPresent bool `json:"fluxOperatorPresent"`
	// FluxImagesPresent: Flux image automation's API is served (S1b).
	FluxImagesPresent bool `json:"fluxImagesPresent"`
	// FluxCompositionPresent / FluxNotificationsPresent: the S1c views'
	// kinds are served (or discovery cannot rule them out).
	FluxCompositionPresent   bool   `json:"fluxCompositionPresent"`
	FluxNotificationsPresent bool   `json:"fluxNotificationsPresent"`
	FluxHealthy              bool   `json:"fluxHealthy"`
	ArgoPresent              bool   `json:"argoPresent"`
	NetworkTier              string `json:"networkTier"`
	NetworkReason            string `json:"networkReason"`
	GatewayAPIVersion        string `json:"gatewayAPIVersion"`
	// GatewayExperimentalKinds / GatewayExperimentalGap: the experimental
	// channel's own group, recognised and never read (coverage S7b §7).
	GatewayExperimentalKinds []string `json:"gatewayExperimentalKinds"`
	GatewayExperimentalGap   string   `json:"gatewayExperimentalGap"`
	CiliumPresent            bool     `json:"ciliumPresent"`
	ClusterMesh              bool     `json:"clusterMesh"`
	HasHubble                bool     `json:"hasHubble"`
	KarpenterPresent         bool     `json:"karpenterPresent"`
	VeleroPresent            bool     `json:"veleroPresent"`
	KEDAPresent              bool     `json:"kedaPresent"`
	AdmissionPresent         bool     `json:"admissionPresent"`
	LoadBalancingPresent     bool     `json:"loadBalancingPresent"`
	AlertsPresent            bool     `json:"alertsPresent"`     // S8a: the alerts snapshot's own evidence
	LogHistoryPresent        bool     `json:"logHistoryPresent"` // S8b: a Loki configured
	MetalLBPresent           bool     `json:"metallbPresent"`
	KarpenterHealthy         bool     `json:"karpenterHealthy"`
	CertManagerTier          string   `json:"certManagerTier"`
	CertManagerReason        string   `json:"certManagerReason"`
	CertManagerPresent       bool     `json:"certManagerPresent"`
	CertManagerHealthy       bool     `json:"certManagerHealthy"`
	ExternalSecretsTier      string   `json:"externalSecretsTier"`
	ExternalSecretsReason    string   `json:"externalSecretsReason"`
	ExternalSecretsPresent   bool     `json:"externalSecretsPresent"`
	ExternalSecretsHealthy   bool     `json:"externalSecretsHealthy"`
	ReloaderTier             string   `json:"reloaderTier"`
	ReloaderReason           string   `json:"reloaderReason"`
	ReloaderPresent          bool     `json:"reloaderPresent"`
	ReloaderHealthy          bool     `json:"reloaderHealthy"`
	Env                      string   `json:"env"`
	Region                   string   `json:"region"`
	Provider                 string   `json:"provider"`
	Group                    string   `json:"group"`
	// Protected is the EFFECTIVE protection - exactly what the backend
	// ProtectGuard enforces (explicit flag OR a prd/prod/production token in
	// env/name/context). The UI decides on typed arming from this field; the
	// raw flag alone left inferred-prod clusters with a plain confirm that the
	// backend then refused ("not armed"). ProtectedInferred marks the
	// token-inferred case for display.
	Protected         bool  `json:"protected"`
	ProtectedInferred bool  `json:"protectedInferred"`
	AgeSeconds        int64 `json:"ageSeconds"`
}

// ToDTO projects a snapshot + its config into a ClusterDTO. now is injected so
// age is deterministic in tests and consistent across a batch.
func ToDTO(s fleet.Snapshot, cc config.ClusterConfig, now time.Time) ClusterDTO {
	age := int64(0)
	if !s.LastSync.IsZero() {
		age = int64(now.Sub(s.LastSync).Seconds())
		if age < 0 {
			age = 0
		}
	}
	tags := cc.EffectiveTags()
	return ClusterDTO{
		Name:                     s.Name,
		State:                    s.State.String(),
		Reason:                   s.Reason,
		NodesReady:               s.NodesReady,
		NodesTotal:               s.NodesTotal,
		Pods:                     s.Pods,
		PodsKnown:                s.PodsKnown,
		PodsStale:                s.PodsStale,
		Version:                  s.Version,
		GitopsTier:               s.Capabilities.GitOps.Tier.String(),
		GitopsReason:             s.Capabilities.GitOps.Reason,
		FluxPresent:              s.Capabilities.GitOps.Flux.Present,
		FluxOperatorPresent:      s.Capabilities.GitOps.FluxOperator.Present,
		FluxImagesPresent:        s.Capabilities.GitOps.Flux.Images.Served,
		FluxCompositionPresent:   s.Capabilities.GitOps.Flux.Components.Composition,
		FluxNotificationsPresent: s.Capabilities.GitOps.Flux.Components.Notifications,
		FluxHealthy:              s.Capabilities.GitOps.Flux.Healthy,
		ArgoPresent:              s.Capabilities.GitOps.Argo.Present,
		NetworkTier:              s.Capabilities.Network.Tier.String(),
		NetworkReason:            s.Capabilities.Network.Reason,
		GatewayAPIVersion:        s.Capabilities.Network.GatewayAPIVersion,
		GatewayExperimentalKinds: append([]string{}, s.Capabilities.Network.GatewayExperimentalKinds...),
		GatewayExperimentalGap:   s.Capabilities.Network.GatewayExperimentalGap,
		CiliumPresent:            s.Capabilities.Network.CiliumPresent,
		ClusterMesh:              s.Capabilities.Network.ClusterMesh,
		HasHubble:                s.Capabilities.Network.HasHubble,
		KarpenterPresent:         s.Capabilities.Karpenter.Present,
		VeleroPresent:            s.Capabilities.Velero.Present,
		KEDAPresent:              s.Capabilities.KEDA.Present,
		AdmissionPresent:         s.Capabilities.Admission.Present,
		LoadBalancingPresent:     s.Capabilities.LoadBalancing.Present(s.Capabilities.Network.CiliumPresent),
		AlertsPresent:            s.Capabilities.Alerting.Present,
		LogHistoryPresent:        s.Capabilities.LogHistory.Present,
		MetalLBPresent:           s.Capabilities.LoadBalancing.MetalLB,
		KarpenterHealthy:         s.Capabilities.Karpenter.Healthy,
		CertManagerTier:          s.Capabilities.CertManager.Tier.String(),
		CertManagerReason:        s.Capabilities.CertManager.Reason,
		CertManagerPresent:       s.Capabilities.CertManager.Present,
		CertManagerHealthy:       s.Capabilities.CertManager.Healthy,
		ExternalSecretsTier:      s.Capabilities.ExternalSecrets.Tier.String(),
		ExternalSecretsReason:    s.Capabilities.ExternalSecrets.Reason,
		ExternalSecretsPresent:   s.Capabilities.ExternalSecrets.Present,
		ExternalSecretsHealthy:   s.Capabilities.ExternalSecrets.Healthy,
		ReloaderTier:             s.Capabilities.Reloader.Tier.String(),
		ReloaderReason:           s.Capabilities.Reloader.Reason,
		ReloaderPresent:          s.Capabilities.Reloader.Present,
		ReloaderHealthy:          s.Capabilities.Reloader.Healthy,
		Env:                      firstNonEmpty(cc.Environment, tags["env"]),
		Protected:                cc.IsProtected(),
		ProtectedInferred:        cc.IsProtected() && !cc.Protected,
		Region:                   tags["region"],
		Provider:                 tags["provider"],
		Group:                    cc.Group,
		AgeSeconds:               age,
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
