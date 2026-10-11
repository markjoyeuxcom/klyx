import { useEffect } from "react";
import { useFleet, type ArgoAppDTO, type ArgoApplicationSetDTO } from "../store/fleet";
import { useRows, type LensRows } from "./useRows";
import { partCounts, partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The Argo CD lens's Applications and ApplicationSets from the Observer's
// shared rows (slice 3 §5.4); AppProjects stay a fetch. Argo's per-list
// fields - available, message, error, loading - come from each part's own
// coverage, so the lens's list evidence keeps its meaning:
// - absent: Argo CD is not installed for that kind (available=false);
// - live: complete; stale: the last good rows with the read's error;
// - unreadable or a failed open: the error, with no rows;
// - checking, or no current rows (a Final, a newer incarnation): loading.

const KIND_NAMES = { applications: "Application", applicationsets: "ApplicationSet" };

type ListState<T> = { rows: T[]; available: boolean; message: string; error: string | null; loading: boolean };

function listState<T>(r: LensRows<T>, kind: string, resource: string): ListState<T> {
  const k = partCoverage(r, [kind], KIND_NAMES).find((c) => c.kind === kind);
  if (r.part && k?.coverage === "absent") {
    return { rows: [], available: false, message: `Argo CD not detected (no ${resource}.argoproj.io resource)`, error: null, loading: false };
  }
  if (k?.coverage === "stale" || k?.coverage === "unreadable") {
    const error = k.error ?? `${kind} read failed`;
    return { rows: partRows(r), available: true, message: error, error, loading: false };
  }
  if (!r.part && r.error) return { rows: [], available: true, message: r.error, error: r.error, loading: false };
  if (!r.part || k?.coverage === "checking") return { rows: partRows(r), available: true, message: "", error: null, loading: true };
  return { rows: partRows(r), available: true, message: "", error: null, loading: false };
}

const owner = sliceOwner(
  () => useFleet.getState().argo.cluster,
  (cluster) => useFleet.getState().setArgoVisit(cluster),
  () => useFleet.getState().clearArgo(),
);

export function useArgoRows(cluster: string | null): void {
  const apps = useRows<ArgoAppDTO>(cluster, "argo", "applications");
  const sets = useRows<ArgoApplicationSetDTO>(cluster, "argo", "applicationsets");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.argo.cluster !== cluster) return; // another cluster's visit owns the slice
    const a = listState(apps, "Application", "applications");
    const s = listState(sets, "ApplicationSet", "applicationsets");
    st.setArgoFromRows(cluster, {
      apps: a.rows, available: a.available, message: a.message, error: a.error, loading: a.loading,
      applicationSets: s.rows, applicationSetsAvailable: s.available, applicationSetsMessage: s.message, applicationSetsError: s.error, applicationSetsLoading: s.loading,
      coverage: [...partCoverage(apps, ["Application"], KIND_NAMES), ...partCoverage(sets, ["ApplicationSet"], KIND_NAMES)],
      rowsState: rowsState([apps, sets]),
      partCounts: { applications: partCounts(apps), applicationsets: partCounts(sets) },
    });
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, apps.rows, apps.part, apps.error, apps.refreshError, apps.evaluatedAt, apps.counts.part, apps.counts.updating, apps.counts.refreshing,
    sets.rows, sets.part, sets.error, sets.refreshError, sets.evaluatedAt, sets.counts.part, sets.counts.updating, sets.counts.refreshing]);
}

// test seam
export function _resetArgoRowsForTest(): void {
  owner.resetForTest();
}
