import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render } from "@testing-library/react";
import { LensLayout, splitGeometry, DRAWER_MAX, INSPECTOR_DEFAULT, INSPECTOR_MIN, INSPECTOR_WIDTH_KEY } from "./LensLayout";
import { InspectorLayout } from "./InspectorLayout";

describe("splitGeometry (spec §2 split and drawer)", () => {
  it("canvas-detail keeps 640px for the graph before the inspector yields", () => {
    expect(splitGeometry(1040, 400)).toMatchObject({ mode: "split", width: 400 });
    expect(splitGeometry(1040, 400, 640)).toMatchObject({ mode: "split", width: 392 });
  });

  it("drawer below a 1040px inner width, split at and above it", () => {
    expect(splitGeometry(1039, 400).mode).toBe("drawer");
    expect(splitGeometry(1040, 400).mode).toBe("split");
  });
  it("clamps the split to keep the 600px body minimum", () => {
    // 1040 - 600 - 8 = 432 max
    expect(splitGeometry(1040, 560)).toEqual({ mode: "split", width: 432, max: 432 });
    expect(splitGeometry(1600, 100)).toEqual({ mode: "split", width: INSPECTOR_MIN, max: 992 });
  });
  // slice 16: no fixed ceiling in the split - the inspector may take all but
  // the body's minimum, and a wider request waits for a wider window
  it("lets the split grow past the old 560px ceiling", () => {
    expect(splitGeometry(1600, 900)).toEqual({ mode: "split", width: 900, max: 992 });
    expect(splitGeometry(1600, 1400)).toEqual({ mode: "split", width: 992, max: 992 });
    expect(splitGeometry(2400, 1400)).toEqual({ mode: "split", width: 1400, max: 1792 });
  });
  it("a drawer keeps its ceiling and never exceeds the available width", () => {
    expect(splitGeometry(300, INSPECTOR_DEFAULT).width).toBe(300);
    expect(splitGeometry(1000, 900)).toEqual({ mode: "drawer", width: DRAWER_MAX, max: DRAWER_MAX });
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

  it("a drawer opened beside the open console leaves focus in the console", () => {
    const console = document.createElement("div");
    console.setAttribute("data-testid", "command-palette");
    const input = document.createElement("input");
    console.appendChild(input);
    document.body.appendChild(console);
    try {
      const { rerender, getByRole } = render(<Harness open={false} onClose={() => {}} />);
      input.focus();
      rerender(<Harness open onClose={() => {}} />);
      expect(getByRole("dialog")).toBeTruthy();
      expect(document.activeElement).toBe(input);
    } finally {
      console.remove();
    }
  });

  it("Tab cycles inside the drawer", () => {
    const { getByText } = render(<Harness open onClose={() => {}} />);
    const only = getByText("inspector action");
    expect(document.activeElement).toBe(only);
    fireEvent.keyDown(window, { key: "Tab" });
    expect(document.activeElement).toBe(only);
  });
});

describe("LensLayout resize and expand (slice 16)", () => {
  let restore: () => void;
  beforeEach(() => {
    const desc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 1600 });
    restore = () => { if (desc) Object.defineProperty(HTMLElement.prototype, "clientWidth", desc); };
    try { localStorage.removeItem(INSPECTOR_WIDTH_KEY); } catch { /* none */ }
  });
  afterEach(() => restore());

  function Harness({ open, onClose = () => {} }: { open: boolean; onClose?: () => void }) {
    return (
      <LensLayout
        label="Things"
        title="Things"
        state="ready"
        body={<button>row one</button>}
        inspector={open ? (
          <InspectorLayout pin={{ cluster: "x" }} kind="Thing" name="one" onClose={onClose}>
            <input aria-label="filter" />
          </InspectorLayout>
        ) : undefined}
        onCloseInspector={onClose}
      />
    );
  }

  it("End drags the separator to the widest the body allows, and it is stored", () => {
    const { getByRole } = render(<Harness open />);
    const sep = getByRole("separator");
    expect(sep.getAttribute("aria-valuemax")).toBe("992");
    fireEvent.keyDown(sep, { key: "End" });
    expect(sep.getAttribute("aria-valuenow")).toBe("992");
    expect(localStorage.getItem(INSPECTOR_WIDTH_KEY)).toBe("992");
  });

  it("a stored width past the old ceiling is kept", () => {
    localStorage.setItem(INSPECTOR_WIDTH_KEY, "900");
    const { getByTestId } = render(<Harness open />);
    expect((getByTestId("lens-inspector") as HTMLElement).style.width).toBe("900px");
  });

  it("expands over the workspace, hides the list, and restores from the same button", () => {
    const { getByTestId, getByLabelText, queryByRole, container } = render(<Harness open />);
    fireEvent.click(getByLabelText("expand details"));
    const lens = container.querySelector(".lens")!;
    expect(lens.hasAttribute("data-expanded")).toBe(true);
    expect(getByTestId("lens-inspector").className).toContain("lens-inspector-wide");
    expect((getByTestId("lens-inspector") as HTMLElement).style.width).toBe("");
    expect(queryByRole("separator")).toBeNull();
    fireEvent.click(getByLabelText("restore details"));
    expect(lens.hasAttribute("data-expanded")).toBe(false);
    expect(queryByRole("separator")).toBeTruthy();
  });

  it("Escape restores an expanded inspector before anything closes it", () => {
    const onClose = vi.fn();
    const closer = vi.fn((e: KeyboardEvent) => { if (e.key === "Escape" && !e.defaultPrevented) onClose(); });
    window.addEventListener("keydown", closer);
    try {
      const { getByLabelText, container } = render(<Harness open onClose={onClose} />);
      fireEvent.click(getByLabelText("expand details"));
      fireEvent.keyDown(document.body, { key: "Escape" });
      expect(container.querySelector(".lens")!.hasAttribute("data-expanded")).toBe(false);
      expect(onClose).not.toHaveBeenCalled();
      // restored: the next Escape is the list's again
      fireEvent.keyDown(document.body, { key: "Escape" });
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener("keydown", closer);
    }
  });

  it("Escape in a field, or with a surface above, is not the expand's", () => {
    const { getByLabelText, container } = render(<Harness open />);
    fireEvent.click(getByLabelText("expand details"));
    fireEvent.keyDown(getByLabelText("filter"), { key: "Escape" });
    expect(container.querySelector(".lens")!.hasAttribute("data-expanded")).toBe(true);
    const menu = document.createElement("div");
    menu.setAttribute("role", "menu");
    document.body.appendChild(menu);
    try {
      fireEvent.keyDown(document.body, { key: "Escape" });
      expect(container.querySelector(".lens")!.hasAttribute("data-expanded")).toBe(true);
    } finally {
      menu.remove();
    }
  });

  it("closing the inspector resets the expand: the next object opens beside the list", () => {
    const { getByLabelText, rerender, container } = render(<Harness open />);
    fireEvent.click(getByLabelText("expand details"));
    rerender(<Harness open={false} />);
    expect(container.querySelector(".lens")!.hasAttribute("data-expanded")).toBe(false);
    rerender(<Harness open />);
    expect(container.querySelector(".lens")!.hasAttribute("data-expanded")).toBe(false);
    expect(getByLabelText("expand details")).toBeTruthy();
  });

  it("an inspector outside a lens layout has no expand button", () => {
    const { queryByTestId } = render(<InspectorLayout pin={{ cluster: "x" }} kind="Thing" name="one" onClose={() => {}}><span /></InspectorLayout>);
    expect(queryByTestId("inspector-expand")).toBeNull();
  });
});
