// FKeysBar — the keyboard made physical (Signal Bench, htop heritage): a
// permanent footer row naming the list's key bindings, plus a right-aligned
// status readout ("14 of 214 · broken first"). useListKeys owns the actual
// bindings; this just makes them visible instead of discoverable-by-accident.
export function FKeysBar({ keys, status }: {
  keys: ReadonlyArray<readonly [string, string]>;
  status?: string;
}) {
  return (
    <div
      data-testid="fkeys-bar"
      style={{
        flexShrink: 0,
        display: "flex",
        gap: 2,
        padding: "5px 8px",
        alignItems: "center",
        borderTop: "1px solid var(--color-border-tertiary)",
      }}
    >
      {keys.map(([k, what]) => (
        <span key={k} style={{ display: "flex", alignItems: "center", gap: 7, padding: "2px 10px", fontSize: 10, color: "var(--color-text-secondary)" }}>
          <kbd
            style={{
              fontFamily: "var(--font-mono)",
              fontSize: 9,
              color: "var(--color-text-secondary)",
              background: "var(--color-background-secondary)",
              borderRadius: 3,
              padding: "0 5px",
              border: "1px solid var(--color-border-secondary)",
              borderTop: "1px solid var(--bench-edge-hi, var(--color-border-secondary))",
              borderBottom: "2px solid var(--bench-edge-lo, var(--color-border-secondary))",
            }}
          >{k}</kbd>
          {what}
        </span>
      ))}
      {status && (
        <span style={{ marginLeft: "auto", fontFamily: "var(--font-mono)", fontSize: 9, letterSpacing: "0.08em", textTransform: "uppercase", color: "var(--color-text-tertiary)", paddingRight: 6, whiteSpace: "nowrap" }}>
          {status}
        </span>
      )}
    </div>
  );
}
