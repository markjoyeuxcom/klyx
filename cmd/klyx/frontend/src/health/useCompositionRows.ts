import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { CompositionRowDTO, ReceiverDTO } from "../bridge/fluxcomposition";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Composition and Notifications views' rows (coverage S1c §3) from the
// Observer's shared rows: the flux task's composition part (generators and
// artifacts) and its receivers part.

export const COMPOSITION_KIND_NAMES = { artifactgenerators: "ArtifactGenerator", externalartifacts: "ExternalArtifact" };
export const COMPOSITION_PART_KINDS = ["ArtifactGenerator", "ExternalArtifact"];
export const RECEIVER_KIND_NAMES = { receivers: "Receiver" };

export type PartRows<T> = {
  rows: T[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
};

function usePartRows<T>(cluster: string | null, part: string, kinds: string[], names: Record<string, string>): PartRows<T> {
  const r = useRows<T>(cluster, "flux", part);
  return useMemo(() => {
    const coverage = partCoverage(r, kinds, names);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r) };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

export const useCompositionRows = (cluster: string | null) => usePartRows<CompositionRowDTO>(cluster, "composition", COMPOSITION_PART_KINDS, COMPOSITION_KIND_NAMES);
export const useReceiverRows = (cluster: string | null) => usePartRows<ReceiverDTO>(cluster, "receivers", ["Receiver"], RECEIVER_KIND_NAMES);
