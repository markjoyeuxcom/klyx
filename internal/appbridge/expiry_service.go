package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/fleet"
)

// ExpiryItemDTO mirrors expiry.Item with the absolute expiry as unix seconds
// (the UI computes the live countdown).
type ExpiryItemDTO struct {
	Source       string `json:"source"`
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Detail       string `json:"detail"`
	Subject      string `json:"subject"`
	NotAfterUnix int64  `json:"notAfterUnix"`
}

// ExpiryScanResultDTO is one cluster's scan: items soonest-first, per-source
// warnings, fold-in error.
type ExpiryScanResultDTO struct {
	Items    []ExpiryItemDTO `json:"items"`
	Warnings []string        `json:"warnings"`
	Error    string          `json:"error,omitempty"`
}

// ExpiryConn is the per-cluster surface the expiry lens needs.
type ExpiryConn interface {
	ExpiryScan(ctx context.Context) (fleet.ExpiryScanResult, error)
}

// ExpiryService is bound to JS: the fleet expiry-horizon lens's backend.
// Read-only; certificate PUBLIC material only, keys never leave the conn.
type ExpiryService struct {
	lookup func(string) (ExpiryConn, bool)
}

func NewExpiryService(lookup func(string) (ExpiryConn, bool)) *ExpiryService {
	return &ExpiryService{lookup: lookup}
}

// GetExpiryScan returns one cluster's expiring-material inventory.
func (s *ExpiryService) GetExpiryScan(cluster string) ExpiryScanResultDTO {
	out := ExpiryScanResultDTO{Items: []ExpiryItemDTO{}, Warnings: []string{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	r, err := conn.ExpiryScan(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	for _, it := range r.Items {
		out.Items = append(out.Items, ExpiryItemDTO{
			Source: it.Source, Namespace: it.Namespace, Name: it.Name,
			Detail: it.Detail, Subject: it.Subject, NotAfterUnix: it.NotAfter.Unix(),
		})
	}
	out.Warnings = append(out.Warnings, r.Warnings...)
	return out
}
