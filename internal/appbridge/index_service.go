package appbridge

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/observe"
)

// IndexService is the console's object search over the fleet metadata
// index (The Line slice 9 note §3). A search is bounded by its caller's
// context: the console cancels a superseded one.
type IndexService struct {
	ix *fleetindex.Index
}

func NewIndexService(ix *fleetindex.Index) *IndexService { return &IndexService{ix: ix} }

// IndexSearchDTO is one search's hits and the coverage it ran under.
type IndexSearchDTO struct {
	Hits  []IndexHitDTO `json:"hits"`
	Total int           `json:"total"` // matches before the bound
	// SearchedClusters and SearchedKinds: the indexed clusters, and the
	// configured kinds readable on at least one of them.
	SearchedClusters int               `json:"searchedClusters"`
	SearchedKinds    int               `json:"searchedKinds"`
	Coverage         []IndexClusterDTO `json:"coverage"`
}

// IndexHitDTO is one matching object, pinned to its cluster and UID.
type IndexHitDTO struct {
	Cluster   string `json:"cluster"`
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	// KindState is the kind's state on its cluster; AsOf its time when the
	// kind is not live (stale, capped, frozen).
	KindState string       `json:"kindState"`
	AsOf      string       `json:"asOf,omitempty"`
	Role      IndexRoleDTO `json:"role"`
}

// IndexRoleDTO is the Observer's word on the object: current, last
// observed (with its time) or unverified (with why).
type IndexRoleDTO struct {
	State  string `json:"state"`
	Role   string `json:"role,omitempty"`
	Label  string `json:"label,omitempty"`
	At     string `json:"at,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// IndexClusterDTO is one cluster's coverage.
type IndexClusterDTO struct {
	Cluster    string         `json:"cluster"`
	Indexed    bool           `json:"indexed"`
	Connection string         `json:"connection"`
	Kinds      []IndexKindDTO `json:"kinds"`
}

// IndexKindDTO is one kind's state on one cluster.
type IndexKindDTO struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Entries int    `json:"entries"`
	AsOf    string `json:"asOf,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Search searches the fleet index; a query under two characters returns
// only the coverage.
func (s *IndexService) Search(ctx context.Context, query string, limit int) (IndexSearchDTO, error) {
	if s.ix == nil {
		return IndexSearchDTO{Hits: []IndexHitDTO{}, Coverage: []IndexClusterDTO{}}, nil
	}
	res, err := s.ix.Search(ctx, query, limit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return IndexSearchDTO{}, errors.New("search cancelled")
		}
		return IndexSearchDTO{}, err
	}
	return searchDTO(res), nil
}

// readable: a kind state that holds searchable entries.
var readable = map[string]bool{fleetindex.StateLive: true, fleetindex.StateStale: true, fleetindex.StateCapped: true,
	fleetindex.StateOversize: true, fleetindex.StateWatchDenied: true}

func searchDTO(res fleetindex.Result) IndexSearchDTO {
	out := IndexSearchDTO{Hits: make([]IndexHitDTO, 0, len(res.Hits)), Total: res.Total, Coverage: make([]IndexClusterDTO, 0, len(res.Coverage))}
	for _, h := range res.Hits {
		group, version, resource := hitAPI(h.GVR, h.Key)
		out.Hits = append(out.Hits, IndexHitDTO{Cluster: h.Cluster, Kind: h.Kind, Group: group, Version: version, Resource: resource,
			Namespace: h.Namespace, Name: h.Name, UID: h.UID, KindState: h.KindState, AsOf: indexTime(h.AsOf), Role: roleDTO(h.Role)})
	}
	kinds := map[string]bool{}
	for _, cc := range res.Coverage {
		dto := IndexClusterDTO{Cluster: cc.Cluster, Indexed: cc.Indexed, Connection: cc.Connection, Kinds: make([]IndexKindDTO, 0, len(cc.Kinds))}
		if cc.Indexed {
			out.SearchedClusters++
		}
		for _, k := range cc.Kinds {
			dto.Kinds = append(dto.Kinds, IndexKindDTO{Key: k.Key, Kind: k.Kind, State: k.State, Entries: k.Entries, AsOf: indexTime(k.AsOf), Err: k.Err})
			if cc.Indexed && readable[k.State] {
				kinds[k.Key] = true
			}
		}
		out.Coverage = append(out.Coverage, dto)
	}
	out.SearchedKinds = len(kinds)
	return out
}

func roleDTO(v observe.RoleView) IndexRoleDTO {
	return IndexRoleDTO{State: v.State, Role: v.Role, Label: v.Label, At: indexTime(v.At), Reason: v.Reason}
}

func indexTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rfc3339(t.UTC())
}

// hitAPI reads a served group/version/resource ("v1/pods" for the core
// group); without one, the key's group and resource.
func hitAPI(gvr, key string) (group, version, resource string) {
	if g, v, r, err := splitGVR(gvr); err == nil {
		return g, v, r
	}
	group, resource, _ = strings.Cut(key, "/")
	return group, "", resource
}
