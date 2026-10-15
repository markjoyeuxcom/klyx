import { describe, it, expect } from "vitest";
import type { LineDTO, LineStationDTO } from "../store/line";
import { lineVerdict, stationPlace } from "./verdict";

// The Line slice 5 §2: the verdict counts proven matches over a named
// denominator, discloses a tie-break and every hold, and says "as of".
const SHA = "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const st = (cluster: string, env: string, over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster, env, track: "westeurope", rank: 0, state: "matches", e: 1, a: [], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as LineStationDTO);
const line = (over: Partial<LineDTO> = {}): LineDTO => ({
  version: 1, key: { kind: "git", repo: "github.com/acme/fleet", refKind: "branch", refName: "main", label: "github.com/acme/fleet @ branch main" },
  keyHow: "most stations", keyStations: 6, keys: [], r: SHA, rStations: 5, applied: 5, compared: 6, notCompared: 1, checking: 0,
  tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }], fallback: false,
  stations: [st("d", "dev"), st("s", "stg"), st("p", "prd"), st("a", "dev"), st("b", "stg"), st("c", "prd")], siding: [st("lab", "", { rank: -1 })], mesh: { drawn: [], card: {}, offFleet: [] },
  ...over,
} as unknown as LineDTO);

describe("lineVerdict", () => {
  it("the key line and a headline of proven matches over the compared stations", () => {
    const v = lineVerdict(line());
    expect(v.key).toBe("Comparing github.com/acme/fleet @ branch main · on 6 of 7 stations · chosen by most stations");
    expect(v.headline).toBe("a1b9e3c is applied on 5 of 6 compared stations · 1 not compared");
    expect(v.clauses).toEqual([]);
    expect(v.asOf).toBeNull();
  });

  it("how the key was chosen is said as such; a chart-name key keeps the backend's disclosure", () => {
    expect(lineVerdict(line({ keyHow: "override" })).key).toMatch(/set in fleet\.yaml$/);
    expect(lineVerdict(line({ keyHow: "selector" })).key).toMatch(/chosen in this session$/);
    const chart = lineVerdict(line({ key: { kind: "chart-name", repo: "", refName: "web", label: "Flux chart web - by recorded name; repository not recorded" } as never }));
    expect(chart.key).toContain("Flux chart web - by recorded name; repository not recorded");
  });

  it("a broken tie is disclosed in its own terms", () => {
    expect(lineVerdict(line({ rTieBroken: true, rTieBy: "fetch time" })).clauses).toEqual(["tie broken by fetch time, not commit order"]);
    expect(lineVerdict(line({ rTieBroken: true, rTieBy: "lexical version string" })).clauses).toEqual(["tie broken by the version string's lexical order, not version recency"]);
  });

  it("each held station is its own clause, with its proof wording", () => {
    const held = st("p", "prd", { held: true, heldWhy: `attempted ${SHA.slice(0, 7)}, failing`, heldSince: { basis: "unknown", firstSeen: new Date(2026, 9, 15, 20, 21).toISOString() } as never });
    const v = lineVerdict(line({ stations: [st("d", "dev"), held, st("q", "prd", { held: true, heldWhy: "attempted a1b9e3c, failing", track: "northeurope" })],
      tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }, { name: "northeurope", kind: "region", label: "northeurope" }] as never }));
    expect(v.clauses).toEqual([
      "prd · West Europe is held: attempted a1b9e3c, failing - first observed 20:21; start unknown",
      "prd · North Europe is held: attempted a1b9e3c, failing",
    ]);
  });

  it("no R, no key and no stations each say so", () => {
    expect(lineVerdict(line({ r: undefined })).headline).toBe("No comparison revision: no station has a single applied revision");
    expect(lineVerdict(line({ key: null, r: undefined })).headline).toBe("Nothing is compared: no source is shared by two stations");
    expect(lineVerdict(line({ key: null, r: undefined })).key).toBeNull();
    expect(lineVerdict(line({ key: null, r: undefined, stations: [], siding: [] })).headline).toBe("No stations on the line yet");
  });

  it("checking stations are named; any read not current adds 'as of' the oldest read", () => {
    const v = lineVerdict(line({ checking: 2, stations: [st("d", "dev", { read: "stale", readAt: new Date(2026, 9, 15, 9, 41).toISOString() } as never), st("p", "prd", { readAt: new Date(2026, 9, 15, 9, 45).toISOString() } as never)] }));
    expect(v.headline).toBe("a1b9e3c is applied on 5 of 6 compared stations · 1 not compared · 2 checking");
    expect(v.asOf).toBe("as of 09:41");
    expect(lineVerdict(line({ stations: [st("d", "dev", { read: "checking" })] })).asOf).toBe("not read yet");
  });

  it("a station's place: env and track, or env alone on the single Fleet track", () => {
    expect(stationPlace(st("p", "prd"), line())).toBe("prd · West Europe");
    expect(stationPlace(st("p", "prd", { track: "fleet" }), { tracks: [{ name: "fleet", kind: "fleet", label: "Fleet" }] } as never)).toBe("prd");
  });
});
