import { describe, it, expect } from "vitest";
import type { LineDTO, LineStationDTO } from "../store/line";
import { lineBoard } from "./board";

// The Line slice 5 §3: tracks × ranks in DTO order, lit only between two
// cells that match, the siding apart, the fallback unlit, mesh where drawn.
const st = (cluster: string, track: string, rank: number, state = "matches"): LineStationDTO =>
  ({ cluster, env: ["dev", "test", "stg", "prd"][rank] ?? "", track, rank, state, e: 1, a: [], u: 0, read: "ok", units: [], flags: { failing: false } } as unknown as LineStationDTO);
const dto = (stations: LineStationDTO[], over: Partial<LineDTO> = {}): LineDTO => ({
  version: 1, tracks: [{ name: "weu", kind: "region", label: "westeurope" }, { name: "neu", kind: "region", label: "northeurope" }],
  stations, siding: [], fallback: false, keys: [], mesh: { drawn: [], card: {}, offFleet: [] }, ...over,
} as unknown as LineDTO);

describe("lineBoard", () => {
  it("places stations by track and rank, in the DTO's track order; empty columns stay empty", () => {
    const b = lineBoard(dto([st("dw", "weu", 0), st("pw", "weu", 3), st("dn", "neu", 0), st("sn", "neu", 2), st("pn", "neu", 3)]));
    expect(b.ranks).toEqual([0, 2, 3]);
    expect(b.tracks.map((t) => [t.label, t.cells.map((c) => [c.col, c.stations.map((s) => s.cluster).join()])])).toEqual([
      ["westeurope", [[0, "dw"], [2, "pw"]]],
      ["northeurope", [[0, "dn"], [1, "sn"], [2, "pn"]]],
    ]);
  });

  it("a segment is lit only when every station at both ends matches", () => {
    const b = lineBoard(dto([st("d", "weu", 0), st("s", "weu", 2), st("p", "weu", 3, "differs"), st("q", "neu", 0), st("r", "neu", 2, "checking")]));
    expect(b.tracks[0].lit).toEqual([true, false]);
    expect(b.tracks[1].lit).toEqual([false]);
  });

  it("the fallback is one row of every station, nothing lit; the siding stays apart", () => {
    const b = lineBoard(dto([st("a", "weu", 3), st("b", "neu", 3)], { fallback: true, siding: [st("lab", "", -1)] }));
    expect(b.fallback).toBe(true);
    expect(b.tracks).toHaveLength(1);
    expect(b.tracks[0].cells[0].stations.map((s) => s.cluster)).toEqual(["a", "b"]);
    expect(b.tracks[0].lit).toEqual([]);
    expect(b.siding.map((s) => s.cluster)).toEqual(["lab"]);
  });

  it("a drawn mesh link joins the same rank on adjacent tracks; card, off-fleet, unknown and error pass through", () => {
    const b = lineBoard(dto([st("pw", "weu", 3), st("pn", "neu", 3), st("dw", "weu", 0)], {
      mesh: { drawn: [{ a: "pw", b: "pn", mutual: false, from: "pn" }, { a: "dw", b: "pn", mutual: true }], card: { dw: [{ a: "dw", b: "pn", mutual: true }] },
        offFleet: [{ a: "pn", b: "elsewhere", mutual: true }], unknown: ["dw"], error: "" } as never,
    }));
    expect(b.links).toEqual([{ a: "pw", b: "pn", mutual: false, from: "pn", rank: 3, upper: 0 }]); // dw-pn spans ranks: never drawn
    expect(b.card.dw).toHaveLength(1);
    expect(b.offFleet).toHaveLength(1);
    expect(b.unknown).toEqual(["dw"]);
    expect(b.meshError).toBeNull();
  });
});
