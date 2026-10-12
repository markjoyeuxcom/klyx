import { useEffect } from "react";
import { useFleet, type NodeSummaryDTO } from "../store/fleet";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The Nodes lens from the Observer's shared rows (slice 3 §5.7): the
// nodes/nodes verdict part fills the nodes slice. Each row's verdict is the
// capture's (its health), the global counts are the one count selector's
// (the part's verdict counts, or the summary's past the A4 deadline), and the
// list evidence is the Node kind's own coverage - a failed open is
// unreadable, never an empty cluster.

export const NODE_KIND_NAMES = { nodes: "Node" };

const owner = sliceOwner(
  () => useFleet.getState().nodes.cluster,
  (cluster) => useFleet.getState().setNodesVisit(cluster),
  () => useFleet.getState().clearNodes(),
);

export function useNodeRows(cluster: string | null): void {
  const r = useRows<NodeSummaryDTO>(cluster, "nodes", "nodes");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.nodes.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = partCoverage(r, ["Node"], NODE_KIND_NAMES);
    st.setNodesFromRows(cluster, {
      items: partRows(r),
      // the visit has its first payload: from here the evidence is the
      // coverage (a part that has not answered is "checking")
      loading: false,
      error: coverageError(coverage),
      coverage,
      rowsState: rowsState([r]),
      counts: r.counts.part?.counts ?? null,
    });
    // reference-stable picks only: the hook's result object is rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

// test seam
export function _resetNodeRowsForTest(): void {
  owner.resetForTest();
}
