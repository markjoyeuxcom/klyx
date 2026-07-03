package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/externalsecrets"
)

// ExternalSecretDTO is the JSON projection of an ESO ExternalSecret.
type ExternalSecretDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	// StoreKind/StoreName bind the secret to its (Cluster)SecretStore - the
	// headline for a stuck secret, exactly as the bound issuer is in M11.
	StoreKind string `json:"storeKind"`
	StoreName string `json:"storeName"`
	// TargetSecret is the Kubernetes Secret ESO writes.
	TargetSecret string `json:"targetSecret"`
	// RefreshIntervalSeconds is spec.refreshInterval; 0 means refresh disabled.
	RefreshIntervalSeconds int64 `json:"refreshIntervalSeconds"`
	// LastRefreshAgeSeconds is now->status.refreshTime, computed server-side
	// for determinism. 0 when the secret has never synced.
	LastRefreshAgeSeconds int64 `json:"lastRefreshAgeSeconds"`
	// Stale: refresh is enabled and the last successful sync is more than 2x
	// the interval ago - the controller has stopped refreshing even though
	// Ready may still describe the last good sync.
	Stale bool `json:"stale"`
}

func toExternalSecretDTO(e externalsecrets.ExternalSecret, now time.Time) ExternalSecretDTO {
	dto := ExternalSecretDTO{
		Namespace:              e.Namespace,
		Name:                   e.Name,
		State:                  string(e.State),
		Reason:                 e.Reason,
		Message:                e.Message,
		StoreKind:              string(e.StoreKind),
		StoreName:              e.StoreName,
		TargetSecret:           e.TargetSecret,
		RefreshIntervalSeconds: int64(e.RefreshInterval.Seconds()),
		Stale:                  e.Stale,
	}
	if !e.RefreshTime.IsZero() {
		age := int64(now.Sub(e.RefreshTime).Seconds())
		if age < 0 {
			age = 0
		}
		dto.LastRefreshAgeSeconds = age
	}
	return dto
}

// StoreDTO is the JSON projection of a SecretStore /
// ClusterSecretStore's validation state.
type StoreDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Provider  string `json:"provider"`
}

func toStoreDTO(s externalsecrets.Store) StoreDTO {
	return StoreDTO{
		Kind:      string(s.Kind),
		Namespace: s.Namespace,
		Name:      s.Name,
		State:     string(s.State),
		Reason:    s.Reason,
		Message:   s.Message,
		Provider:  s.Provider,
	}
}
