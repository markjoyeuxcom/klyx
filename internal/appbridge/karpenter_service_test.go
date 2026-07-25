package appbridge

import (
	"context"
	"errors"
	"testing"

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
	cpu := 100.0
	conn := &fakeKarpConn{snap: fleet.KarpenterSnapshot{
		Detected: true, ControllerHealthy: true,
		Pools:  []karpenter.NodePool{{Name: "workers", Ready: "ready", CPULimit: &cpu, Nodes: 3}},
		Claims: []karpenter.NodeClaim{{Name: "wk-1", Pool: "workers", Ready: "not-ready", FailingType: "Registered", FailingReason: "NodeNotFound", CapacityType: "spot"}},
		Events: []workloads.EventSummary{{Type: "Normal", Reason: "DisruptionTerminating", Kind: "NodeClaim", Name: "wk-old", LastSeenUnix: 900}},
	}}
	s := NewKarpenterService(func(string) (KarpenterConn, bool) { return conn, true })
	dto := s.GetKarpenterState("c")
	if dto.Error != "" || !dto.Detected || !dto.ControllerHealthy {
		t.Fatalf("dto: %+v", dto)
	}
	if len(dto.Pools) != 1 || dto.Pools[0].CPULimit == nil || *dto.Pools[0].CPULimit != 100 || dto.Pools[0].Nodes != 3 {
		t.Fatalf("pools: %+v", dto.Pools)
	}
	if len(dto.Claims) != 1 || dto.Claims[0].FailingType != "Registered" || dto.Claims[0].CapacityType != "spot" {
		t.Fatalf("claims: %+v", dto.Claims)
	}
	if len(dto.Events) != 1 || dto.Events[0].Reason != "DisruptionTerminating" {
		t.Fatalf("events: %+v", dto.Events)
	}
}

func TestGetKarpenterStateHonestFailures(t *testing.T) {
	miss := NewKarpenterService(func(string) (KarpenterConn, bool) { return nil, false })
	if dto := miss.GetKarpenterState("nope"); dto.Error == "" || dto.Pools == nil || dto.Claims == nil || dto.Events == nil {
		t.Fatalf("cluster miss: %+v", dto)
	}
	failing := NewKarpenterService(func(string) (KarpenterConn, bool) {
		return &fakeKarpConn{err: errors.New("list nodepools: forbidden")}, true
	})
	if dto := failing.GetKarpenterState("c"); dto.Error != "list nodepools: forbidden" {
		t.Fatalf("error fold-in: %+v", dto)
	}
}
