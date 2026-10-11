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
  if (k.coverage) return OBSERVER_EVIDENCE[k.coverage];
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

// The health Observer's five states, as lens evidence (slice 3 design §5a):
// live is complete; stale keeps its last good rows; checking has no complete
// read yet; unreadable is a failed first read; absent is definitively none.
const OBSERVER_EVIDENCE: Record<NonNullable<KindCoverageDTO["coverage"]>, KindEvidence> = {
  live: "complete", stale: "stale", checking: "pending", unreadable: "failed", absent: "complete",
};

type HealthKind = { kind: string; coverage: NonNullable<KindCoverageDTO["coverage"]>; class?: string; error?: string; asOf?: string; lastGoodAt?: string; discovery?: string };

/** coverageFromHealth maps an Observer part's per-kind coverage onto the
 * lens's KindCoverageDTO, renaming kinds by `names` (Observer kind -> lens
 * kind). Counted kinds only; nothing is reduced: the five-state coverage,
 * class, last good time and discovery gap travel along. */
export function coverageFromHealth(kinds: HealthKind[] | undefined, names: Record<string, string>): KindCoverageDTO[] {
  const out: KindCoverageDTO[] = [];
  for (const k of kinds ?? []) {
    const kind = names[k.kind];
    if (!kind) continue;
    const served = k.coverage !== "absent";
    const synced = k.coverage === "live" || k.coverage === "stale";
    const error = k.error ? (k.class ? `${k.class}: ${k.error}` : k.error) : undefined;
    out.push({ kind, served, synced, error, coverage: k.coverage, class: k.class, asOf: k.asOf, lastGoodAt: k.lastGoodAt, discovery: k.discovery });
  }
  return out;
}

/** coverageError joins the failing kinds' errors (the lens's aggregate
 * watch error), or null. */
export function coverageError(coverage: KindCoverageDTO[]): string | null {
  const errs = coverage.filter((k) => k.error && k.coverage !== "live").map((k) => `${k.kind}: ${k.error}`);
  return errs.length ? errs.join("; ") : null;
}

/** coverageGaps names failing discovery re-probes: a capability gap beside
 * live data, never a reason to treat that data as stale (spec §3.3). */
export function coverageGaps(coverage: KindCoverageDTO[]): string | null {
  const gaps = coverage.filter((k) => k.discovery).map((k) => `${k.kind}: ${k.discovery}`);
  return gaps.length ? gaps.join("; ") : null;
}

/** lastGoodAt is the oldest last-good time among the given kinds' stale
 * evidence (the conservative "as of"), or null. */
export function lastGoodAt(coverage: KindCoverageDTO[] | undefined, kinds: string[]): string | null {
  let at: string | null = null;
  for (const k of coverage ?? []) {
    if (kinds.includes(k.kind) && k.coverage === "stale" && k.lastGoodAt && (at === null || k.lastGoodAt < at)) at = k.lastGoodAt;
  }
  return at;
}

/** clock formats an ISO time as the local wall clock (HH:MM:SS). */
export const clock = (iso: string) => new Date(iso).toLocaleTimeString();
