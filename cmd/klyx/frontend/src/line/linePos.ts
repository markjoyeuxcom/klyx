import type { LineDTO } from "../store/line";
import { RANK_LABEL } from "./board";

// LinePos's model (The Line slice 8a, parent §5.4): one square per rank
// present on the station's track, the station's own filled (in danger when
// the cluster has a fault); a siding station is one hollow square and
// "siding". The glyph always pairs with the cluster's short code, and its
// accessible name is the full cluster name with its position. The fault is
// the fleet indicator's own (its "fault" axis), never the line's GitOps
// comparison flag: LineDTO places the cluster, nothing more.
export type LinePosModel = {
  cluster: string;
  code: string;
  siding: boolean;
  squares: { rank: number; here: boolean }[];
  fault: boolean;
  name: string; // "aks-prd-neu, prd on North Europe"
};

// linePosOf places the cluster on the line; null when the line does not
// place it (no DTO yet, or not a station).
export function linePosOf(dto: LineDTO | null, cluster: string, codes: Record<string, string>, fault: boolean): LinePosModel | null {
  if (!dto) return null;
  const code = codes[cluster] ?? cluster.slice(0, 2);
  const side = (dto.siding ?? []).find((s) => s.cluster === cluster);
  if (side) return { cluster, code, siding: true, squares: [], fault, name: `${cluster}, siding` };
  const me = (dto.stations ?? []).find((s) => s.cluster === cluster);
  if (!me) return null;
  const ranks = [...new Set(dto.stations.filter((s) => s.track === me.track && s.rank >= 0).map((s) => s.rank))].sort((a, b) => a - b);
  const track = (dto.tracks ?? []).find((t) => t.name === me.track)?.label || me.track;
  const position = me.env || RANK_LABEL[me.rank] || `rank ${me.rank}`;
  return {
    cluster,
    code,
    siding: false,
    squares: ranks.map((rank) => ({ rank, here: rank === me.rank })),
    fault,
    name: `${cluster}, ${position} on ${track}`,
  };
}
