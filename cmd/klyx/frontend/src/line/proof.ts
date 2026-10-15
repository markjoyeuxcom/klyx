import type { LineProofDTO } from "../../bindings/github.com/moomora/klyx/internal/appbridge/models.js";

// proofText words how long a station has been failing or held (The Line
// slice 5 §3.3, after parent §8.2), from LineService's proof:
//   rule      a proven start: "since 20:21"
//   observed  began in (notBefore, since] under continuous watch coverage
//   sampled   began in (notBefore, since] between two reads
//   derived   a time-only change: start unknown
//   unknown   first seen at a baseline, after a gap or a replacement; a
//             capped proof (not kept) carries no time at all
// Nothing here reads a first sighting, or the log's own start, as an age.
const hhmm = (iso: string | null | undefined): string => {
  if (!iso) return "";
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}`;
};

type Proof = Pick<LineProofDTO, "basis" | "afterGap" | "capped"> & { since?: unknown; notBefore?: unknown; firstSeen?: unknown };
const iso = (t: unknown) => (typeof t === "string" ? t : t instanceof Date ? t.toISOString() : null);

export function proofText(p: Proof | null | undefined): string | null {
  if (!p) return null;
  const since = iso(p.since), notBefore = iso(p.notBefore), firstSeen = iso(p.firstSeen);
  const window = (how: string) => (since ? (notBefore ? `between ${hhmm(notBefore)} and ${hhmm(since)} (${how})` : `by ${hhmm(since)} (${how})`) : "start unknown");
  switch (p.basis) {
    case "rule": return since ? `since ${hhmm(since)}` : "start unknown";
    case "observed": return window("observed");
    case "sampled": return window("between two reads");
    case "unknown":
      if (p.capped) return "start unknown (proof capped)";
      if (firstSeen) return p.afterGap ? `observed ${hhmm(firstSeen)}, after a gap; start unknown` : `first observed ${hhmm(firstSeen)}; start unknown`;
      return "start unknown";
    default: return "start unknown"; // derived, or a basis this build does not know
  }
}
