import type { LineDTO } from "../store/line";
import { short } from "./stationStrip";

// The Ledger without Git (The Line slice 5 §5): "revisions on the line now",
// for the chosen key only - each distinct single applied revision, R first,
// then by station count; a row of dots per track for the stations that run
// it. A revision is dated ("fetched 20:31") only when some unit currently
// compares exactly that revision (comparedAt, §5.1): an applied revision the
// sources have moved past carries no time.

export type LedgerStop = { cluster: string; env: string; holds: boolean };
export type LedgerRevision = { id: string; short: string; isR: boolean; stations: number; fetchedAt: string | null; tracks: { label: string; stops: LedgerStop[] }[] };

export function lineLedger(dto: LineDTO): LedgerRevision[] {
  if (!dto.key) return [];
  const stations = dto.stations ?? [];
  const ids = [...new Set(stations.map((s) => s.single).filter((x): x is string => !!x))];
  const count = (id: string) => stations.filter((s) => s.single === id).length;
  const fetchedAt = (id: string): string | null => {
    let best: string | null = null;
    for (const s of stations) for (const u of s.units ?? []) {
      const at = (u as { comparedAt?: unknown }).comparedAt;
      if (u.compared === id && typeof at === "string" && (!best || at > best)) best = at;
    }
    return best;
  };
  return ids
    .sort((a, b) => (a === dto.r ? -1 : b === dto.r ? 1 : 0) || count(b) - count(a) || a.localeCompare(b))
    .map((id) => ({
      id, short: short(id), isR: id === dto.r, stations: count(id), fetchedAt: fetchedAt(id),
      tracks: (dto.tracks ?? []).map((t) => ({
        label: t.label,
        stops: stations.filter((s) => s.track === t.name).sort((a, b) => a.rank - b.rank).map((s) => ({ cluster: s.cluster, env: s.env, holds: s.single === id })),
      })).filter((t) => t.stops.length > 0),
    }));
}
