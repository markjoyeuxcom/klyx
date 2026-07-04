import { describe, it, expect } from "vitest";
import { loadBoosts, recordUse } from "./frecency";

const DAY = 86_400_000;

describe("palette frecency", () => {
  it("unused commands have zero boost", () => {
    expect(loadBoosts()(  "cluster:x")).toBe(0);
  });

  it("boost grows with uses and caps at 10", () => {
    const now = 1_000_000_000_000;
    for (let i = 0; i < 3; i++) recordUse("cluster:x", now);
    const b3 = loadBoosts(now)("cluster:x");
    expect(b3).toBe(3);
    for (let i = 0; i < 20; i++) recordUse("cluster:x", now);
    expect(loadBoosts(now)("cluster:x")).toBe(10);
  });

  it("boost halves every 14 days", () => {
    const now = 1_000_000_000_000;
    recordUse("a", now);
    recordUse("a", now);
    expect(loadBoosts(now + 14 * DAY)("a")).toBeCloseTo(1, 5);
    expect(loadBoosts(now + 28 * DAY)("a")).toBeCloseTo(0.5, 5);
  });

  it("trims to the 300 most recently used entries", () => {
    const now = 1_000_000_000_000;
    for (let i = 0; i < 305; i++) recordUse(`cmd-${i}`, now + i);
    const boost = loadBoosts(now + 1000);
    // The five oldest were evicted; the newest survive.
    expect(boost("cmd-0")).toBe(0);
    expect(boost("cmd-4")).toBe(0);
    expect(boost("cmd-304")).toBeGreaterThan(0);
  });

  it("tolerates corrupt storage", () => {
    localStorage.setItem("klyx-palette-frecency", "{corrupt");
    expect(loadBoosts()("x")).toBe(0);
    recordUse("x"); // must not throw
    expect(loadBoosts()("x")).toBeGreaterThan(0);
  });
});
