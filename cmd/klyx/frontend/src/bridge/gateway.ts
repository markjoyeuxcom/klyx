import { useFleet, GatewayListDTO, TopologyDTO, GatewayRef, RouteMetricsResultDTO } from "../store/fleet";
import { GatewayService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

export async function listGateways(cluster: string): Promise<void> {
  useFleet.getState().setGatewaysLoading(cluster);
  try {
    const l = (await GatewayService.ListGateways(cluster)) as GatewayListDTO;
    useFleet.getState().setGateways(cluster, l ?? { gatewayAPIServed: false, gateways: [] });
  } catch (e) {
    // Clear the loading flag so the view doesn't get stuck on "Loading…".
    if (useFleet.getState().network.cluster === cluster) {
      useFleet.setState((s) => ({ network: { ...s.network, listLoading: false } }));
    }
    throw e;
  }
}

export async function getGatewayTopology(cluster: string, ref: GatewayRef): Promise<void> {
  useFleet.getState().setTopologyLoading(cluster, ref);
  try {
    const t = (await GatewayService.GetGatewayTopology(cluster, ref.namespace, ref.name)) as TopologyDTO;
    const cur = useFleet.getState().network.selected;
    if (!cur || cur.namespace !== ref.namespace || cur.name !== ref.name) return;
    useFleet.getState().setTopology(t);
  } catch (e) {
    // Clear the loading flag so the topology pane doesn't spin forever.
    const cur = useFleet.getState().network.selected;
    if (cur && cur.namespace === ref.namespace && cur.name === ref.name) {
      useFleet.setState((s) => ({ network: { ...s.network, topologyLoading: false } }));
    }
    throw e;
  }
}

export async function getRouteMetrics(cluster: string, gwNamespace: string, gwName: string, routeKeys: string[]): Promise<void> {
  const r = (await GatewayService.GetRouteMetrics(cluster, routeKeys)) as RouteMetricsResultDTO;
  // Drop a stale result if the user switched gateways while this was in flight.
  const cur = useFleet.getState().network.selected;
  if (!cur || cur.namespace !== gwNamespace || cur.name !== gwName) return;
  useFleet.getState().setRouteMetrics(r ?? { status: { available: false, message: "", updatedAt: "" }, routes: {} });
}
