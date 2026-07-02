import type { CSSProperties } from "react";

// Staleness visual language (owner-ratified 2026-07-02): when a resource's
// spec has changed but the controller has not observed it yet
// (metadata.generation > observedGeneration), its Ready/Programmed state
// describes the PREVIOUS spec. The row dims to 0.6 ("dim = not yet realized")
// and the status chip is replaced by a grey "Syncing…" chip whose tooltip
// names the generations, so the at-a-glance signal never reads as
// confidently green while changes are pending.

export function staleTitle(generation?: number, observedGeneration?: number): string {
  return `Changes detected. Waiting for controller reconciliation (Observed Gen: ${observedGeneration ?? "?"} vs Current: ${generation ?? "?"}).`;
}

// staleStyle dims a whole row while its changes are unrealized.
export function staleStyle(stale?: boolean): CSSProperties {
  return stale ? { opacity: 0.6 } : {};
}

// SyncingChip replaces the status chip while the controller catches up.
export function SyncingChip({ generation, observedGeneration }: { generation?: number; observedGeneration?: number }) {
  return (
    <span
      title={staleTitle(generation, observedGeneration)}
      style={{
        display: "inline-block",
        padding: "1px 7px",
        borderRadius: "var(--border-radius-sm)",
        border: "0.5px solid var(--color-border-tertiary)",
        background: "var(--color-background-secondary)",
        color: "var(--color-text-tertiary)",
        fontSize: 10,
      }}
    >
      Syncing…
    </span>
  );
}
