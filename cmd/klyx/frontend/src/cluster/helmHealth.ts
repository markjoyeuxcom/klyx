// Helm release health (shared by the Helm lens and the cluster attention
// summary, so both say the same thing about the same release).

export type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown"; label: string };

// A pending-* or uninstalling release past PENDING_DEADLINE_S (Klyx's
// attention heuristic: twice helm's default 5m --timeout) "appears stuck".
// Elapsed time is not proof: no configured timeout or process evidence is
// visible in a snapshot, so the wording never claims the operation stopped.
export const PENDING_DEADLINE_S = 600;
export const IN_FLIGHT = new Set(["pending-install", "pending-upgrade", "pending-rollback", "uninstalling"]);
const HISTORICAL = new Set(["superseded", "uninstalled"]);

// releaseHealth maps helm's status vocabulary onto the shared roles (only
// faults glow). Historical states never fault.
export function releaseHealth(r: { status: string; updatedUnix: number }, nowS: number): Role {
  if (r.status === "deployed") return { kind: "ok", label: "deployed" };
  if (r.status === "failed") return { kind: "fault", label: "failed" };
  if (IN_FLIGHT.has(r.status)) {
    if (r.updatedUnix > 0 && nowS - r.updatedUnix > PENDING_DEADLINE_S) return { kind: "degraded", label: `${r.status}, appears stuck` };
    return { kind: "progressing", label: r.status };
  }
  if (HISTORICAL.has(r.status)) return { kind: "unknown", label: `${r.status} (historical)` };
  return { kind: "unknown", label: r.status || "unknown" };
}
