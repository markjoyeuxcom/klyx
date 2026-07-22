package appbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/upgrade"
)

type fakeUpgradeConn struct {
	report fleet.UpgradeReport
	err    error
}

func (f *fakeUpgradeConn) UpgradeReadiness(context.Context) (fleet.UpgradeReport, error) {
	return f.report, f.err
}

func TestGetUpgradeReadinessMapsReport(t *testing.T) {
	conn := &fakeUpgradeConn{report: fleet.UpgradeReport{
		ServerVersion: "v1.31.4", ServerMinor: 31, EOLDate: "2025-10-28",
		Skew: []upgrade.SkewFinding{{Node: "old", Version: "v1.27.1", Delta: 4, Level: "danger", Detail: "out of window"}},
		Deprecated: []fleet.DeprecatedUse{{
			Group: "flowcontrol.apiserver.k8s.io", Version: "v1beta3", Resource: "flowschemas", Kind: "FlowSchema",
			RemovedInMinor: 32, ReplacedBy: "flowcontrol.apiserver.k8s.io/v1 FlowSchema", Count: 12, CountKnown: true,
		}},
	}}
	s := NewUpgradeService(func(string) (UpgradeConn, bool) { return conn, true })
	dto := s.GetUpgradeReadiness("c")
	if dto.Error != "" || dto.ServerMinor != 31 || dto.EOLDate != "2025-10-28" {
		t.Fatalf("dto: %+v", dto)
	}
	if len(dto.Skew) != 1 || dto.Skew[0].Level != "danger" {
		t.Fatalf("skew: %+v", dto.Skew)
	}
	if len(dto.Deprecated) != 1 || dto.Deprecated[0].RemovedInMinor != 32 || !dto.Deprecated[0].CountKnown {
		t.Fatalf("deprecated: %+v", dto.Deprecated)
	}
}

func TestGetUpgradeReadinessHonestFailures(t *testing.T) {
	miss := NewUpgradeService(func(string) (UpgradeConn, bool) { return nil, false })
	if dto := miss.GetUpgradeReadiness("nope"); dto.Error == "" || dto.Skew == nil || dto.Deprecated == nil {
		t.Fatalf("cluster miss: %+v", dto)
	}
	failing := NewUpgradeService(func(string) (UpgradeConn, bool) {
		return &fakeUpgradeConn{err: errors.New("server version: boom")}, true
	})
	if dto := failing.GetUpgradeReadiness("c"); dto.Error != "server version: boom" {
		t.Fatalf("error fold-in: %+v", dto)
	}
}
