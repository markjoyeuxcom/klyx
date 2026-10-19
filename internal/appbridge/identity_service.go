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

// maxIdentityDTO bounds every reply (S5 §6), measured over the final
// serialised DTO with its omission counts: past it pods (or a pod's
// containers) drop from the end, counted; a reply that still does not fit
// says so in place of its report.
const maxIdentityDTO = 256 << 10

// overBound is the error a reply carries when even its trimmed report
// does not fit.
const overBound = "the identity reply exceeds its 256 KB bound even trimmed; its report is not shown"

func fits(v any) bool {
	raw, err := json.Marshal(v)
	return err == nil && len(raw) <= maxIdentityDTO
}

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
	// ContainersDropped counts the containers whose evidence was dropped
	// to fit the reply bound (their readings stay).
	ContainersDropped int `json:"containersDropped,omitempty"`
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
	// PodsDropped counts the handler pods dropped to fit the reply bound.
	PodsDropped int `json:"podsDropped,omitempty"`
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
	fitSAReport(&out)
	return out
}

// fitSAReport drops pods from the end until the reply fits its bound.
func fitSAReport(d *SAIdentityDTO) {
	for !fits(d) && len(d.Report.Pods) > 0 {
		d.Report.Pods = d.Report.Pods[:len(d.Report.Pods)-1]
		d.PodsDropped++
	}
	if !fits(d) {
		d.Report, d.Error = wid.SAReport{State: wid.StateUnreadable, Pods: []wid.PodReport{}, Stores: []wid.StoreLink{}}, overBound
	}
}

// fitPodReport drops the pod's container evidence from the end (its
// readings stay) until the reply fits its bound.
func fitPodReport(d *PodIdentityDTO) {
	for !fits(d) && d.Report.Report != nil && len(d.Report.Report.Pod.Containers) > 0 {
		cs := d.Report.Report.Pod.Containers
		d.Report.Report.Pod.Containers = cs[:len(cs)-1]
		d.ContainersDropped++
	}
	if !fits(d) {
		d.Report, d.Error = wid.PodInspectorReport{State: wid.StateUnreadable}, overBound
	}
}

// fitStoreReport drops handler pods from the end of the last group that
// still has any; a group whose pods all dropped keeps its ServiceAccount.
func fitStoreReport(d *StoreIdentityDTO) {
	for !fits(d) && d.Report.Handlers != nil {
		gs := d.Report.Handlers.Groups
		i := len(gs) - 1
		for i >= 0 && len(gs[i].Pods) == 0 {
			i--
		}
		if i < 0 {
			break
		}
		gs[i].Pods = gs[i].Pods[:len(gs[i].Pods)-1]
		d.PodsDropped++
	}
	if !fits(d) {
		d.Report, d.Error = wid.StoreReport{State: wid.StateUnreadable}, overBound
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
	fitPodReport(&out)
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
	fitStoreReport(&out)
	return out
}
