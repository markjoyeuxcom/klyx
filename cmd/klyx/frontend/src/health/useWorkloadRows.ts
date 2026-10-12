import { useEffect } from "react";
import { useFleet, type WorkloadDTO } from "../store/fleet";
import { coverageError, coverageFromHealth } from "../layout/coverage";
import { useRows } from "./useRows";
import { partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The Workloads lens from the Observer's shared rows (slice 3 §5.8): the
// workloads/workloads verdict part, all namespaces, fills the workloads
// slice. Each row's verdict is the capture's (its health), evaluated with the
// fluxPresent fact that travels with the rows (§2a); the global counts are
// the one count selector's; the list evidence is the counted kinds' own
// coverage, and what pod attribution read (Pods, ReplicaSets) is carried as
// the capture's dependencies - never the list's own evidence.

export const WORKLOAD_KIND_NAMES = { deployments: "Deployment", statefulsets: "StatefulSet", daemonsets: "DaemonSet" };
export const WORKLOAD_DEP_NAMES = { pods: "Pod", replicasets: "ReplicaSet" };
const COUNTED = ["Deployment", "StatefulSet", "DaemonSet"];

const owner = sliceOwner(
  () => useFleet.getState().workloads.cluster,
  (cluster) => useFleet.getState().setWorkloadsVisit(cluster),
  () => useFleet.getState().clearWorkloads(),
);

export function useWorkloadRows(cluster: string | null): void {
  const r = useRows<WorkloadDTO>(cluster, "workloads", "workloads");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.workloads.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = partCoverage(r, COUNTED, WORKLOAD_KIND_NAMES);
    const items = partRows(r);
    st.setWorkloadsFromRows(cluster, {
      items,
      namespaces: [...new Set(items.map((w) => w.namespace))].sort(),
      fluxPresent: r.facts?.fluxPresent === true,
      // the visit has its first payload: from here the evidence is the
      // coverage (a part that has not answered is "checking")
      loading: false,
      error: coverageError(coverage),
      coverage,
      dependencies: r.part ? coverageFromHealth(r.part.kinds, WORKLOAD_DEP_NAMES, "dependency") : [],
      rowsState: rowsState([r]),
      counts: r.counts.part?.counts ?? null,
    });
    // reference-stable picks only: the hook's result object is rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, r.rows, r.facts, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

// test seam
export function _resetWorkloadRowsForTest(): void {
  owner.resetForTest();
}
