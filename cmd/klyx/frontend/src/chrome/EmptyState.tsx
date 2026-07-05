// EmptyState — the designed "nothing here" block: muted icon, one-line title,
// optional hint, optional action. Replaces bare "No pods." strings so empty
// views read as intentional, not unfinished. Honesty rule: the title states
// what IS (no pods), the hint says what would change it - never a cheerful
// filler. When the emptiness is filter-made, the action ("Clear filters")
// undoes it in one click instead of making the user hunt the chips.
export function EmptyState({
  icon,
  title,
  hint,
  action,
}: {
  icon?: React.ReactNode;
  title: string;
  hint?: string;
  action?: { label: string; onClick: () => void };
}) {
  return (
    <div
      data-testid="empty-state"
      style={{
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        gap: 6,
        padding: "56px 20px",
        textAlign: "center",
      }}
    >
      {icon && <div style={{ color: "var(--color-text-tertiary)", opacity: 0.6, marginBottom: 2 }}>{icon}</div>}
      <div style={{ fontSize: 13, color: "var(--color-text-secondary)" }}>{title}</div>
      {hint && <div style={{ fontSize: 11, color: "var(--color-text-tertiary)", maxWidth: 400, lineHeight: 1.5 }}>{hint}</div>}
      {action && (
        <button
          onClick={action.onClick}
          style={{
            marginTop: 6,
            fontSize: 11,
            padding: "3px 10px",
            borderRadius: "var(--radius-sm)",
            border: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-primary)",
            color: "var(--color-text-secondary)",
            cursor: "pointer",
          }}
        >
          {action.label}
        </button>
      )}
    </div>
  );
}
