import { useEffect } from "react";
import { useFleet, type PodSummaryDTO } from "../store/fleet";
import { coverageError, coverageFromHealth } from "../layout/coverage";
import { useRows } from "./useRows";
import { partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The Pods lens from the Observer's shared rows (slice 3 §5.9): the
// pods/pods verdict part, all namespaces and uncapped, fills the pods slice.
// Each row's verdict is the capture's (its health); the global counts are
// the one count selector's; the list evidence is the Pod kind's own
// coverage, and what owner resolution read (ReplicaSets, which name a
// Deployment) is carried as the capture's dependency - never the list's own
// evidence. A checking or unreadable pods kind is incomplete and shown as
// such, never as a full list.

export const POD_KIND_NAMES = { pods: "Pod" };
export const POD_DEP_NAMES = { replicasets: "ReplicaSet" };

const owner = sliceOwner(
  () => useFleet.getState().pods.cluster,
  (cluster) => useFleet.getState().setPodsVisit(cluster),
  () => useFleet.getState().clearPods(),
);

export function usePodRows(cluster: string | null): void {
  const r = useRows<PodSummaryDTO>(cluster, "pods", "pods");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.pods.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = partCoverage(r, ["Pod"], POD_KIND_NAMES);
    const items = partRows(r);
    st.setPodsFromRows(cluster, {
      items,
      namespaces: [...new Set(items.map((p) => p.namespace))].sort(),
      // the visit has its first payload: from here the evidence is the
      // coverage (a part that has not answered is "checking")
      loading: false,
      error: coverageError(coverage),
      coverage,
      dependencies: r.part ? coverageFromHealth(r.part.kinds, POD_DEP_NAMES, "dependency") : [],
      rowsState: rowsState([r]),
      counts: r.counts.part?.counts ?? null,
    });
    // reference-stable picks only: the hook's result object is rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

// test seam
export function _resetPodRowsForTest(): void {
  owner.resetForTest();
}
