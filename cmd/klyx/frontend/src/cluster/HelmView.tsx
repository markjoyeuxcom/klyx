import { useEffect, useCallback, useState } from "react";
import { SkeletonRows, btn, ellipsis } from "../chrome/primitives";
import { useFleet } from "../store/fleet";
import type { HelmReleaseDTO, HelmHistoryEntryDTO } from "../store/fleet";
import { listHelmReleases, openHelmRelease, helmRollback } from "../bridge/helm";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { useResizablePanel } from "../chrome/useResizablePanel";

// Helm status color map — deployed/failed/superseded/pending-*
const statusColor = (status: string): string => {
  switch (status) {
    case "deployed":
      return "var(--color-text-success)";
    case "failed":
      return "var(--color-text-danger)";
    case "superseded":
      return "var(--color-text-tertiary)";
    default:
      // pending-install / pending-upgrade / pending-rollback / uninstalling
      return "var(--color-text-warning)";
  }
};

const statusDot = (status: string): string => {
  switch (status) {
    case "deployed":
      return "var(--color-text-success)";
    case "failed":
      return "var(--color-text-danger)";
    case "superseded":
      return "var(--color-text-tertiary)";
    default:
      return "var(--color-text-warning)";
  }
};

function fmtUnix(unix: number): string {
  if (!unix || unix === 0) return "—";
  const secs = Math.floor(Date.now() / 1000) - unix;
  if (secs < 0) return "—";
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

// columns: dot | name | namespace | chart | appVersion | revision | updated
const gridCols = "12px minmax(0,1.4fr) minmax(0,0.8fr) minmax(0,1.2fr) 80px 52px 80px";

export function HelmView({ cluster }: { cluster: string }) {
  const helm = useFleet((s) => s.helm);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  useEffect(() => {
    void listHelmReleases(cluster);
    return () => { useFleet.getState().clearHelm(); };
  }, [cluster]);

  const onRefresh = () => { void listHelmReleases(cluster); };

  // availability gate
  if (!helm.loading && !helm.available) {
    const displayMsg = helm.message.includes("helm not found")
      ? "helm not found - install helm or set KLYX_HELM_PATH to inspect releases"
      : helm.message || "helm not available for this cluster";
    return (
      <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>
        {displayMsg}
      </div>
    );
  }

  return (
    <div style={{ padding: "16px 20px", display: "flex", gap: 0, height: "100%", boxSizing: "border-box" }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        {/* Controls row */}
        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
          <button onClick={onRefresh} style={btn}>refresh</button>
        </div>

        {/* List */}
        {helm.loading && helm.releases.length === 0 ? (
          <SkeletonRows rows={6} label="loading releases" />
        ) : helm.releases.length === 0 ? (
          <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No Helm releases found.</div>
        ) : (
          <div style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>
            {/* Header */}
            <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "0 8px 6px", fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)" }}>
              <span /><span>release</span><span>namespace</span><span>chart</span><span>app version</span><span>rev</span><span>updated</span>
            </div>
            {/* Rows */}
            {helm.releases.map((r) => {
              const isSelected = helm.selected?.namespace === r.namespace && helm.selected?.name === r.name;
              return (
                <div
                  key={`${r.namespace}/${r.name}`}
                  onClick={() => void openHelmRelease(cluster, r.namespace, r.name)}
                  style={{
                    display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center",
                    padding: "7px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)",
                    cursor: "pointer",
                    background: isSelected ? "var(--color-background-secondary)" : undefined,
                  }}
                >
                  <span style={{ width: 8, height: 8, borderRadius: "50%", background: statusDot(r.status), display: "inline-block" }} />
                  <span style={{ fontWeight: 500, ...ellipsis }} title={r.name}>{r.name}</span>
                  <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }} title={r.namespace}>{r.namespace}</span>
                  <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }} title={r.chart}>{r.chart}</span>
                  <span style={{ color: "var(--color-text-secondary)", ...ellipsis }} title={r.appVersion}>{r.appVersion || "—"}</span>
                  <span style={{ color: "var(--color-text-secondary)" }}>{r.revision}</span>
                  <span style={{ color: "var(--color-text-tertiary)" }}>{fmtUnix(r.updatedUnix)}</span>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* Detail panel */}
      {helm.selected && (
        <HelmDetailPanel
          cluster={cluster}
          namespace={helm.selected.namespace}
          name={helm.selected.name}
          releases={helm.releases}
          history={helm.history}
          values={helm.values}
          detailLoading={helm.detailLoading}
          isProtected={isProtected}
          onClose={() => useFleet.getState().selectHelmRelease(null)}
        />
      )}
    </div>
  );
}

type RollbackPending = { revision: number };

function HelmDetailPanel({
  cluster, namespace, name, releases, history, values, detailLoading, isProtected, onClose,
}: {
  cluster: string;
  namespace: string;
  name: string;
  releases: HelmReleaseDTO[];
  history: HelmHistoryEntryDTO[];
  values: string;
  detailLoading: boolean;
  isProtected: boolean;
  onClose: () => void;
}) {
  const [rollbackPending, setRollbackPending] = useState<RollbackPending | null>(null);
  const { width, handleProps } = useResizablePanel();

  // Current revision = max revision in history, or fallback from release list
  const currentRevision = history.length > 0
    ? Math.max(...history.map((e) => e.revision))
    : (releases.find((r) => r.namespace === namespace && r.name === name)?.revision ?? 0);

  const handleKey = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape") {
      if (rollbackPending) { setRollbackPending(null); return; }
      onClose();
    }
  }, [onClose, rollbackPending]);

  useEffect(() => {
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [handleKey]);

  // Find the current release for the header
  const release = releases.find((r) => r.namespace === namespace && r.name === name);

  return (
    <div style={{
      width, flexShrink: 0, position: "relative",
      borderLeft: "0.5px solid var(--color-border-tertiary)",
      overflowY: "auto",
      fontFamily: "var(--font-mono)",
      fontSize: 12,
      paddingLeft: 16,
      marginLeft: 16,
    }}>
      {/* Resize handle — drag left edge */}
      <div {...handleProps} />

      {/* Panel header */}
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12, position: "sticky", top: 0, background: "var(--color-background-primary)", paddingTop: 2, paddingBottom: 6, borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            {release && (
              <span style={{ width: 8, height: 8, borderRadius: "50%", background: statusDot(release.status), display: "inline-block", flexShrink: 0 }} />
            )}
            <span style={{ fontWeight: 500, ...ellipsis }} title={`${namespace}/${name}`}>
              <span style={{ color: "var(--color-text-tertiary)" }}>{namespace}</span>/{name}
            </span>
          </div>
          {release && (
            <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginTop: 2 }}>
              <span style={{ color: statusColor(release.status) }}>{release.status}</span>
              {" · "}{release.chart}
              {release.appVersion ? ` · ${release.appVersion}` : ""}
            </div>
          )}
        </div>
        <button onClick={onClose} style={{ ...btn, padding: "2px 8px", fontSize: 12, flexShrink: 0 }}>✕</button>
      </div>

      {detailLoading && history.length === 0 ? (
        <div style={{ color: "var(--color-text-secondary)" }}>Loading detail…</div>
      ) : (
        <>
          {/* History section */}
          <HelmSection title="History">
            {history.length === 0 ? (
              <span style={{ color: "var(--color-text-tertiary)" }}>No history available.</span>
            ) : (
              <div>
                {/* History header */}
                <div style={{ display: "grid", gridTemplateColumns: "40px 80px minmax(0,1fr) minmax(0,1.5fr) 70px 60px", gap: 8, padding: "0 0 4px", fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-tertiary)", marginBottom: 4 }}>
                  <span>rev</span><span>status</span><span>chart</span><span>description</span><span>updated</span><span />
                </div>
                {/* History rows — newest first */}
                {[...history].sort((a, b) => b.revision - a.revision).map((e) => {
                  const isCurrent = e.revision === currentRevision;
                  return (
                    <div
                      key={e.revision}
                      style={{ display: "grid", gridTemplateColumns: "40px 80px minmax(0,1fr) minmax(0,1.5fr) 70px 60px", gap: 8, alignItems: "center", padding: "5px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}
                    >
                      <span style={{ color: isCurrent ? "var(--color-text-primary)" : "var(--color-text-tertiary)", fontWeight: isCurrent ? 600 : 400 }}>{e.revision}</span>
                      <span style={{ color: statusColor(e.status), ...ellipsis }} title={e.status}>{e.status}</span>
                      <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }} title={e.chart}>{e.chart}</span>
                      <span style={{ color: "var(--color-text-secondary)", ...ellipsis }} title={e.description}>{e.description || "—"}</span>
                      <span style={{ color: "var(--color-text-tertiary)" }}>{fmtUnix(e.updatedUnix)}</span>
                      <span>
                        {!isCurrent && (
                          <button
                            onClick={() => setRollbackPending({ revision: e.revision })}
                            style={{ ...btn, fontSize: 9, padding: "2px 6px", color: "var(--color-text-warning)", borderColor: "var(--color-text-warning)" }}
                          >
                            rollback
                          </button>
                        )}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </HelmSection>

          {/* Values section */}
          <HelmSection title="Values">
            <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 6, fontFamily: "var(--font-sans, sans-serif)" }}>
              values may contain sensitive data
            </div>
            {values === "" ? (
              <span style={{ color: "var(--color-text-tertiary)" }}>no user-supplied values</span>
            ) : (
              <pre style={{
                margin: 0, padding: 10,
                background: "var(--color-background-secondary)",
                border: "0.5px solid var(--color-border-tertiary)",
                borderRadius: 4,
                fontFamily: "var(--font-mono)", fontSize: 11, lineHeight: 1.5,
                overflowX: "auto", overflowY: "auto", maxHeight: 400,
                whiteSpace: "pre-wrap", wordBreak: "break-all",
                color: "var(--color-text-primary)",
              }}>{values}</pre>
            )}
          </HelmSection>
        </>
      )}

      {/* Rollback confirm dialog */}
      {rollbackPending && (
        <ConfirmDialog
          title="rollback release"
          cluster={cluster}
          detail={`roll back ${namespace}/${name} to revision ${rollbackPending.revision}? helm will create a new revision.`}
          protected={isProtected}
          danger={false}
          confirmLabel="Rollback"
          onCancel={() => setRollbackPending(null)}
          onConfirm={() => {
            const { revision } = rollbackPending;
            setRollbackPending(null);
            void helmRollback(cluster, namespace, name, revision);
          }}
        />
      )}
    </div>
  );
}

function HelmSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>{title}</div>
      {children}
    </div>
  );
}
