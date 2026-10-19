import { describe, it, expect } from "vitest";
import { emptyHeld, withOlder, HISTORY_LINES_MAX } from "./logHistoryModel";
import type { HistoryLine } from "../bridge/logHistory";

// a page of n lines, newest first, ending just before `from`
const page = (from: number, n: number, line = "x"): HistoryLine[] =>
  Array.from({ length: n }, (_, i) => ({ ts: String(from - 1 - i).padStart(12, "0"), line: `${line} ${from - 1 - i}`, branch: 0 }));

describe("what a History view holds (coverage S8b §5)", () => {
  it("an older page goes on the old end; the view holds oldest first", () => {
    let h = withOlder(emptyHeld(), page(1000, 3));
    h = withOlder(h, page(997, 2));
    expect(h.lines.map((l) => l.line)).toEqual(["x 995", "x 996", "x 997", "x 998", "x 999"]);
    expect(h.newerDropped).toBe(0);
  });

  it("past 10,000 lines the NEWEST go, so older pages stay reachable past 2,000 and 10,000", () => {
    let h = emptyHeld();
    let from = 100_000;
    for (let i = 0; i < 12; i++) { h = withOlder(h, page(from, 1000)); from -= 1000; }
    expect(h.lines.length).toBe(HISTORY_LINES_MAX);
    expect(h.newerDropped).toBe(2000);
    expect(h.lines[0].line).toBe(`x ${from}`); // the oldest page read is held
    expect(h.lines[h.lines.length - 1].line).toBe(`x ${100_000 - 2001}`); // the newest 2,000 went
  });

  it("the byte budget evicts from the newest end too, never the page just read", () => {
    const big = (from: number) => page(from, 10, "y".repeat(1000));
    let h = withOlder(emptyHeld(), big(100), 10_000, 30_000);
    h = withOlder(h, big(90), 10_000, 30_000);
    expect(h.lines[0].line.endsWith(" 80")).toBe(true); // the older page kept whole
    expect(h.newerDropped).toBeGreaterThan(0);
    expect(h.lines.length).toBeLessThan(20);
  });
});
