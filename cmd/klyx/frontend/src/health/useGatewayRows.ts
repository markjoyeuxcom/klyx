import { useMemo } from "react";
import type { GatewayRefDTO, KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Gateways list from the Observer's shared rows (slice 3 §5.6): the
// gateway/gateways part, handed straight to the list (its only reader - no
// store slice). Whether the Gateway API is served is the kind's own
// coverage: only a confirmed absence is "not installed". The topology stays
// a fetch.

export const GATEWAY_KIND_NAMES = { gateways: "Gateway" };

export type GatewayRows = {
  gateways: GatewayRefDTO[];
  coverage: KindCoverageDTO[]; // the part's OWN counted kind
  watchError: string | null;
  loading: boolean; // the part has not answered yet
  rowsState: RowsStateDTO;
  partCounts: PartCountsDTO | null;
};

export function useGatewayRows(cluster: string | null): GatewayRows {
  const g = useRows<GatewayRefDTO>(cluster, "gateway", "gateways");
  return useMemo(() => {
    const coverage = partCoverage(g, ["Gateway"], GATEWAY_KIND_NAMES);
    return {
      gateways: partRows(g),
      coverage,
      watchError: coverageError(coverage),
      loading: !answered(g),
      rowsState: rowsState([g]),
      partCounts: partCounts(g),
    };
    // reference-stable picks only: the hook's result object is rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [g.rows, g.part, g.error, g.refreshError, g.evaluatedAt, g.counts.part, g.counts.updating, g.counts.refreshing]);
}
