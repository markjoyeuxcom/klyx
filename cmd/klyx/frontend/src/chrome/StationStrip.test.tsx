import { describe, it, expect, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { applyLine, resetLineStore, type LineDTO, type LineStationDTO } from "../store/line";
import { StationStrip } from "./StationStrip";

// The Line slice 4 §5.3: a labelled group whose words are one phrase; the
// drawing is hidden; segments are never announced.

const st = (cluster: string, env: string, state: string, over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster, env, state, track: "weu", rank: 0, e: 1, a: [], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as LineStationDTO);

beforeEach(() => resetLineStore());

describe("StationStrip", () => {
  it("renders nothing before the first DTO", () => {
    const { container } = render(<StationStrip cluster="p" />);
    expect(container.firstChild).toBeNull();
  });

  it("is a group named 'Position on the line' with one phrase that says protected; the drawing is aria-hidden", () => {
    applyLine({ version: 1, stations: [st("d", "dev", "matches"), st("p", "prd", "differs", { single: "9f2c11d0a1b2c3d4e5f60718293a4b5c6d7e8f90", flags: { failing: true } })], siding: [], tracks: [], keys: [] } as unknown as LineDTO);
    const { getByRole } = render(<StationStrip cluster="p" protection="protected" />);
    const g = getByRole("group", { name: "Position on the line" });
    expect(g.querySelector(".sr-only")?.textContent).toBe("dev matches; prd, this station, protected, differs at 9f2c11d, failing");
    for (const stop of g.querySelectorAll(".strip-stop")) expect(stop.getAttribute("aria-hidden")).toBe("true");
    const box = g.querySelector(".strip-box") as HTMLElement;
    expect(box.getAttribute("data-mark")).toBe("fault");
    expect(box.textContent).toBe("prddiffers at 9f2c11d");
    expect(g.querySelector(".locbar-protected")).toBeNull(); // drawn once, on the station crumb
    expect(g.querySelector("[data-here]")?.contains(box)).toBe(true);
    expect(g.querySelector(".strip-seg")?.hasAttribute("data-lit")).toBe(false);
  });

  it("a siding reads 'siding'", () => {
    applyLine({ version: 1, stations: [], siding: [st("lab", "", "not compared", { rank: -1 })], tracks: [], keys: [] } as unknown as LineDTO);
    const { getByRole } = render(<StationStrip cluster="lab" />);
    expect(getByRole("group", { name: "Position on the line" }).textContent).toBe("siding");
  });
});
