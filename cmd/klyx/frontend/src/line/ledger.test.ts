import { describe, it, expect } from "vitest";
import type { LineDTO, LineStationDTO } from "../store/line";
import { lineLedger } from "./ledger";

// The Line slice 5 §5: revisions on the line now, R first; a revision is
// dated only by a unit that compares exactly it (§5.1).
const R = "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const OLD = "9f2c11d0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const NEW = "c0ffee10a1b2c3d4e5f60718293a4b5c6d7e8f90";
const st = (cluster: string, track: string, rank: number, single: string | undefined, units: object[] = []): LineStationDTO =>
  ({ cluster, env: ["dev", "test", "stg", "prd"][rank], track, rank, state: single ? "matches" : "mixed", single, e: 1, a: [], u: 0, read: "ok", units, flags: { failing: false } } as unknown as LineStationDTO);
const dto = (stations: LineStationDTO[], over: Partial<LineDTO> = {}): LineDTO => ({
  version: 1, key: { kind: "git", label: "repo @ branch main" }, r: R, stations, siding: [], keys: [],
  tracks: [{ name: "weu", kind: "region", label: "westeurope" }, { name: "neu", kind: "region", label: "northeurope" }], ...over,
} as unknown as LineDTO);

describe("lineLedger", () => {
  it("R first, then by station count; dots per track in rank order", () => {
    const l = lineLedger(dto([st("dw", "weu", 0, R), st("pw", "weu", 3, OLD), st("dn", "neu", 0, R), st("pn", "neu", 3, OLD), st("sn", "neu", 2, NEW), st("mx", "neu", 1, undefined)]));
    expect(l.map((r) => [r.short, r.isR, r.stations])).toEqual([["a1b9e3c", true, 2], ["9f2c11d", false, 2], ["c0ffee1", false, 1]]);
    expect(l[0].tracks.map((t) => [t.label, t.stops.map((s) => `${s.env}:${s.holds ? "x" : "o"}`).join(" ")])).toEqual([
      ["westeurope", "dev:x prd:o"],
      ["northeurope", "dev:x test:o stg:o prd:o"],
    ]);
  });

  it("a revision is dated only by a unit comparing exactly it - never an applied one the source moved past", () => {
    const at = "2026-10-15T09:41:00Z";
    const l = lineLedger(dto([
      st("dw", "weu", 0, R, [{ applied: R, compared: R, comparedAt: at }]),
      st("pw", "weu", 3, OLD, [{ applied: OLD, compared: R, comparedAt: "2026-10-15T09:50:00Z" }]),
    ]));
    expect(l.find((r) => r.id === R)?.fetchedAt).toBe("2026-10-15T09:50:00Z"); // the latest fetch that compares R
    expect(l.find((r) => r.id === OLD)?.fetchedAt).toBeNull(); // applied, but no unit compares it now
  });

  it("no key: no revisions", () => {
    expect(lineLedger(dto([st("dw", "weu", 0, R)], { key: null }))).toEqual([]);
  });
});
