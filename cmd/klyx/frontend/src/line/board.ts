import type { LineDTO, LineStationDTO } from "../store/line";
import type { LineMeshLinkDTO } from "../../bindings/github.com/moomora/klyx/internal/appbridge/models.js";

// The board's model (The Line slice 5 §3): tracks × ranks from LineDTO, in
// the DTO's own order. A segment joins consecutive cells on a track and is
// lit only when every station at both ends matches R; the fallback (fewer
// than two ranks) is one row with no lighting. Mesh links the backend drew
// (same rank, adjacent tracks) sit between those tracks; every other pair is
// the cell's to list.

export const RANK_LABEL: Record<number, string> = { 0: "dev", 1: "test", 2: "stg", 3: "prd" };

export type BoardCell = { col: number; rank: number; stations: LineStationDTO[] };
export type BoardTrack = { name: string; label: string; kind: string; cells: BoardCell[]; lit: boolean[] };
export type BoardLink = LineMeshLinkDTO & { rank: number; upper: number }; // between tracks upper and upper+1
export type Board = {
  ranks: number[];
  tracks: BoardTrack[];
  links: BoardLink[];
  siding: LineStationDTO[];
  fallback: boolean;
  card: Record<string, LineMeshLinkDTO[]>;
  offFleet: LineMeshLinkDTO[];
  unknown: string[];
  meshError: string | null;
};

const matches = (c: BoardCell) => c.stations.length > 0 && c.stations.every((s) => s.state === "matches");

export function lineBoard(dto: LineDTO): Board {
  const stations = dto.stations ?? [];
  const ranks = [...new Set(stations.map((s) => s.rank))].sort((a, b) => a - b);
  const mesh = dto.mesh ?? { drawn: [], card: {}, offFleet: [] };
  const base = {
    siding: dto.siding ?? [],
    card: Object.fromEntries(Object.entries(mesh.card ?? {}).map(([k, v]) => [k, v ?? []])) as Record<string, LineMeshLinkDTO[]>,
    offFleet: mesh.offFleet ?? [],
    unknown: mesh.unknown ?? [],
    meshError: mesh.error || null,
  };
  if (dto.fallback) {
    // one row, every station in DTO order, nothing lit
    const cells = stations.length > 0 ? [{ col: 0, rank: stations[0].rank, stations }] : [];
    return { ...base, ranks, fallback: true, links: [], tracks: [{ name: "", label: "", kind: "fallback", cells, lit: [] }] };
  }
  const tracks: BoardTrack[] = (dto.tracks ?? []).map((t) => {
    const cells: BoardCell[] = [];
    ranks.forEach((rank, col) => {
      const here = stations.filter((s) => s.track === t.name && s.rank === rank);
      if (here.length > 0) cells.push({ col, rank, stations: here });
    });
    const lit = cells.slice(1).map((c, i) => matches(cells[i]) && matches(c));
    return { name: t.name, label: t.label, kind: t.kind, cells, lit };
  });
  // a drawn link joins the same rank on adjacent tracks
  const trackOf = (cluster: string) => tracks.findIndex((t) => t.cells.some((c) => c.stations.some((s) => s.cluster === cluster)));
  const rankOf = (cluster: string) => stations.find((s) => s.cluster === cluster)?.rank;
  const links: BoardLink[] = [];
  for (const l of mesh.drawn ?? []) {
    const ta = trackOf(l.a), tb = trackOf(l.b), ra = rankOf(l.a);
    if (ta < 0 || tb < 0 || ra === undefined || Math.abs(ta - tb) !== 1 || rankOf(l.b) !== ra) continue;
    links.push({ ...l, rank: ra, upper: Math.min(ta, tb) });
  }
  return { ...base, ranks, fallback: false, tracks, links };
}
