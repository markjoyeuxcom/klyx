import { describe, it, expect } from "vitest";
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
