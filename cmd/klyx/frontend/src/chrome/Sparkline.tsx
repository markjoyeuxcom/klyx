import { useId } from "react";

// Sparkline — a tiny pure-SVG line for 30m metric series. No chart library,
// no animation, no axes. Honesty rules: x is mapped by TIMESTAMP (not index),
// and the line BREAKS where Prometheus skipped steps — a gap renders as a gap,
// never an interpolated bridge. Empty input renders a muted "no data" label.

export type SparkPoint = { t: number; v: number };

export function Sparkline({
  points,
  width = 120,
  height = 24,
  stepSeconds = 60,
  stroke = "var(--color-text-info)",
  domain,
  hatch,
  label = "metric sparkline",
  emptyText = "no data",
}: {
  points: SparkPoint[];
  // domain fixes the time axis (The Line slice 8d): the whole window keeps
  // its width, so missing time and a hatched span are never compressed.
  domain?: [number, number];
  // hatch marks a span of the axis (an unattributable region) as hatched.
  hatch?: [number, number];
  label?: string;
  emptyText?: string;
  width?: number;
  height?: number;
  // Expected sample spacing; consecutive points further apart than 1.5 steps
  // start a new segment (the visible gap).
  stepSeconds?: number;
  stroke?: string;
}) {
  const hatchId = `spark-hatch-${useId().replace(/:/g, "")}`;
  if (points.length === 0 && !domain) {
    return (
      <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{emptyText}</span>
    );
  }

  const pad = 2;
  const minT = domain ? domain[0] : points[0].t;
  const maxT = domain ? domain[1] : points[points.length - 1].t;
  const spanT = Math.max(1, maxT - minT);
  let maxV = 0;
  for (const p of points) if (p.v > maxV) maxV = p.v;
  // Flat-zero series still draws a line along the baseline.
  const spanV = maxV === 0 ? 1 : maxV;

  const x = (t: number) => pad + ((t - minT) / spanT) * (width - 2 * pad);
  const y = (v: number) => height - pad - (v / spanV) * (height - 2 * pad);

  // Split into segments at gaps.
  const segments: SparkPoint[][] = [];
  if (points.length > 0) {
    let cur: SparkPoint[] = [points[0]];
    for (let i = 1; i < points.length; i++) {
      if (points[i].t - points[i - 1].t > 1.5 * stepSeconds) {
        segments.push(cur);
        cur = [];
      }
      cur.push(points[i]);
    }
    segments.push(cur);
  }
  const clampX = (t: number) => Math.min(width - pad, Math.max(pad, x(t)));

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={label}
      style={{ display: "block", flexShrink: 0 }}
    >
      {hatch && hatch[1] > hatch[0] && (
        <>
          <defs>
            <pattern id={hatchId} width="4" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <line x1="0" y1="0" x2="0" y2="4" stroke="var(--color-text-tertiary)" strokeWidth="1" opacity="0.5" />
            </pattern>
          </defs>
          <rect data-hatch x={clampX(hatch[0])} y={0} width={Math.max(0, clampX(hatch[1]) - clampX(hatch[0]))} height={height} fill={`url(#${hatchId})`} />
        </>
      )}
      {points.length === 0 && <text x={width / 2} y={height / 2 + 3} textAnchor="middle" fontSize="9" fill="var(--color-text-tertiary)">{emptyText}</text>}
      {segments.map((seg, i) =>
        seg.length === 1 ? (
          // A lone point between gaps still deserves a mark.
          <circle key={i} cx={x(seg[0].t)} cy={y(seg[0].v)} r={1.2} fill={stroke} />
        ) : (
          <polyline
            key={i}
            points={seg.map((p) => `${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ")}
            fill="none"
            stroke={stroke}
            strokeWidth={1.2}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ),
      )}
    </svg>
  );
}
