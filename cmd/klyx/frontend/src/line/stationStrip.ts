import type { AspectKind } from "../chrome/primitives/Aspect";
import type { LineDTO, LineStationDTO } from "../store/line";

// The station strip (The Line slice 4 §5.2): the open cluster's own track
// from the LineDTO - its stations in DTO order (rank, then fleet order),
// this one boxed with its state word in the tool-neutral §3.3 wording. A
// segment is lit only when the stations at both ends match R. The strip
// draws no behind, ahead or held order: that is Git's (slice 6).

export type StripStation = {
  cluster: string;
  label: string;
  mark: AspectKind;
  here: boolean;
  state: string; // the station's state word ("matches", "differs", ...)
  box: string | null; // this station's boxed words ("differs at 9f2c11d"); null for the others
  text: string; // its accessible phrase
};
export type Strip =
  | { kind: "track"; stations: StripStation[]; lit: boolean[]; text: string }
  | { kind: "siding"; text: string };

// short names a revision: a git SHA by its first seven characters, any
// other revision (a chart version) as it is.
export const short = (rev: string) => (/^[0-9a-f]{40}$/.test(rev) ? rev.slice(0, 7) : rev);

// markOf: failing, then held, then checking, then syncing, else none.
export function markOf(s: LineStationDTO): AspectKind {
  if (s.flags?.failing) return "fault";
  if (s.held) return "attention";
  if (s.state === "checking") return "checking";
  if (s.flags?.syncing) return "syncing";
  return "none";
}

// phrase: the station as the strip says it; this station's read still
// checking says "checking…", as its box does.
function phrase(s: LineStationDTO, label: string, here: boolean, protection: string | undefined): string {
  const state = here && s.read === "checking" ? "checking…" : s.state === "differs" && s.single ? `differs at ${short(s.single)}` : s.state;
  const flags = [s.flags?.failing ? "failing" : "", s.held ? "held" : "", s.flags?.syncing ? "syncing" : ""].filter(Boolean);
  return here ? [label, "this station", ...(protection ? [protection] : []), state, ...flags].join(", ") : [`${label} ${state}`, ...flags].join(", ");
}

// protection: the open cluster's "protected" word, said with this station
// (the visible tag is the station crumb's).
export function stationStrip(dto: LineDTO | null, cluster: string, protection?: string): Strip | null {
  if (!dto) return null;
  if ((dto.siding ?? []).some((s) => s.cluster === cluster)) return { kind: "siding", text: "siding" };
  const me = (dto.stations ?? []).find((s) => s.cluster === cluster);
  if (!me) return null;
  const track = dto.stations.filter((s) => s.track === me.track);
  const envs = track.map((s) => s.env);
  const stations = track.map((s): StripStation => {
    const label = s.env && envs.filter((e) => e === s.env).length === 1 ? s.env : s.cluster;
    const here = s.cluster === cluster;
    return { cluster: s.cluster, label, mark: markOf(s), here, state: s.state, box: here ? (s.read === "checking" ? "checking…" : s.state === "differs" && s.single ? `differs at ${short(s.single)}` : s.state) : null, text: phrase(s, label, here, protection) };
  });
  const lit = track.slice(1).map((s, i) => track[i].state === "matches" && s.state === "matches");
  return { kind: "track", stations, lit, text: stations.map((s) => s.text).join("; ") };
}
