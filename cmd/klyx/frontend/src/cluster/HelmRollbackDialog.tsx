import { useState } from "react";
import type { HelmHistoryEntryDTO } from "../store/fleet";
import type { RollbackOptions } from "../bridge/helm";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";

const rollbackTimeouts = [60, 120, 300, 600];

// revLabel names a history entry as "rev N · chart (appVersion)". Chart is
// part of the value on purpose: when the target revision runs a different
// chart version, that difference is the interesting fact of the rollback.
function revLabel(rev: number, e?: HelmHistoryEntryDTO): string {
  if (!e) return `rev ${rev}`;
  return `rev ${e.revision} · ${e.chart}${e.appVersion ? ` (${e.appVersion})` : ""}`;
}

// RollbackComposerDialog assembles the helm rollback flags per rollback:
// wait-for-readiness (on by default, matching helm's safest interactive use)
// and hook skipping (off by default). Helm creates a NEW revision from the
// target's manifest - the from → to tiles show exactly what changes.
export function RollbackComposerDialog({ cluster, namespace, name, current, target, targetRevision, isProtected, onCancel, onConfirm }: {
  cluster: string;
  namespace: string;
  name: string;
  current?: HelmHistoryEntryDTO;
  target?: HelmHistoryEntryDTO;
  targetRevision: number;
  isProtected: boolean;
  onCancel: () => void;
  onConfirm: (opts: RollbackOptions) => void;
}) {
  const [wait, setWait] = useState(true);
  const [noHooks, setNoHooks] = useState(false);
  const [timeoutSeconds, setTimeoutSeconds] = useState(120);

  return (
    <OperationDialog
      title={`Roll back "${name}"`}
      ariaLabel={`Roll back ${name}`}
      subtitle={`helm rollback: re-deploys ${namespace}/${name} from revision ${targetRevision}'s manifest as a new revision.`}
      cluster={cluster}
      protected={isProtected}
      confirmLabel="Roll back"
      danger
      onCancel={onCancel}
      onConfirm={() => onConfirm({ wait, noHooks, timeoutSeconds })}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <MetaTile label="from (current)" value={revLabel(current?.revision ?? 0, current)} />
        <MetaTile label="to (target)" value={revLabel(targetRevision, target)} />
      </div>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={composerLabel}>options</div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
          <OptionTile
            active={wait}
            title="Wait for readiness"
            body="Block until rolled-back resources are ready (--wait); a failed rollout surfaces as a failed rollback instead of a silent one."
            onClick={() => setWait((v) => !v)}
          />
          <OptionTile
            active={noHooks}
            title="Skip hooks"
            body="Skip pre/post-rollback hooks (--no-hooks); use when a broken hook is what you are rolling back from."
            onClick={() => setNoHooks((v) => !v)}
          />
        </div>
      </div>

      <label style={{ display: "flex", alignItems: "center", gap: 10, opacity: wait ? 1 : 0.5 }}>
        <span style={composerLabel}>timeout</span>
        <select
          aria-label="rollback timeout"
          value={timeoutSeconds}
          disabled={!wait}
          onChange={(e) => setTimeoutSeconds(Number(e.target.value))}
          style={composerSelect}
        >
          {rollbackTimeouts.map((t) => <option key={t} value={t}>{t}s</option>)}
        </select>
        {!wait && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>only applies when waiting for readiness</span>}
      </label>
    </OperationDialog>
  );
}

const composerLabel: React.CSSProperties = { color: "var(--color-text-tertiary)", fontSize: 10, textTransform: "uppercase", fontWeight: 600, letterSpacing: 0.2 };
const composerSelect: React.CSSProperties = { padding: "4px 8px", fontSize: 12, fontFamily: "var(--font-mono)", background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" };
