import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { ImageRowDTO } from "../bridge/fluximages";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Images view's rows (coverage S1b §2) from the Observer's shared rows:
// the flux task's images part, one part for the three kinds.

export const IMAGE_KIND_NAMES = { imagerepositories: "ImageRepository", imagepolicies: "ImagePolicy", imageupdateautomations: "ImageUpdateAutomation" };
export const IMAGE_PART_KINDS = ["ImageRepository", "ImagePolicy", "ImageUpdateAutomation"];

export type ImageRows = {
  rows: ImageRowDTO[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
};

export function useImageRows(cluster: string | null): ImageRows {
  const r = useRows<ImageRowDTO>(cluster, "flux", "images");
  return useMemo(() => {
    const coverage = partCoverage(r, IMAGE_PART_KINDS, IMAGE_KIND_NAMES);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r) };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}
