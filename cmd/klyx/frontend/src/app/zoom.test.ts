import { describe, it, expect, beforeEach } from "vitest";
import { getZoom, handleZoomKey, initZoom, layoutViewportHeight, loadZoom, setZoom, unzoom, vh, vw, zoomIn, zoomOut, zoomPercent, zoomReset } from "./zoom";

const rootZoom = () => (document.documentElement.style as CSSStyleDeclaration & { zoom: string }).zoom;
const key = (k: string, over: KeyboardEventInit = {}) => new KeyboardEvent("keydown", { key: k, metaKey: true, cancelable: true, ...over });

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

  it("the root carries --app-zoom so viewport units can be compensated; 100% removes it", () => {
    setZoom(2);
    expect(document.documentElement.style.getPropertyValue("--app-zoom")).toBe("2");
    expect(vh(50)).toBe("calc(50vh / var(--app-zoom, 1))");
    expect(vw(92)).toBe("calc(92vw / var(--app-zoom, 1))");
    zoomReset();
    expect(document.documentElement.style.getPropertyValue("--app-zoom")).toBe("");
  });

  it("the layout viewport is the window divided by the zoom", () => {
    Object.defineProperty(window, "innerHeight", { writable: true, configurable: true, value: 800 });
    setZoom(2);
    expect(layoutViewportHeight()).toBe(400);
  });

  it("zoom keys work for any root, unless a handler already took the key", () => {
    expect(handleZoomKey(key("="))).toBe(true);
    expect(getZoom()).toBe(1.1);
    expect(handleZoomKey(key("+", { shiftKey: true }))).toBe(true);
    expect(getZoom()).toBe(1.25);
    expect(handleZoomKey(key("0", { metaKey: false, ctrlKey: true }))).toBe(true);
    expect(getZoom()).toBe(1);
    const taken = key("=");
    taken.preventDefault();
    expect(handleZoomKey(taken)).toBe(true);
    expect(getZoom()).toBe(1);
    expect(handleZoomKey(key("=", { altKey: true }))).toBe(false);
    expect(handleZoomKey(key("k"))).toBe(false);
  });

  it("a zoom change in another Klyx window is followed", () => {
    initZoom();
    let heard = 0;
    const on = () => { heard++; };
    window.addEventListener("klyx:zoom", on);
    localStorage.setItem("klyx.layout.v1.zoom", "1.5");
    window.dispatchEvent(new StorageEvent("storage", { key: "klyx.layout.v1.zoom" }));
    window.removeEventListener("klyx:zoom", on);
    expect(getZoom()).toBe(1.5);
    expect(heard).toBe(1);
  });
});
