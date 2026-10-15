import type { ClusterDTO } from "../store/fleet";
import { lineText, mastReading, type Area, type ClusterAttention, type Line } from "./clusterAttention";
import { connectionText, healthGaps, unreachable } from "./fleetAttention";
import { stationOrder } from "./stationOrder";

// The fleet indicator's model (The Line slice 4 §3): clusters per axis,
// never objects. Fault and warning are exclusive (danger wins, as the spine's
// lamp did); an unreachable cluster is on no other axis, since its health
// reading is not current; otherwise the axes are independent facts, as
// fleetMeters counts them. Nothing is summed across axes.

export type FleetAxis = "fault" | "warning" | "unreachable" | "connection" | "unverified";
export const FLEET_AXES: FleetAxis[] = ["fault", "warning", "unreachable", "connection", "unverified"];

export type AxisEntry = { cluster: ClusterDTO; reason: string };
export type FleetAxes = { total: number; axes: Record<FleetAxis, AxisEntry[]> };

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const lineName = (a: Area, l: Line) => `${a.label}${a.lines.length > 1 ? ` ${l.label}` : ""}`;
const controllerReason = (a: Area) => (a.controller?.reason ? ` - ${a.controller.reason}` : "");

// litReason names the first lit line of the given tone, in area order: a
// fault's reason is a danger line, never a warning beside it.
function litReason(m: ClusterAttention, tone: "danger" | "warning"): string {
  for (const a of m.areas) {
    if (a.state !== "attention" || a.tone !== tone) continue;
    if (a.controller?.state === "not ready") return `${a.label}: controller not ready${controllerReason(a)}`;
    const l = a.lines.find((x) => x.state === "attention" && (tone === "warning" || x.tone === "danger"));
    if (l) return `${lineName(a, l)}: ${lineText(l)}`;
  }
  return "needs attention";
}

// gapReason names the first thing the reading leaves unestablished.
function gapReason(m: ClusterAttention): string {
  if (m.ended) return `observation ended at ${clock(m.ended)}`;
  if (mastReading(m).state === "checking") return "health checking…";
  for (const a of m.areas) {
    if (!a.gap) continue;
    if (a.controller?.state === "unknown") return `${a.label}: controller health unknown${controllerReason(a)}`;
    const l = a.lines.find((x) => x.state === "checking" || x.state === "unreadable" || x.gaps.length > 0);
    if (l) return `${lineName(a, l)}: ${l.state === "checking" || l.state === "unreadable" ? l.text : l.gaps[0]}`;
  }
  return "health not established";
}

const CONNECTED = new Set(["Synced", "Degraded"]);
const SETTLED = new Set(["Synced", "Stale", "Degraded", "Failed", "Unconnected"]);

// fleetAxes places every cluster, in station order, on its axes.
export function fleetAxes(clusters: ClusterDTO[], models: Record<string, ClusterAttention>): FleetAxes {
  const axes: Record<FleetAxis, AxisEntry[]> = { fault: [], warning: [], unreachable: [], connection: [], unverified: [] };
  for (const c of stationOrder(clusters)) {
    if (unreachable(c)) {
      axes.unreachable.push({ cluster: c, reason: connectionText(c) });
      continue;
    }
    const m = models[c.name];
    if (!m) {
      axes.unverified.push({ cluster: c, reason: SETTLED.has(c.state) ? "health checking…" : connectionText(c) });
      if (c.state === "Stale" || c.state === "Degraded") axes.connection.push({ cluster: c, reason: connectionText(c) });
      continue;
    }
    const r = mastReading(m);
    if (r.state === "attention" && r.tone === "danger") axes.fault.push({ cluster: c, reason: litReason(m, "danger") });
    else if (r.state === "attention") axes.warning.push({ cluster: c, reason: litReason(m, "warning") });
    if (c.state === "Stale" || c.state === "Degraded") axes.connection.push({ cluster: c, reason: connectionText(c) });
    // unverified: a connection transition, an ended observation, or a
    // connected cluster whose reading leaves something unestablished
    if (!SETTLED.has(c.state)) axes.unverified.push({ cluster: c, reason: connectionText(c) });
    else if (m.ended || (CONNECTED.has(c.state) && healthGaps(m))) axes.unverified.push({ cluster: c, reason: gapReason(m) });
  }
  return { total: clusters.length, axes };
}

const PHRASE: Record<FleetAxis, (n: number) => string> = {
  fault: (n) => `with ${n === 1 ? "a fault" : "faults"}`,
  warning: (n) => `with ${n === 1 ? "a warning" : "warnings"}`,
  unreachable: () => "unreachable",
  connection: () => "with a stale or degraded connection",
  unverified: () => "unverified",
};

const clustersWord = (n: number) => `${n} cluster${n === 1 ? "" : "s"}`;

// fleetAxesLabel is the indicator's accessible name: every non-zero axis in
// chip order, counted as clusters, then the fleet's size. All zero reads as
// the fleet's size alone - never an all-clear wording.
export function fleetAxesLabel(f: FleetAxes): string {
  const parts = FLEET_AXES.filter((a) => f.axes[a].length > 0).map((a, i) => {
    const n = f.axes[a].length;
    return `${i === 0 ? clustersWord(n) : n} ${PHRASE[a](n)}`;
  });
  return parts.length === 0 ? `Fleet: ${clustersWord(f.total)}` : `Fleet: ${parts.join(", ")}, of ${f.total}`;
}

export const AXIS_TITLE: Record<FleetAxis, string> = {
  fault: "Fault",
  warning: "Warning",
  unreachable: "Unreachable",
  connection: "Connection stale or degraded",
  unverified: "Unverified",
};
