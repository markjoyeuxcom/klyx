import type { LineDTO, LineStationDTO } from "../store/line";
import { proofText } from "./proof";
import { short } from "./stationStrip";

// The line's verdict sentence (The Line slice 5 §2, parent §3.3): the key it
// compares, a headline counting only proven matches over a named
// denominator, a clause per held station, and "as of" when a read is not
// current. It never claims a cause, a delivery path or that the line is live.

export type LineVerdict = { key: string | null; headline: string; clauses: string[]; asOf: string | null };

const KEY_HOW: Record<string, string> = { "most stations": "chosen by most stations", override: "set in fleet.yaml", selector: "chosen in this session" };
const TIE_BY: Record<string, string> = { "fetch time": "tie broken by fetch time, not commit order", "lexical version string": "tie broken by the version string's lexical order, not version recency" };
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

// trackLabel is a station's track as the board labels it, or null on the
// single Fleet track.
export function trackLabel(s: Pick<LineStationDTO, "track">, dto: Pick<LineDTO, "tracks">): string | null {
  const t = (dto.tracks ?? []).find((x) => x.name === s.track);
  return t && t.kind !== "fleet" ? t.label : null;
}

// stationPlace names a station as the board does: "prd · westeurope", or the
// environment alone on the single Fleet track.
export function stationPlace(s: Pick<LineStationDTO, "env" | "track">, dto: Pick<LineDTO, "tracks">): string {
  const t = trackLabel(s, dto);
  return t ? `${s.env} · ${t}` : s.env;
}

export function lineVerdict(dto: LineDTO): LineVerdict {
  const total = (dto.stations?.length ?? 0) + (dto.siding?.length ?? 0);
  const key = dto.key ? `Comparing ${dto.key.label} · on ${dto.keyStations} of ${plural(total, "station")} · ${KEY_HOW[dto.keyHow ?? ""] ?? dto.keyHow ?? ""}`.replace(/ · $/, "") : null;
  let headline: string;
  if (total === 0) headline = "No stations on the line yet";
  else if (!dto.key) headline = "Nothing is compared: no source is shared by two stations";
  else if (!dto.r) headline = "No comparison revision: no station has a single applied revision";
  else {
    const rest = [dto.notCompared > 0 ? `${dto.notCompared} not compared` : "", dto.checking > 0 ? `${dto.checking} checking` : ""].filter(Boolean);
    headline = [`${short(dto.r)} is applied on ${dto.applied} of ${plural(dto.compared, "compared station")}`, ...rest].join(" · ");
  }
  const clauses: string[] = [];
  if (dto.r && dto.rTieBroken && dto.rTieBy) clauses.push(TIE_BY[dto.rTieBy] ?? `tie broken by ${dto.rTieBy}`);
  for (const s of dto.stations ?? []) {
    if (!s.held) continue;
    const since = proofText(s.heldSince ?? null);
    clauses.push(`${stationPlace(s, dto)} is held: ${s.heldWhy || "held"}${since ? ` - ${since}` : ""}`);
  }
  // "as of" the oldest read, when any station's read is not current
  const all = [...(dto.stations ?? []), ...(dto.siding ?? [])];
  const behind = all.some((s) => s.read !== "ok");
  const times = all.map((s) => (typeof s.readAt === "string" ? s.readAt : null)).filter((t): t is string => !!t).sort();
  const asOf = behind ? (times.length > 0 ? `as of ${hhmm(times[0])}` : "not read yet") : null;
  return { key, headline, clauses, asOf };
}
