package appbridge

import (
	"context"
	"encoding/json"
	"time"

	wid "github.com/moomora/klyx/internal/identity"
)

// Coverage S5: workload identity, read on demand when an inspector opens
// (§5, §6). The reports carry only shown values (identity.Value keeps the
// exact bytes unexported); nothing is sent to the AI and nothing is
// retained after the reply.

// IdentityConn is what IdentityService needs of one cluster.
type IdentityConn interface {
	ServiceAccountIdentity(ctx context.Context, ns, name, uid string) (wid.SAReport, error)
	PodIdentity(ctx context.Context, ns, name, uid string) (wid.PodInspectorReport, error)
	StoreIdentity(ctx context.Context, kind, ns, name, uid string) (wid.StoreReport, error)
}

// IdentityService is bound to JS.
type IdentityService struct {
	lookup func(string) (IdentityConn, bool)
}

func NewIdentityService(lookup func(string) (IdentityConn, bool)) *IdentityService {
	return &IdentityService{lookup: lookup}
}

// maxIdentityDTO bounds a reply (S5 §6); past it pods drop from the end,
// counted.
const maxIdentityDTO = 256 << 10

const identityCallTimeout = 25 * time.Second

// SAIdentityDTO is the ServiceAccount inspector's Identity block. Cluster,
// Namespace, Name and UID fence it.
type SAIdentityDTO struct {
	Cluster   string       `json:"cluster"`
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	UID       string       `json:"uid"`
	Error     string       `json:"error,omitempty"`
	Report    wid.SAReport `json:"report"`
	// PodsDropped counts the pods dropped to fit the reply bound.
	PodsDropped int `json:"podsDropped,omitempty"`
}

// PodIdentityDTO is the Pod inspector's Identity block.
type PodIdentityDTO struct {
	Cluster   string                 `json:"cluster"`
	Namespace string                 `json:"namespace"`
	Name      string                 `json:"name"`
	UID       string                 `json:"uid"`
	Error     string                 `json:"error,omitempty"`
	Report    wid.PodInspectorReport `json:"report"`
}

// StoreIdentityDTO is the ESO store inspector's "Authenticates as" block.
type StoreIdentityDTO struct {
	Cluster   string          `json:"cluster"`
	Kind      string          `json:"kind"`
	Namespace string          `json:"namespace,omitempty"`
	Name      string          `json:"name"`
	UID       string          `json:"uid"`
	Error     string          `json:"error,omitempty"`
	Report    wid.StoreReport `json:"report"`
}

func (s *IdentityService) conn(cluster string) (IdentityConn, string) {
	c, ok := s.lookup(cluster)
	if !ok {
		return nil, "cluster not connected"
	}
	return c, ""
}

// ServiceAccount reads a ServiceAccount's identity.
func (s *IdentityService) ServiceAccount(cluster, namespace, name, uid string) SAIdentityDTO {
	out := SAIdentityDTO{Cluster: cluster, Namespace: namespace, Name: name, UID: uid}
	c, msg := s.conn(cluster)
	if c == nil {
		out.Error = msg
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityCallTimeout)
	defer cancel()
	r, err := c.ServiceAccountIdentity(ctx, namespace, name, uid)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Report = r
	out.PodsDropped = fitSAReport(&out)
	return out
}

// fitSAReport drops pods from the end until the reply fits its bound.
func fitSAReport(d *SAIdentityDTO) int {
	dropped := 0
	for {
		raw, err := json.Marshal(d)
		if err != nil || len(raw) <= maxIdentityDTO || len(d.Report.Pods) == 0 {
			return dropped
		}
		d.Report.Pods = d.Report.Pods[:len(d.Report.Pods)-1]
		dropped++
	}
}

// Pod reads a pod's identity.
func (s *IdentityService) Pod(cluster, namespace, name, uid string) PodIdentityDTO {
	out := PodIdentityDTO{Cluster: cluster, Namespace: namespace, Name: name, UID: uid}
	c, msg := s.conn(cluster)
	if c == nil {
		out.Error = msg
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityCallTimeout)
	defer cancel()
	r, err := c.PodIdentity(ctx, namespace, name, uid)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Report = r
	return out
}

// Store reads who an ESO store authenticates as.
func (s *IdentityService) Store(cluster, kind, namespace, name, uid string) StoreIdentityDTO {
	out := StoreIdentityDTO{Cluster: cluster, Kind: kind, Namespace: namespace, Name: name, UID: uid}
	c, msg := s.conn(cluster)
	if c == nil {
		out.Error = msg
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityCallTimeout)
	defer cancel()
	r, err := c.StoreIdentity(ctx, kind, namespace, name, uid)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Report = r
	return out
}
