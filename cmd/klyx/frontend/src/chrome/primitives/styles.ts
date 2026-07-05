import type { CSSProperties } from "react";

// One tone system for every chip/badge/cell/field, replacing the per-view
// `toneColor`/`readyColor`/`syncColor` maps that had drifted apart.
export type Tone = "success" | "warning" | "danger" | "info" | "muted" | "neutral";

export const toneColor: Record<Tone, string> = {
  success: "var(--color-text-success)",
  warning: "var(--color-text-warning)",
  danger: "var(--color-text-danger)",
  info: "var(--color-text-info)",
  muted: "var(--color-text-tertiary)",
  neutral: "var(--color-text-primary)",
};

export const toneBg: Record<Tone, string> = {
  success: "var(--color-background-success)",
  warning: "var(--color-background-warning)",
  danger: "var(--color-background-danger)",
  info: "var(--color-background-info)",
  muted: "var(--color-background-secondary)",
  neutral: "var(--color-background-secondary)",
};

export const toneBorder: Record<Tone, string> = {
  success: "var(--color-border-success)",
  warning: "var(--color-border-warning)",
  danger: "var(--color-border-danger)",
  info: "var(--color-border-info)",
  muted: "var(--color-border-tertiary)",
  neutral: "var(--color-border-tertiary)",
};

// readyColor maps a reconciliation/health state word to a text colour. Shared by
// the Flux / Argo / Helm / cert-manager / external-secrets lenses. Distinct from
// `stateColor` (cluster/stateColors.ts), which is cluster *connection* state.
export const readyColor: Record<string, string> = {
  Ready: "var(--color-text-success)",
  Synced: "var(--color-text-success)",
  Reconciling: "var(--color-text-info)",
  Issuing: "var(--color-text-info)",
  Failed: "var(--color-text-danger)",
  Unknown: "var(--color-text-tertiary)",
};

export const ellipsis: CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" };
export const mono: CSSProperties = { fontFamily: "var(--font-mono)" };

// frame: the module face (Signal Bench) — machined top edge + drop shadow.
// "Wells hold data, modules hold chrome": panels/navs/dialogs are modules.
// Non-bench themes have no machining tokens; the fallbacks render the old
// flat hairline card.
export const frame: CSSProperties = {
  border: "var(--border-hairline) solid var(--color-border-tertiary)",
  borderTop: "1px solid var(--bench-edge-hi, var(--color-border-tertiary))",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-primary)",
  boxShadow: "var(--bench-module-shadow, none)",
};

// well: the inset data surface (Signal Bench) — lists, log panes, counter
// windows, command previews and inputs sit IN the bench, not on it.
export const well: CSSProperties = {
  background: "var(--bench-well-bg, var(--color-background-secondary))",
  borderRadius: "var(--radius-sm)",
  boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))",
};

// inputWell / selectWell: form controls sit IN the bench like every other
// data surface - inset, mono, no OS chrome fighting the module face. The
// select keeps a neutral chevron via a data-URI (currentColor is not legal
// in url() images; the grey reads in both light and dark).
export const inputWell: CSSProperties = {
  fontFamily: "var(--font-mono)",
  fontSize: 11,
  padding: "4px 9px",
  border: "none",
  borderRadius: "var(--radius-sm)",
  background: "var(--bench-well-bg, var(--color-background-secondary))",
  boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))",
  color: "var(--color-text-primary)",
};

export const selectWell: CSSProperties = {
  ...inputWell,
  appearance: "none",
  WebkitAppearance: "none",
  cursor: "pointer",
  paddingRight: 22,
  backgroundImage: `url("data:image/svg+xml;charset=utf-8,%3Csvg xmlns='http://www.w3.org/2000/svg' width='8' height='5' viewBox='0 0 8 5'%3E%3Cpath d='M1 1l3 3 3-3' fill='none' stroke='%23888c92' stroke-width='1.4'/%3E%3C/svg%3E")`,
  backgroundRepeat: "no-repeat",
  backgroundPosition: "right 8px center",
};

// btn: the small secondary control (filter inputs, row actions), machined as
// a raised key on bench themes. Inherits the global hover/active/disabled/
// focus rules from tokens.css — keep it a <button>.
export const btn: CSSProperties = {
  fontSize: "var(--text-xs)",
  padding: "3px 10px",
  borderRadius: "var(--radius-sm)",
  cursor: "pointer",
  border: "var(--border-hairline) solid var(--color-border-tertiary)",
  borderTop: "1px solid var(--bench-edge-hi, var(--color-border-tertiary))",
  borderBottom: "1px solid var(--bench-edge-lo, var(--color-border-tertiary))",
  background: "var(--color-background-primary)",
  color: "var(--color-text-secondary)",
};

// actionBtn: a btn that reads at primary weight (the GitOps/Cert action rows).
export const actionBtn: CSSProperties = { ...btn, color: "var(--color-text-primary)" };

// twoPaneGrid: the list + inspector split shared by GitOps/CertsView/etc.
export const twoPaneGrid: CSSProperties = {
  flex: 1,
  minHeight: 0,
  display: "grid",
  gridTemplateColumns: "minmax(440px, 1.25fr) minmax(320px, 0.75fr)",
  gap: "var(--space-5)",
  alignItems: "stretch",
  overflow: "hidden",
};
