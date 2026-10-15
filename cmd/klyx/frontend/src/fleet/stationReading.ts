import type { ClusterDTO } from "../store/fleet";
import { mastReading, type ClusterAttention } from "../nav/clusterAttention";
import { connectionText, healthGaps, healthReading, unreachable } from "../nav/fleetAttention";

// A station cell's reading of its cluster (The Line slice 5 §3.2): the
// retired ClusterCard's honest words - areas, never objects; never
// "healthy" on missing or partial evidence; the connection named apart.
export type StationReading = { text: string; tone: "danger" | "warning" | undefined; unreachable: boolean };

const areas = (n: number) => `${n} area${n === 1 ? "" : "s"} need${n === 1 ? "s" : ""} attention`;

export function stationReading(c: ClusterDTO | undefined, m: ClusterAttention | undefined): StationReading {
  if (!c) return { text: "not in the fleet", tone: undefined, unreachable: false };
  if (unreachable(c)) return { text: connectionText(c), tone: undefined, unreachable: true };
  const conn = c.state === "Stale" ? " · watches stale" : c.state === "Degraded" ? " · connected with notes" : c.state === "Synced" ? "" : ` · ${c.state.toLowerCase()}`;
  if (!m) return { text: `health checking…${conn}`, tone: undefined, unreachable: false };
  if (m.ended) return { text: `${healthReading(m)[0]}${conn}`, tone: undefined, unreachable: false };
  const r = mastReading(m);
  if (r.state === "attention") return { text: `${areas(r.areas)}${r.gaps > 0 ? ` · ${r.gaps} unverified` : ""}${conn}`, tone: r.tone, unreachable: false };
  if (r.state === "checking") return { text: `health checking…${conn}`, tone: undefined, unreachable: false };
  if (healthGaps(m)) return { text: `health partly unverified${conn}`, tone: undefined, unreachable: false };
  return { text: c.state === "Synced" ? "nothing raised" : `nothing raised${conn}`, tone: undefined, unreachable: false };
}
