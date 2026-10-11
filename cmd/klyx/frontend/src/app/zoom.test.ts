import { describe, it, expect, beforeEach } from "vitest";
import { getZoom, initZoom, loadZoom, setZoom, unzoom, zoomIn, zoomOut, zoomPercent, zoomReset } from "./zoom";

const rootZoom = () => (document.documentElement.style as CSSStyleDeclaration & { zoom: string }).zoom;

beforeEach(() => { localStorage.clear(); zoomReset(); });

describe("app zoom (accessibility)", () => {
  it("steps through 80-200% and clamps at the ends", () => {
    for (const want of [1.1, 1.25, 1.5, 1.75, 2, 2]) { zoomIn(); expect(getZoom()).toBe(want); }
    zoomReset();
    for (const want of [0.9, 0.8, 0.8]) { zoomOut(); expect(getZoom()).toBe(want); }
  });

  it("scales the document root and persists the step; 100% leaves no inline zoom", () => {
    setZoom(1.5);
    expect(rootZoom()).toBe("1.5");
    expect(loadZoom()).toBe(1.5);
    zoomReset();
    expect(rootZoom()).toBe("");
    expect(zoomPercent()).toBe("100%");
  });

  it("restores a valid persisted step on boot; junk is 100%", () => {
    localStorage.setItem("klyx.layout.v1.zoom", "1.25");
    initZoom();
    expect(getZoom()).toBe(1.25);
    localStorage.setItem("klyx.layout.v1.zoom", "7");
    initZoom();
    expect(getZoom()).toBe(1);
  });

  it("drag deltas are divided by the zoom so handles track the pointer", () => {
    setZoom(2);
    expect(unzoom(100)).toBe(50);
  });
});
