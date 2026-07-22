package appbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/moomora/klyx/internal/fleet"
)

type fakePromConn struct {
	snap fleet.PromotionSnapshot
	err  error
}

func (f *fakePromConn) GitOpsPromotion(context.Context) (fleet.PromotionSnapshot, error) {
	return f.snap, f.err
}

func TestGetPromotionMapsSnapshot(t *testing.T) {
	conn := &fakePromConn{snap: fleet.PromotionSnapshot{
		FluxPresent: true,
		Units: []fleet.PromotionUnit{{
			Kind: "Kustomization", Namespace: "flux-system", Name: "apps",
			SourceKind: "GitRepository", SourceName: "fleet-repo",
			Revision: "main@sha1:abc", Ready: "ready",
		}},
		Sources: []fleet.PromotionSource{{Kind: "GitRepository", Namespace: "flux-system", Name: "fleet-repo", Revision: "main@sha1:abc", Ready: "ready"}},
	}}
	s := NewPromotionService(func(string) (PromotionConn, bool) { return conn, true })
	dto := s.GetPromotion("dev")
	if dto.Error != "" || !dto.FluxPresent || len(dto.Units) != 1 || len(dto.Sources) != 1 {
		t.Fatalf("dto: %+v", dto)
	}
	if dto.Units[0].Revision != "main@sha1:abc" || dto.Units[0].SourceName != "fleet-repo" {
		t.Fatalf("unit: %+v", dto.Units[0])
	}
}

func TestGetPromotionHonestFailures(t *testing.T) {
	miss := NewPromotionService(func(string) (PromotionConn, bool) { return nil, false })
	if dto := miss.GetPromotion("nope"); dto.Error == "" || dto.Units == nil || dto.Sources == nil {
		t.Fatalf("cluster miss: %+v", dto)
	}
	failing := NewPromotionService(func(string) (PromotionConn, bool) {
		return &fakePromConn{err: errors.New("list kustomizations: forbidden")}, true
	})
	if dto := failing.GetPromotion("c"); dto.Error != "list kustomizations: forbidden" {
		t.Fatalf("error fold-in: %+v", dto)
	}
}
