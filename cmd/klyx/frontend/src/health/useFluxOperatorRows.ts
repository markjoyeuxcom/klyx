import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { FluxInstanceDTO, FluxReportDTO, FluxSetRowDTO } from "../bridge/fluxop";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Flux Operator view's rows (coverage S1a §2) from the Observer's shared
// rows: the flux task's fluxinstance, resourcesets and fluxreport parts. The
// view is their only reader, so the hook hands them to it directly. Each
// part's evidence is its own, per kind.

export const FLUX_OPERATOR_KIND_NAMES = {
  fluxinstances: "FluxInstance", resourcesets: "ResourceSet", resourcesetinputproviders: "ResourceSetInputProvider", fluxreports: "FluxReport",
};
export const FLUX_OPERATOR_PART_KINDS = {
  fluxinstance: ["FluxInstance"],
  resourcesets: ["ResourceSet", "ResourceSetInputProvider"],
  fluxreport: ["FluxReport"],
};

export type FluxOperatorRows = {
  instances: FluxInstanceDTO[];
  sets: FluxSetRowDTO[];
  reports: FluxReportDTO[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  // the evaluation time of the report part's capture: "read by Klyx"
  reportReadAt: string | null;
  partCounts: { fluxinstance: PartCountsDTO | null; resourcesets: PartCountsDTO | null };
};

export function useFluxOperatorRows(cluster: string | null): FluxOperatorRows {
  const inst = useRows<FluxInstanceDTO>(cluster, "flux", "fluxinstance");
  const sets = useRows<FluxSetRowDTO>(cluster, "flux", "resourcesets");
  const reps = useRows<FluxReportDTO>(cluster, "flux", "fluxreport");
  return useMemo(() => {
    const coverage = [
      ...partCoverage(inst, FLUX_OPERATOR_PART_KINDS.fluxinstance, FLUX_OPERATOR_KIND_NAMES),
      ...partCoverage(sets, FLUX_OPERATOR_PART_KINDS.resourcesets, FLUX_OPERATOR_KIND_NAMES),
      ...partCoverage(reps, FLUX_OPERATOR_PART_KINDS.fluxreport, FLUX_OPERATOR_KIND_NAMES),
    ];
    return {
      instances: partRows(inst),
      sets: partRows(sets),
      reports: partRows(reps),
      coverage,
      watchError: coverageError(coverage),
      loading: !answered(inst) && !answered(sets) && !answered(reps),
      rowsState: rowsState([inst, sets]),
      reportReadAt: reps.evaluatedAt,
      partCounts: { fluxinstance: partCounts(inst), resourcesets: partCounts(sets) },
    };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inst.rows, inst.part, inst.error, inst.refreshError, inst.evaluatedAt, inst.counts.part, inst.counts.updating, inst.counts.refreshing,
    sets.rows, sets.part, sets.error, sets.refreshError, sets.evaluatedAt, sets.counts.part, sets.counts.updating, sets.counts.refreshing,
    reps.rows, reps.part, reps.error, reps.evaluatedAt]);
}
