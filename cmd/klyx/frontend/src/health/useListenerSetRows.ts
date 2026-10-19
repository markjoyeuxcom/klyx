import { useMemo } from "react";
import type { KindCoverageDTO, ListenerSetRowDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import { coverageError, coverageFromHealth } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Gateways task's ListenerSets part (coverage S7a §4): one row per
// ListenerSet with its captured verdict. Its own evidence is the ListenerSet
// kind; the parent Gateways, their classes and the namespaces' labels are
// DEPENDENCIES - they qualify the verdicts and the allowance reading, never
// the list's completeness.

const NAMES = { listenersets: "ListenerSet", gateways: "Gateway", gatewayclasses: "GatewayClass", namespaces: "Namespace" };

export type ListenerSetRows = {
  sets: ListenerSetRowDTO[];
  coverage: KindCoverageDTO[]; // the part's OWN counted kind (ListenerSet)
  dependencies: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  partCounts: PartCountsDTO | null;
};

export function useListenerSetRows(cluster: string | null): ListenerSetRows {
  const r = useRows<ListenerSetRowDTO>(cluster, "gateway", "listenersets");
  return useMemo(() => {
    const coverage = partCoverage(r, ["ListenerSet"], NAMES);
    return {
      sets: partRows(r),
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
