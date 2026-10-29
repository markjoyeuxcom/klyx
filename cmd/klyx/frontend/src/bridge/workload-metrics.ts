import { useFleet, WorkloadMetricsResultDTO } from "../store/fleet";
import { WorkloadsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// getWorkloadMetrics fetches live usage into the slice's usage map, joined to
// the rows at render (a rows push never erases it). Stale-guarded on
// cluster+namespace by setWorkloadUsage. Failures are swallowed (the store
// keeps last-good usage and marks stale).
// A reply applies only when no later request was made: an earlier read's
// late reply never replaces a newer one (Settings S3b §3.2).
let gen = 0;

export async function getWorkloadMetrics(cluster: string, namespace: string): Promise<void> {
  const g = ++gen;
  let r: WorkloadMetricsResultDTO;
  try {
    r = ((await WorkloadsService.GetWorkloadMetrics(cluster, namespace)) as WorkloadMetricsResultDTO) ?? { status: { available: false, message: "", updatedAt: "" }, usage: {} };
  } catch {
    r = { status: { available: false, message: "metrics request failed", updatedAt: "" }, usage: {} };
  }
  if (g !== gen) return;
  useFleet.getState().setWorkloadUsage(cluster, namespace, r);
}
