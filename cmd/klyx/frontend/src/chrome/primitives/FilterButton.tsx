// FilterButton: a toggleable filter pill with an optional dim count, replacing
// the FilterButton/ModeButton copies in GitOps/NodesView/GatewayApiView/CertsView.
// A real <button> (inherits the global hover/active/focus rules); active reads as
// an info-tinted fill — the clearest "selected" treatment of the prior variants.
// label + count are separate spans so the count stays de-emphasised and tests can
// still match the label text.
export function FilterButton({ label, active, count, onClick }: {
  label: string;
  active: boolean;
  count?: number;
  onClick: () => void;
}) {
  // Active = pressed INTO a well (Signal Bench): a filter that is on reads as
  // a latched switch, not a lit pill. Non-bench themes fall back to the old
  // info tint via the token fallbacks.
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 5,
        fontSize: "var(--text-xs)",
        padding: "3px 9px",
        borderRadius: "var(--radius-sm)",
        cursor: "pointer",
        border: `var(--border-hairline) solid ${active ? "transparent" : "var(--color-border-tertiary)"}`,
        background: active ? "var(--bench-well-bg, var(--color-background-info))" : "var(--color-background-primary)",
        boxShadow: active ? "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-info))" : undefined,
        color: active ? "var(--color-text-info)" : "var(--color-text-secondary)",
        fontWeight: active ? 600 : undefined,
      }}
    >
      <span>{label}</span>
      {count != null && count > 0 && <span style={{ fontSize: "var(--text-2xs)", opacity: 0.7 }}>{count}</span>}
    </button>
  );
}
