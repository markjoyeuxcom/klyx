// Staleness visual language (owner-ratified 2026-07-02): when a resource's
// spec has changed but the controller has not observed it yet
// (metadata.generation > observedGeneration), its Ready/Programmed state
// describes the PREVIOUS spec. The status chip is replaced by a grey
// "Syncing…" chip whose tooltip names the generations, so the at-a-glance
// signal never reads as confidently healthy while changes are pending.
//
// Since The Line slice 11a nothing is dimmed: a pending row whose verdict
// would draw no mark takes the dashed syncing mark (VerdictMark), and its
// status cell carries the neutral "Syncing…" chip. A mark carries meaning,
// so it keeps its 3:1 contrast; an earlier whole-row 0.6 dim took text below
// a readable contrast (UI review 2026-10-13), and the later mark-only dim
// is retired too.

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
