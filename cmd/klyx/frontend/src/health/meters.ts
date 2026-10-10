import type { Meter } from "../chrome/primitives/MetersStrip";
import { attentionTone, type Coverage, type VerdictCounts } from "./verdict";

// verdictMeters is the one meter vocabulary for every migrated lens (Pods,
// Workloads, Nodes): the entity total with its scope and coverage, then
// attention split by fault/degraded, then unknown, then the neutral
// in-between states. Coverage is never dropped: an unavailable observation
// shows no numbers at all, a capped one is marked on the total and never
// reads as quiet, a failed refresh says "last good".
export function verdictMeters(opts: {
  noun: string;             // "pods"
  scope?: string;           // namespace, "" = all
  counts: VerdictCounts;
  coverage: Coverage;
  extra?: ReadonlyArray<Meter | null | false>; // lens-specific neutral states
}): Array<Meter | null | false> {
  const { noun, scope, counts, coverage } = opts;
  const scoped = scope ? `${noun} in ${scope}` : noun;
  if (coverage === "unavailable") return [{ label: scoped, value: "—" }];
  const qualifier = coverage === "partial" ? " (capped)" : coverage === "stale" ? " (last good)" : "";
  return [
    { label: `${scoped}${qualifier}`, value: coverage === "partial" ? `${counts.total}+` : counts.total },
    counts.attention > 0 && { label: `attention${qualifier}`, value: coverage === "partial" ? `${counts.attention}+` : counts.attention, tone: attentionTone(counts) },
    counts.fault > 0 && { label: "fault", value: counts.fault, tone: "danger" },
    counts.degraded > 0 && { label: "degraded", value: counts.degraded, tone: "warning" },
    counts.unknown > 0 && { label: "unknown", value: counts.unknown },
    ...(opts.extra ?? []),
  ];
}
