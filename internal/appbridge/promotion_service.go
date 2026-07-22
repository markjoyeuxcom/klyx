package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/fleet"
)

// PromotionConn is the per-cluster surface the promotion tracker needs: one
// on-demand read-only snapshot verb.
type PromotionConn interface {
	GitOpsPromotion(ctx context.Context) (fleet.PromotionSnapshot, error)
}

// PromotionService is bound to JS: the fleet promotion tracker's backend. The
// frontend fans out one GetPromotion per cluster (mirrors the fleet-compare
// pattern) and joins the snapshots by unit identity.
type PromotionService struct {
	lookup func(string) (PromotionConn, bool)
}

func NewPromotionService(lookup func(string) (PromotionConn, bool)) *PromotionService {
	return &PromotionService{lookup: lookup}
}

// GetPromotion returns one cluster's GitOps promotion snapshot. Errors fold
// into the DTO; a cluster miss is honest, never an empty snapshot.
func (s *PromotionService) GetPromotion(cluster string) PromotionSnapshotDTO {
	out := PromotionSnapshotDTO{Units: []PromotionUnitDTO{}, Sources: []PromotionSourceDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	snap, err := conn.GitOpsPromotion(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.FluxPresent = snap.FluxPresent
	out.ArgoPresent = snap.ArgoPresent
	for _, u := range snap.Units {
		out.Units = append(out.Units, PromotionUnitDTO{
			Kind: u.Kind, Namespace: u.Namespace, Name: u.Name,
			SourceKind: u.SourceKind, SourceName: u.SourceName,
			Revision: u.Revision, Ready: u.Ready, Suspended: u.Suspended,
			Stale: u.Stale, Message: u.Message,
		})
	}
	for _, src := range snap.Sources {
		out.Sources = append(out.Sources, PromotionSourceDTO{
			Kind: src.Kind, Namespace: src.Namespace, Name: src.Name,
			Revision: src.Revision, Ready: src.Ready, Suspended: src.Suspended,
		})
	}
	return out
}
