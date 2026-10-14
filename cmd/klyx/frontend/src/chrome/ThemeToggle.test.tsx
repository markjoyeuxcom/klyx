import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ThemeProvider } from "../theme/ThemeProvider";
import { ThemeToggle } from "./ThemeToggle";

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", vi.fn(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })));
});

describe("ThemeToggle", () => {
  it("opens a theme menu and selects day; the retired palettes are gone", () => {
    const { getByLabelText, getByRole, queryByRole } = render(<ThemeProvider><ThemeToggle /></ThemeProvider>);
    expect(document.documentElement.getAttribute("data-theme")).toBe("night");
    fireEvent.click(getByLabelText("Theme: Night"));
    expect(queryByRole("menuitemradio", { name: "Crimson" })).toBeNull();
    expect(getByRole("menuitemradio", { name: "Light (classic)" })).toBeTruthy();
    fireEvent.click(getByRole("menuitemradio", { name: "Day" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("day");
    expect(localStorage.getItem("klyx-theme")).toBe("day");
  });
});
