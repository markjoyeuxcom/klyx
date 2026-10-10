// The frontend half of the shared health model
// (docs/superpowers/specs/2026-10-10-klyx-redesign-health-model.md).
//
// The backend classifies every pod, workload and node (internal/health) and
// sends one `health` verdict per row. This module only PRESENTS it: tone,
// lamp, order and counts. No view derives health from legacy fields, and
// anything missing or unrecognised fails closed to "unknown" - never "ok".
import type { Tone } from "../chrome/primitives/styles";

export type VerdictKind = "fault" | "degraded" | "unknown" | "progressing" | "paused" | "ok";

/** Triage order, worst first (matches the Go Kind declaration order). */
export const VERDICT_ORDER: readonly VerdictKind[] = ["fault", "degraded", "unknown", "progressing", "paused", "ok"];

const KNOWN = new Set<string>(VERDICT_ORDER);

/** The verdict as the bridge delivers it (HealthDTO). */
export type HealthLike = {
  kind?: string;
  label?: string;
  reason?: string;
  message?: string;
  native?: string;
  note?: string;
  rule?: string;
  rank?: number;
};

/** kindOf reads a row's verdict kind; missing or unrecognised is "unknown". */
export function kindOf(h: HealthLike | null | undefined): VerdictKind {
  const k = h?.kind ?? "";
  return KNOWN.has(k) ? (k as VerdictKind) : "unknown";
}

/** labelOf is the headline word; a missing label never invents health. */
export function labelOf(h: HealthLike | null | undefined): string {
  // Validate the kind BEFORE trusting the label: a missing or unsupported
  // kind must never display a healthy-sounding label ("Available").
  if (!KNOWN.has(h?.kind ?? "")) return "Unknown";
  const l = h?.label?.trim();
  return l || kindOf(h);
}

export function isAttention(k: VerdictKind): boolean {
  return k === "fault" || k === "degraded";
}

export function orderOf(k: VerdictKind): number {
  return VERDICT_ORDER.indexOf(k);
}

/** Tone used for text, chips and lamps. Only fault and degraded are lit. */
export const verdictTone: Record<VerdictKind, Tone> = {
  fault: "danger",
  degraded: "warning",
  unknown: "muted",
  progressing: "neutral",
  paused: "neutral",
  ok: "success",
};

export type VerdictCounts = {
  total: number;
  fault: number;
  degraded: number;
  unknown: number;
  progressing: number;
  paused: number;
  ok: number;
  /** fault + degraded */
  attention: number;
};

/** summarize counts rows by verdict kind. Every count names its verdict set. */
export function summarize<T>(rows: readonly T[], health: (row: T) => HealthLike | null | undefined): VerdictCounts {
  const c: VerdictCounts = { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0, attention: 0 };
  for (const r of rows) {
    const k = kindOf(health(r));
    c[k]++;
    c.total++;
  }
  c.attention = c.fault + c.degraded;
  return c;
}

/** attentionTone is the tone for an attention count: zero is neutral. */
export function attentionTone(c: Pick<VerdictCounts, "fault" | "degraded">): Tone | undefined {
  if (c.fault > 0) return "danger";
  if (c.degraded > 0) return "warning";
  return undefined;
}

/** Filters a verdict list view offers. */
export type VerdictFilter = "all" | "attention" | "unknown";

export function matchesFilter(k: VerdictKind, f: VerdictFilter): boolean {
  if (f === "attention") return isAttention(k);
  if (f === "unknown") return k === "unknown";
  return true;
}

/**
 * Coverage is how much of a scope an observation actually saw. It is kept
 * apart from verdict counts: summarize() classifies the rows we HAVE; this
 * says whether those rows are the whole scope.
 *   complete    - read in full for exactly this cluster+scope
 *   partial     - read, but truncated: counts are a lower bound
 *   stale       - the latest read failed; rows are the last good read
 *   unavailable - no rows for this cluster+scope (loading, switching, failed)
 */
export type Coverage = "complete" | "partial" | "stale" | "unavailable";

export type ObservedSlice = {
  rowsScope?: string | null;
  error?: string | null;
  truncated?: boolean;
};

export function coverageOf(slice: ObservedSlice, cluster: string, scope: string): Coverage {
  if (!cluster || slice.rowsScope !== `${cluster}|${scope}`) return "unavailable";
  if (slice.error) return "stale";
  if (slice.truncated) return "partial";
  return "complete";
}

/** A count is shown as a plain number only on complete coverage. */
export function countIsExact(c: Coverage): boolean {
  return c === "complete";
}

/** Workload attention on a fleet-board entry; null when unreadable. */
export function boardAttention(entry: { workloads: { counts: VerdictCounts } | null } | undefined): number | null {
  return entry?.workloads ? entry.workloads.counts.attention : null;
}

/**
 * VerdictObservation is a set of verdict counts together with how complete
 * the read behind them was. Every count surface (tape, Overview, fleet
 * board) carries this pair, never a bare number, so a capped or partly
 * unclassified read can never be presented as an exact "quiet".
 */
export type VerdictObservation = {
  counts: VerdictCounts;
  coverage: "complete" | "partial";
  // asOf is when this observation was RECEIVED (ms since epoch). Separate
  // surfaces read independently until the shared summary (1c); the stamp lets
  // an operator see which of two disagreeing numbers is older.
  asOf?: number;
};

export function observe<T>(rows: readonly T[], health: (row: T) => HealthLike | null | undefined, partial: boolean): VerdictObservation {
  return { counts: summarize(rows, health), coverage: partial ? "partial" : "complete", asOf: Date.now() };
}

/** asOfText renders an observation's receipt time for tooltips. */
export function asOfText(o: { asOf?: number } | null | undefined): string {
  if (!o?.asOf) return "";
  const d = new Date(o.asOf);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `as of ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/**
 * describeObservation words an observation for a count surface:
 *   attention   -> "3 need attention" (+ for a capped read), tone by worst verdict
 *   unknown     -> "2 unverified", muted, NOT quiet
 *   partial     -> "unverified (capped)", muted, NOT quiet
 *   otherwise   -> quiet
 */
export function describeObservation(o: VerdictObservation): { attention: number; text: string; tone: Tone | undefined; quiet: boolean; gap: boolean } {
  const { counts, coverage } = o;
  const plus = coverage === "partial" ? "+" : "";
  // A coverage gap is independent of attention: known attention stays lit
  // AND the gap stays visible, so neither hides the other.
  const gap = coverage === "partial" || counts.unknown > 0;
  if (counts.attention > 0) {
    const also = counts.unknown > 0 ? ` · ${counts.unknown} unverified` : "";
    return { attention: counts.attention, text: `${counts.attention}${plus} need attention${also}`, tone: attentionTone(counts), quiet: false, gap };
  }
  if (counts.unknown > 0) return { attention: 0, text: `${counts.unknown} unverified`, tone: undefined, quiet: false, gap };
  if (coverage === "partial") return { attention: 0, text: "unverified (capped)", tone: undefined, quiet: false, gap };
  return { attention: 0, text: "quiet", tone: undefined, quiet: true, gap };
}
