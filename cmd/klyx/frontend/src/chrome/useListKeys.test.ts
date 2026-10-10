import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook } from "@testing-library/react";
import { useListKeys } from "./useListKeys";
import { _setPaletteOpenForTest } from "./CommandPalette";

// Helper to fire a window keydown event with optional modifiers.
function keydown(key: string, opts: { metaKey?: boolean; ctrlKey?: boolean; altKey?: boolean; target?: EventTarget } = {}) {
  const e = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    metaKey: opts.metaKey ?? false,
    ctrlKey: opts.ctrlKey ?? false,
    altKey: opts.altKey ?? false,
  });
  if (opts.target) {
    Object.defineProperty(e, "target", { value: opts.target, writable: false });
  }
  window.dispatchEvent(e);
  return e;
}

describe("useListKeys", () => {
  let onSelect: (idx: number) => void;
  let onActivate: (idx: number) => void;
  let onEscape: () => void;

  beforeEach(() => {
    onSelect = vi.fn() as (idx: number) => void;
    onActivate = vi.fn() as (idx: number) => void;
    onEscape = vi.fn() as () => void;
    // Ensure palette is closed.
    _setPaletteOpenForTest(false);
  });

  afterEach(() => {
    _setPaletteOpenForTest(false);
  });

  it("j moves selection down (clamp at 0 → 1)", () => {
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j");
    expect(onSelect).toHaveBeenCalledWith(1);
  });

  it("j from -1 selects 0", () => {
    renderHook(() => useListKeys({ count: 3, selected: -1, onSelect, onActivate }));
    keydown("j");
    expect(onSelect).toHaveBeenCalledWith(0);
  });

  it("j clamps at end", () => {
    renderHook(() => useListKeys({ count: 3, selected: 2, onSelect, onActivate }));
    keydown("j");
    expect(onSelect).toHaveBeenCalledWith(2);
  });

  it("ArrowDown moves selection down", () => {
    renderHook(() => useListKeys({ count: 5, selected: 1, onSelect, onActivate }));
    keydown("ArrowDown");
    expect(onSelect).toHaveBeenCalledWith(2);
  });

  it("k moves selection up (clamp at 0)", () => {
    renderHook(() => useListKeys({ count: 3, selected: 1, onSelect, onActivate }));
    keydown("k");
    expect(onSelect).toHaveBeenCalledWith(0);
  });

  it("k clamps at 0", () => {
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("k");
    expect(onSelect).toHaveBeenCalledWith(0);
  });

  it("ArrowUp moves selection up", () => {
    renderHook(() => useListKeys({ count: 5, selected: 3, onSelect, onActivate }));
    keydown("ArrowUp");
    expect(onSelect).toHaveBeenCalledWith(2);
  });

  it("Enter calls onActivate with selected index", () => {
    renderHook(() => useListKeys({ count: 3, selected: 1, onSelect, onActivate }));
    keydown("Enter");
    expect(onActivate).toHaveBeenCalledWith(1);
  });

  it("Enter does nothing when selected is -1", () => {
    renderHook(() => useListKeys({ count: 3, selected: -1, onSelect, onActivate }));
    keydown("Enter");
    expect(onActivate).not.toHaveBeenCalled();
  });

  it("Escape calls onEscape", () => {
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate, onEscape }));
    keydown("Escape");
    expect(onEscape).toHaveBeenCalled();
  });

  it("Escape does nothing when onEscape is not provided", () => {
    // Should not throw.
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    expect(() => keydown("Escape")).not.toThrow();
  });

  it("/ focuses searchRef", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    const searchRef = { current: input };
    const focusSpy = vi.spyOn(input, "focus");
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate, searchRef }));
    keydown("/");
    expect(focusSpy).toHaveBeenCalled();
    document.body.removeChild(input);
  });

  it("/ does nothing when searchRef is null", () => {
    const searchRef = { current: null };
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate, searchRef }));
    expect(() => keydown("/")).not.toThrow();
  });

  it("ignores keys when palette is open", () => {
    _setPaletteOpenForTest(true);
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j");
    keydown("Enter");
    expect(onSelect).not.toHaveBeenCalled();
    expect(onActivate).not.toHaveBeenCalled();
  });

  it("ignores keys with metaKey modifier", () => {
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j", { metaKey: true });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("ignores keys with ctrlKey modifier", () => {
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j", { ctrlKey: true });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("ignores keys with altKey modifier", () => {
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("k", { altKey: true });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("ignores keys when target is an INPUT element", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j", { target: input });
    expect(onSelect).not.toHaveBeenCalled();
    document.body.removeChild(input);
  });

  it("ignores keys when target is a TEXTAREA element", () => {
    const ta = document.createElement("textarea");
    document.body.appendChild(ta);
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j", { target: ta });
    expect(onSelect).not.toHaveBeenCalled();
    document.body.removeChild(ta);
  });

  it("ignores keys when target is contentEditable", () => {
    const div = document.createElement("div");
    div.contentEditable = "true";
    document.body.appendChild(div);
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate }));
    keydown("j", { target: div });
    expect(onSelect).not.toHaveBeenCalled();
    document.body.removeChild(div);
  });

  it("removes listener on unmount", () => {
    const { unmount } = renderHook(() =>
      useListKeys({ count: 3, selected: 0, onSelect, onActivate }),
    );
    unmount();
    keydown("j");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("j does nothing when count is 0", () => {
    renderHook(() => useListKeys({ count: 0, selected: -1, onSelect, onActivate }));
    keydown("j");
    expect(onSelect).not.toHaveBeenCalled();
  });

  // --- extraKeys ---

  it("extraKeys handler fires for registered key with selected index", () => {
    const onL = vi.fn() as (idx: number) => void;
    renderHook(() => useListKeys({ count: 3, selected: 1, onSelect, onActivate, extraKeys: { l: onL } }));
    keydown("l");
    expect(onL).toHaveBeenCalledWith(1);
  });

  it("extraKeys handler does not fire when selected is -1", () => {
    const onL = vi.fn() as (idx: number) => void;
    renderHook(() => useListKeys({ count: 3, selected: -1, onSelect, onActivate, extraKeys: { l: onL } }));
    keydown("l");
    expect(onL).not.toHaveBeenCalled();
  });

  it("extraKeys handler does not fire when selected >= count", () => {
    const onL = vi.fn() as (idx: number) => void;
    renderHook(() => useListKeys({ count: 2, selected: 5, onSelect, onActivate, extraKeys: { l: onL } }));
    keydown("l");
    expect(onL).not.toHaveBeenCalled();
  });

  it("extraKeys handler is guarded by palette open", () => {
    _setPaletteOpenForTest(true);
    const onL = vi.fn() as (idx: number) => void;
    renderHook(() => useListKeys({ count: 3, selected: 1, onSelect, onActivate, extraKeys: { l: onL } }));
    keydown("l");
    expect(onL).not.toHaveBeenCalled();
  });

  it("extraKeys handler is guarded by modifier keys", () => {
    const onL = vi.fn() as (idx: number) => void;
    renderHook(() => useListKeys({ count: 3, selected: 1, onSelect, onActivate, extraKeys: { l: onL } }));
    keydown("l", { metaKey: true });
    expect(onL).not.toHaveBeenCalled();
  });

  it("extraKeys handler is guarded by editable target", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    const onL = vi.fn() as (idx: number) => void;
    renderHook(() => useListKeys({ count: 3, selected: 1, onSelect, onActivate, extraKeys: { l: onL } }));
    keydown("l", { target: input });
    expect(onL).not.toHaveBeenCalled();
    document.body.removeChild(input);
  });
});

describe("useListKeys scoping (redesign: panels own their keys)", () => {
  function fire(key: string, target: HTMLElement, prevented = false) {
    const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
    if (prevented) e.preventDefault();
    target.dispatchEvent(e);
  }

  it("ignores list keys from inside an inspector, menu or dialog - but Escape still closes", () => {
    const onSelect = vi.fn(), onActivate = vi.fn(), onEscape = vi.fn();
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect, onActivate, onEscape }));
    for (const attr of [["data-list-keys", "ignore"], ["role", "menu"], ["role", "dialog"]]) {
      const host = document.createElement("div");
      host.setAttribute(attr[0], attr[1]);
      const btn = document.createElement("button");
      host.appendChild(btn);
      document.body.appendChild(host);
      fire("Enter", btn);
      fire("j", btn);
      fire("ArrowDown", btn);
      document.body.removeChild(host);
    }
    expect(onActivate).not.toHaveBeenCalled();
    expect(onSelect).not.toHaveBeenCalled();
    const host = document.createElement("div");
    host.setAttribute("data-list-keys", "ignore");
    document.body.appendChild(host);
    fire("Escape", host);
    expect(onEscape).toHaveBeenCalledTimes(1);
    document.body.removeChild(host);
  });

  it("ignores events another handler already handled", () => {
    const onActivate = vi.fn();
    renderHook(() => useListKeys({ count: 3, selected: 0, onSelect: vi.fn(), onActivate }));
    fire("Enter", document.body, true);
    expect(onActivate).not.toHaveBeenCalled();
  });
});
