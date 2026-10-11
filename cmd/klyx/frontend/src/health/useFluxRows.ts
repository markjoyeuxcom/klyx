import { useEffect } from "react";
import { useFleet, type FluxResourceDTO, type FluxSourceDTO } from "../store/fleet";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The Flux lens's data from the Observer's shared rows (slice 3 §5.3): the
// flux/reconciliations and flux/sources parts - the old combined
// gitops:updated push as two parts - fill the existing gitops slice, whose
// selection and detail stay the lens's own. Coverage as for certificates
// (health/lensRows): each part's evidence is its own, per kind.

export const FLUX_KIND_NAMES = {
  kustomizations: "Kustomization", helmreleases: "HelmRelease",
  gitrepositories: "GitRepository", ocirepositories: "OCIRepository", buckets: "Bucket", helmrepositories: "HelmRepository", helmcharts: "HelmChart",
};
export const FLUX_PART_KINDS = {
  reconciliations: ["Kustomization", "HelmRelease"],
  sources: ["GitRepository", "OCIRepository", "Bucket", "HelmRepository", "HelmChart"],
};

const owner = sliceOwner(
  () => useFleet.getState().gitops.cluster,
  (cluster) => useFleet.getState().setGitOpsLoading(cluster),
  () => useFleet.getState().clearGitOps(),
);

export function useFluxRows(cluster: string | null): void {
  const recs = useRows<FluxResourceDTO>(cluster, "flux", "reconciliations");
  const sources = useRows<FluxSourceDTO>(cluster, "flux", "sources");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.gitops.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = [...partCoverage(recs, FLUX_PART_KINDS.reconciliations, FLUX_KIND_NAMES), ...partCoverage(sources, FLUX_PART_KINDS.sources, FLUX_KIND_NAMES)];
    st.setGitOpsFromRows(cluster, {
      resources: partRows(recs),
      sources: partRows(sources),
      coverage,
      watchError: coverageError(coverage),
      loading: !answered(recs) && !answered(sources),
      rowsState: rowsState([recs, sources]),
      partCounts: { reconciliations: partCounts(recs), sources: partCounts(sources) },
    });
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, recs.rows, recs.part, recs.error, recs.refreshError, recs.evaluatedAt, recs.counts.part, recs.counts.updating, recs.counts.refreshing,
    sources.rows, sources.part, sources.error, sources.refreshError, sources.evaluatedAt, sources.counts.part, sources.counts.updating, sources.counts.refreshing]);
}

// test seam
export function _resetFluxRowsForTest(): void {
  owner.resetForTest();
}
