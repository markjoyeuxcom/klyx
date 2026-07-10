import { useEffect, useState, useCallback, useRef } from "react";
import { IconTerminal2, IconExternalLink, IconBox, IconX, IconAlertTriangle } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { WorkloadDTO, PodDTO, WorkloadKind, ResourceCellDTO } from "../store/fleet";
import { listWorkloads, openLiveWorkloads, rolloutRestart, scaleWorkload } from "../bridge/workloads";
import { deletePod, openPodDetail } from "../bridge/pods";
import { getWorkloadMetrics } from "../bridge/workload-metrics";
import { getWorkloadSparklines } from "../bridge/metrics";
import type { SparklinesDTO } from "../bridge/metrics";
import { openWorkloadLogsWindow } from "../bridge/windows";
import { Sparkline } from "../chrome/Sparkline";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { LogsPane } from "./LogsPane";
import { saturation, nearLimitSort, fmtCpu, fmtMem } from "./saturation";
import { VirtualList } from "../chrome/VirtualList";
import type { VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { useResizableDock } from "../chrome/useResizableDock";
import { Chip } from "../chrome/Chip";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { inputWell, selectWell, MetersStrip, LiveIndicator, btn, ellipsis, type Tone } from "../chrome/primitives";
import { InspectorFacts, InspectorIdle, InspectorStatus } from "./ResourceInspector";
import { loadClusterPrefs, loadSearch, saveSearch, saveWorkloadsViewPrefs } from "../store/persist";
import { SyncingChip, staleStyle } from "./staleness";
import { wellKnownResourceRef } from "./resourceRefs";
import { ManifestButton } from "./ManifestButton";

const rankDot: Record<string, string> = {
  unhealthy: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  restarts: "var(--color-text-info)",
  healthy: "var(--color-text-tertiary)",
};
const rankTone: Record<string, Tone> = {
  unhealthy: "danger",
  degraded: "warning",
  restarts: "info",
  healthy: "success",
};
const KINDS: WorkloadKind[] = ["Deployment", "StatefulSet", "DaemonSet"];
const kindShort: Record<WorkloadKind, string> = { Deployment: "deploy", StatefulSet: "sts", DaemonSet: "daemonset" };
const keyOf = (w: WorkloadDTO) => `${w.kind}/${w.namespace}/${w.name}`;
// Fixed row height (px, incl. border) — must match the VirtualList rowHeight so
// the windowing math lines up with the real DOM rows.
const ROW_HEIGHT = 32;

const tierColor: Record<string, string | undefined> = {
  none: undefined, neutral: "var(--color-text-success)", warn: "var(--color-text-warning)", danger: "var(--color-text-danger)",
};

function ResourceCellView({ resource, cell, hasPods }: { resource: "cpu" | "mem"; cell: ResourceCellDTO; hasPods: boolean }) {
  const fmt = resource === "cpu" ? fmtCpu : fmtMem;
  const usage = cell.usage == null ? "—" : fmt(cell.usage);
  if (!hasPods) return <span style={{ color: "var(--color-text-tertiary)" }}>—</span>;
  if (cell.limit == null) {
    return <span>{usage} <span style={{ color: "var(--color-text-tertiary)" }}>· no limit</span></span>;
  }
  const sat = saturation(resource, cell.usage, cell.limit);
  const color = tierColor[sat.tier];
  return (
    <span style={{ color }}>{usage} / {fmt(cell.limit)}
      {sat.pct != null && (
        <span style={{ display: "inline-block", width: 46, height: 6, background: "var(--color-background-tertiary)", borderRadius: 3, verticalAlign: "middle", marginLeft: 6 }}>
          <span style={{ display: "block", width: `${Math.min(100, sat.pct * 100)}%`, height: "100%", background: color ?? "var(--color-text-success)", borderRadius: 3 }} />
        </span>
      )}
    </span>
  );
}

function riskLabel(resource: "cpu" | "mem", cell: ResourceCellDTO): string {
  const sat = saturation(resource, cell.usage, cell.limit);
  if (sat.pct == null || sat.tier === "none" || sat.tier === "neutral") return "";
  const pct = Math.round(sat.pct * 100);
  return resource === "mem" ? `· OOM risk ${pct}%` : `· throttling risk ${pct}%`;
}

type PendingRestart = { kind: "restart"; w: WorkloadDTO };
type PendingScale = { kind: "scale"; w: WorkloadDTO; replicas: number };
type PendingScaleInput = { kind: "scale-input"; w: WorkloadDTO };
type PendingDeletePod = { kind: "delete-pod"; w: WorkloadDTO; pod: PodDTO };
type Pending = PendingRestart | PendingScale | PendingScaleInput | PendingDeletePod;

export function WorkloadsView({ cluster }: { cluster: string }) {
  const wl = useFleet((s) => s.workloads);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const reloaderPresent = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.reloaderPresent ?? false);
  const reloaderHealthy = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.reloaderHealthy ?? false);
  const [pending, setPending] = useState<Pending | null>(null);
  // Selection is tracked by stable key (kind/namespace/name), never by array
  // index — live emits re-rank the rows, and an index-bound selection silently
  // retargets the context panel (and the day-2 verbs derived from it).
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  // Aggregate-logs dock target. Persists across row selection; closed via ✕ only.
  const [logsTarget, setLogsTarget] = useState<{ namespace: string; name: string; kind: WorkloadKind } | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  // Holds the cleanup for the current live subscription so namespace changes
  // can close the old sub before opening the new one.
  const liveCleanupRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    // Open live subscription for the list. getWorkloadMetrics for usage stays
    // separate (polling, not watch-driven). Hydration fills only fields still
    // at their reset defaults, so a palette openWorkloads(kinds) or an
    // Overview needs-attention intent set just before mount always wins.
    const st = useFleet.getState();
    const prefs = loadClusterPrefs(cluster).workloads ?? {};
    if (prefs.needsAttention && !st.workloads.needsAttention) st.setWorkloadsNeedsAttention(true);
    if (prefs.nearLimitSort && !st.workloads.nearLimitSort) st.toggleNearLimitSort();
    const kindsAtDefault = KINDS.every((k) => st.workloads.kindFilter[k]);
    if (prefs.kinds && prefs.kinds.length > 0 && prefs.kinds.length < KINDS.length && kindsAtDefault) {
      st.setWorkloadKindFilter(prefs.kinds);
    }
    const savedSearch = loadSearch(cluster, "workloads");
    if (savedSearch && st.workloads.search === "") st.setWorkloadsSearch(savedSearch);
    const ns = st.workloads.namespace || (prefs.namespace ?? "");
    liveCleanupRef.current = openLiveWorkloads(cluster, ns);
    void getWorkloadMetrics(cluster, ns);
    const id = setInterval(() => {
      const cur = useFleet.getState().workloads;
      if (cur.cluster === cluster) getWorkloadMetrics(cluster, cur.namespace);
    }, 30000);
    return () => {
      clearInterval(id);
      if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
      useFleet.getState().clearWorkloads();
    };
  }, [cluster]);

  // Mirror filters into per-cluster prefs (namespace + toggles survive a
  // relaunch; search survives section switches within this run).
  useEffect(() => {
    if (wl.cluster !== cluster) return; // transient state during open/switch
    saveWorkloadsViewPrefs(cluster, {
      namespace: wl.namespace,
      needsAttention: wl.needsAttention,
      kinds: KINDS.filter((k) => wl.kindFilter[k]),
      nearLimitSort: wl.nearLimitSort,
    });
    saveSearch(cluster, "workloads", wl.search);
  }, [cluster, wl.cluster, wl.namespace, wl.needsAttention, wl.kindFilter, wl.nearLimitSort, wl.search]);

  // Palette verbs / cross-links land here as one-shot intents once the list
  // is loaded. Scale opens the inline popover in the expanded row, so the row
  // is expanded first; restart's confirm renders at the root.
  useNavIntent(cluster, ["restart-workload", "scale-workload", "select-workload"], (intent) => {
    if (wl.loading || wl.items.length === 0) return "wait";
    if (!("workloadKind" in intent)) return "missing";
    const w = wl.items.find((x) => x.kind === intent.workloadKind && x.namespace === intent.namespace && x.name === intent.name);
    if (!w) return "missing";
    const key = keyOf(w);
    setSelectedKey(key);
    if (!useFleet.getState().workloads.expanded.includes(key)) {
      useFleet.getState().toggleWorkloadExpand(key);
    }
    if (intent.kind === "restart-workload") setPending({ kind: "restart", w });
    else if (intent.kind === "scale-workload") setPending({ kind: "scale-input", w });
    return "consumed";
  }, [wl.items, wl.loading]);

  const onNamespace = (ns: string) => {
    // Close the current live sub, then reopen for the selected namespace.
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLiveWorkloads(cluster, ns);
    void getWorkloadMetrics(cluster, ns);
  };
  const onRefresh = () => { listWorkloads(cluster, wl.namespace).then(() => getWorkloadMetrics(cluster, wl.namespace)); };

  const showMetrics = wl.metricsAvailable;
  const rankCount = (rank: string) => wl.items.filter((w) => w.rank === rank).length;
  const gridCols = showMetrics
    ? "12px 90px 1fr 70px 64px 1.1fr 140px 140px 130px 28px"
    : "12px 90px 1fr 70px 64px 1.2fr 160px 28px";
  const query = wl.search.trim().toLowerCase();
  const filtered = wl.items.filter((w) => {
    if (!wl.kindFilter[w.kind as WorkloadKind]) return false;
    if (wl.needsAttention && w.rank === "healthy") return false;
    if (!query) return true;
    const gitops = w.gitops ? `${w.gitops.kind} ${w.gitops.namespace} ${w.gitops.name}` : "";
    const pods = w.pods.map((p) => `${p.name} ${p.node} ${p.reason}`).join(" ");
    return [
      w.kind,
      w.namespace,
      w.name,
      w.reason,
      w.rank,
      gitops,
      pods,
    ].some((v) => v.toLowerCase().includes(query));
  });
  const rows = showMetrics && wl.nearLimitSort ? nearLimitSort(filtered) : filtered;

  // Derive the selected row from the key. When the selected workload leaves the
  // visible set (filter change, delete) this resolves to -1 — no selection —
  // instead of silently pointing at whatever row took its index.
  const effectiveIdx = selectedKey === null ? -1 : rows.findIndex((w) => keyOf(w) === selectedKey);

  const handleSelect = useCallback((idx: number) => {
    const w = rows[idx];
    if (!w) return;
    setSelectedKey(keyOf(w));
    listRef.current?.scrollToIndex(idx);
  }, [rows]);

  const handleActivate = useCallback((idx: number) => {
    const w = rows[idx];
    if (w) useFleet.getState().toggleWorkloadExpand(keyOf(w));
  }, [rows]);

  const handleEscape = useCallback(() => {
    // Collapse selected row if expanded, else no-op.
    if (effectiveIdx >= 0) {
      const w = rows[effectiveIdx];
      if (w && wl.expanded.includes(keyOf(w))) {
        useFleet.getState().toggleWorkloadExpand(keyOf(w));
      }
    }
  }, [effectiveIdx, rows, wl.expanded]);

  const handleLogsKey = useCallback((idx: number) => {
    const w = rows[idx];
    if (w) setLogsTarget({ namespace: w.namespace, name: w.name, kind: w.kind as WorkloadKind });
  }, [rows]);

  useListKeys({
    count: rows.length,
    selected: effectiveIdx,
    onSelect: handleSelect,
    onActivate: handleActivate,
    onEscape: handleEscape,
    searchRef,
    extraKeys: { l: handleLogsKey },
  });

  const selectedWorkload = effectiveIdx >= 0 ? rows[effectiveIdx] : null;
  const visibleKinds = KINDS.filter((k) => wl.kindFilter[k]).map((k) => kindShort[k]).join(", ");
  const attentionCount = wl.items.filter((w) => w.rank !== "healthy").length;
  const selectedExpanded = selectedWorkload ? wl.expanded.includes(keyOf(selectedWorkload)) : false;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box", background: "var(--color-background-primary)" }}>
      {/* Workloads workspace - the logs dock pins below it. */}
      <div style={{ flex: 1, minHeight: 0, overflow: "hidden", display: "grid", gridTemplateColumns: "minmax(620px, 1fr) minmax(320px, 380px)" }}>
        <div style={{ minWidth: 0, minHeight: 0, overflow: "hidden", display: "flex", flexDirection: "column", borderRight: "0.5px solid var(--color-border-tertiary)" }}>
          {/* Meters: workload population by rank - the same buckets the rows sort by. */}
          <MetersStrip
            note="broken first · rank order"
            meters={[
              { label: "workloads", value: wl.items.length },
              rankCount("unhealthy") > 0 && { label: "unhealthy", value: rankCount("unhealthy"), tone: "danger" as const },
              rankCount("degraded") > 0 && { label: "degraded", value: rankCount("degraded"), tone: "warning" as const },
              rankCount("restarts") > 0 && { label: "restarts", value: rankCount("restarts"), tone: "info" as const },
              { label: "healthy", value: rankCount("healthy"), tone: "success" as const },
            ]}
          />
          <div style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: 12,
            padding: "10px 12px",
            borderBottom: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-secondary)",
            flexWrap: "wrap",
            flexShrink: 0,
          }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
              <select value={wl.namespace} onChange={(e) => onNamespace(e.target.value)}
                style={selectWell}>
                <option value="">all namespaces</option>
                {wl.namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
              </select>
              <Chip on={wl.needsAttention} onClick={() => useFleet.getState().toggleNeedsAttention()}>needs attention</Chip>
              {showMetrics && (
                <Chip on={wl.nearLimitSort} onClick={() => useFleet.getState().toggleNearLimitSort()}>near limit</Chip>
              )}
              <WorkloadStatusTrail
                shown={rows.length}
                total={wl.items.length}
                attention={attentionCount}
                kinds={visibleKinds || "none"}
                live={wl.live}
              />
            </div>
            <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
              <input
                ref={searchRef}
                value={wl.search}
                onChange={(e) => useFleet.getState().setWorkloadsSearch(e.target.value)}
                aria-label="filter workloads"
                placeholder="filter workloads"
                style={{ ...inputWell, width: 180 }}
              />
              <button onClick={onRefresh} style={btn}>refresh</button>
              <LiveIndicator live={wl.live} />
            </div>
          </div>

          {/* Truncation banner - LOUD: broken-first ranking happens after the
              list, so a truncated view can hide a fault past the cap. */}
          {wl.truncated && (
            <div role="status" style={{ display: "flex", alignItems: "center", gap: 8, margin: "0 0 10px", padding: "7px 11px", border: "0.5px solid var(--color-border-warning)", background: "var(--color-background-warning)", borderRadius: 4, fontSize: 12, color: "var(--color-text-warning)", flexShrink: 0 }}>
              <IconAlertTriangle size={14} stroke={1.5} />
              <span>Showing the first 5000 workloads — this list is incomplete, so broken-first triage may miss a fault. Scope by namespace for the full picture.</span>
            </div>
          )}

          <div style={{ flex: 1, minHeight: 0, overflowX: "auto", overflowY: "hidden", display: "flex", flexDirection: "column" }}>
            {wl.loading && wl.items.length === 0 ? (
              <div style={{ padding: "16px 20px" }}>
                <SkeletonRows rows={8} label="loading workloads" />
              </div>
            ) : rows.length === 0 ? (
              <div style={{ padding: "16px 20px" }}>
                <EmptyState
                  icon={<IconBox size={28} stroke={1.2} />}
                  title={`No workloads${wl.namespace ? ` in ${wl.namespace}` : ""}.`}
                  hint={wl.needsAttention && !wl.search ? "The needs-attention filter is on - everything here is healthy." : undefined}
                  action={(wl.search || wl.needsAttention || !KINDS.every((k) => wl.kindFilter[k])) ? {
                    label: "Clear filters",
                    onClick: () => {
                      const st = useFleet.getState();
                      st.setWorkloadsSearch("");
                      st.setWorkloadsNeedsAttention(false);
                      st.setWorkloadKindFilter(KINDS);
                    },
                  } : undefined}
                />
              </div>
            ) : (
              <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, minWidth: showMetrics ? 980 : 760, flex: 1, minHeight: 0, display: "flex", flexDirection: "column" }}>
                <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "0 8px 6px", fontSize: 10, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)", flexShrink: 0, background: "var(--color-background-primary)" }}>
                  <span /><span>kind</span><span>workload</span><span>ready</span><span>restarts</span><span>status</span>
                  {showMetrics && <span>cpu</span>}
                  {showMetrics && <span>mem</span>}
                  <span>gitops</span>
                  <span />
                </div>
                {/*
                 * VirtualList windows >=100 rows. Expanded rows have variable
                 * height (metrics + sparklines), which breaks fixed-row
                 * windowing math, so any expansion forces the plain path —
                 * users expand rows to read details, at which point full
                 * rendering is the correct tradeoff.
                 */}
                <VirtualList
                  ref={listRef}
                  items={rows}
                  rowHeight={ROW_HEIGHT}
                  forcePlain={wl.expanded.length > 0}
                  rowKey={(w) => keyOf(w)}
                  style={{ flex: 1, minHeight: 0 }}
                  render={(w, i) => {
                    const expanded = wl.expanded.includes(keyOf(w));
                    const isKbSelected = i === effectiveIdx;
                    return (
                      <div key={keyOf(w)}>
                        <div
                          role="button"
                          tabIndex={0}
                          aria-expanded={expanded}
                          aria-selected={isKbSelected}
                          onClick={() => {
                            setSelectedKey(keyOf(w));
                            useFleet.getState().toggleWorkloadExpand(keyOf(w));
                          }}
                          onKeyDown={(e) => {
                            if (e.key === "Enter" || e.key === " ") {
                              e.preventDefault();
                              setSelectedKey(keyOf(w));
                              useFleet.getState().toggleWorkloadExpand(keyOf(w));
                            }
                          }}
                          data-rowhost="true"
                          style={{
                            display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center",
                            height: ROW_HEIGHT, boxSizing: "border-box",
                            padding: isKbSelected ? "7px 8px 7px 6px" : "7px 8px",
                            borderBottom: "0.5px solid var(--color-border-tertiary)",
                            borderLeft: isKbSelected ? "2px solid var(--bench-selection-edge, var(--color-text-info))" : "2px solid transparent",
                            cursor: "pointer",
                            background: isKbSelected ? "var(--color-background-secondary)" : undefined,
                            outline: "none",
                            ...staleStyle(w.stale),
                          }}
                        >
                          <span style={{ width: 8, height: 8, borderRadius: "50%", background: rankDot[w.rank] }} />
                          <span style={{ color: "var(--color-text-tertiary)" }}>{kindShort[w.kind as WorkloadKind]}</span>
                          <span style={{ display: "flex", alignItems: "center", gap: 2, minWidth: 0 }}>
                            <span style={ellipsis}><span style={{ color: "var(--color-text-tertiary)" }}>{w.namespace}</span> / <span style={{ fontWeight: 500 }}>{w.name}</span></span>
                            <CopyButton text={w.name} label={`copy workload name ${w.name}`} appearOnRowHover />
                          </span>
                          <span style={{ color: w.ready < w.desired ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{w.ready} / {w.desired}</span>
                          <span style={{ color: w.restarts > 0 ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{w.restarts}</span>
                          <span style={{ ...ellipsis, color: w.rank === "unhealthy" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }} title={w.reason}>
                            {w.stale ? <SyncingChip generation={w.generation} observedGeneration={w.observedGeneration} /> : w.reason}
                          </span>
                          {showMetrics && <ResourceCellView resource="cpu" cell={w.resources.cpu} hasPods={w.pods.length > 0} />}
                          {showMetrics && <ResourceCellView resource="mem" cell={w.resources.mem} hasPods={w.pods.length > 0} />}
                          <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={w.gitops ? `Flux ownership label: ${w.gitops.kind} ${w.gitops.namespace}/${w.gitops.name}` : undefined}>
                            {w.gitops ? `flux ${w.gitops.kind === "HelmRelease" ? "hr" : "ks"}/${w.gitops.name}` : "—"}
                          </span>
                          <LogsIconButton
                            label={`aggregate logs for ${w.namespace}/${w.name}`}
                            onClick={() => setLogsTarget({ namespace: w.namespace, name: w.name, kind: w.kind as WorkloadKind })}
                          />
                        </div>
                        {expanded && showMetrics && (
                          <div style={{ display: "flex", gap: 28, fontSize: 11, padding: "6px 8px 6px 32px", background: "var(--color-background-secondary)", fontFamily: "var(--font-mono)", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
                            <span><span style={{ color: "var(--color-text-tertiary)" }}>cpu</span> {w.pods.length === 0 ? "—" : `usage ${w.resources.cpu.usage == null ? "—" : fmtCpu(w.resources.cpu.usage)} · req ${w.resources.cpu.request == null ? "—" : fmtCpu(w.resources.cpu.request)} · ${w.resources.cpu.limit == null ? "no limit" : `lim ${fmtCpu(w.resources.cpu.limit)}`}`} <span style={{ color: tierColor[saturation("cpu", w.resources.cpu.usage, w.resources.cpu.limit).tier] }}>{riskLabel("cpu", w.resources.cpu)}</span></span>
                            <span><span style={{ color: "var(--color-text-tertiary)" }}>mem</span> {w.pods.length === 0 ? "—" : `usage ${w.resources.mem.usage == null ? "—" : fmtMem(w.resources.mem.usage)} · req ${w.resources.mem.request == null ? "—" : fmtMem(w.resources.mem.request)} · ${w.resources.mem.limit == null ? "no limit" : `lim ${fmtMem(w.resources.mem.limit)}`}`} <span style={{ color: tierColor[saturation("mem", w.resources.mem.usage, w.resources.mem.limit).tier] }}>{riskLabel("mem", w.resources.mem)}</span></span>
                          </div>
                        )}
                        {expanded && showMetrics && (
                          <WorkloadSparkRow cluster={cluster} kind={w.kind} namespace={w.namespace} name={w.name} />
                        )}
                      </div>
                    );
                  }}
                />
              </div>
            )}
          </div>
        </div>

        <WorkloadContextPanel
          workload={selectedWorkload}
          attention={rankCount("unhealthy") + rankCount("degraded")}
          onClose={() => setSelectedKey(null)}
          expanded={selectedExpanded}
          isProtected={isProtected}
          reloaderPresent={reloaderPresent}
          reloaderHealthy={reloaderHealthy}
          pending={pending}
          onLogs={(w) => setLogsTarget({ namespace: w.namespace, name: w.name, kind: w.kind as WorkloadKind })}
          onRestart={(w) => setPending({ kind: "restart", w })}
          onScale={(w) => setPending({ kind: "scale-input", w })}
          onScaleConfirm={(w, replicas) => {
            if (isProtected) {
              setPending({ kind: "scale", w, replicas });
            } else {
              setPending(null);
              void scaleWorkload(cluster, w.kind, w.namespace, w.name, replicas);
            }
          }}
          onCancelScale={() => setPending(null)}
          onDeletePod={(w, pod) => setPending({ kind: "delete-pod", w, pod })}
          onOpenPod={(w, pod) => {
            useFleet.getState().setSection("pods");
            void openPodDetail(cluster, w.namespace, pod.name);
          }}
          onOpenNode={(node) => {
            useFleet.getState().setSection("nodes");
            useFleet.getState().requestNavIntent({ kind: "select-node", cluster, node });
          }}
          onOpenFluxOwner={(w) => {
            if (!w.gitops) return;
            useFleet.getState().setSection("gitops");
            useFleet.getState().requestNavIntent({ kind: "select-flux", cluster, fluxKind: w.gitops.kind, namespace: w.gitops.namespace, name: w.gitops.name });
          }}
        />
      </div>

      {/* Aggregate-logs dock — persists across row selection changes; close via ✕ only */}
      {logsTarget && (
        <WorkloadLogsDock
          cluster={cluster}
          target={logsTarget}
          onClose={() => setLogsTarget(null)}
          onPopOut={async (container) => {
            const ok = await openWorkloadLogsWindow(cluster, logsTarget.namespace, logsTarget.kind, logsTarget.name, container);
            // On success the tail moved to the native window; close the dock so we
            // don't double-tail the same workload. On failure keep the dock open.
            if (ok) setLogsTarget(null);
          }}
        />
      )}

      {pending?.kind === "restart" && (
        <ConfirmDialog
          title="restart"
          cluster={cluster}
          detail={`${pending.w.kind.toLowerCase()} ${pending.w.namespace}/${pending.w.name}`}
          protected={isProtected}
          confirmLabel="Restart"
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const { w } = pending;
            setPending(null);
            void rolloutRestart(cluster, w.kind, w.namespace, w.name);
          }}
        />
      )}
      {pending?.kind === "scale" && (
        <ConfirmDialog
          title="scale"
          cluster={cluster}
          detail={`${pending.w.kind.toLowerCase()} ${pending.w.namespace}/${pending.w.name} → ${pending.replicas} replicas`}
          protected={isProtected}
          confirmLabel="Scale"
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const { w, replicas } = pending;
            setPending(null);
            void scaleWorkload(cluster, w.kind, w.namespace, w.name, replicas);
          }}
        />
      )}
      {pending?.kind === "delete-pod" && (
        <ConfirmDialog
          title="delete pod"
          cluster={cluster}
          detail={`pod ${pending.w.namespace}/${pending.pod.name}`}
          protected={isProtected}
          danger
          confirmLabel="Delete"
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const { w, pod } = pending;
            setPending(null);
            void deletePod(cluster, w.namespace, pod.name).then(() => listWorkloads(cluster, wl.namespace));
          }}
        />
      )}
    </div>
  );
}

function WorkloadStatusTrail({
  shown,
  total,
  attention,
  kinds,
  live,
}: {
  /** Rows surviving the current filters. */
  shown: number;
  /** All loaded workloads, pre-filter. */
  total: number;
  attention: number;
  kinds: string;
  live: boolean;
}) {
  return (
    <span style={{
      display: "inline-flex",
      alignItems: "center",
      gap: 8,
      color: "var(--color-text-tertiary)",
      fontFamily: "var(--font-mono)",
      fontSize: 9,
      letterSpacing: "0.08em",
      textTransform: "uppercase",
      marginLeft: 4,
      whiteSpace: "nowrap",
    }}>
      <span>{shown}/{total}</span>
      <span style={{ color: attention > 0 ? "var(--color-text-danger)" : undefined }}>{attention} attention</span>
      <span>{kinds}</span>
      <span style={{ color: live ? undefined : "var(--color-text-warning)" }}>{live ? "watch-backed" : "manual refresh"}</span>
    </span>
  );
}

function WorkloadContextPanel({
  workload,
  attention,
  expanded,
  onClose,
  isProtected,
  reloaderPresent,
  reloaderHealthy,
  pending,
  onLogs,
  onRestart,
  onScale,
  onScaleConfirm,
  onCancelScale,
  onDeletePod,
  onOpenPod,
  onOpenNode,
  onOpenFluxOwner,
}: {
  workload: WorkloadDTO | null;
  attention: number;
  expanded: boolean;
  onClose: () => void;
  isProtected: boolean;
  reloaderPresent: boolean;
  reloaderHealthy: boolean;
  pending: Pending | null;
  onLogs: (w: WorkloadDTO) => void;
  onRestart: (w: WorkloadDTO) => void;
  onScale: (w: WorkloadDTO) => void;
  onScaleConfirm: (w: WorkloadDTO, replicas: number) => void;
  onCancelScale: () => void;
  onDeletePod: (w: WorkloadDTO, pod: PodDTO) => void;
  onOpenPod: (w: WorkloadDTO, pod: PodDTO) => void;
  onOpenNode: (node: string) => void;
  onOpenFluxOwner: (w: WorkloadDTO) => void;
}) {
  if (!workload) {
    return (
      <InspectorIdle
        subject="workloads"
        attention={attention}
        hint="Select a workload for ownership, freshness, pods, and day-2 actions."
        width="100%"
      />
    );
  }

  const badPods = workload.pods.filter((p) => !p.ready || p.restarts > 0).length;
  const deleteTarget = workload.pods.find((p) => !p.ready || p.restarts > 0) ?? workload.pods[0] ?? null;
  const owner = workload.gitops
    ? `${workload.gitops.kind} ${workload.gitops.namespace}/${workload.gitops.name}`
    : "—";

  return (
    <aside style={{ minWidth: 0, minHeight: 0, overflowY: "auto", background: "var(--bench-well-bg, var(--color-background-secondary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))", borderRadius: "var(--radius-sm)", display: "flex", flexDirection: "column" }}>
      <div style={{ padding: 14, borderBottom: "0.5px solid var(--color-border-tertiary)", display: "grid", gap: 10 }}>
        <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 10 }}>
          <div style={{ minWidth: 0, flex: 1 }}>
            <div style={{ color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)", fontSize: 10 }}>{workload.kind} / {workload.namespace}</div>
            <div style={{ fontFamily: "var(--font-mono)", fontSize: 15, fontWeight: 600, overflowWrap: "anywhere" }}>{workload.name}</div>
          </div>
          <CopyButton text={`${workload.namespace}/${workload.name}`} label="copy workload name" />
          {isProtected && (
            <span style={{ boxShadow: "inset 0 0 0 1px var(--color-border-warning)", background: "var(--color-background-warning)", color: "var(--color-text-warning)", fontFamily: "var(--font-mono)", fontSize: 10, padding: "2px 6px", borderRadius: 3, flexShrink: 0 }}>
              prd lock
            </span>
          )}
          <button onClick={onClose} aria-label="close workload panel" style={{ ...btn, padding: "2px 8px", flexShrink: 0, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
        </div>
        {/* State first: the rank + reason headline, scannable before the
            status grid below. */}
        <InspectorStatus
          tone={workload.stale ? "muted" : rankTone[workload.rank] ?? "muted"}
          label={workload.stale ? "syncing…" : workload.rank}
          reason={workload.stale ? undefined : (workload.reason || undefined)}
          detail={`${workload.ready}/${workload.desired} ready`}
        />
        <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
          <button onClick={() => onLogs(workload)} style={{ ...btn, color: "var(--color-text-info)", borderColor: "var(--color-border-info)", background: "var(--color-background-info)" }}>tail logs</button>
          <button onClick={() => onRestart(workload)} style={btn}>restart</button>
          {/* Match the pending action by stable key, not DTO identity — live
              emits replace the DTOs, and an identity match dismissed the open
              popover on every update. */}
          {(workload.kind === "Deployment" || workload.kind === "StatefulSet") && (
            pending?.kind === "scale-input" && keyOf(pending.w) === keyOf(workload) ? (
              <ScalePopover
                initial={workload.desired}
                onConfirm={(n) => onScaleConfirm(workload, n)}
                onCancel={onCancelScale}
              />
            ) : (
              <button onClick={() => onScale(workload)} style={btn}>scale</button>
            )
          )}
          {deleteTarget && (
            <button
              onClick={() => onDeletePod(workload, deleteTarget)}
              style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-border-danger)", background: "var(--color-background-danger)" }}
            >delete pod</button>
          )}
          {/* One manifest surface: jump to InstanceDetail (YAML, labels,
              annotations, edit) instead of duplicating a YAML tab here. */}
          <ManifestButton group="apps" kind={workload.kind} namespace={workload.namespace} name={workload.name} />
          <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>
            {expanded ? "expanded" : "enter expands"}
          </span>
        </div>
      </div>

      <div style={{ padding: "12px 14px", borderBottom: "0.5px solid var(--color-border-tertiary)", display: "grid", gap: 8 }}>
        <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>status</div>
        <InspectorFacts
          facts={[
            { label: "ready", value: `${workload.ready}/${workload.desired}`, tone: workload.ready < workload.desired ? "warning" : undefined },
            { label: "restarts", value: workload.restarts, tone: workload.restarts > 0 ? "warning" : undefined },
            { label: "updated", value: workload.updated },
            { label: "available", value: workload.available },
          ]}
        />
        <div style={{ display: "grid", gridTemplateColumns: "88px 1fr", gap: "5px 10px", fontFamily: "var(--font-mono)", fontSize: 11 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>reason</span><span style={{ color: workload.rank === "unhealthy" ? "var(--color-text-danger)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{workload.reason}</span>
          <span style={{ color: "var(--color-text-tertiary)" }}>owner</span>
          {workload.gitops ? (
            <button
              onClick={() => onOpenFluxOwner(workload)}
              title={`open ${owner} in the Flux lens`}
              style={{ background: "transparent", border: "none", padding: 0, cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", fontFamily: "var(--font-mono)", fontSize: 11, overflowWrap: "anywhere", textDecoration: "underline", textDecorationColor: "var(--color-border-secondary)", textUnderlineOffset: 2 }}
            >{owner}</button>
          ) : (
            <span style={{ color: "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>{owner}</span>
          )}
        </div>
      </div>

      {workload.reloader && (
        <div style={{ padding: "12px 14px", borderBottom: "0.5px solid var(--color-border-tertiary)", display: "grid", gap: 8 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>reloads on</div>
            {workload.reloader.auto && (
              <span title="reloader.stakater.com/auto: reloads on any mounted ConfigMap/Secret" style={{ fontFamily: "var(--font-mono)", fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, padding: "1px 5px", borderRadius: 3, color: "var(--color-text-info)", boxShadow: "inset 0 0 0 1px var(--color-border-info)", background: "var(--color-background-info)" }}>auto</span>
            )}
          </div>
          {/* Honesty: reload annotations declare intent; they are inert unless the
              Reloader operator is present AND healthy. A present-but-Degraded
              controller (crashlooping, 0 ready) fires no reloads either. */}
          {!(reloaderPresent && reloaderHealthy) && (
            <div style={{ display: "flex", alignItems: "center", gap: 5, fontSize: 11, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
              <IconAlertTriangle size={12} stroke={1.5} />
              {reloaderPresent
                ? "Reloader operator is present but not ready — reloads may not fire until it recovers."
                : "Reloader operator not detected on this cluster — these annotations may be inert."}
            </div>
          )}
          <div style={{ display: "flex", flexWrap: "wrap", gap: 5 }}>
            {workload.reloader.configMaps.map((n) => (
              <ReloadChip key={`cm-${n}`} kind="ConfigMap" name={n} namespace={workload.namespace} />
            ))}
            {workload.reloader.secrets.map((n) => (
              <ReloadChip key={`sec-${n}`} kind="Secret" name={n} namespace={workload.namespace} />
            ))}
            {workload.reloader.configMaps.length === 0 && workload.reloader.secrets.length === 0 && (
              <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-tertiary)" }}>
                {workload.reloader.auto ? "no mounted config" : "—"}
              </span>
            )}
          </div>
        </div>
      )}

      <div style={{ padding: "12px 14px", borderBottom: "0.5px solid var(--color-border-tertiary)", display: "grid", gap: 8 }}>
        <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>pods</div>
        <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, color: badPods > 0 ? "var(--color-text-warning)" : "var(--color-text-success)" }}>
          {badPods > 0 ? `${badPods} affected / ${workload.pods.length} total` : `${workload.pods.length} healthy`}
        </div>
        <div style={{ display: "grid", gap: 6 }}>
          {/* Every other pod-adjacent surface links pods; this one was the
              exception. Click opens the pod detail in the Pods section; the
              node name jumps to the Nodes board. */}
          {workload.pods.slice(0, 6).map((p) => (
            <div
              key={p.name}
              role="button"
              tabIndex={0}
              aria-label={`open pod ${p.name}`}
              onClick={() => onOpenPod(workload, p)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onOpenPod(workload, p);
                }
              }}
              style={{ display: "grid", gridTemplateColumns: "8px minmax(0, 1fr) auto", gap: 8, alignItems: "center", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", padding: "7px 8px", fontFamily: "var(--font-mono)", fontSize: 11, cursor: "pointer" }}
            >
              <span style={{ width: 7, height: 7, borderRadius: "50%", background: p.ready ? "var(--color-text-success)" : "var(--color-text-danger)" }} />
              <span style={{ ...ellipsis, color: "var(--color-text-info)" }} title={`open pod ${p.name}`}>{p.name}</span>
              <span style={{ color: p.ready ? "var(--color-text-secondary)" : "var(--color-text-danger)" }}>{p.ready ? "ready" : p.reason || "not ready"}</span>
              <span />
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onOpenNode(p.node);
                }}
                title={`open node ${p.node}`}
                style={{ background: "transparent", border: "none", padding: 0, cursor: "pointer", textAlign: "left", color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)", fontSize: 11, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", textDecoration: "underline", textDecorationColor: "var(--color-border-secondary)", textUnderlineOffset: 2 }}
              >{p.node}</button>
              <span style={{ color: p.restarts > 0 ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{p.restarts}</span>
            </div>
          ))}
          {workload.pods.length > 6 && (
            <div style={{ color: "var(--color-text-tertiary)", fontSize: 11, fontFamily: "var(--font-mono)" }}>+{workload.pods.length - 6} more pods</div>
          )}
        </div>
      </div>

      <div style={{ padding: "12px 14px", display: "grid", gap: 8 }}>
        <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>resources</div>
        <div style={{ display: "grid", gridTemplateColumns: "88px 1fr", gap: "5px 10px", fontFamily: "var(--font-mono)", fontSize: 11 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>cpu</span>
          <span><ResourceCellView resource="cpu" cell={workload.resources.cpu} hasPods={workload.pods.length > 0} /></span>
          <span style={{ color: "var(--color-text-tertiary)" }}>mem</span>
          <span><ResourceCellView resource="mem" cell={workload.resources.mem} hasPods={workload.pods.length > 0} /></span>
        </div>
      </div>
    </aside>
  );
}

// WorkloadSparkRow — 30m cpu/mem sparklines for an expanded workload row.
// Fetched once per expand (component mount); not polled. Renders nothing while
// loading and a muted reason when the series are unavailable.
function WorkloadSparkRow({ cluster, kind, namespace, name }: { cluster: string; kind: string; namespace: string; name: string }) {
  const [spark, setSpark] = useState<SparklinesDTO | null>(null);

  useEffect(() => {
    let cancelled = false;
    getWorkloadSparklines(cluster, namespace, kind, name).then((dto) => {
      if (!cancelled) setSpark(dto);
    });
    return () => { cancelled = true; };
  }, [cluster, namespace, kind, name]);

  if (spark === null) return null;
  if (!spark.available) {
    return (
      <div style={{ padding: "0 8px 6px 32px", background: "var(--color-background-secondary)", fontSize: 10, color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)" }}>
        sparklines unavailable{spark.message ? `: ${spark.message}` : ""}
      </div>
    );
  }
  return (
    <div style={{ display: "flex", gap: 28, alignItems: "center", padding: "0 8px 8px 32px", background: "var(--color-background-secondary)", fontSize: 10, fontFamily: "var(--font-mono)" }}>
      <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
        <span style={{ color: "var(--color-text-tertiary)" }}>cpu 30m</span>
        <Sparkline points={spark.cpu} />
      </span>
      <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
        <span style={{ color: "var(--color-text-tertiary)" }}>mem 30m</span>
        <Sparkline points={spark.mem} />
      </span>
    </div>
  );
}

// LogsIconButton — subtle trailing-column affordance, full opacity on own hover.
function LogsIconButton({ label, onClick }: { label: string; onClick: () => void }) {
  const [hovered, setHovered] = useState(false);
  return (
    <button
      aria-label={label}
      title="aggregate logs"
      onClick={(e) => { e.stopPropagation(); onClick(); }}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        display: "flex", alignItems: "center", justifyContent: "center",
        width: 20, height: 20,
        padding: 0, border: "none", background: "transparent", cursor: "pointer",
        color: "var(--color-text-tertiary)",
        opacity: hovered ? 1 : 0.45,
        borderRadius: 3,
      }}
    >
      <IconTerminal2 size={14} />
    </button>
  );
}

// ---------------------------------------------------------------------------
// WorkloadLogsDock — bottom dock tailing the AGGREGATE logs of one workload
// (all its pods fan in with dimmed pod-name prefixes). Mirrors PodsView's
// LogsDock, including the shared dock-height storage key via useResizableDock.
// ---------------------------------------------------------------------------

function WorkloadLogsDock({
  cluster,
  target,
  onClose,
  onPopOut,
}: {
  cluster: string;
  target: { namespace: string; name: string; kind: WorkloadKind };
  onClose: () => void;
  onPopOut: (container: string) => void;
}) {
  const { height, handleProps } = useResizableDock();
  // Track the container currently selected inside LogsPane so the pop-out opens
  // the same tail. In workload mode the selector is static ("default containers")
  // unless a container was chosen, so this is usually "".
  const [container, setContainer] = useState("");

  return (
    <div
      data-testid="workload-logs-dock"
      style={{
        flexShrink: 0,
        height,
        position: "relative",
        borderTop: "0.5px solid var(--color-border-tertiary)",
        display: "flex",
        flexDirection: "column",
        background: "var(--color-background-primary)",
        fontFamily: "var(--font-mono)",
        fontSize: 12,
      }}
    >
      {/* Drag handle on top edge */}
      <div {...handleProps} />

      {/* Dock header */}
      <div style={{
        display: "flex", alignItems: "center", gap: 8,
        padding: "4px 12px 4px 14px",
        borderBottom: "0.5px solid var(--color-border-tertiary)",
        flexShrink: 0,
      }}>
        <span style={{ fontSize: 11, fontWeight: 500 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{kindShort[target.kind]} {target.namespace}/</span>{target.name}
        </span>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>aggregate logs</span>
        <div style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 4 }}>
          <button
            onClick={() => onPopOut(container)}
            style={{ ...btn, padding: "2px 6px", fontSize: 12, display: "flex", alignItems: "center" }}
            aria-label="open logs in window"
            title="Open logs in a separate window"
          >
            <IconExternalLink size={13} stroke={1.5} />
          </button>
          <button
            onClick={onClose}
            style={{ ...btn, padding: "2px 8px", display: "inline-flex", alignItems: "center" }}
            aria-label="close logs dock"
          ><IconX size={13} stroke={1.5} /></button>
        </div>
      </div>

      {/* LogsPane body — workload prop switches it to OpenWorkloadLogStream. */}
      <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", padding: "8px 12px" }}>
        <LogsPane
          key={`${target.kind}/${target.namespace}/${target.name}`}
          cluster={cluster}
          pod={{ namespace: target.namespace, name: target.name, containers: [] }}
          workload={{ kind: target.kind, name: target.name }}
          onContainerChange={setContainer}
        />
      </div>
    </div>
  );
}

// ReloadChip is one reload-target ConfigMap/Secret, cross-linked to its manifest
// in Resources (same openResource+openInstance pattern as the workload manifest
// button). A kind tag (CM/SEC) disambiguates the two lists in one row.
function ReloadChip({ kind, name, namespace }: { kind: "ConfigMap" | "Secret"; name: string; namespace: string }) {
  const open = () => {
    const ref = wellKnownResourceRef("", kind);
    if (!ref) return;
    const nav = useFleet.getState();
    nav.openResource(ref);
    nav.openInstance(namespace, name);
  };
  return (
    <button
      onClick={open}
      title={`open ${kind} ${namespace}/${name} in Resources`}
      style={{ display: "inline-flex", alignItems: "center", gap: 5, fontFamily: "var(--font-mono)", fontSize: 11, padding: "2px 7px", borderRadius: 3, cursor: "pointer", border: "none", color: "var(--color-text-info)", maxWidth: "100%", overflow: "hidden", background: "var(--bench-well-bg, var(--color-background-secondary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))" }}
    >
      <span style={{ fontSize: 8, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", flexShrink: 0 }}>{kind === "ConfigMap" ? "cm" : "sec"}</span>
      <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{name}</span>
    </button>
  );
}

function ScalePopover({ initial, onConfirm, onCancel }: { initial: number; onConfirm: (n: number) => void; onCancel: () => void }) {
  const [value, setValue] = useState(String(initial));
  const n = parseInt(value, 10);
  const valid = !Number.isNaN(n) && n >= 0 && n <= 10000;
  return (
    <span
      onClick={(e) => e.stopPropagation()}
      style={{ display: "inline-flex", alignItems: "center", gap: 4, border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, padding: "2px 6px", background: "var(--color-background-primary)" }}
    >
      <input
        aria-label="replica count"
        type="number"
        min={0}
        max={10000}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter" && valid) onConfirm(n); else if (e.key === "Escape") onCancel(); }}
        autoFocus
        style={{ width: 52, fontSize: 11, padding: "2px 4px", fontFamily: "var(--font-mono)", background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 3, color: "var(--color-text-primary)" }}
      />
      <button onClick={() => { if (valid) onConfirm(n); }} disabled={!valid} aria-label="apply scale" style={{ ...btn, padding: "2px 8px", opacity: valid ? 1 : 0.4, cursor: valid ? "pointer" : "not-allowed" }}>apply</button>
      <button onClick={onCancel} aria-label="cancel scale" style={{ ...btn, padding: "2px 8px", display: "inline-flex", alignItems: "center" }}><IconX size={12} stroke={1.5} /></button>
    </span>
  );
}


