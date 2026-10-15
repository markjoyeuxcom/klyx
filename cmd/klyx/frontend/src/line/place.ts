// placeName reads a cloud region id as a place (The Line slice 13a.4):
// Azure's run-together ids become their display names ("westeurope" →
// "West Europe", "eastus2" → "East US 2"). An id that does not split wholly
// into known words - AWS ("eu-west-1"), GCP ("europe-west4"), anything
// unknown - is shown exactly as given: never guessed at.

const WORDS: Record<string, string> = {
  north: "North", south: "South", east: "East", west: "West", central: "Central",
  northeast: "Northeast", northwest: "Northwest", southeast: "Southeast", southwest: "Southwest",
  europe: "Europe", us: "US", uk: "UK", uae: "UAE", asia: "Asia", india: "India", canada: "Canada", brazil: "Brazil",
  japan: "Japan", korea: "Korea", australia: "Australia", france: "France", germany: "Germany", sweden: "Sweden",
  norway: "Norway", switzerland: "Switzerland", poland: "Poland", italy: "Italy", spain: "Spain", africa: "Africa",
  qatar: "Qatar", israel: "Israel", mexico: "Mexico", newzealand: "New Zealand", chile: "Chile", indonesia: "Indonesia",
  malaysia: "Malaysia", taiwan: "Taiwan", austria: "Austria", belgium: "Belgium", denmark: "Denmark", finland: "Finland",
  greece: "Greece", saudiarabia: "Saudi Arabia",
};
const LONGEST = Math.max(...Object.keys(WORDS).map((w) => w.length));

export function placeName(region: string): string {
  const id = region.trim();
  const m = /^([a-z]+)(\d*)$/.exec(id);
  if (!m) return id;
  const [, letters, digits] = m;
  const out: string[] = [];
  let i = 0;
  while (i < letters.length) {
    let hit = "";
    for (let n = Math.min(LONGEST, letters.length - i); n > 0; n--) {
      const w = letters.slice(i, i + n);
      if (WORDS[w]) { hit = w; break; }
    }
    if (!hit) return id; // not wholly known words: as given
    out.push(WORDS[hit]);
    i += hit.length;
  }
  // a single word ("europe", "us") is a geography, not an Azure region id
  if (out.length < 2) return id;
  return digits ? `${out.join(" ")} ${digits}` : out.join(" ");
}

// trackName is a line track's label as the board shows it: a region track
// reads as its place, any other track as given.
export function trackName(t: { kind: string; label: string }): string {
  return t.kind === "region" ? placeName(t.label) : t.label;
}
