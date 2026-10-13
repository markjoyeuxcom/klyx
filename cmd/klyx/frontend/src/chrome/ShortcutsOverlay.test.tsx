import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ShortcutsOverlay, openShortcutsOverlay } from "./ShortcutsOverlay";
import { act } from "@testing-library/react";

describe("ShortcutsOverlay", () => {
  it("opens on ? and closes on Escape", () => {
    const { queryByTestId } = render(<ShortcutsOverlay />);
    expect(queryByTestId("shortcuts-overlay")).toBeNull();
    fireEvent.keyDown(window, { key: "?" });
    expect(queryByTestId("shortcuts-overlay")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(queryByTestId("shortcuts-overlay")).toBeNull();
  });

  it("ignores ? typed into an input", () => {
    const { queryByTestId, getByPlaceholderText } = render(
      <>
        <input placeholder="filter" />
        <ShortcutsOverlay />
      </>,
    );
    fireEvent.keyDown(getByPlaceholderText("filter"), { key: "?" });
    expect(queryByTestId("shortcuts-overlay")).toBeNull();
  });

  it("opens via the palette command event and lists the core bindings", () => {
    const { getByTestId, getByText } = render(<ShortcutsOverlay />);
    act(() => {
      openShortcutsOverlay();
    });
    expect(getByTestId("shortcuts-overlay")).toBeTruthy();
    expect(getByText(/command palette/)).toBeTruthy();
    expect(getByText(/move selection/)).toBeTruthy();
  });
});

// Codex UI review 2026-10-13: the cheatsheet is a real modal.
describe("ShortcutsOverlay focus", () => {
  it("moves focus to Close, keeps Tab inside, and returns focus on close", () => {
    const { getByTestId, getByRole, queryByTestId, getByText } = render(
      <>
        <button>behind</button>
        <ShortcutsOverlay />
      </>,
    );
    const behind = getByText("behind");
    behind.focus();
    act(() => {
      openShortcutsOverlay();
    });
    const close = getByRole("button", { name: "Close" });
    expect(document.activeElement).toBe(close);
    // Close is the only focusable control: Tab and Shift+Tab stay on it.
    fireEvent.keyDown(close, { key: "Tab" });
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(close);
    expect(getByTestId("shortcuts-overlay").querySelector('[role="dialog"]')?.getAttribute("aria-labelledby")).toBeTruthy();
    fireEvent.click(close);
    expect(queryByTestId("shortcuts-overlay")).toBeNull();
    expect(document.activeElement).toBe(behind);
  });

  it("Escape inside the dialog closes it without reaching the list behind", () => {
    const behind = vi.fn();
    const { getByRole, queryByTestId } = render(
      <div onKeyDown={behind}>
        <ShortcutsOverlay />
      </div>,
    );
    act(() => {
      openShortcutsOverlay();
    });
    fireEvent.keyDown(getByRole("button", { name: "Close" }), { key: "j" });
    expect(behind).not.toHaveBeenCalled();
    fireEvent.keyDown(getByRole("button", { name: "Close" }), { key: "Escape" });
    expect(queryByTestId("shortcuts-overlay")).toBeNull();
  });
});
