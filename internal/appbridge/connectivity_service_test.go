package appbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/netpol"
)

type fakeConnectivityConn struct {
	report fleet.ConnectivityReport
	err    error
}

func (f *fakeConnectivityConn) ConnectivityCanI(context.Context, string, string, string, string, int, string) (fleet.ConnectivityReport, error) {
	return f.report, f.err
}

func TestCanReachMapsVerdict(t *testing.T) {
	conn := &fakeConnectivityConn{report: fleet.ConnectivityReport{
		Verdict: netpol.Verdict{
			Allowed: false,
			Ingress: netpol.DirectionVerdict{Isolated: true, Allowed: false, Policies: []netpol.PolicyRef{{Namespace: "team-b", Name: "deny-all"}}},
			Egress:  netpol.DirectionVerdict{Isolated: false, Allowed: true},
		},
		SrcLabels: map[string]string{"app": "web"}, DstLabels: map[string]string{"app": "db"},
		IngressExplain: "ingress DENIED - 1 policy selects the pod, none admits this peer/port",
		EgressExplain:  "egress allowed - no policy selects the pod (not isolated)",
		CiliumPolicies: 3,
	}}
	s := NewConnectivityService(func(string) (ConnectivityConn, bool) { return conn, true })
	dto := s.CanReach("c", "team-a", "web-1", "team-b", "db-1", 5432, "TCP")
	if dto.Error != "" || dto.Allowed || !dto.Egress.Allowed || dto.Ingress.Allowed {
		t.Fatalf("dto: %+v", dto)
	}
	if len(dto.Ingress.Policies) != 1 || dto.Ingress.Policies[0].Name != "deny-all" {
		t.Fatalf("policies: %+v", dto.Ingress.Policies)
	}
	if dto.CiliumPolicies != 3 {
		t.Fatalf("the cilium honesty count must carry: %+v", dto)
	}
}

func TestCanReachValidatesAndFoldsErrors(t *testing.T) {
	s := NewConnectivityService(func(string) (ConnectivityConn, bool) { return &fakeConnectivityConn{}, true })
	if dto := s.CanReach("c", "", "web", "b", "db", 80, "TCP"); dto.Error == "" {
		t.Fatalf("missing ns must refuse: %+v", dto)
	}
	if dto := s.CanReach("c", "a", "web", "b", "db", 99999, "TCP"); dto.Error == "" {
		t.Fatalf("bad port must refuse: %+v", dto)
	}
	failing := NewConnectivityService(func(string) (ConnectivityConn, bool) {
		return &fakeConnectivityConn{err: errors.New("source pod team-a/ghost: not found")}, true
	})
	if dto := failing.CanReach("c", "team-a", "ghost", "b", "db", 80, "TCP"); dto.Error == "" || dto.Ingress.Policies == nil {
		t.Fatalf("error fold-in with non-nil slices: %+v", dto)
	}
}
