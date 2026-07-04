import { useEffect, useCallback, useMemo, useRef, useState } from "react";
import { IconAnchor, IconX } from "@tabler/icons-react";
import { EmptyState, FilterButton, SkeletonRows, SummaryCell, btn, ellipsis, frame } from "../chrome/primitives";
import { useFleet } from "../store/fleet";
import type { HelmReleaseDTO, HelmHistoryEntryDTO } from "../store/fleet";
import { listHelmReleases, openHelmRelease, helmRollback } from "../bridge/helm";
import type { RollbackOptions } from "../bridge/helm";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";
import { useResizablePanel } from "../chrome/useResizablePanel";
import { useListKeys } from "../chrome/useListKeys";
import { CopyButton } from "../chrome/CopyButton";
import { YamlBlock } from "../chrome/YamlBlock";
import { Age, UpdatedAt, useReceivedAt } from "../chrome/Age";
import { loadClusterPrefs, loadSearch, saveHelmViewPrefs, saveSearch } from "../store/persist";

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

// statusGroup buckets helm's statuses for triage: failed floats first,
// in-flight pending-*/uninstalling states next, settled releases last.
type HelmStatusGroup = "failed" | "pending" | "ok";
export function statusGroup(status: string): HelmStatusGroup {
  if (status === "failed") return "failed";
  if (status === "deployed" || status === "superseded") return "ok";
  return "pending";
}
const groupRank: Record<HelmStatusGroup, number> = { failed: 0, pending: 1, ok: 2 };

type HelmFilter = "all" | HelmStatusGroup;

function matchesQuery(r: HelmReleaseDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [r.name, r.namespace, r.chart, r.appVersion, r.status].some((v) => v.toLowerCase().includes(q));
}

// columns: dot | name | namespace | chart | appVersion | revision | updated
const gridCols = "12px minmax(0,1.4fr) minmax(0,0.8fr) minmax(0,1.2fr) 80px 52px 80px";

export function HelmView({ cluster }: { cluster: string }) {
  const helm = useFleet((s) => s.helm);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  // Filters follow the persistence rules of the other triage views: the
  // namespace survives a relaunch, the free-text query survives section
  // switches within this run.
  const [filter, setFilter] = useState<HelmFilter>("all");
  const [namespace, setNamespace] = useState<string>(() => loadClusterPrefs(cluster).helm?.namespace ?? "");
  const [query, setQuery] = useState<string>(() => loadSearch(cluster, "helm"));
  const [kbKey, setKbKey] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    void listHelmReleases(cluster);
    return () => { useFleet.getState().clearHelm(); };
  }, [cluster]);

  useEffect(() => {
    saveHelmViewPrefs(cluster, { namespace });
    saveSearch(cluster, "helm", query);
  }, [cluster, namespace, query]);

  const onRefresh = () => { void listHelmReleases(cluster); };

  const releases = helm.cluster === cluster ? helm.releases : [];
  const receivedAt = useReceivedAt(helm.cluster === cluster && !helm.loading ? helm.releases : null);
  const namespaces = useMemo(() => Array.from(new Set(releases.map((r) => r.namespace))).sort(), [releases]);
  // Broken-first: a failed release floats to the top like every other lens;
  // the backend returns helm's own ordering, which buries failures.
  const orderedReleases = useMemo(
    () => [...releases].sort((a, b) => groupRank[statusGroup(a.status)] - groupRank[statusGroup(b.status)] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [releases],
  );
  const visibleReleases = useMemo(
    () => orderedReleases.filter((r) => (!namespace || r.namespace === namespace) && (filter === "all" || statusGroup(r.status) === filter) && matchesQuery(r, query)),
    [orderedReleases, namespace, filter, query],
  );
  const failed = releases.filter((r) => statusGroup(r.status) === "failed").length;
  const pendingCount = releases.filter((r) => statusGroup(r.status) === "pending").length;
  const deployed = releases.filter((r) => statusGroup(r.status) === "ok").length;

  // Keyboard parity: j/k moves a key-stable selection, Enter opens the
  // release, Esc closes the panel, / focuses the filter.
  const kbIdx = kbKey === null ? -1 : visibleReleases.findIndex((r) => `${r.namespace}/${r.name}` === kbKey);
  const handleKbSelect = useCallback((idx: number) => {
    const r = visibleReleases[idx];
    if (r) setKbKey(`${r.namespace}/${r.name}`);
  }, [visibleReleases]);
  const handleKbActivate = useCallback((idx: number) => {
    const r = visibleReleases[idx];
    if (r) void openHelmRelease(cluster, r.namespace, r.name);
  }, [visibleReleases, cluster]);
  const handleKbEscape = useCallback(() => {
    if (useFleet.getState().helm.selected) useFleet.getState().selectHelmRelease(null);
  }, []);
  useListKeys({
    count: visibleReleases.length,
    selected: kbIdx,
    onSelect: handleKbSelect,
    onActivate: handleKbActivate,
    onEscape: handleKbEscape,
    searchRef,
  });

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
      <div style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column", minHeight: 0 }}>
        {/* Health strip - the at-a-glance block every other lens already has */}
        <div style={{ ...frame, display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(120px, 1fr))", overflow: "hidden", marginBottom: 12, flexShrink: 0 }}>
          <SummaryCell label="releases" value={String(releases.length)} />
          <SummaryCell label="deployed" value={String(deployed)} tone="success" />
          <SummaryCell label="failed" value={String(failed)} tone={failed > 0 ? "danger" : "muted"} />
          <SummaryCell label="pending" value={String(pendingCount)} tone={pendingCount > 0 ? "warning" : "muted"} />
          <SummaryCell label="namespaces" value={String(namespaces.length)} />
        </div>

        {/* Controls row */}
        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12, flexWrap: "wrap", flexShrink: 0 }}>
          <select
            value={namespace}
            onChange={(e) => setNamespace(e.target.value)}
            aria-label="filter release namespace"
            style={{ fontSize: 12, padding: "3px 6px", background: "var(--color-background-primary)", color: "var(--color-text-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4 }}
          >
            <option value="">all namespaces</option>
            {namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
          </select>
          <FilterButton label="all" count={releases.length} active={filter === "all"} onClick={() => setFilter("all")} />
          <FilterButton label="failed" count={failed} active={filter === "failed"} onClick={() => setFilter("failed")} />
          <FilterButton label="pending" count={pendingCount} active={filter === "pending"} onClick={() => setFilter("pending")} />
          <FilterButton label="deployed" count={deployed} active={filter === "ok"} onClick={() => setFilter("ok")} />
          <input
            ref={searchRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="filter releases"
            aria-label="filter releases"
            style={{ marginLeft: "auto", fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 180 }}
          />
          <button onClick={onRefresh} style={btn}>refresh</button>
          <UpdatedAt at={receivedAt} />
        </div>

        {/* List */}
        {helm.loading && releases.length === 0 ? (
          <SkeletonRows rows={6} label="loading releases" />
        ) : releases.length === 0 ? (
          <EmptyState
            icon={<IconAnchor size={28} stroke={1.2} />}
            title="No Helm releases on this cluster."
            hint="Nothing installed via helm - Flux HelmReleases appear in the Flux lens instead."
          />
        ) : (
          <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, flex: 1, minHeight: 0, overflowY: "auto" }}>
            {/* Header */}
            <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "0 8px 6px", fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)" }}>
              <span /><span>release</span><span>namespace</span><span>chart</span><span>app version</span><span>rev</span><span>updated</span>
            </div>
            {visibleReleases.length === 0 && (
              <EmptyState
                title="No releases match the current filter."
                action={{ label: "Clear filters", onClick: () => { setFilter("all"); setNamespace(""); setQuery(""); } }}
              />
            )}
            {/* Rows */}
            {visibleReleases.map((r) => {
              const isSelected = helm.selected?.namespace === r.namespace && helm.selected?.name === r.name;
              const isKb = kbIdx >= 0 && kbKey === `${r.namespace}/${r.name}`;
              return (
                <div
                  key={`${r.namespace}/${r.name}`}
                  data-rowhost="true"
                  aria-selected={isKb}
                  ref={isKb ? (el) => el?.scrollIntoView?.({ block: "nearest" }) : undefined}
                  onClick={() => void openHelmRelease(cluster, r.namespace, r.name)}
                  style={{
                    display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center",
                    padding: "7px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)",
                    cursor: "pointer",
                    background: isKb || isSelected ? "var(--color-background-secondary)" : undefined,
                    boxShadow: isKb ? "inset 2px 0 0 var(--color-text-info)" : undefined,
                  }}
                >
                  <span style={{ width: 8, height: 8, borderRadius: "50%", background: statusDot(r.status), display: "inline-block" }} />
                  <span style={{ display: "flex", alignItems: "center", gap: 2, minWidth: 0 }}>
                    <span style={{ fontWeight: 500, ...ellipsis }} title={r.name}>{r.name}</span>
                    <CopyButton text={r.name} label={`copy release name ${r.name}`} appearOnRowHover />
                  </span>
                  <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }} title={r.namespace}>{r.namespace}</span>
                  <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }} title={r.chart}>{r.chart}</span>
                  <span style={{ color: "var(--color-text-secondary)", ...ellipsis }} title={r.appVersion}>{r.appVersion || "—"}</span>
                  <span style={{ color: "var(--color-text-secondary)" }}>{r.revision}</span>
                  <span style={{ color: "var(--color-text-tertiary)" }}><Age unixSeconds={r.updatedUnix} suffix=" ago" /></span>
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
        <button onClick={onClose} aria-label="close release detail panel" style={{ ...btn, padding: "2px 8px", flexShrink: 0, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
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
                      <span style={{ color: "var(--color-text-tertiary)" }}><Age unixSeconds={e.updatedUnix} suffix=" ago" /></span>
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
              <YamlBlock text={values} copyLabel="release values" maxHeight={400} />
            )}
          </HelmSection>
        </>
      )}

      {/* Rollback composer dialog */}
      {rollbackPending && (
        <RollbackComposerDialog
          cluster={cluster}
          namespace={namespace}
          name={name}
          current={history.find((e) => e.revision === currentRevision)}
          target={history.find((e) => e.revision === rollbackPending.revision)}
          targetRevision={rollbackPending.revision}
          isProtected={isProtected}
          onCancel={() => setRollbackPending(null)}
          onConfirm={(opts) => {
            const { revision } = rollbackPending;
            setRollbackPending(null);
            void helmRollback(cluster, namespace, name, revision, opts);
          }}
        />
      )}
    </div>
  );
}

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
function RollbackComposerDialog({ cluster, namespace, name, current, target, targetRevision, isProtected, onCancel, onConfirm }: {
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

function HelmSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>{title}</div>
      {children}
    </div>
  );
}
