package appbridge

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/observe"
)

// The Line slice 9b: the console's search DTO - hits pinned to cluster and
// UID, the role word as the Observer gave it, coverage that names every
// cluster, and the counts for "Searching N clusters · M kinds".
func TestIndexSearchDTO(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	res := fleetindex.Result{
		Total: 3,
		Hits: []fleetindex.Hit{
			{Cluster: "prd", Kind: "Pod", Key: "/pods", GVR: "v1/pods", Namespace: "shop", Name: "web", UID: "u1", KindState: fleetindex.StateLive,
				Role: observe.RoleView{State: observe.RoleCurrent, Role: "fault", Label: "CrashLoopBackOff", At: &at}},
			{Cluster: "stg", Kind: "Kustomization", Key: "kustomize.toolkit.fluxcd.io/kustomizations", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations",
				Namespace: "flux-system", Name: "web", UID: "u2", KindState: fleetindex.StateCapped, AsOf: &at,
				Role: observe.RoleView{State: observe.RoleLastObserved, Role: "ok", At: &at}},
			{Cluster: "prd", Kind: "ConfigMap", Key: "/configmaps", Namespace: "shop", Name: "web", UID: "u3", KindState: fleetindex.StateLive,
				Role: observe.RoleView{State: observe.RoleUnverified, Reason: observe.ReasonNoLedger}},
		},
		Coverage: []fleetindex.ClusterCoverage{
			{Cluster: "prd", Indexed: true, Connection: "connected", Kinds: []fleetindex.KindCoverage{
				{Key: "/pods", Kind: "Pod", KindStatus: fleetindex.KindStatus{State: fleetindex.StateLive, Entries: 10}},
				{Key: "/secrets", Kind: "Secret", KindStatus: fleetindex.KindStatus{State: fleetindex.StateDenied, Err: "403"}},
			}},
			{Cluster: "stg", Indexed: true, Connection: "stale", Kinds: []fleetindex.KindCoverage{
				{Key: "/pods", Kind: "Pod", KindStatus: fleetindex.KindStatus{State: fleetindex.StateStale, Entries: 4, AsOf: &at}},
				{Key: "kustomize.toolkit.fluxcd.io/kustomizations", Kind: "Kustomization", KindStatus: fleetindex.KindStatus{State: fleetindex.StateCapped, Entries: 2, AsOf: &at}},
			}},
			{Cluster: "dev", Connection: "unreachable"},
		},
	}
	d := searchDTO(res)
	h := d.Hits[1]
	if h.Group != "kustomize.toolkit.fluxcd.io" || h.Version != "v1" || h.Resource != "kustomizations" || h.AsOf != "2026-10-15T12:00:00Z" || h.Role.State != "lastObserved" || h.Role.At == "" {
		t.Fatalf("hit %+v", h)
	}
	if h := d.Hits[0]; h.Group != "" || h.Version != "v1" || h.Resource != "pods" || h.Role.Role != "fault" || h.AsOf != "" {
		t.Fatalf("core hit %+v", h)
	}
	if h := d.Hits[2]; h.Resource != "configmaps" || h.Role.Reason != observe.ReasonNoLedger {
		t.Fatalf("an unserved GVR falls back to the key: %+v", h)
	}
	if d.SearchedClusters != 2 || d.SearchedKinds != 2 || d.Total != 3 || len(d.Coverage) != 3 || d.Coverage[2].Indexed || d.Coverage[0].Kinds[1].State != fleetindex.StateDenied {
		t.Fatalf("coverage: %d clusters %d kinds %+v", d.SearchedClusters, d.SearchedKinds, d.Coverage)
	}
}

func TestIndexServiceCancelAndNil(t *testing.T) {
	if d, err := (&IndexService{}).Search(context.Background(), "web", 50); err != nil || d.Hits == nil || d.Coverage == nil {
		t.Fatal("without an index: empty, never nil")
	}
	ix := fleetindex.New(fleetindex.Settings{})
	defer ix.Stop()
	ix.SetClusters([]string{"prd"})
	s := NewIndexService(ix)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := s.Search(ctx, "w", 50)
	if err != nil || len(d.Hits) != 0 || len(d.Coverage) != 1 {
		t.Fatalf("a short query answers coverage only: %+v %v", d, err)
	}
}
