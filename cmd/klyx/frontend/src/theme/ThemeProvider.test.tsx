import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, act } from "@testing-library/react";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Toggle() {
  const { theme, effectiveTheme, toggle, setTheme } = useTheme();
  return (
    <>
      <button onClick={toggle}>{theme}/{effectiveTheme}</button>
      <button onClick={() => setTheme("dark")}>dark</button>
      <button onClick={() => setTheme("bench")}>bench</button>
      <button onClick={() => setTheme("system")}>system</button>
    </>
  );
}

const root = () => document.documentElement;

describe("ThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })));
  });

  it("defaults to night (The Line) and sets data-theme on the root", () => {
    render(<ThemeProvider><Toggle /></ThemeProvider>);
    expect(root().getAttribute("data-theme")).toBe("night");
    expect(root().getAttribute("data-theme-choice")).toBe("night");
    expect(root().style.colorScheme).toBe("dark");
  });

  it("toggles to the next concrete theme and persists", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("night/night").click());
    expect(root().getAttribute("data-theme")).toBe("day");
    expect(root().style.colorScheme).toBe("light");
    expect(localStorage.getItem("klyx-theme")).toBe("day");
  });

  it("keeps classic dark and the retiring bench selectable", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("dark").click());
    expect(root().getAttribute("data-theme")).toBe("dark");
    act(() => getByText("bench").click());
    expect(root().getAttribute("data-theme")).toBe("bench");
    expect(localStorage.getItem("klyx-theme")).toBe("bench");
  });

  it("resolves system to night or day by the OS while storing system", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("system").click());
    expect(root().getAttribute("data-theme-choice")).toBe("system");
    expect(root().getAttribute("data-theme")).toBe("night"); // matchMedia stub prefers dark
    expect(localStorage.getItem("klyx-theme")).toBe("system");
  });

  // Owner decision 1 (2026-10-14): one move onto The Line, then choices stick.
  for (const [saved, benchMigrated, want] of [
    ["bench", true, "night"],
    ["bench-light", true, "day"],
    ["midnight", true, "night"],
    ["violet", false, "night"],
    ["light", false, "day"],
    ["dark", false, "night"],
    ["light", true, "light"],
    ["dark", true, "dark"],
    ["system", true, "system"],
  ] as const) {
    it(`migrates a saved ${saved}${benchMigrated ? "" : " (before the bench migration)"} to ${want} exactly once`, () => {
      localStorage.setItem("klyx-theme", saved);
      if (benchMigrated) localStorage.setItem("klyx-theme-bench-migrated", "1");
      const first = render(<ThemeProvider><Toggle /></ThemeProvider>);
      expect(root().getAttribute("data-theme-choice")).toBe(want);
      expect(localStorage.getItem("klyx-theme")).toBe(want);
      first.unmount();
      // A choice made after the migration sticks: the flag is never re-read.
      localStorage.setItem("klyx-theme", "bench");
      render(<ThemeProvider><Toggle /></ThemeProvider>);
      expect(root().getAttribute("data-theme")).toBe("bench");
    });
  }

  it("a retired palette saved after the migration falls back to night", () => {
    localStorage.setItem("klyx-theme-line-migrated", "1");
    localStorage.setItem("klyx-theme", "crimson");
    render(<ThemeProvider><Toggle /></ThemeProvider>);
    expect(root().getAttribute("data-theme")).toBe("night");
  });
});
