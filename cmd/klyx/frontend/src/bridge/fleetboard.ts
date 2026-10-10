import { observe, type HealthLike } from "../health/verdict";
import { useFleet } from "../store/fleet";
import type { FleetBoardEntry } from "../store/fleet";
import {
  MetricsService,
  GitOpsService,
  ArgoService,
  WorkloadsService,
  GatewayService,
} from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type MetricsDTO = { available: boolean; cpuFraction: number | null; memFraction: number | null };
type GitOpsSummaryDTO = { fluxPresent: boolean; total: number; notReady: number; suspended: number };
type ArgoResultDTO = { available: boolean; apps: { broken: boolean }[] };
type WorkloadsResultDTO = { workloads: { health?: HealthLike }[]; truncated?: boolean; error?: string };
type GatewayListDTO = { gatewayAPIServed: boolean; gateways: { namespace: string; name: string; accepted: boolean; programmed: boolean }[] };
type TopologyDTO = { routes?: { accepted: boolean; resolvedRefs: boolean }[]; error?: string };

// fetchFleetBoard enriches each connected cluster's card with utilization,
// workload health, GitOps state, and Gateway API counts. On-demand: once per
// fleet-view visit, all clusters in parallel, each field independently
// null/absent on failure - the card states unreadable, never a fabricated zero.
export async function fetchFleetBoard(clusters: string[]): Promise<void> {
  await Promise.all(clusters.map(async (cluster) => {
    const [metrics, gitops, argo, workloads, gateway] = await Promise.allSettled([
      MetricsService.GetClusterMetrics(cluster, false),
      GitOpsService.GetGitOpsSummary(cluster),
      ArgoService.ListApplications(cluster),
      WorkloadsService.ListWorkloads(cluster, ""),
      gatewaySummary(cluster),
    ]);

    const entry: FleetBoardEntry = {
      cpuFraction: null, memFraction: null,
      workloads: null, flux: null, argo: null, gateway: null,
    };

    if (metrics.status === "fulfilled" && metrics.value) {
      const m = metrics.value as MetricsDTO;
      if (m.available) {
        entry.cpuFraction = m.cpuFraction;
        entry.memFraction = m.memFraction;
      }
    }
    if (gitops.status === "fulfilled" && gitops.value) {
      const g = gitops.value as GitOpsSummaryDTO;
      if (g.fluxPresent) entry.flux = { total: g.total, notReady: g.notReady };
    }
    if (argo.status === "fulfilled" && argo.value) {
      const a = argo.value as ArgoResultDTO;
      if (a.available) entry.argo = { total: a.apps.length, broken: a.apps.filter((x) => x.broken).length };
    }
    if (workloads.status === "fulfilled" && workloads.value) {
      // A fulfilled call can still carry a failed read (error) or a capped
      // list (truncated): the first is unreadable, never a zero; the second
      // is a lower bound.
      const r = workloads.value as WorkloadsResultDTO;
      if (!r.error) {
        entry.workloads = observe(r.workloads ?? [], (x) => x.health, !!r.truncated);
      }
    }
    if (gateway.status === "fulfilled") entry.gateway = gateway.value;

    useFleet.getState().setFleetBoardEntry(cluster, entry);
  }));
}

async function gatewaySummary(cluster: string): Promise<NonNullable<FleetBoardEntry["gateway"]>> {
  const list = (await GatewayService.ListGateways(cluster)) as GatewayListDTO;
  if (!list?.gatewayAPIServed) {
    return { served: false, gateways: 0, routes: null, brokenRoutes: null, unprogrammed: 0 };
  }

  const gateways = list.gateways ?? [];
  const topologies = await Promise.allSettled(
    gateways.map((g) => GatewayService.GetGatewayTopology(cluster, g.namespace, g.name)),
  );
  let routes = 0;
  let brokenRoutes = 0;
  let allRouteReadsSucceeded = true;
  for (const t of topologies) {
    if (t.status !== "fulfilled" || (t.value as TopologyDTO)?.error) {
      allRouteReadsSucceeded = false;
      continue;
    }
    const rs = ((t.value as TopologyDTO).routes ?? []);
    routes += rs.length;
    brokenRoutes += rs.filter((r) => !r.accepted || !r.resolvedRefs).length;
  }

  return {
    served: true,
    gateways: gateways.length,
    routes: allRouteReadsSucceeded ? routes : null,
    brokenRoutes: allRouteReadsSucceeded ? brokenRoutes : null,
    unprogrammed: gateways.filter((g) => !g.accepted || !g.programmed).length,
  };
}
