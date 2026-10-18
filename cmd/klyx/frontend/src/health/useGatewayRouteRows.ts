import { useMemo } from "react";
import type { GatewayRouteRowDTO, KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import { coverageError, coverageFromHealth } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Gateways task's routes part (slice 3 §4a; coverage S7b §5): one row
// per route UID of any of the five kinds, reduced from its attachments. Its
// own evidence is the route kinds; the parent Gateways and ListenerSets,
// their GatewayClasses, the Services, EndpointSlices, ReferenceGrants and
// Namespaces it was evaluated against are DEPENDENCIES - they qualify the
// verdicts, never the list's completeness.

export const ROUTE_KIND_NAMES: Record<string, string> = { httproutes: "HTTPRoute", grpcroutes: "GRPCRoute", tlsroutes: "TLSRoute", tcproutes: "TCPRoute", udproutes: "UDPRoute" };
const NAMES = {
  ...ROUTE_KIND_NAMES, gateways: "Gateway", listenersets: "ListenerSet", gatewayclasses: "GatewayClass", services: "Service",
  endpointslices: "EndpointSlice", referencegrants: "ReferenceGrant", namespaces: "Namespace",
};

export type GatewayRouteRows = {
  routes: GatewayRouteRowDTO[];
  coverage: KindCoverageDTO[]; // the part's OWN counted kinds (the route kinds)
  dependencies: KindCoverageDTO[]; // what the verdicts were evaluated against
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  partCounts: PartCountsDTO | null;
};

export function useGatewayRouteRows(cluster: string | null): GatewayRouteRows {
  const r = useRows<GatewayRouteRowDTO>(cluster, "gateway", "routes");
  return useMemo(() => {
    const coverage = partCoverage(r, Object.values(ROUTE_KIND_NAMES), NAMES);
    return {
      routes: partRows(r),
      coverage,
      dependencies: r.part ? coverageFromHealth(r.part.kinds, NAMES, "dependency") : [],
      watchError: coverageError(coverage),
      loading: !answered(r),
      rowsState: rowsState([r]),
      partCounts: partCounts(r),
    };
    // reference-stable picks only: the hook's result object is rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}
