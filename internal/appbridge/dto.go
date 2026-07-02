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
	Name               string `json:"name"`
	State              string `json:"state"`
	Reason             string `json:"reason"`
	NodesReady         int    `json:"nodesReady"`
	NodesTotal         int    `json:"nodesTotal"`
	Pods               int    `json:"pods"`
	Version            string `json:"version"`
	GitopsTier         string `json:"gitopsTier"`
	GitopsReason       string `json:"gitopsReason"`
	FluxPresent        bool   `json:"fluxPresent"`
	FluxHealthy        bool   `json:"fluxHealthy"`
	ArgoPresent        bool   `json:"argoPresent"`
	NetworkTier        string `json:"networkTier"`
	NetworkReason      string `json:"networkReason"`
	GatewayAPIVersion  string `json:"gatewayAPIVersion"`
	CiliumPresent      bool   `json:"ciliumPresent"`
	ClusterMesh        bool   `json:"clusterMesh"`
	CertManagerTier    string `json:"certManagerTier"`
	CertManagerReason  string `json:"certManagerReason"`
	CertManagerPresent bool   `json:"certManagerPresent"`
	CertManagerHealthy bool   `json:"certManagerHealthy"`
	Env                string `json:"env"`
	Region             string `json:"region"`
	Provider           string `json:"provider"`
	Group              string `json:"group"`
	Protected          bool   `json:"protected"`
	AgeSeconds         int64  `json:"ageSeconds"`
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
		Name:               s.Name,
		State:              s.State.String(),
		Reason:             s.Reason,
		NodesReady:         s.NodesReady,
		NodesTotal:         s.NodesTotal,
		Pods:               s.Pods,
		Version:            s.Version,
		GitopsTier:         s.Capabilities.GitOps.Tier.String(),
		GitopsReason:       s.Capabilities.GitOps.Reason,
		FluxPresent:        s.Capabilities.GitOps.Flux.Present,
		FluxHealthy:        s.Capabilities.GitOps.Flux.Healthy,
		ArgoPresent:        s.Capabilities.GitOps.Argo.Present,
		NetworkTier:        s.Capabilities.Network.Tier.String(),
		NetworkReason:      s.Capabilities.Network.Reason,
		GatewayAPIVersion:  s.Capabilities.Network.GatewayAPIVersion,
		CiliumPresent:      s.Capabilities.Network.CiliumPresent,
		ClusterMesh:        s.Capabilities.Network.ClusterMesh,
		CertManagerTier:    s.Capabilities.CertManager.Tier.String(),
		CertManagerReason:  s.Capabilities.CertManager.Reason,
		CertManagerPresent: s.Capabilities.CertManager.Present,
		CertManagerHealthy: s.Capabilities.CertManager.Healthy,
		Env:                firstNonEmpty(cc.Environment, tags["env"]),
		Protected:          cc.Protected,
		Region:             tags["region"],
		Provider:           tags["provider"],
		Group:              cc.Group,
		AgeSeconds:         age,
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
