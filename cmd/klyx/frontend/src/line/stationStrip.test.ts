import { describe, it, expect } from "vitest";
import type { LineDTO, LineStationDTO } from "../store/line";
import { short, stationStrip } from "./stationStrip";

// The Line slice 4 §5.2: the open cluster's track, its marks, its box and
// lighting at both ends only.

const SHA = "9f2c11d0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const st = (cluster: string, env: string, state: string, over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster, env, state, track: "weu", rank: 0, e: 1, a: [], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as LineStationDTO);
const line = (stations: LineStationDTO[], siding: LineStationDTO[] = []): LineDTO =>
  ({ version: 1, stations, siding, tracks: [], keys: [] } as unknown as LineDTO);

describe("the station strip", () => {
  it("no DTO, or a cluster the line does not place: nothing", () => {
    expect(stationStrip(null, "a")).toBeNull();
    expect(stationStrip(line([st("a", "dev", "matches")]), "zz")).toBeNull();
  });

  it("a siding says so", () => {
    expect(stationStrip(line([], [st("lab", "", "not compared", { rank: -1 })]), "lab")).toEqual({ kind: "siding", text: "siding" });
  });

  it("its own track only, in DTO order; this station boxed with its state words", () => {
    const s = stationStrip(line([st("d", "dev", "matches"), st("s", "stg", "matches"), st("p", "prd", "differs", { single: SHA }), st("x", "dev", "matches", { track: "neu" })]), "p");
    expect(s?.kind).toBe("track");
    if (s?.kind !== "track") return;
    expect(s.stations.map((x) => [x.label, x.here, x.box])).toEqual([["dev", false, null], ["stg", false, null], ["prd", true, "differs at 9f2c11d"]]);
    expect(s.text).toBe("dev matches; stg matches; prd, this station, differs at 9f2c11d");
  });

  it("a segment is lit only when both ends match", () => {
    const s = stationStrip(line([st("d", "dev", "matches"), st("s", "stg", "matches"), st("p", "prd", "differs"), st("q", "dr", "matches")]), "d");
    expect(s?.kind === "track" && s.lit).toEqual([true, false, false]);
  });

  it("marks: failing, then held, then checking, then syncing; flags join the phrase", () => {
    const s = stationStrip(line([
      st("a", "dev", "differs", { flags: { failing: true }, held: true }),
      st("b", "test", "matches", { held: true }),
      st("c", "stg", "checking"),
      st("d", "prd", "matches", { flags: { failing: false, syncing: true } }),
      st("e", "dr", "matches"),
    ]), "a");
    expect(s?.kind === "track" && s.stations.map((x) => x.mark)).toEqual(["fault", "attention", "checking", "syncing", "none"]);
    expect(s?.text).toBe("dev, this station, differs, failing, held; test matches, held; stg checking; prd matches, syncing; dr matches");
  });

  it("this station's read still checking: its marks, and checking… in its box", () => {
    const s = stationStrip(line([st("d", "dev", "matches"), st("p", "prd", "differs", { read: "checking", flags: { failing: true } })]), "p");
    expect(s?.kind === "track" && s.stations[1]).toMatchObject({ mark: "fault", box: "checking…" });
    expect(s?.text).toBe("dev matches; prd, this station, checking…, failing");
  });

  it("two stations of one environment on a track are named by cluster", () => {
    const s = stationStrip(line([st("prd-weu", "prd", "matches"), st("prd-neu", "prd", "matches")]), "prd-weu");
    expect(s?.kind === "track" && s.stations.map((x) => x.label)).toEqual(["prd-weu", "prd-neu"]);
  });

  it("a SHA is shortened; a chart version is not", () => {
    expect(short(SHA)).toBe("9f2c11d");
    expect(short("1.4.2")).toBe("1.4.2");
  });
});
