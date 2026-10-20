package appbridge

import (
	"context"
	"errors"
	"reflect"
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
		Detected: true, AsOf: time.Unix(1000, 0),
		Events: []workloads.EventSummary{{Type: "Normal", Reason: "DisruptionTerminating", Kind: "NodeClaim", Name: "wk-old", LastSeenUnix: 900}},
	}}
	s := NewKarpenterService(func(string) (KarpenterConn, bool) { return conn, true })
	dto := s.GetKarpenterState("c")
	if dto.Error != "" || !dto.Detected || dto.AsOfUnix != 1000 {
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
	p := karpenterPoolFields(karpenter.NodePool{Meta: karpenter.Meta{Name: "workers"}, Ready: "ready", CPULimit: &cpu, Nodes: 3})
	if p.CPULimit == nil || *p.CPULimit != 100 || p.Nodes != 3 {
		t.Fatalf("pool: %+v", p)
	}
	c := karpenterClaimFields(karpenter.NodeClaim{Meta: karpenter.Meta{Name: "wk-1"}, Pool: "workers", Ready: "not-ready", FailingType: "Registered", FailingReason: "NodeNotFound", CapacityType: "spot", Created: time.Unix(500, 0)})
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

// The Karpenter DTOs stopped being 1:1 mirrors in coverage S10: identity is
// in Meta, and the rule reads authoritative conditions the lens row does not
// repeat. Every pure field is accounted for here, so a new field still fails
// until it is sent or its omission is stated (the dto_parity tripwire's job).
func TestKarpenterDTOFieldLedger(t *testing.T) {
	const mirrored = "mirrored"
	ledger := []struct {
		pure, dto reflect.Type
		fields    map[string]string
	}{
		{reflect.TypeOf(karpenter.NodePool{}), reflect.TypeOf(KarpenterPoolDTO{}), map[string]string{
			"Meta": "name; identity rides the row's Ref", "Ref": "nodeClassRef",
			"Ready": mirrored, "Reason": mirrored, "Message": mirrored, "Weight": mirrored, "CPULimit": mirrored, "CPUUsed": mirrored,
			"MemLimit": mirrored, "MemUsed": mirrored, "Nodes": mirrored, "ReadySince": mirrored,
			"CondReady": "the rule's evidence, said through findings and conditions", "CondValidation": "the rule's evidence, said through findings and conditions",
			"CondNodeClassReady": "the rule's evidence, said through findings and conditions", "CondRegistration": "the rule's evidence, said through findings and conditions",
			"NodeClassObservedGeneration": mirrored, "Conds": "conditions, conditionsTotal and conditionsCut",
		}},
		{reflect.TypeOf(karpenter.NodeClaim{}), reflect.TypeOf(KarpenterClaimDTO{}), map[string]string{
			"Meta": "name; identity rides the row's Ref", "Ref": "nodeClassRef",
			"Pool": mirrored, "NodeName": mirrored, "Ready": mirrored, "FailingType": mirrored, "FailingStatus": mirrored,
			"FailingReason": mirrored, "FailingReasonWithheld": mirrored, "FailingMessage": mirrored,
			"Drifted": mirrored, "Consolidatable": mirrored, "CapacityType": mirrored, "InstanceType": mirrored, "AgeSeconds": mirrored,
			"Created": mirrored, "ReadySince": mirrored,
		}},
	}
	for _, l := range ledger {
		for i := 0; i < l.pure.NumField(); i++ {
			f := l.pure.Field(i)
			if !f.IsExported() {
				continue
			}
			why, ok := l.fields[f.Name]
			if !ok {
				t.Errorf("%s.%s is not accounted for: send it on %s or state why not", l.pure.Name(), f.Name, l.dto.Name())
				continue
			}
			if _, has := l.dto.FieldByName(f.Name); why == mirrored && !has {
				t.Errorf("%s.%s is mirrored but %s has no such field", l.pure.Name(), f.Name, l.dto.Name())
			}
		}
	}
}
