// Staleness visual language (owner-ratified 2026-07-02): when a resource's
// spec has changed but the controller has not observed it yet
// (metadata.generation > observedGeneration), its Ready/Programmed state
// describes the PREVIOUS spec. The row dims ("dim = not yet realized") and
// the status chip is replaced by a grey "Syncing…" chip whose tooltip names
// the generations, so the at-a-glance signal never reads as confidently
// green while changes are pending.
//
// Since the UI review of 2026-10-13 the dim applies to the row's lamp only;
// its text stays at full strength and the neutral chip says "Syncing…". A
// whole-row (or every-text-cell) 0.6 dim took the identity, status and
// source text below a readable contrast, so a pending row looked disabled.
// The rule lives in layout.css (.lens-row[data-stale]).

export function staleTitle(generation?: number, observedGeneration?: number): string {
  return `Changes detected. Waiting for controller reconciliation (Observed Gen: ${observedGeneration ?? "?"} vs Current: ${generation ?? "?"}).`;
}

// staleProps marks a lens row whose changes are unrealized.
export function staleProps(stale?: boolean): { "data-stale"?: "true" } {
  return stale ? { "data-stale": "true" } : {};
}

// SyncingChip replaces the status chip while the controller catches up.
export function SyncingChip({ generation, observedGeneration }: { generation?: number; observedGeneration?: number }) {
  return (
    <span
      title={staleTitle(generation, observedGeneration)}
      style={{
        display: "inline-block",
        padding: "1px 7px",
        borderRadius: "var(--radius-sm)",
        border: "0.5px solid var(--color-border-tertiary)",
        background: "var(--color-background-secondary)",
        color: "var(--color-text-secondary)",
        fontSize: 10,
      }}
    >
      Syncing…
    </span>
  );
}
