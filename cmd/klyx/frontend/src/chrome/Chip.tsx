// Chip is the small on/off filter toggle ("needs attention", "warnings only").
// tone colours the on-state: info (default) for plain filters, warning for
// filters that surface trouble.
export function Chip({ on, tone = "info", onClick, children }: {
  on: boolean;
  tone?: "info" | "warning";
  onClick: () => void;
  children: React.ReactNode;
}) {
  const text = tone === "warning" ? "var(--color-text-warning)" : "var(--color-text-info)";
  const bg = tone === "warning" ? "var(--color-background-warning, transparent)" : "var(--color-background-info, transparent)";
  // On-state edge is an inset ring of the full tone (Signal Bench), not a
  // translucent border; the border stays transparent so layout never shifts.
  return (
    <button onClick={onClick} style={{ fontSize: 10, padding: "3px 9px", borderRadius: 3, cursor: "pointer",
      border: on ? "0.5px solid transparent" : "0.5px solid var(--color-border-tertiary)",
      boxShadow: on ? `inset 0 0 0 1px ${text}` : undefined,
      background: on ? bg : "transparent",
      color: on ? text : "var(--color-text-tertiary)" }}>{children}</button>
  );
}
