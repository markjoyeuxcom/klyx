import { useEffect } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import { coverageFromHealth } from "../layout/coverage";
import type { LensRows } from "./useRows";

// Shared pieces of the slice 3 rows adapters (health/use*Rows): each lens
// opens its task's parts and fills its existing store slice from them. The
// rules are the Certificates migration's, as Codex accepted them (7709df0):
// - each part's evidence is its own: a part without current rows (no answer
//   yet, or fenced by a Final or a newer incarnation) is checking with no
//   rows - never another part's evidence, never its own old rows;
// - a failed open (retrying) is unreadable with its error, never empty;
// - the slice loads once any part answers;
// - the global counts are the one count selector's, per part.

const kindsAs = (kinds: string[], coverage: "checking" | "unreadable", error?: string): KindCoverageDTO[] =>
  kinds.map((kind) => ({ kind, served: true, synced: false, error, coverage }));

/** partCoverage: a part's own per-kind coverage (Observer kinds renamed by
 * `names`); a failed open is unreadable with its error; no current rows is
 * checking. `kinds` are the part's lens kinds. */
export function partCoverage<T>(r: LensRows<T>, kinds: string[], names: Record<string, string>): KindCoverageDTO[] {
  if (r.part) return coverageFromHealth(r.part.kinds, names);
  if (r.error) return kindsAs(kinds, "unreadable", r.error);
  return kindsAs(kinds, "checking");
}

/** partDependency: the coverage a part's own capture had of one of its
 * dependency kinds (what a join such as a NodePool's node count was read
 * from), or null without a current capture. */
export function partDependency<T>(r: LensRows<T>, kind: string, names: Record<string, string>): KindCoverageDTO | null {
  if (!r.part) return null;
  return coverageFromHealth(r.part.kinds, names, "dependency").find((k) => k.kind === kind) ?? null;
}

/** partRows: a part's rows only while it has current ones. */
export const partRows = <T,>(r: LensRows<T>): T[] => (r.part ? r.rows ?? [] : []);

/** answered: the part has rows of its own or a failed open to show. */
export const answered = (r: LensRows<unknown>) => r.part !== null || r.error !== null;

/** partCounts: the one selector's counts for the part (rows', or the
 * summary's past the A4 deadline); none while nothing current is known. */
export function partCounts<T>(r: LensRows<T>): PartCountsDTO | null {
  const n = r.counts.part?.native;
  return n ? { total: n.total, included: n.included } : null;
}

/** rowsState: the lens's A4 state across its parts. A refreshing view names
 * the oldest capture among its refreshing parts, else the first part's. */
export function rowsState(parts: LensRows<unknown>[]): RowsStateDTO {
  const times = parts.filter((r) => r.counts.refreshing && r.evaluatedAt).map((r) => r.evaluatedAt as string).sort();
  return {
    updating: parts.some((r) => r.counts.updating),
    refreshing: parts.some((r) => r.counts.refreshing),
    evaluatedAt: times[0] ?? parts.find((r) => r.evaluatedAt)?.evaluatedAt ?? null,
    refreshError: parts.find((r) => r.refreshError)?.refreshError ?? null,
  };
}

/** sliceOwner counts a lens slice's mounted consumers per cluster: the
 * first resets the slice (a new visit's loading state), the last clears it,
 * and a consumer never clears another cluster's visit. */
export function sliceOwner(owner: () => string | null, reset: (cluster: string) => void, clear: () => void) {
  const consumers = new Map<string, number>();
  return {
    useOwner(cluster: string | null): void {
      useEffect(() => {
        if (!cluster) return;
        const n = (consumers.get(cluster) ?? 0) + 1;
        consumers.set(cluster, n);
        if (n === 1 || owner() !== cluster) reset(cluster);
        return () => {
          const left = (consumers.get(cluster) ?? 1) - 1;
          if (left > 0) consumers.set(cluster, left);
          else {
            consumers.delete(cluster);
            if (owner() === cluster) clear();
          }
        };
      }, [cluster]);
    },
    resetForTest: () => consumers.clear(),
  };
}
