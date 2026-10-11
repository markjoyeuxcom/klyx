package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/workloads"
)

type fakeKarpConn struct {
	snap fleet.KarpenterSnapshot
	err  error
}

func (f *fakeKarpConn) KarpenterState(context.Context) (fleet.KarpenterSnapshot, error) {
	return f.snap, f.err
}

func TestGetKarpenterStateMapsSnapshot(t *testing.T) {
	conn := &fakeKarpConn{snap: fleet.KarpenterSnapshot{
		Detected: true, ControllerHealthy: true, AsOf: time.Unix(1000, 0),
		Events: []workloads.EventSummary{{Type: "Normal", Reason: "DisruptionTerminating", Kind: "NodeClaim", Name: "wk-old", LastSeenUnix: 900}},
	}}
	s := NewKarpenterService(func(string) (KarpenterConn, bool) { return conn, true })
	dto := s.GetKarpenterState("c")
	if dto.Error != "" || !dto.Detected || !dto.ControllerHealthy || dto.AsOfUnix != 1000 {
		t.Fatalf("dto: %+v", dto)
	}
	if len(dto.Events) != 1 || dto.Events[0].Reason != "DisruptionTerminating" {
		t.Fatalf("events: %+v", dto.Events)
	}
}

// The rows' pure field conversions keep the snapshot's old mapping (1c
// slice 3 §5.5).
func TestKarpenterFields(t *testing.T) {
	cpu := 100.0
	p := karpenterPoolFields(karpenter.NodePool{Name: "workers", Ready: "ready", CPULimit: &cpu, Nodes: 3})
	if p.CPULimit == nil || *p.CPULimit != 100 || p.Nodes != 3 {
		t.Fatalf("pool: %+v", p)
	}
	c := karpenterClaimFields(karpenter.NodeClaim{Name: "wk-1", Pool: "workers", Ready: "not-ready", FailingType: "Registered", FailingReason: "NodeNotFound", CapacityType: "spot", Created: time.Unix(500, 0)})
	if c.FailingType != "Registered" || c.CapacityType != "spot" || c.Created != 500 {
		t.Fatalf("claim: %+v", c)
	}
}

func TestGetKarpenterStateHonestFailures(t *testing.T) {
	miss := NewKarpenterService(func(string) (KarpenterConn, bool) { return nil, false })
	if dto := miss.GetKarpenterState("nope"); dto.Error == "" || dto.Events == nil {
		t.Fatalf("cluster miss: %+v", dto)
	}
	failing := NewKarpenterService(func(string) (KarpenterConn, bool) {
		return &fakeKarpConn{err: errors.New("list nodepools: forbidden")}, true
	})
	if dto := failing.GetKarpenterState("c"); dto.Error != "list nodepools: forbidden" {
		t.Fatalf("error fold-in: %+v", dto)
	}
}
