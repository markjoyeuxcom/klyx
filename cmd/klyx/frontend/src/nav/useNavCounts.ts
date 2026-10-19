import { useMemo } from "react";
import type { Tone } from "../chrome/primitives/styles";
import { TASKS, lineText, provenance, strongestLine, type Area, type ClusterAttention, type Line } from "./clusterAttention";
import { useAttentionModel } from "./useAttentionModel";

// A navigation count: a number with the tone of its line (danger for any
// fault, warning for degraded only). Zero renders nothing - never a green
// "0" and never a claim that an unread area is quiet. Evidence (warning
// events) carries no tone.
export type NavBadge = { n: number; tone: Tone | undefined; title: string };

export type NavCounts = {
  group: Record<string, NavBadge>;
  destination: Record<string, NavBadge>;
  // What could not be established ("controller not ready", "unreadable",
  // "last good", "unverified", "checking…"), beside the
  // count and never as zero; a destination's note also names a lit part
  // that has no destination of its own ("issuers: 1 not ready").
  groupNote: Record<string, string>;
  destinationNote: Record<string, string>;
  // attention: the destinations to open for what needs attention - a lit
  // part's own destination, else its task's (a secondary part or a
  // controller has no badge of its own; review 1/3 #3)
  attention: Set<string>;
};

const EMPTY: NavCounts = { group: {}, destination: {}, groupNote: {}, destinationNote: {}, attention: new Set() };
const WEIGHT: Record<string, number> = { "observation ended": 6, "controller not ready": 5, unreadable: 4, "last good": 3, unverified: 2, "checking…": 1 };
const stronger = (a: string | undefined, b: string) => (!a || WEIGHT[b] > WEIGHT[a] ? b : a);

const badge = (a: Area, l: Line): NavBadge => ({
  n: l.n, tone: l.state === "attention" ? l.tone : undefined,
  title: `${a.label} - ${l.label}: ${lineText(l)} - ${provenance(a, l)}`,
});

// noteOf is a line's coverage word, or "".
function noteOf(l: Line): string {
  if (l.state === "unreadable") return "unreadable";
  if (l.state === "checking") return "checking…";
  if (l.gaps.some((g) => g.includes("last good"))) return "last good";
  return l.gaps.length > 0 ? "unverified" : "";
}

// navCounts (slice 4 §2.2, §5.2): a group shows its strongest lit line,
// never a sum; a destination shows exactly its own part - or, where parts
// share one (Flux's operator view; coverage S9's Autoscalers, with the
// standalone HPAs and KEDA's ScaledObjects), the strongest of them, as a
// group does: never the last one written.
export function navCounts(m: ClusterAttention | null): NavCounts {
  if (!m) return EMPTY;
  const out: NavCounts = { group: {}, destination: {}, groupNote: {}, destinationNote: {}, attention: new Set() };
  if (m.ended) {
    for (const t of TASKS) out.groupNote[t.groupId] = "observation ended";
    return out;
  }
  const byGroup = new Map<string, Area[]>();
  for (const a of m.areas) byGroup.set(a.groupId, [...(byGroup.get(a.groupId) ?? []), a]);
  for (const [g, areas] of byGroup) {
    const lines = areas.flatMap((a) => a.lines.map((l) => ({ a, l })));
    const best = strongestLine(lines.map((x) => x.l));
    const owner = best ? lines.find((x) => x.l === best)!.a : null;
    if (best && owner) out.group[g] = badge(owner, best);
    for (const a of areas) {
      if (a.controller?.state === "not ready") out.groupNote[g] = stronger(out.groupNote[g], "controller not ready");
      if (a.controller?.state === "unknown") out.groupNote[g] = stronger(out.groupNote[g], "unverified");
      for (const l of a.lines) {
        const w = noteOf(l);
        if (w) out.groupNote[g] = stronger(out.groupNote[g], w);
      }
    }
  }
  const shown = new Map<string, { a: Area; l: Line }[]>();
  for (const a of m.areas) {
    const notes: string[] = [];
    for (const l of a.lines) {
      if (l.destinationId) {
        // the part's own count: lit, evidence, or an unlit count in the
        // tool's word (neutral, as its lens shows it; review 1/3 #2)
        if (l.state === "attention" || l.state === "evidence" || l.state === "unlit" || l.state === "alerts") shown.set(l.destinationId, [...(shown.get(l.destinationId) ?? []), { a, l }]);
        if (l.state === "attention") out.attention.add(l.destinationId);
        const w = noteOf(l);
        // "checking…" is said once, on the group - not beside every view
        if (w && w !== "checking…") out.destinationNote[l.destinationId] = stronger(out.destinationNote[l.destinationId], w);
      } else if (l.state === "attention" || l.state === "unlit" || (noteOf(l) && noteOf(l) !== "checking…")) {
        // a part without a destination of its own: a note on its task's
        // destination, never a number on that badge or a resource's
        notes.push(`${l.label.toLowerCase()}: ${l.state === "attention" || l.state === "unlit" ? l.text : noteOf(l)}`);
      }
    }
    if (a.controller?.state === "not ready") notes.unshift("controller not ready");
    if (a.controller?.state === "not ready" || a.lines.some((l) => l.state === "attention" && !l.destinationId)) out.attention.add(a.destinationId);
    if (notes.length) out.destinationNote[a.destinationId] = [out.destinationNote[a.destinationId], ...notes].filter(Boolean).join(" · ");
  }
  for (const [id, lines] of shown) {
    const best = strongestLine(lines.map((x) => x.l));
    const pick = lines.find((x) => x.l === best) ?? lines[0];
    out.destination[id] = badge(pick.a, pick.l);
  }
  return out;
}

// useNavCounts reads the open cluster's attention model: every number
// through the one count selector, so a lens header, its badge and the mast
// agree.
export function useNavCounts(cluster: string | null): NavCounts {
  const m = useAttentionModel(cluster);
  return useMemo(() => navCounts(m), [m]);
}
