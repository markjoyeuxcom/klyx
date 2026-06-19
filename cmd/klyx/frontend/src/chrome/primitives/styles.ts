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
// the Flux / Argo / Helm / cert-manager lenses. Distinct from `stateColor`
// (cluster/stateColors.ts), which is cluster *connection* state.
export const readyColor: Record<string, string> = {
  Ready: "var(--color-text-success)",
  Reconciling: "var(--color-text-info)",
  Issuing: "var(--color-text-info)",
  Failed: "var(--color-text-danger)",
  Unknown: "var(--color-text-tertiary)",
};

export const ellipsis: CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" };
export const mono: CSSProperties = { fontFamily: "var(--font-mono)" };

// frame: the standard card surface — a hairline edge, md radius, primary bg.
export const frame: CSSProperties = {
  border: "var(--border-hairline) solid var(--color-border-tertiary)",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-primary)",
};

// btn: the small secondary control (filter inputs, row actions). Inherits the
// global hover/active/disabled/focus rules from tokens.css — keep it a <button>.
export const btn: CSSProperties = {
  fontSize: "var(--text-xs)",
  padding: "3px 10px",
  borderRadius: "var(--radius-sm)",
  cursor: "pointer",
  border: "var(--border-hairline) solid var(--color-border-tertiary)",
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
