import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render } from "@testing-library/react";
import { LensLayout, splitGeometry, INSPECTOR_DEFAULT, INSPECTOR_MAX, INSPECTOR_MIN } from "./LensLayout";

describe("splitGeometry (spec §2 split and drawer)", () => {
  it("drawer below a 1040px inner width, split at and above it", () => {
    expect(splitGeometry(1039, 400).mode).toBe("drawer");
    expect(splitGeometry(1040, 400).mode).toBe("split");
  });
  it("clamps the split to keep the 600px body minimum", () => {
    // 1040 - 600 - 8 = 432 max
    expect(splitGeometry(1040, 560)).toEqual({ mode: "split", width: 432 });
    expect(splitGeometry(1600, 560)).toEqual({ mode: "split", width: INSPECTOR_MAX });
    expect(splitGeometry(1600, 100)).toEqual({ mode: "split", width: INSPECTOR_MIN });
  });
  it("a drawer never exceeds the available width", () => {
    expect(splitGeometry(300, INSPECTOR_DEFAULT).width).toBe(300);
  });
});

describe("LensLayout drawer is a modal dialog", () => {
  let restore: () => void;
  beforeEach(() => {
    // jsdom has no layout: report a narrow workspace for this suite.
    const desc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 900 });
    restore = () => { if (desc) Object.defineProperty(HTMLElement.prototype, "clientWidth", desc); };
  });
  afterEach(() => restore());

  function Harness({ open, onClose }: { open: boolean; onClose: () => void }) {
    return (
      <LensLayout
        label="Things"
        title="Things"
        state="ready"
        body={<button>row one</button>}
        inspector={open ? <div><button>inspector action</button></div> : undefined}
        onCloseInspector={onClose}
      />
    );
  }

  it("marks the list inert, moves focus in, and returns it on close", () => {
    const onClose = vi.fn();
    const { getByText, getByRole, rerender, container } = render(<Harness open={false} onClose={onClose} />);
    const row = getByText("row one");
    row.focus();
    rerender(<Harness open onClose={onClose} />);
    const dialog = getByRole("dialog");
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(container.querySelector(".lens-main")?.hasAttribute("inert")).toBe(true);
    expect(document.activeElement).toBe(getByText("inspector action"));
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(onClose).toHaveBeenCalledTimes(1);
    rerender(<Harness open={false} onClose={onClose} />);
    expect(container.querySelector(".lens-main")?.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(row);
  });

  it("Tab cycles inside the drawer", () => {
    const { getByText } = render(<Harness open onClose={() => {}} />);
    const only = getByText("inspector action");
    expect(document.activeElement).toBe(only);
    fireEvent.keyDown(window, { key: "Tab" });
    expect(document.activeElement).toBe(only);
  });
});
