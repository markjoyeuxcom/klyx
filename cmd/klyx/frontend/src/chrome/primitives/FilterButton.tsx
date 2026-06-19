// FilterButton: a toggleable filter pill with an optional count, replacing the
// FilterButton/ModeButton copies in GitOps/NodesView/NetworkView/CertsView. A
// real <button>, so it inherits the global hover/active/focus rules.
export function FilterButton({ label, active, count, onClick }: {
  label: string;
  active: boolean;
  count?: number;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      style={{
        fontSize: "var(--text-xs)",
        padding: "3px 9px",
        borderRadius: "var(--radius-sm)",
        cursor: "pointer",
        border: `var(--border-hairline) solid ${active ? "var(--color-text-info)" : "var(--color-border-tertiary)"}`,
        background: active ? "var(--color-background-secondary)" : "var(--color-background-primary)",
        color: active ? "var(--color-text-primary)" : "var(--color-text-secondary)",
      }}
    >
      {label}{count != null && count > 0 ? ` ${count}` : ""}
    </button>
  );
}
