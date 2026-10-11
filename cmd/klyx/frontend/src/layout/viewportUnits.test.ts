import { describe, it, expect } from "vitest";

// Viewport units are not compensated by app zoom in WebKit (app/zoom.ts): a raw
// 88vh dialog cap renders 176% of the window at 200% and pushes its buttons off
// screen. Every viewport-relative length goes through vh()/vw() or
// calc(Nvh / var(--app-zoom, 1)); this guard keeps raw ones from coming back.

const RAW = /(?<![\w-])\d+(\.\d+)?(vh|vw|dvh|svh|lvh|vmin|vmax)\b/g;
const SOURCES = import.meta.glob<string>(["../**/*.{ts,tsx,css}", "!../**/*.test.{ts,tsx}"], { query: "?raw", import: "default", eager: true });

describe("viewport units are zoom-compensated", () => {
  it("no source uses a raw viewport unit outside the compensated form", () => {
    const offenders: string[] = [];
    expect(Object.keys(SOURCES).length).toBeGreaterThan(50);
    for (const [file, text] of Object.entries(SOURCES)) {
      text.split("\n").forEach((line, i) => {
        const code = line.replace(/\/\/.*$/, "").replace(/\/\*.*?\*\//g, "");
        for (const m of code.matchAll(RAW)) {
          const after = code.slice((m.index ?? 0) + m[0].length);
          if (/^\s*\/\s*var\(--app-zoom/.test(after)) continue;
          offenders.push(`${file}:${i + 1}: ${m[0]}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });
});
