import { describe, it, expect } from "vitest";

// tokens.css is read from disk: CSS imports are stubbed under vitest, and the
// app's tsconfig has no Node types, so fs is loaded by a computed specifier.
const fsModule = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fsModule)) as { readFileSync: (p: URL, enc: "utf8") => string };
// vitest runs from the frontend root; under jsdom import.meta.url is not a
// file URL, so the path is resolved from the working directory.
const cwd = (globalThis as unknown as { process: { cwd(): string } }).process.cwd();
const css = readFileSync(new URL(`file://${cwd}/src/theme/tokens.css`), "utf8");

// UI review 2026-10-13: readable text in the default bench themes. Measured
// on the surfaces text sits on (module, panel, well), WCAG AA for body text.

function block(theme: string): Record<string, string> {
  const start = css.indexOf(`[data-theme="${theme}"] {`);
  const body = css.slice(start, css.indexOf("}", start));
  return Object.fromEntries([...body.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})/g)].map((m) => [m[1], m[2]]));
}
function lum(hex: string): number {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}
const ratio = (a: string, b: string) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

describe("bench theme text contrast", () => {
  for (const theme of ["bench", "bench-light"]) {
    it(`${theme}: secondary and tertiary text pass 4.5:1 on module, panel and well`, () => {
      const t = block(theme);
      const surfaces = [t["color-background-primary"], t["color-background-secondary"], t["bench-well-bg"]];
      for (const s of surfaces) {
        expect(s).toBeTruthy();
        expect(ratio(t["color-text-secondary"], s)).toBeGreaterThanOrEqual(4.5);
        expect(ratio(t["color-text-tertiary"], s)).toBeGreaterThanOrEqual(4.5);
      }
      // the Syncing chip: secondary text on the panel colour, never dimmed
      expect(ratio(t["color-text-secondary"], t["color-background-secondary"])).toBeGreaterThanOrEqual(4.5);
    });
  }
});

// The Line (spec 2026-10-14 §2.1): night and day, measured on every surface
// text and strokes sit on - ground, raised, the step above raised, and sunk.
// Text and syntax at 4.5:1; meaningful strokes (track, route, control, solid
// danger and caution) at 3:1; diff text on its own fill, composited.
function rgba(theme: string, name: string): [number, number, number, number] {
  const start = css.indexOf(`[data-theme="${theme}"] {`);
  const body = css.slice(start, css.indexOf("}", start));
  const m = body.match(new RegExp(`--${name}:\\s*rgba\\((\\d+),\\s*(\\d+),\\s*(\\d+),\\s*([\\d.]+)\\)`));
  if (!m) throw new Error(`${theme}: no rgba --${name}`);
  return [Number(m[1]), Number(m[2]), Number(m[3]), Number(m[4])];
}
const hex = (rgb: number[]) => "#" + rgb.map((v) => Math.round(v).toString(16).padStart(2, "0")).join("");
const over = ([r, g, b, a]: [number, number, number, number], under: string) => {
  const u = [1, 3, 5].map((i) => parseInt(under.slice(i, i + 2), 16));
  return hex([r, g, b].map((c, i) => c * a + u[i] * (1 - a)));
};

describe("the line themes", () => {
  for (const theme of ["night", "day"]) {
    const t = block(theme);
    const surfaces = ["line-ground", "line-raised", "line-raised-2", "line-sunk"];
    it(`${theme}: text, status and code at 4.5:1 on every surface`, () => {
      for (const s of surfaces) {
        expect(t[s]).toBeTruthy();
        for (const fg of ["line-ink-1", "line-ink-2", "line-ink-3", "line-danger", "line-caution", "line-route", "line-diff-add", "line-diff-del", "line-syntax-key", "line-syntax-str", "line-syntax-num"]) {
          expect(ratio(t[fg], t[s]), `${fg} on ${s}`).toBeGreaterThanOrEqual(4.5);
        }
      }
      expect(ratio(t["line-route-ink"], t["line-route"])).toBeGreaterThanOrEqual(4.5);
    });
    it(`${theme}: meaningful strokes at 3:1 on every surface`, () => {
      for (const s of surfaces) {
        for (const stroke of ["line-track", "line-route", "line-control", "line-danger", "line-caution"]) {
          expect(ratio(t[stroke], t[s]), `${stroke} on ${s}`).toBeGreaterThanOrEqual(3);
        }
      }
    });
    it(`${theme}: diff text on its own fill, composited on every surface`, () => {
      for (const s of surfaces) {
        expect(ratio(t["line-diff-add"], over(rgba(theme, "line-diff-add-bg"), t[s]))).toBeGreaterThanOrEqual(4.5);
        expect(ratio(t["line-diff-del"], over(rgba(theme, "line-diff-del-bg"), t[s]))).toBeGreaterThanOrEqual(4.5);
      }
    });
    it(`${theme}: healthy carries no colour; nothing glows`, () => {
      const start = css.indexOf(`[data-theme="${theme}"] {`);
      const body = css.slice(start, css.indexOf("}", start));
      expect(body).toMatch(/--color-text-success:\s*var\(--line-ink-2\)/);
      expect(body).toMatch(/--color-background-success:\s*transparent/);
      expect(body).toMatch(/--lamp-glow-danger:\s*none/);
      expect(body).toMatch(/--lamp-glow-warning:\s*none/);
    });
  }
  it("no lamp blinks anywhere", () => {
    expect(css).not.toMatch(/klyx-lamp-blink/);
  });
});
