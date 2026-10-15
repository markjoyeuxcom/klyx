import type { LineStationDTO } from "../store/line";
import type { LineGitDTO } from "../store/lineGit";
import { gitStation, orderedText } from "./history";
import { short } from "./stationStrip";
import { proofText } from "./proof";

// A station's words, shared by the fleet board's cell and the station page
// (The Line slice 11c): what it runs and when it was held or failing.

// comparisonText: what the station runs on the chosen key, in §3.3's words,
// or where it stands against T when the line is ordered (slice 6d).
export function comparisonText(s: LineStationDTO, git: LineGitDTO | null): string {
  const g = gitStation(git, s.cluster);
  if (g && git) return orderedText(g, git);
  if (s.state === "matches" && s.single) return short(s.single);
  if (s.state === "differs" && s.single) return `${short(s.single)} · differs`;
  return s.state;
}

// timeText puts a proof's words after what it dates: "held since 20:21",
// "failing · first observed 20:19; start unknown".
export function timeText(what: string, proof: Parameters<typeof proofText>[0]): string | null {
  const t = proofText(proof);
  if (!t) return null;
  return /^(since|between|by) /.test(t) ? `${what} ${t}` : `${what} · ${t}`;
}

// stationTimes: the held and failing times a station carries.
export function stationTimes(s: LineStationDTO): string[] {
  return [s.held ? timeText("held", s.heldSince ?? null) : null, s.flags?.failing ? timeText("failing", s.failingSince ?? null) : null].filter((x): x is string => !!x);
}
