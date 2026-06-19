// JS mirror of the CSS-var design tokens in src/theme/tokens.css, for inline
// styles. Values are `var(--…)` strings so a single source (the CSS) drives both
// the stylesheet and inline-style usage, and theming stays automatic.

export const space = {
  1: "var(--space-1)",
  2: "var(--space-2)",
  3: "var(--space-3)",
  4: "var(--space-4)",
  5: "var(--space-5)",
  6: "var(--space-6)",
} as const;

export const text = {
  "2xs": "var(--text-2xs)",
  xs: "var(--text-xs)",
  sm: "var(--text-sm)",
  md: "var(--text-md)",
} as const;

export const radius = {
  xs: "var(--radius-xs)",
  sm: "var(--radius-sm)",
  md: "var(--border-radius-md)",
  lg: "var(--border-radius-lg)",
} as const;

export const motion = {
  fast: "var(--motion-fast)",
  base: "var(--motion-base)",
} as const;

export const hairline = "var(--border-hairline)";
