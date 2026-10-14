package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/karpenter"
)

// KarpenterPoolDTO mirrors karpenter.NodePool.
type KarpenterPoolDTO struct {
	UID        string   `json:"uid,omitempty"` // identity (1c slice 3 §2a)
	GVR        string   `json:"gvr,omitempty"`
	Generation int64    `json:"generation,omitempty"`
	Name       string   `json:"name"`
	Ready      string   `json:"ready"`
	Reason     string   `json:"reason"`
	Message    string   `json:"message"`
	Weight     int64    `json:"weight"`
	CPULimit   *float64 `json:"cpuLimit"`
	CPUUsed    *float64 `json:"cpuUsed"`
	MemLimit   *float64 `json:"memLimit"`
	MemUsed    *float64 `json:"memUsed"`
	Nodes      int      `json:"nodes"`
	// ReadySince is the Ready condition's transition time (unix), 0 when absent.
	ReadySince int64 `json:"readySinceUnix,omitempty"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule RuleDTO `json:"rule"`
}

// KarpenterClaimDTO mirrors karpenter.NodeClaim. AgeSeconds is relative to
// the capture; the lens ticks the age from createdUnix (1c slice 3 §3).
type KarpenterClaimDTO struct {
	UID            string `json:"uid,omitempty"` // identity (1c slice 3 §2a)
	GVR            string `json:"gvr,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	Name           string `json:"name"`
	Pool           string `json:"pool"`
	NodeName       string `json:"nodeName"`
	Ready          string `json:"ready"`
	FailingType    string `json:"failingType"`
	FailingStatus  string `json:"failingStatus"`
	FailingReason  string `json:"failingReason"`
	FailingMessage string `json:"failingMessage"`
	Drifted        bool   `json:"drifted"`
	Consolidatable bool   `json:"consolidatable"`
	CapacityType   string `json:"capacityType"`
	InstanceType   string `json:"instanceType"`
	AgeSeconds     int    `json:"ageSeconds"`
	// Rule is the relocated lens rule (1c addendum §2).
	Rule    RuleDTO `json:"rule"`
	Created int64   `json:"createdUnix"` // creation time (unix): the launch-deadline anchor
	// ReadySince is the Ready condition's transition time (unix), 0 when absent.
	ReadySince int64 `json:"readySinceUnix,omitempty"`
}

// KarpenterEventDTO is one row of the disruption feed.
type KarpenterEventDTO struct {
	Type         string `json:"type"`
	Reason       string `json:"reason"`
	Message      string `json:"message"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	LastSeenUnix int64  `json:"lastSeenUnix"`
}

// KarpenterSnapshotDTO is GetKarpenterState's payload: the controller probe
// and the disruption feed as of AsOfUnix. The NodePools and NodeClaims are
// the health Observer's shared rows (1c slice 3 §5.5).
type KarpenterSnapshotDTO struct {
	Detected           bool                `json:"detected"`
	AsOfUnix           int64               `json:"asOfUnix,omitempty"`
	ControllerVerified bool                `json:"controllerVerified"`
	ControllerHealthy  bool                `json:"controllerHealthy"`
	ControllerError    string              `json:"controllerError,omitempty"`
	EventsError        string              `json:"eventsError,omitempty"`
	EventsTruncated    bool                `json:"eventsTruncated,omitempty"`
	Events             []KarpenterEventDTO `json:"events"`
	Error              string              `json:"error,omitempty"`
}

// KarpenterConn is the per-cluster surface the Karpenter lens needs.
type KarpenterConn interface {
	KarpenterState(ctx context.Context) (fleet.KarpenterSnapshot, error)
}

// KarpenterService is bound to JS: the Karpenter lens's backend. Read-only.
type KarpenterService struct {
	lookup func(string) (KarpenterConn, bool)
}

func NewKarpenterService(lookup func(string) (KarpenterConn, bool)) *KarpenterService {
	return &KarpenterService{lookup: lookup}
}

// GetKarpenterState returns one cluster's Karpenter controller probe and
// disruption feed. Errors fold in; slices are never nil.
func (s *KarpenterService) GetKarpenterState(cluster string) KarpenterSnapshotDTO {
	out := KarpenterSnapshotDTO{Events: []KarpenterEventDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	snap, err := conn.KarpenterState(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Detected = snap.Detected
	if !snap.AsOf.IsZero() {
		out.AsOfUnix = snap.AsOf.Unix()
	}
	out.ControllerVerified = snap.ControllerVerified
	out.ControllerHealthy = snap.ControllerHealthy
	out.ControllerError = snap.ControllerError
	out.EventsError = snap.EventsError
	out.EventsTruncated = snap.EventsTruncated
	for _, e := range snap.Events {
		out.Events = append(out.Events, KarpenterEventDTO{
			Type: e.Type, Reason: e.Reason, Message: e.Message,
			Kind: e.Kind, Name: e.Name, LastSeenUnix: e.LastSeenUnix,
		})
	}
	return out
}

func toKarpenterPoolDTO(p karpenter.NodePool) KarpenterPoolDTO {
	dto := karpenterPoolFields(p)
	dto.Rule = toRuleDTO(karpenter.PoolRule(p))
	return dto
}

// karpenterPoolFields is the pure field conversion: no rule (the rows
// converter copies the capture's evaluation, slice 3 §2a).
func karpenterPoolFields(p karpenter.NodePool) KarpenterPoolDTO {
	return KarpenterPoolDTO{
		Name: p.Name, Ready: p.Ready, Reason: p.Reason, Message: p.Message, Weight: p.Weight,
		CPULimit: p.CPULimit, CPUUsed: p.CPUUsed, MemLimit: p.MemLimit, MemUsed: p.MemUsed,
		Nodes: p.Nodes, ReadySince: unixOrZero(p.ReadySince),
	}
}

func toKarpenterClaimDTO(cl karpenter.NodeClaim) KarpenterClaimDTO {
	dto := karpenterClaimFields(cl)
	dto.Rule = toRuleDTO(karpenter.ClaimRule(cl))
	return dto
}

// karpenterClaimFields is the pure field conversion (no rule).
func karpenterClaimFields(cl karpenter.NodeClaim) KarpenterClaimDTO {
	return KarpenterClaimDTO{
		Name: cl.Name, Pool: cl.Pool, NodeName: cl.NodeName, Ready: cl.Ready,
		FailingType: cl.FailingType, FailingStatus: cl.FailingStatus, FailingReason: cl.FailingReason, FailingMessage: cl.FailingMessage,
		Drifted: cl.Drifted, Consolidatable: cl.Consolidatable,
		CapacityType: cl.CapacityType, InstanceType: cl.InstanceType, AgeSeconds: cl.AgeSeconds,
		Created: cl.Created.Unix(), ReadySince: unixOrZero(cl.ReadySince),
	}
}
