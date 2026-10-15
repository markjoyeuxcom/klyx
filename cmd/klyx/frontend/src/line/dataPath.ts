import type { AppliedValue, RouteUnit, RulePoint } from "../bridge/ruleMetrics";

// The data path's pure model (The Line slice 8d, parent §5.2): onset as a
// typed result, revision proximity only beside a witnessed crossing, and the
// wording of applied values. Never a cause.

export const ONSET_THRESHOLD = 0.01; // 1%
const PROXIMITY_WINDOW_S = 10 * 60;

export type Onset =
  | { kind: "crossing"; at: number } // a sample at or below 1%, then one above it, in one uninterrupted segment
  | { kind: "above-at-first"; at: number; afterGap: boolean } // a segment's first sample is already above 1%: no crossing time
  | { kind: "none" } // no sample above 1%
  | { kind: "no-samples" }; // nothing attributable

// onset reads the 5xx share's attributed points in segments split at gaps
// over 1.5 steps. A gap restarts the search: the result is that of the
// latest segment with any sample above the threshold.
export function onset(points: RulePoint[], stepSeconds: number, threshold = ONSET_THRESHOLD): Onset {
  if (points.length === 0) return { kind: "no-samples" };
  const segments: RulePoint[][] = [];
  let cur: RulePoint[] = [points[0]];
  for (let i = 1; i < points.length; i++) {
    if (points[i].t - points[i - 1].t > 1.5 * stepSeconds) {
      segments.push(cur);
      cur = [];
    }
    cur.push(points[i]);
  }
  segments.push(cur);
  let result: Onset = { kind: "none" };
  segments.forEach((seg, i) => {
    if (!seg.some((p) => p.v > threshold)) return;
    if (seg[0].v > threshold) {
      result = { kind: "above-at-first", at: seg[0].t, afterGap: i > 0 };
      return;
    }
    for (let j = 1; j < seg.length; j++) {
      if (seg[j - 1].v <= threshold && seg[j].v > threshold) {
        result = { kind: "crossing", at: seg[j].t };
        return;
      }
    }
  });
  return result;
}

const hhmm = (unix: number) => new Date(unix * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });

export function onsetText(o: Onset, rateSeconds: number, stepSeconds: number): string {
  const how = `${rateSeconds / 60}-minute rate, ${stepSeconds}-second step`;
  switch (o.kind) {
    case "crossing":
      return `first sampled crossing of 1% at ${hhmm(o.at)} (${how})`;
    case "above-at-first":
      return `already above 1% at ${hhmm(o.at)}, the first sample${o.afterGap ? " after a gap" : ""} (${how})`;
    case "none":
      return "no sample above 1% in the attributed window";
    case "no-samples":
      return "no samples";
  }
}

// appliedText words a typed applied value: a commit's short id (git), an
// artifact's digest (never read as a commit), or a chart's version.
export function appliedText(a: AppliedValue): string {
  switch (a.kind) {
    case "git":
      return a.value.slice(0, 7);
    case "artifact": {
      const m = /sha256:([0-9a-f]+)/.exec(a.value);
      return `artifact ${m ? `sha256:${m[1].slice(0, 8)}` : a.value}`;
    }
    case "chart": {
      const at = a.value.lastIndexOf("@");
      return at > 0 ? `chart ${a.value.slice(0, at)} ${a.value.slice(at + 1)}` : `chart ${a.value}`;
    }
  }
  return a.value;
}

// proximityText places the unit's applied change beside the onset, only
// when: the onset is a witnessed crossing; the change was observed; it is at
// or before the crossing and within the 10 minutes before it; and the
// applied history was live continuously from the change through now (so
// through the crossing). It names the unit, and claims only that the unit
// manages the route now.
export function proximityText(unit: RouteUnit | undefined, o: Onset): string | null {
  const a = unit?.applied;
  if (!unit || !a || o.kind !== "crossing" || a.basis !== "observed" || !a.observedAt || !a.liveSince) return null;
  const changed = Date.parse(a.observedAt) / 1000;
  const live = Date.parse(a.liveSince) / 1000;
  if (!(changed <= o.at) || o.at - changed > PROXIMITY_WINDOW_S || live > changed) return null;
  return `${unit.kind} ${unit.namespace}/${unit.name} (manages this route now): applied ${appliedText(a)}, change observed ${hhmm(changed)}; first sampled crossing ${hhmm(o.at)}`;
}

// weightShares: each backend's share of its rule's configured weight
// (weights split traffic within one rule; an unset weight counts as 1).
export function weightShares(weights: number[]): number[] {
  const total = weights.reduce((a, b) => a + b, 0);
  return weights.map((w) => (total > 0 ? w / total : 0));
}

export const latest = (pts: RulePoint[]) => (pts.length > 0 ? pts[pts.length - 1].v : undefined);

export function fmtRate(v: number | undefined): string {
  if (v === undefined) return "no samples";
  return v >= 100 ? `${v.toFixed(0)}/s` : v >= 1 ? `${v.toFixed(1)}/s` : `${v.toFixed(2)}/s`;
}

export function fmtShare(v: number | undefined): string {
  if (v === undefined) return "no samples";
  const pct = v * 100;
  return pct === 0 ? "0%" : pct < 0.1 ? "<0.1%" : `${pct < 10 ? pct.toFixed(1) : pct.toFixed(0)}%`;
}
