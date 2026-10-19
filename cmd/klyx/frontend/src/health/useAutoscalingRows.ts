import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { HPARowDTO, KEDAAuthDTO, ScaledJobDTO, ScaledObjectDTO } from "../bridge/keda";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Autoscaling lens's rows (coverage S9 §9) from the Observer's shared
// rows: the autoscaling task's hpas part and the keda task's scaledobjects,
// scaledjobs and authentications parts.

export const AUTOSCALING_KIND_NAMES = {
  horizontalpodautoscalers: "HorizontalPodAutoscaler", scaledobjects: "ScaledObject", scaledjobs: "ScaledJob",
  triggerauthentications: "TriggerAuthentication", clustertriggerauthentications: "ClusterTriggerAuthentication",
  deployments: "Deployment", statefulsets: "StatefulSet",
};

export type AutoscalingPartRows<T> = {
  rows: T[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
};

function usePart<T>(cluster: string | null, task: string, part: string, kinds: string[]): AutoscalingPartRows<T> {
  const r = useRows<T>(cluster, task, part);
  return useMemo(() => {
    const coverage = partCoverage(r, kinds, AUTOSCALING_KIND_NAMES);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r) };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

// The counted kinds name the lens's own read; dependencies leave their
// evidence "unverified" in the inspector instead.
export const useHPAs = (cluster: string | null) => usePart<HPARowDTO>(cluster, "autoscaling", "hpas", ["HorizontalPodAutoscaler"]);
export const useScaledObjects = (cluster: string | null) => usePart<ScaledObjectDTO>(cluster, "keda", "scaledobjects", ["ScaledObject"]);
export const useScaledJobs = (cluster: string | null) => usePart<ScaledJobDTO>(cluster, "keda", "scaledjobs", ["ScaledJob"]);
export const useKEDAAuths = (cluster: string | null) => usePart<KEDAAuthDTO>(cluster, "keda", "authentications", ["TriggerAuthentication", "ClusterTriggerAuthentication"]);
