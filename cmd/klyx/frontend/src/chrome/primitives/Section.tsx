import type { ReactNode } from "react";

// Section: a labelled block in a detail/inspector panel, replacing the six local
// Section copies (GitOps/Overview/InstanceDetail/PodsView/CertsView/SettingsView).
// Spacing is uniform; parents space sections with their own flex `gap`.
export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <div style={{ fontSize: "var(--text-2xs)", color: "var(--color-text-tertiary)", marginBottom: "var(--space-2)" }}>{title}</div>
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-1)" }}>{children}</div>
    </div>
  );
}
