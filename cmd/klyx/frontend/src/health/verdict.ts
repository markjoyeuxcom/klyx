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
  const l = h?.label?.trim();
  if (l) return l;
  return kindOf(h) === "unknown" ? "Unknown" : kindOf(h);
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
