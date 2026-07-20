package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/hubble"
	"github.com/moomora/klyx/internal/routemetrics"
)

type GatewayConn interface {
	ListGateways(ctx context.Context) ([]gwapi.GatewayRef, bool, error)
	GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error)
	RouteMetrics(ctx context.Context, routeKeys []string) (map[string]routemetrics.RouteMetrics, routemetrics.Status)
	HubbleFlowStats(ctx context.Context) (hubble.FlowStats, hubble.Status)
}

const gatewayTimeout = 30 * time.Second

type GatewayService struct {
	lookup      func(string) (GatewayConn, bool)
	globalReach func(cluster, ns, name string) (peers []string, unconfirmed bool)
}

func NewGatewayService(lookup func(string) (GatewayConn, bool)) *GatewayService {
	return &GatewayService{lookup: lookup}
}

// SetGlobalReach wires the fleet cross-reference used to fill global services'
// meshClusters / meshUnconfirmed. Optional - without it, global services still
// render (just without the confirmed-peer list).
func (s *GatewayService) SetGlobalReach(f func(cluster, ns, name string) ([]string, bool)) {
	s.globalReach = f
}

func (s *GatewayService) ListGateways(cluster string) GatewayListDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return GatewayListDTO{Gateways: []GatewayRefDTO{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayTimeout)
	defer cancel()
	refs, served, err := conn.ListGateways(ctx)
	if err != nil {
		// Served but unlistable is an error state, not an empty cluster.
		return GatewayListDTO{GatewayAPIServed: served, Gateways: []GatewayRefDTO{}, Error: err.Error()}
	}
	out := GatewayListDTO{GatewayAPIServed: served, Gateways: make([]GatewayRefDTO, 0, len(refs))}
	for _, r := range refs {
		g := GatewayRefDTO{Namespace: r.Namespace, Name: r.Name, ClassName: r.ClassName, Accepted: r.Accepted, Programmed: r.Programmed, Stale: r.Stale, Generation: r.Generation, ObservedGeneration: r.ObservedGeneration}
		for _, a := range r.Addresses {
			g.Addresses = append(g.Addresses, GatewayAddressDTO{Type: a.Type, Value: a.Value})
		}
		for _, l := range r.Listeners {
			g.Listeners = append(g.Listeners, ListenerDTO{Name: l.Name, Protocol: l.Protocol, Hostname: l.Hostname, Port: l.Port, CertificateRefs: certificateRefDTOs(l.CertificateRefs)})
		}
		out.Gateways = append(out.Gateways, g)
	}
	return out
}

func (s *GatewayService) GetGatewayTopology(cluster, namespace, name string) TopologyDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return TopologyDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayTimeout)
	defer cancel()
	topo, err := conn.GetGatewayTopology(ctx, namespace, name)
	if err != nil {
		return TopologyDTO{Error: err.Error()}
	}
	dto := toTopologyDTO(topo)
	if s.globalReach != nil {
		for ri := range dto.Routes {
			for si := range dto.Routes[ri].Services {
				sn := &dto.Routes[ri].Services[si]
				if sn.Global {
					peers, unconfirmed := s.globalReach(cluster, sn.Namespace, sn.Name)
					if peers == nil {
						peers = []string{}
					}
					sn.MeshClusters = peers
					sn.MeshUnconfirmed = unconfirmed
				}
			}
		}
	}
	return dto
}

// GetRouteMetrics returns per-route traffic metrics + an Envoy-route status for
// the given route keys ("<ns>/<name>"). On-demand; the frontend polls it.
// GetHubbleFlowStats returns the cluster's Hubble flow/drop triage readout
// (from its Prometheus hubble_* metrics). Read-only, on-demand; capability- and
// metrics-gated in the fleet layer with honest unavailable reasons.
func (s *GatewayService) GetHubbleFlowStats(cluster string) HubbleStatsDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return HubbleStatsDTO{DropReasons: []ReasonRateDTO{}, Status: RouteMetricsStatusDTO{Available: false, Message: "cluster not connected"}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayTimeout)
	defer cancel()
	stats, st := conn.HubbleFlowStats(ctx)
	updatedAt := ""
	if !st.UpdatedAt.IsZero() {
		updatedAt = st.UpdatedAt.Format(time.RFC3339)
	}
	dto := HubbleStatsDTO{
		ForwardRate: stats.ForwardRate, DropRate: stats.DropRate,
		DNSErrorRate: stats.DNSErrorRate, DNSTotalRate: stats.DNSTotalRate,
		HTTPErrorRate: stats.HTTPErrorRate, HTTPTotalRate: stats.HTTPTotalRate,
		DropReasons: []ReasonRateDTO{},
		Status:      RouteMetricsStatusDTO{Available: st.Available, Message: st.Message, UpdatedAt: updatedAt},
	}
	for _, r := range stats.DropReasons {
		dto.DropReasons = append(dto.DropReasons, ReasonRateDTO{Reason: r.Reason, Protocol: r.Protocol, Rate: r.Rate})
	}
	return dto
}

func (s *GatewayService) GetRouteMetrics(cluster string, routeKeys []string) RouteMetricsResultDTO {
	empty := RouteMetricsResultDTO{Routes: map[string]RouteMetricDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		empty.Status = RouteMetricsStatusDTO{Available: false, Message: "cluster not connected"}
		return empty
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayTimeout)
	defer cancel()
	m, st := conn.RouteMetrics(ctx, routeKeys)
	routes := make(map[string]RouteMetricDTO, len(m))
	for k, rm := range m {
		routes[k] = RouteMetricDTO{RPS: rm.RPS, P50: rm.P50, P99: rm.P99, ErrRate: rm.ErrRate}
	}
	updatedAt := ""
	if !st.UpdatedAt.IsZero() {
		updatedAt = st.UpdatedAt.Format(time.RFC3339)
	}
	return RouteMetricsResultDTO{
		Status: RouteMetricsStatusDTO{Available: st.Available, Message: st.Message, UpdatedAt: updatedAt},
		Routes: routes,
	}
}
