package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/fleet"
)

// NetpolRefDTO mirrors netpol.PolicyRef.
type NetpolRefDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Admits    bool   `json:"admits"`
}

// DirectionVerdictDTO mirrors netpol.DirectionVerdict.
type DirectionVerdictDTO struct {
	Isolated bool           `json:"isolated"`
	Allowed  bool           `json:"allowed"`
	Policies []NetpolRefDTO `json:"policies"`
	Explain  string         `json:"explain"`
}

// ConnectivityReportDTO is the connectivity can-i answer.
type ConnectivityReportDTO struct {
	Allowed        bool                `json:"allowed"`
	Ingress        DirectionVerdictDTO `json:"ingress"`
	Egress         DirectionVerdictDTO `json:"egress"`
	SrcLabels      map[string]string   `json:"srcLabels"`
	DstLabels      map[string]string   `json:"dstLabels"`
	CiliumPolicies int                 `json:"ciliumPolicies"`
	// CiliumUnknown: a Cilium policy count FAILED (not absent) - the verdict is
	// PARTIAL because whether Cilium policies apply is unknown.
	CiliumUnknown string `json:"ciliumUnknown,omitempty"`
	Error         string `json:"error,omitempty"`
}

// ConnectivityConn is the per-cluster surface the connectivity lens needs.
type ConnectivityConn interface {
	ConnectivityCanI(ctx context.Context, srcNS, srcPod, dstNS, dstPod string, port int, protocol string) (fleet.ConnectivityReport, error)
}

// ConnectivityService is bound to JS: the NetworkPolicy simulator's backend.
// Read-only throughout.
type ConnectivityService struct {
	lookup func(string) (ConnectivityConn, bool)
}

func NewConnectivityService(lookup func(string) (ConnectivityConn, bool)) *ConnectivityService {
	return &ConnectivityService{lookup: lookup}
}

// CanReach runs the simulation. Validation happens here (the input boundary);
// errors fold into the DTO.
func (s *ConnectivityService) CanReach(cluster, srcNS, srcPod, dstNS, dstPod string, port int, protocol string) ConnectivityReportDTO {
	empty := func(msg string) ConnectivityReportDTO {
		return ConnectivityReportDTO{
			Ingress: DirectionVerdictDTO{Policies: []NetpolRefDTO{}},
			Egress:  DirectionVerdictDTO{Policies: []NetpolRefDTO{}},
			Error:   msg,
		}
	}
	if srcNS == "" || srcPod == "" || dstNS == "" || dstPod == "" {
		return empty("source and destination namespace/pod are required")
	}
	if port < 0 || port > 65535 {
		return empty("port out of range")
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return empty("cluster not connected: " + cluster)
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	r, err := conn.ConnectivityCanI(ctx, srcNS, srcPod, dstNS, dstPod, port, protocol)
	if err != nil {
		return empty(err.Error())
	}
	dto := ConnectivityReportDTO{
		Allowed:        r.Verdict.Allowed,
		SrcLabels:      r.SrcLabels,
		DstLabels:      r.DstLabels,
		CiliumPolicies: r.CiliumPolicies,
		CiliumUnknown:  r.CiliumUnknown,
	}
	dto.Ingress = DirectionVerdictDTO{Isolated: r.Verdict.Ingress.Isolated, Allowed: r.Verdict.Ingress.Allowed, Explain: r.IngressExplain, Policies: make([]NetpolRefDTO, 0, len(r.Verdict.Ingress.Policies))}
	for _, p := range r.Verdict.Ingress.Policies {
		dto.Ingress.Policies = append(dto.Ingress.Policies, NetpolRefDTO{Namespace: p.Namespace, Name: p.Name, Admits: p.Admits})
	}
	dto.Egress = DirectionVerdictDTO{Isolated: r.Verdict.Egress.Isolated, Allowed: r.Verdict.Egress.Allowed, Explain: r.EgressExplain, Policies: make([]NetpolRefDTO, 0, len(r.Verdict.Egress.Policies))}
	for _, p := range r.Verdict.Egress.Policies {
		dto.Egress.Policies = append(dto.Egress.Policies, NetpolRefDTO{Namespace: p.Namespace, Name: p.Name, Admits: p.Admits})
	}
	return dto
}
