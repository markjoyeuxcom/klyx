import { useFleet, MetricsDTO } from "../store/fleet";
import { MetricsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

export async function getClusterMetrics(cluster: string, forceReprobe: boolean): Promise<void> {
  useFleet.getState().setMetricsLoading(cluster);
  try {
    const dto = (await MetricsService.GetClusterMetrics(cluster, forceReprobe)) as MetricsDTO;
    // Ignore a stale response if the user navigated to another cluster.
    if (useFleet.getState().metrics.cluster !== cluster) return;
    useFleet.getState().setMetrics(cluster, dto);
  } catch (e) {
    console.error("getClusterMetrics", e);
    // Clear the loading flag so the overview doesn't get stuck on "Loading…".
    if (useFleet.getState().metrics.cluster === cluster) {
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
