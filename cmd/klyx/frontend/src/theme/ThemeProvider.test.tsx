import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, act } from "@testing-library/react";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Toggle() {
  const { theme, effectiveTheme, toggle, setTheme } = useTheme();
  return (
    <>
      <button onClick={toggle}>{theme}/{effectiveTheme}</button>
      <button onClick={() => setTheme("midnight")}>midnight</button>
      <button onClick={() => setTheme("crimson")}>crimson</button>
      <button onClick={() => setTheme("system")}>system</button>
    </>
  );
}

describe("ThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })));
  });

  it("defaults to bench (direction C) and sets data-theme on the root", () => {
    render(<ThemeProvider><Toggle /></ThemeProvider>);
    expect(document.documentElement.getAttribute("data-theme")).toBe("bench");
    expect(document.documentElement.getAttribute("data-theme-choice")).toBe("bench");
  });

  it("toggles to the next concrete theme and persists", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("bench/bench").click());
    expect(document.documentElement.getAttribute("data-theme")).toBe("bench-light");
    expect(localStorage.getItem("klyx-theme")).toBe("bench-light");
  });

  it("can select midnight as a concrete theme", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("midnight").click());
    expect(document.documentElement.getAttribute("data-theme")).toBe("midnight");
    expect(localStorage.getItem("klyx-theme")).toBe("midnight");
  });

  it("can select crimson as a concrete theme", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("crimson").click());
    expect(document.documentElement.getAttribute("data-theme")).toBe("crimson");
    expect(localStorage.getItem("klyx-theme")).toBe("crimson");
  });

  it("resolves system to the OS dark preference while storing system", () => {
    const { getByText } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByText("system").click());
    expect(document.documentElement.getAttribute("data-theme-choice")).toBe("system");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem("klyx-theme")).toBe("system");
  });
});
