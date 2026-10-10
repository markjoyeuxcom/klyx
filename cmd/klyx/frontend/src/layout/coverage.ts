import type { KindCoverageDTO } from "../store/fleet";

// Evidence for one watched kind (backend KindCoverageDTO), as a lens may use
// it. The contract (Codex cert-manager / ESO reviews):
// - "pending": no first list yet and no error - loading/incomplete, never
//   proven empty and never "all ready";
// - "failed": no first list and a current error - unreadable, never empty;
// - "stale": the first list completed but the watch is now failing - rows are
//   last observed, never an unqualified current claim;
// - "complete": synced with no current error (or the kind is not served:
//   definitively none). Absence of an error is not evidence of a read.
export type KindEvidence = "complete" | "stale" | "pending" | "failed";

/** kindEvidence reads one kind from the coverage list. Before the first watch
 * delivery everything is pending. An older payload without coverage falls
 * back to the aggregate: with a watch error nothing proves a first list ever
 * completed, so it is "failed" (rows, if any, still show under the banner). */
export function kindEvidence(loaded: boolean, coverage: KindCoverageDTO[] | undefined, kind: string, watchError: string | null | undefined): KindEvidence {
  if (!loaded) return "pending";
  const k = (coverage ?? []).find((x) => x.kind === kind);
  if (!k) return watchError ? "failed" : "complete";
  if (!k.served) return "complete";
  if (!k.synced) return k.error ? "failed" : "pending";
  return k.error ? "stale" : "complete";
}

/** combineEvidence: a dataset spanning several kinds is only as complete as
 * its weakest kind (failed > pending > stale > complete). */
export function combineEvidence(list: KindEvidence[]): KindEvidence {
  if (list.includes("failed")) return "failed";
  if (list.includes("pending")) return "pending";
  if (list.includes("stale")) return "stale";
  return "complete";
}

/** joinCoverage: what a cross-kind relationship may claim from a kind's
 * evidence - unknown without a first list, last-observed when stale. */
export type JoinCoverage = "complete" | "stale" | "unknown";
export const joinCoverage = (e: KindEvidence): JoinCoverage => (e === "complete" ? "complete" : e === "stale" ? "stale" : "unknown");
