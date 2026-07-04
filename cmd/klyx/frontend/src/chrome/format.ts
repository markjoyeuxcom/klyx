// compactCount renders unbounded counters in bounded space: "×526k", "×1.1M".
// Kubernetes dedupes recurring events by bumping count, so a steadily
// reconciling Flux resource crosses six figures within months - a raw render
// overflows its fixed column. One decimal below 10 of a unit, integer above;
// rounding that crosses a unit boundary promotes (999,500 -> "1M", never
// "1000k"). Callers put the exact value in a title tooltip.
const UNITS = [
  { v: 1e3, s: "k" },
  { v: 1e6, s: "M" },
  { v: 1e9, s: "B" },
];

export function compactCount(n: number): string {
  if (!Number.isFinite(n)) return "0";
  if (n < 1000) return String(n);

  let i = UNITS.length - 1;
  while (i > 0 && n < UNITS[i].v) i--;
  let scaled = n / UNITS[i].v;
  // 999.5k rounds to 1000k - that reads as the next unit, so promote it.
  if (Math.round(scaled) >= 1000 && i < UNITS.length - 1) {
    i++;
    scaled = n / UNITS[i].v;
  }
  const str = scaled < 10 ? scaled.toFixed(1).replace(/\.0$/, "") : String(Math.round(scaled));
  return `${str}${UNITS[i].s}`;
}
