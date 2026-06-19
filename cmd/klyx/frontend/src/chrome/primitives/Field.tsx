import { type Tone, toneColor, ellipsis, mono } from "./styles";

// Field: a label/value row for detail panels (right-aligned mono value).
export function Field({ label, value, tone = "muted" }: { label: string; value: string; tone?: Tone }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", gap: "var(--space-4)", fontSize: "var(--text-xs)" }}>
      <span style={{ color: "var(--color-text-tertiary)" }}>{label}</span>
      <span style={{ ...mono, ...ellipsis, color: tone === "muted" ? "var(--color-text-secondary)" : toneColor[tone], textAlign: "right" }}>{value}</span>
    </div>
  );
}
