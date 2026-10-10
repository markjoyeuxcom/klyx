import { useFleet, GatewayListDTO, TopologyDTO, GatewayRef, RouteMetricsResultDTO, HubbleStatsDTO } from "../store/fleet";
import { GatewayService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Generation fences: a reply is applied only while it answers the latest
// request for the same cluster (and, for a topology, the same Gateway). A
// late reply from another cluster with a colliding namespace/name - the
// same "envoy-gateway-system/eg" exists across a fleet - is dropped.
let listGen = 0;
let topoGen = 0;

const sameGateway = (cluster: string, ref: GatewayRef) => {
  const n = useFleet.getState().network;
  return n.cluster === cluster && !!n.selected && n.selected.namespace === ref.namespace && n.selected.name === ref.name;
};

export async function listGateways(cluster: string): Promise<void> {
  const g = ++listGen;
  useFleet.getState().setGatewaysLoading(cluster);
  try {
    const l = (await GatewayService.ListGateways(cluster)) as GatewayListDTO;
    if (g !== listGen) return;
    useFleet.getState().setGateways(cluster, l ?? { gatewayAPIServed: false, gateways: [] });
  } catch (e) {
    if (g !== listGen) return;
    // A failed call is an error, never "no Gateways" and never stuck loading.
    if (useFleet.getState().network.cluster === cluster) {
      useFleet.setState((s) => ({ network: { ...s.network, listLoading: false, listError: errText(e) } }));
    }
    throw e;
  }
}

export async function getGatewayTopology(cluster: string, ref: GatewayRef): Promise<void> {
  const g = ++topoGen;
  useFleet.getState().setTopologyLoading(cluster, ref);
  try {
    const t = (await GatewayService.GetGatewayTopology(cluster, ref.namespace, ref.name)) as TopologyDTO;
    if (g !== topoGen || !sameGateway(cluster, ref)) return;
    useFleet.getState().setTopology(t);
  } catch (e) {
    if (g !== topoGen || !sameGateway(cluster, ref)) return;
    useFleet.getState().setTopologyError(errText(e));
    throw e;
  }
}

export async function getRouteMetrics(cluster: string, gwNamespace: string, gwName: string, routeKeys: string[]): Promise<void> {
  const r = (await GatewayService.GetRouteMetrics(cluster, routeKeys)) as RouteMetricsResultDTO;
  // Drop a stale result if the user switched clusters or gateways meanwhile.
  if (!sameGateway(cluster, { namespace: gwNamespace, name: gwName })) return;
  useFleet.getState().setRouteMetrics(r ?? { status: { available: false, message: "", updatedAt: "" }, routes: {} });
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : typeof e === "string" ? e : "the read failed";
}

// getHubbleFlowStats fetches the Hubble flow/drop triage readout (on-demand,
// read-only). Availability + honest reasons come from the backend status.
export async function getHubbleFlowStats(cluster: string): Promise<HubbleStatsDTO> {
  return (await GatewayService.GetHubbleFlowStats(cluster)) as HubbleStatsDTO;
}
