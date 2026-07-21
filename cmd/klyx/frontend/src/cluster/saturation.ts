import type { WorkloadDTO } from "../store/fleet";

export type SatTier = "none" | "neutral" | "warn" | "danger";
export type Resource = "cpu" | "mem";

// saturation returns usage/limit and its risk tier. CPU and memory are asymmetric:
// memory limit is a hard OOM ceiling (75% warn, 90% danger); cpu limit is throttling
// proximity (90% warn, 100% danger). No usage or no limit → no calculable saturation.
export function saturation(resource: Resource, usage: number | null, limit: number | null): { pct: number | null; tier: SatTier } {
  if (usage == null || limit == null || limit <= 0) return { pct: null, tier: "none" };
  const pct = usage / limit;
  if (resource === "mem") {
    if (pct >= 0.9) return { pct, tier: "danger" };
    if (pct >= 0.75) return { pct, tier: "warn" };
    return { pct, tier: "neutral" };
  }
  if (pct >= 1.0) return { pct, tier: "danger" };
  if (pct >= 0.9) return { pct, tier: "warn" };
  return { pct, tier: "neutral" };
}

const RANK_ORDER: Record<WorkloadDTO["rank"], number> = { unhealthy: 0, degraded: 1, restarts: 2, healthy: 3 };

function memSat(w: WorkloadDTO): number {
  const s = saturation("mem", w.resources.mem.usage, w.resources.mem.limit);
  return s.pct ?? -1; // no calculable saturation sinks below any calculable one
}
function cpuSat(w: WorkloadDTO): number {
  const s = saturation("cpu", w.resources.cpu.usage, w.resources.cpu.limit);
  return s.pct ?? -1;
}

// nearLimitSort: mem saturation desc → cpu saturation desc → k8s rank → ns/name.
// Rows with no calculable saturation (no limit OR usage absent) sort below calculable
// ones; full ties fall back to rank then namespace/name. Pure, returns a new array.
export function nearLimitSort(items: WorkloadDTO[]): WorkloadDTO[] {
  return [...items].sort((a, b) => {
    const dm = memSat(b) - memSat(a);
    if (dm !== 0) return dm;
    const dc = cpuSat(b) - cpuSat(a);
    if (dc !== 0) return dc;
    const dr = RANK_ORDER[a.rank] - RANK_ORDER[b.rank];
    if (dr !== 0) return dr;
    if (a.namespace !== b.namespace) return a.namespace < b.namespace ? -1 : 1;
    return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
  });
}

// Rightsizing verdict for one container+resource: usage vs declared request/limit.
// Diagnosis framing, NOT FinOps: the three findings are "near the limit" (OOM /
// throttle risk), "oversized request" (scheduling waste that starves the node),
// and null (nothing noteworthy or not calculable). Oversize needs BOTH a
// meaningful request (cpu ≥ 50m, mem ≥ 64Mi - below that the noise beats the
// signal) and usage under 1/8 of it; the ratio caps at "≥20×" so a near-idle
// container doesn't render a silly number.
export type RightsizingFinding = { tier: "warn" | "danger" | "info"; label: string };

const OVERSIZE_RATIO = 8;
const OVERSIZE_CAP = 20;
const MIN_CPU_REQUEST = 0.05; // 50m
const MIN_MEM_REQUEST = 64 * 1048576; // 64Mi

export function rightsizing(
  resource: Resource,
  usage: number | null | undefined,
  request: number | null | undefined,
  limit: number | null | undefined,
): RightsizingFinding | null {
  if (usage == null) return null;
  const sat = saturation(resource, usage, limit ?? null);
  if (sat.tier === "danger") {
    return { tier: "danger", label: resource === "mem" ? "near mem limit — OOM risk" : "cpu at limit — throttling" };
  }
  if (sat.tier === "warn") {
    return { tier: "warn", label: resource === "mem" ? "approaching mem limit" : "approaching cpu limit" };
  }
  const floor = resource === "cpu" ? MIN_CPU_REQUEST : MIN_MEM_REQUEST;
  if (request != null && request >= floor && usage < request / OVERSIZE_RATIO) {
    const ratio = usage > 0 ? request / usage : Infinity;
    const shown = ratio >= OVERSIZE_CAP ? `≥${OVERSIZE_CAP}×` : `${Math.round(ratio)}×`;
    return { tier: "info", label: `${resource} request ${shown} actual — oversized` };
  }
  return null;
}

export function fmtCpu(cores: number): string {
  return cores >= 1 ? cores.toFixed(2) : `${Math.round(cores * 1000)}m`;
}

export function fmtMem(bytes: number): string {
  const Mi = 1048576, Gi = 1073741824;
  return bytes >= Gi ? `${(bytes / Gi).toFixed(1)}Gi` : `${Math.round(bytes / Mi)}Mi`;
}
