import { useCallback, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { LensLayout } from "./LensLayout";
import { ActionMenu, InspectorLayout } from "./InspectorLayout";
import { useListKeys } from "../chrome/useListKeys";
import { ConfirmDialog } from "../chrome/ConfirmDialog";

vi.mock("../chrome/CommandPalette", () => ({ getPaletteOpen: () => false }));

// The expand against a real list and real Escape owners (slice 16 review 1,
// from Codex's probes): focus across the breakpoint, a hidden list that
// never moves, and Escape left with a field, a confirm or a menu.

let width = 1600;
let resized: ResizeObserverCallback;
let descriptor: PropertyDescriptor | undefined;
beforeEach(() => {
  descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
  Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => width });
  vi.stubGlobal("ResizeObserver", class { constructor(cb: ResizeObserverCallback) { resized = cb; } observe() {} disconnect() {} });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  if (descriptor) Object.defineProperty(HTMLElement.prototype, "clientWidth", descriptor);
  width = 1600;
});

const noop = () => {};
function Harness({ onClose = noop }: { onClose?: () => void }) {
  const [open, setOpen] = useState(true);
  const [selected, select] = useState(0);
  const close = useCallback(() => { onClose(); setOpen(false); }, [onClose]);
  useListKeys({ count: 4, selected, onSelect: select, onActivate: noop, onEscape: close });
  return (
    <LensLayout label="things" title="Things" state="ready" body={<button>row {selected}</button>}
      inspector={open ? <InspectorLayout pin={{ cluster: "review" }} kind="Thing" name="one" onClose={close}><input aria-label="field" /></InspectorLayout> : undefined}
      onCloseInspector={close} />
  );
}
const resize = (next: number) => act(() => { width = next; resized([{ contentRect: { width: next } } as ResizeObserverEntry], {} as ResizeObserver); });

describe("the expanded inspector with a real list (slice 16 review 1)", () => {
  it("a confirm owns Escape: neither restored nor closed", () => {
    const onClose = vi.fn(), cancel = vi.fn();
    const view = render(<Harness onClose={onClose} />);
    fireEvent.click(view.getByLabelText("expand details"));
    const dialog = render(<ConfirmDialog title="Review confirm" cluster="review" detail="local fixture" protected={false} onConfirm={noop} onCancel={cancel} />);
    fireEvent.keyDown(dialog.getByRole("button", { name: "Cancel" }), { key: "Escape" });
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
    expect(view.getByTestId("lens-layout").hasAttribute("data-expanded")).toBe(true);
  });

  it("a menu owns Escape: neither restored nor closed", () => {
    const onClose = vi.fn();
    const view = render(<Harness onClose={onClose} />);
    fireEvent.click(view.getByLabelText("expand details"));
    const menu = render(<ActionMenu label="Review menu" items={[{ label: "One action", onSelect: noop }]} />);
    fireEvent.click(menu.getByRole("button", { name: "Review menu" }));
    fireEvent.keyDown(menu.getByRole("menuitem"), { key: "Escape" });
    expect(menu.queryByRole("menu")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(view.getByTestId("lens-layout").hasAttribute("data-expanded")).toBe(true);
  });

  it("focus stays in an expanded inspector when the drawer becomes a split", () => {
    width = 900;
    const { getByLabelText, getByTestId } = render(<Harness />);
    const expand = getByLabelText("expand details");
    expand.focus();
    fireEvent.click(expand);
    const aside = getByTestId("lens-inspector");
    expect(aside.contains(document.activeElement)).toBe(true);
    resize(1600);
    expect(getByTestId("lens-layout").hasAttribute("data-expanded")).toBe(true);
    // the same inspector, not a remounted one
    expect(getByTestId("lens-inspector")).toBe(aside);
    expect(aside.contains(document.activeElement)).toBe(true);
  });

  it("a plain drawer that becomes a split keeps focus in its inspector; closing still returns it to the row", () => {
    function Opener() {
      const [open, setOpen] = useState(false);
      return (
        <LensLayout label="things" title="Things" state="ready" body={<button onClick={() => setOpen(true)}>row</button>}
          inspector={open ? <InspectorLayout pin={{ cluster: "review" }} kind="Thing" name="one" onClose={() => setOpen(false)}><input aria-label="field" /></InspectorLayout> : undefined}
          onCloseInspector={() => setOpen(false)} />
      );
    }
    width = 900;
    const { getByText, getByLabelText, getByTestId } = render(<Opener />);
    const row = getByText("row");
    row.focus();
    fireEvent.click(row);
    getByLabelText("field").focus();
    resize(1600);
    expect(getByTestId("lens-layout").getAttribute("data-mode")).toBe("split");
    expect(document.activeElement).toBe(getByLabelText("field"));
    resize(900);
    fireEvent.click(getByLabelText("close details"));
    expect(document.activeElement).toBe(row);
  });

  it("a hidden list never moves, whatever has focus", () => {
    width = 900;
    const { getByLabelText, getByText } = render(<Harness />);
    const expand = getByLabelText("expand details");
    expand.focus();
    fireEvent.click(expand);
    resize(1600);
    fireEvent.keyDown(document.activeElement!, { key: "j" });
    (document.activeElement as HTMLElement | null)?.blur();
    fireEvent.keyDown(document.body, { key: "j" });
    expect(getByText("row 0")).toBeTruthy();
  });

  it("Escape in a field stays with the field, in an expanded drawer and a plain one", () => {
    width = 900;
    const onClose = vi.fn();
    const { getByLabelText, getByTestId } = render(<Harness onClose={onClose} />);
    const input = getByLabelText("field");
    input.focus();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(getByLabelText("expand details"));
    input.focus();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    expect(getByTestId("lens-layout").hasAttribute("data-expanded")).toBe(true);
  });

  // review 2: the return target belongs to the inspector, not the drawer
  function Rows() {
    const [open, setOpen] = useState<string | null>(null);
    return (
      <LensLayout label="things" title="Things" state="ready"
        body={<><button onClick={() => setOpen("A")}>row A</button><button onClick={() => setOpen("B")}>row B</button></>}
        inspector={open ? <InspectorLayout pin={{ cluster: "review" }} kind="Thing" name={open} onClose={() => setOpen(null)}><input aria-label="field" /></InspectorLayout> : undefined}
        onCloseInspector={() => setOpen(null)} />
    );
  }

  it("closing in the split returns focus to the row the drawer was opened from", () => {
    width = 900;
    const { getByText, getByLabelText, queryByTestId } = render(<Rows />);
    const row = getByText("row A");
    row.focus();
    fireEvent.click(row);
    const expand = getByLabelText("expand details");
    expand.focus();
    fireEvent.click(expand);
    resize(1600);
    const close = getByLabelText("close details");
    close.focus();
    fireEvent.click(close);
    expect(queryByTestId("lens-inspector")).toBeNull();
    expect(document.activeElement).toBe(row);
  });

  it("a closed inspector's target never passes to the next one", () => {
    width = 900;
    const { getByText, getByLabelText } = render(<Rows />);
    const a = getByText("row A");
    a.focus();
    fireEvent.click(a);
    getByLabelText("field").focus();
    resize(1600);
    fireEvent.click(getByLabelText("close details"));
    const b = getByText("row B");
    b.focus();
    fireEvent.click(b);
    getByLabelText("field").focus();
    resize(900);
    fireEvent.click(getByLabelText("close details"));
    expect(document.activeElement).toBe(b);
  });

  it("the target follows focus in the list; focus the user moved elsewhere is not pulled back", () => {
    const { getByText, getByLabelText } = render(<Rows />);
    const a = getByText("row A");
    a.focus();
    fireEvent.click(a);
    // in the split the list stays live: focus moves to row B, which opens B
    const b = getByText("row B");
    b.focus();
    fireEvent.click(b);
    getByLabelText("field").focus();
    fireEvent.click(getByLabelText("close details"));
    expect(document.activeElement).toBe(b);
    // focus already elsewhere when the inspector closes stays there
    fireEvent.click(a);
    const elsewhere = document.createElement("button");
    document.body.appendChild(elsewhere);
    try {
      elsewhere.focus();
      fireEvent.click(getByLabelText("close details"));
      expect(document.activeElement).toBe(elsewhere);
    } finally {
      elsewhere.remove();
    }
  });
});
