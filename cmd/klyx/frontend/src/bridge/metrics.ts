import { useFleet, MetricsDTO, NodeUtilizationResultDTO, PodUsageResultDTO } from "../store/fleet";
import { MetricsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// A reply applies only when no later request was made: an earlier read's
// late reply never replaces a newer one (Settings S3b §3.2).
let metricsGen = 0;

export async function getClusterMetrics(cluster: string, forceReprobe: boolean): Promise<void> {
  const g = ++metricsGen;
  useFleet.getState().setMetricsLoading(cluster);
  try {
    const dto = (await MetricsService.GetClusterMetrics(cluster, forceReprobe)) as MetricsDTO;
    // Ignore a stale response if the user navigated to another cluster,
    // or asked again since.
    if (g !== metricsGen || useFleet.getState().metrics.cluster !== cluster) return;
    useFleet.getState().setMetrics(cluster, dto);
  } catch (e) {
    console.error("getClusterMetrics", e);
    // Clear the loading flag so the overview doesn't get stuck on "Loading…".
    if (g === metricsGen && useFleet.getState().metrics.cluster === cluster) {
      useFleet.setState((s) => ({ metrics: { ...s.metrics, loading: false } }));
    }
  }
}

// Sparkline DTOs — 30m range series. available=false carries the reason; an
// empty cpu/mem array with available=true means "no samples in the window".
export type PointDTO = { t: number; v: number };
export type SparklinesDTO = {
  available: boolean;
  message?: string;
  cpu: PointDTO[];
  mem: PointDTO[];
  sentence?: string;
  lineage?: string;
  noData?: string;
};

const failedSparklines = (message: string): SparklinesDTO => ({ available: false, message, cpu: [], mem: [] });

// On-demand fetches (no store slice): callers hold the result in local state
// for the lifetime of the expanded row / overview mount. Errors resolve to an
// unavailable DTO so the UI degrades to its no-data state instead of throwing.
export async function getWorkloadSparklines(
  cluster: string,
  namespace: string,
  kind: string,
  name: string,
): Promise<SparklinesDTO> {
  try {
    return (await MetricsService.GetWorkloadSparklines(cluster, namespace, kind, name)) as SparklinesDTO;
  } catch (e) {
    return failedSparklines(String(e));
  }
}

export async function getClusterSparklines(cluster: string): Promise<SparklinesDTO> {
  try {
    return (await MetricsService.GetClusterSparklines(cluster)) as SparklinesDTO;
  } catch (e) {
    return failedSparklines(String(e));
  }
}

// Node + per-container utilization (on-demand, no store slice). Errors resolve
// to an unavailable DTO so the views degrade to their no-data captions.
export async function getNodeUtilization(cluster: string): Promise<NodeUtilizationResultDTO> {
  try {
    return (await MetricsService.GetNodeUtilization(cluster)) as NodeUtilizationResultDTO;
  } catch (e) {
    return { available: false, message: String(e), updatedAtUnix: 0, items: [] };
  }
}

export async function getPodContainerUsage(cluster: string, namespace: string, pod: string): Promise<PodUsageResultDTO> {
  try {
    return (await MetricsService.GetPodContainerUsage(cluster, namespace, pod)) as PodUsageResultDTO;
  } catch (e) {
    return { available: false, message: String(e), updatedAtUnix: 0, items: [] };
  }
}
