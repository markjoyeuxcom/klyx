import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import type { LineDTO, LineStationDTO } from "../store/line";
import { LinePos } from "../chrome/primitives";
import { linePosOf } from "./linePos";

// The Line slice 8a (parent §5.4): one square per rank on the station's
// track, its own filled (danger with a fault from the fleet axes), a siding
// as one hollow square; one image named by the full cluster and position.

const st = (cluster: string, track: string, rank: number, env: string, over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster, track, rank, env, state: "matches", e: 1, a: [], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as LineStationDTO);

const dto = {
  version: 1,
  tracks: [{ name: "neu", kind: "region", label: "North Europe" }, { name: "weu", kind: "region", label: "West Europe" }],
  stations: [st("aks-dev-neu", "neu", 0, "dev"), st("aks-stg-neu", "neu", 2, "stg"), st("aks-prd-neu", "neu", 3, "prd"), st("aks-prd-weu", "weu", 3, "prd")],
  siding: [st("lab", "", -1, "")],
} as unknown as LineDTO;
const codes = { "aks-prd-neu": "pn", lab: "la" };

describe("linePosOf", () => {
  it("one square per rank present on the station's track, its own marked", () => {
    const p = linePosOf(dto, "aks-prd-neu", codes, false)!;
    expect(p.squares).toEqual([{ rank: 0, here: false }, { rank: 2, here: false }, { rank: 3, here: true }]);
    expect(p.name).toBe("aks-prd-neu, prd on North Europe");
    expect(p.code).toBe("pn");
    expect(p.fault).toBe(false);
  });
  it("the fault is the caller's (the fleet axes), never the line's GitOps flag", () => {
    const failingLine = { ...dto, stations: dto.stations.map((s) => ({ ...s, flags: { failing: true } })) } as unknown as LineDTO;
    expect(linePosOf(failingLine, "aks-prd-neu", codes, false)!.fault).toBe(false);
    expect(linePosOf(dto, "aks-prd-neu", codes, true)!.fault).toBe(true);
  });
  it("a siding is one hollow square and 'siding'; an unplaced cluster has none", () => {
    const p = linePosOf(dto, "lab", codes, false)!;
    expect(p.siding).toBe(true);
    expect(p.name).toBe("lab, siding");
    expect(linePosOf(dto, "nowhere", codes, false)).toBeNull();
    expect(linePosOf(null, "lab", codes, false)).toBeNull();
  });
});

describe("LinePos", () => {
  it("is one image named by the cluster and its position, the code always beside it", () => {
    const { getByRole } = render(<LinePos pos={linePosOf(dto, "aks-prd-neu", codes, true)!} />);
    const img = getByRole("img", { name: "aks-prd-neu, prd on North Europe" });
    expect(img.querySelector("[data-code]")?.textContent).toBe("pn");
    const squares = img.querySelectorAll("[data-sq]");
    expect(squares).toHaveLength(3);
    const here = img.querySelector("[data-here]") as HTMLElement;
    expect(here.style.background).toContain("danger");
    expect((squares[0] as HTMLElement).style.border).toContain("1px solid");
  });
  it("a siding shows one hollow square and the word", () => {
    const { getByRole } = render(<LinePos pos={linePosOf(dto, "lab", codes, false)!} />);
    const img = getByRole("img", { name: "lab, siding" });
    expect(img.querySelectorAll("[data-sq]")).toHaveLength(1);
    expect(img.textContent).toBe("lasiding");
  });
});
