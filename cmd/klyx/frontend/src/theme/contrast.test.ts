import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// UI review 2026-10-13: readable text in the default bench themes. Measured
// on the surfaces text sits on (module, panel, well), WCAG AA for body text.
const css = readFileSync(fileURLToPath(new URL("./tokens.css", import.meta.url)), "utf8");

function block(theme: string): Record<string, string> {
  const start = css.indexOf(`[data-theme="${theme}"]`);
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
