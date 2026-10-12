import { useMemo } from "react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { useHealth } from "../store/health";
import type { Tone } from "../chrome/primitives/styles";
import { clusterAttention, lineText, mastReading, provenance, type ClusterAttention } from "./clusterAttention";

// The fleet on summaries (health 1c slice 5): the slice 4 attention model
// applied per cluster - the open cluster's includes its lens rows - for the
// spine lamps, the board's meters and its cards. Two axes, kept honest:
// the connection (the fleet DTO's state) and the health reading (the model).

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
export const unreachable = (c: ClusterDTO) => c.state === "Failed" || c.state === "Unconnected";
export const connectionText = (c: ClusterDTO) => `${c.state.toLowerCase()}${c.reason ? `: ${c.reason}` : ""}`;

/** healthGaps: does the reading leave anything unestablished? */
export function healthGaps(m: ClusterAttention): boolean {
  if (m.ended) return true;
  const r = mastReading(m);
  return r.state === "checking" || r.gaps > 0 || r.notCovered > 0;
}

/** healthReading: the model in words, one line each - what needs attention
 * (each line with its own provenance), what could not be established, what
 * the summary does not cover, or the observation's end. */
export function healthReading(m: ClusterAttention): string[] {
  if (m.ended) return [`observation ended at ${clock(m.ended)}`];
  const r = mastReading(m);
  if (r.state === "checking") return ["health checking…"];
  const out: string[] = [];
  for (const a of m.areas) {
    if (a.controller?.state === "not ready") out.push(`${a.label}: controller not ready${a.controller.reason ? ` - ${a.controller.reason}` : ""}`);
    for (const l of a.lines) {
      if (l.state === "attention") out.push(`${a.label}${a.lines.length > 1 ? ` ${l.label}` : ""}: ${lineText(l)} (${provenance(a, l)})`);
    }
  }
  for (const a of m.areas) {
    if (a.state === "attention" || !a.gap) continue;
    const gaps = [
      ...(a.controller?.state === "unknown" ? ["controller health unknown"] : []),
      ...a.lines.filter((l) => l.state === "checking" || l.state === "unreadable" || l.gaps.length > 0)
        .map((l) => `${a.lines.length > 1 ? `${l.label} ` : ""}${l.state === "checking" || l.state === "unreadable" ? l.text : l.gaps.join(", ")}`),
    ];
    out.push(`${a.label}: ${gaps.join("; ")}`);
  }
  if (m.notCovered.length > 0) out.push(`not covered by the summary: ${m.notCovered.map((u) => (u.capability === "present" ? u.label : `${u.label} (capability not yet known)`)).join(", ")}`);
  if (r.state === "quiet" && !healthGaps(m)) out.push(`no attention in ${r.read} area${r.read === 1 ? "" : "s"} read`);
  return out;
}

export type LampKind = "unreachable" | "danger" | "warning" | "connection" | "connecting" | "unverified" | "healthy";
export type Lamp = { kind: LampKind; tone: Tone; blink: boolean; title: string };

// spineLamp (slice 5 §2): the FIRST matching rule sets the lamp; the title
// always keeps both axes.
export function spineLamp(c: ClusterDTO, m: ClusterAttention): Lamp {
  const r = mastReading(m);
  const title = [`${c.name} — ${connectionText(c)}`, ...healthReading(m)].join("\n");
  const lamp = (kind: LampKind, tone: Tone, blink = false): Lamp => ({ kind, tone, blink, title });
  if (unreachable(c)) return lamp("unreachable", "muted");
  if (r.state === "attention" && r.tone === "danger") return lamp("danger", "danger", true);
  if (r.state === "attention") return lamp("warning", "warning");
  if (c.state === "Stale" || c.state === "Degraded") return lamp("connection", "warning");
  if (c.state !== "Synced") return lamp("connecting", "muted"); // a transition never implies health
  if (healthGaps(m)) return lamp("unverified", "muted");
  return lamp("healthy", "success"); // Signal Bench's dim, unlit healthy lamp
}

export type FleetMeters = {
  attention: number; // clusters with an area needing attention
  attentionDanger: boolean; // any of them with a danger area
  unknown: number; // connected clusters whose reading leaves something unestablished
};

// fleetMeters (slice 5 §3): independent facts, counted as clusters - never
// objects, never areas summed across clusters.
export function fleetMeters(clusters: ClusterDTO[], models: Record<string, ClusterAttention>): FleetMeters {
  let attention = 0;
  let attentionDanger = false;
  let unknown = 0;
  for (const c of clusters) {
    const m = models[c.name];
    if (!m) continue;
    const r = mastReading(m);
    if (r.state === "attention") {
      attention++;
      if (r.tone === "danger") attentionDanger = true;
    }
    if ((c.state === "Synced" || c.state === "Degraded") && healthGaps(m)) unknown++;
  }
  return { attention, attentionDanger, unknown };
}

// useFleetAttention: every cluster's model, live from the health store.
export function useFleetAttention(): Record<string, ClusterAttention> {
  const st = useHealth((s) => s);
  const clusters = useFleet((s) => s.clusters);
  return useMemo(() => Object.fromEntries(clusters.map((c) => [c.name, clusterAttention(st, c.name, c)])), [st, clusters]);
}
