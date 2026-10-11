import { describe, it, expect, beforeEach, vi } from "vitest";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";

vi.mock("../nav/history", () => ({ back: vi.fn(), forward: vi.fn() }));
import { back } from "../nav/history";
import { _setPaletteOpenForTest } from "../chrome/CommandPalette";
import { handleShellKey, handleShellMouse } from "./AppShell";

const c = (name: string, env = "") => ({ name, env, state: "Synced" } as unknown as ClusterDTO);
const key = (k: string, target: EventTarget = document.body, init: KeyboardEventInit = {}) => {
  const e = new KeyboardEvent("keydown", { key: k, metaKey: true, bubbles: true, cancelable: true, ...init });
  Object.defineProperty(e, "target", { value: target });
  return e;
};

beforeEach(() => {
  vi.clearAllMocks();
  _setPaletteOpenForTest(false);
  document.body.innerHTML = "";
  useFleet.setState({ clusters: [c("aks-prd-weu", "prd"), c("aks-dev-weu", "dev")], route: { name: "fleet" } });
});

describe("shell shortcuts yield to keyboard owners (step 7b review)", () => {
  it("mod+1 opens the first cluster in spine order; mod+[ goes back", () => {
    handleShellKey(key("1"));
    expect(useFleet.getState().route).toMatchObject({ cluster: "aks-dev-weu" });
    handleShellKey(key("["));
    expect(back).toHaveBeenCalledTimes(1);
  });

  it("a text field, textarea (YAML) or contenteditable keeps ctrl+[ and mod+N", () => {
    const ta = document.createElement("textarea");
    const input = document.createElement("input");
    const ce = document.createElement("div");
    ce.setAttribute("contenteditable", "true");
    for (const t of [ta, input, ce]) {
      document.body.appendChild(t);
      handleShellKey(key("[", t, { metaKey: false, ctrlKey: true }));
      handleShellKey(key("1", t));
    }
    expect(back).not.toHaveBeenCalled();
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("an event a handler already took is left alone", () => {
    const e = key("1");
    e.preventDefault();
    handleShellKey(e);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("mod+N never switches cluster under an open dialog or the palette", () => {
    const dlg = document.createElement("div");
    dlg.setAttribute("role", "dialog");
    dlg.setAttribute("aria-modal", "true");
    document.body.appendChild(dlg);
    handleShellKey(key("1"));
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
    dlg.remove();
    _setPaletteOpenForTest(true);
    handleShellKey(key("1"));
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("mouse back respects the same ownership", () => {
    const dlg = document.createElement("div");
    dlg.setAttribute("role", "dialog");
    document.body.appendChild(dlg);
    handleShellMouse(new MouseEvent("mouseup", { button: 3, cancelable: true }));
    expect(back).not.toHaveBeenCalled();
    dlg.remove();
    handleShellMouse(new MouseEvent("mouseup", { button: 3, cancelable: true }));
    expect(back).toHaveBeenCalledTimes(1);
  });

  it("mod+= / mod+- / mod+0 zoom anywhere - also from a text field - unless already handled", async () => {
    const { getZoom, zoomReset } = await import("./zoom");
    zoomReset();
    const input = document.createElement("input");
    document.body.appendChild(input);
    handleShellKey(key("=", input));
    expect(getZoom()).toBe(1.1);
    handleShellKey(key("+", document.body, { shiftKey: true }));
    expect(getZoom()).toBe(1.25);
    handleShellKey(key("-"));
    expect(getZoom()).toBe(1.1);
    const handled = key("0");
    handled.preventDefault();
    handleShellKey(handled);
    expect(getZoom()).toBe(1.1);
    handleShellKey(key("0"));
    expect(getZoom()).toBe(1);
  });
});
