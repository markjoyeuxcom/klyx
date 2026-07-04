import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, act } from "@testing-library/react";
import { Age, UpdatedAt, formatAge } from "./Age";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-07-04T12:00:00Z"));
});
afterEach(() => {
  vi.useRealTimers();
});

describe("formatAge", () => {
  it("keeps the app's terse vocabulary", () => {
    expect(formatAge(42)).toBe("42s");
    expect(formatAge(300)).toBe("5m");
    expect(formatAge(7200)).toBe("2h");
    expect(formatAge(200000)).toBe("2d");
    expect(formatAge(-5)).toBe("0s");
  });
});

describe("Age", () => {
  it("renders relative time with the absolute instant on hover", () => {
    const fiveMinAgo = Math.floor(Date.now() / 1000) - 300;
    const { getByText } = render(<Age unixSeconds={fiveMinAgo} />);
    const el = getByText("5m");
    expect(el.getAttribute("title")).toBeTruthy();
  });

  it("ticks forward as time passes", () => {
    const now = Math.floor(Date.now() / 1000);
    const { getByText } = render(<Age unixSeconds={now - 50} />);
    expect(getByText("50s")).toBeTruthy();
    act(() => {
      vi.advanceTimersByTime(31_000); // past one 30s tick
    });
    expect(getByText("1m")).toBeTruthy();
  });

  it("renders the fallback for zero/unparsable inputs", () => {
    const { container, rerender } = render(<Age unixSeconds={0} />);
    expect(container.textContent).toBe("—");
    rerender(<Age iso="not-a-date" fallback="never" />);
    expect(container.textContent).toBe("never");
  });

  it("accepts ISO input and a suffix", () => {
    const iso = new Date(Date.now() - 3600_000).toISOString();
    const { getByText } = render(<Age iso={iso} suffix=" ago" />);
    expect(getByText("1h ago")).toBeTruthy();
  });
});

describe("UpdatedAt", () => {
  it("renders nothing until data arrived, then the receipt time", () => {
    const { container, rerender } = render(<UpdatedAt at={null} />);
    expect(container.firstChild).toBeNull();
    rerender(<UpdatedAt at={Date.now()} />);
    expect(container.textContent).toMatch(/^updated \d{2}:\d{2}:\d{2}$/);
  });
});
