import { useEffect, useMemo, useState, useCallback, useRef } from "react";
import { IconTerminal2, IconExternalLink, IconBox, IconX, IconAlertTriangle } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { WorkloadDTO, PodDTO, WorkloadKind, ResourceCellDTO, RolloutRevisionDTO } from "../store/fleet";
import { listWorkloads, openLiveWorkloads, rolloutRestart, rolloutHistory, rolloutUndo, rolloutPause, scaleWorkload } from "../bridge/workloads";
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
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LiveIndicator, btn, ellipsis } from "../chrome/primitives";
import { loadClusterPrefs, loadSearch, saveSearch, saveWorkloadsViewPrefs } from "../store/persist";
import { staleStyle } from "./staleness";
import { wellKnownResourceRef } from "./resourceRefs";
import { FleetCompare } from "./FleetCompare";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";

import { VerdictLabel, VerdictLamp, VerdictStatus } from "../health/VerdictView";
import { attentionTone, coverageOf, isAttention, kindOf, labelOf, summarize } from "../health/verdict";
import { UpdatedAt, useReceivedAt } from "../chrome/Age";
import { LensLayout, LensTable, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
const KINDS: WorkloadKind[] = ["Deployment", "StatefulSet", "DaemonSet"];
const kindShort: Record<WorkloadKind, string> = { Deployment: "deploy", StatefulSet: "sts", DaemonSet: "daemonset" };
const keyOf = (w: WorkloadDTO) => `${w.kind}/${w.namespace}/${w.name}`;

function ago(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}
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
type PendingUndo = { kind: "undo"; w: WorkloadDTO; rev: RolloutRevisionDTO };
type PendingPause = { kind: "pause"; w: WorkloadDTO; pause: boolean };
type Pending = PendingRestart | PendingScale | PendingScaleInput | PendingDeletePod | PendingUndo | PendingPause;

// Workload columns: identity keeps its 240px floor; the rest join by priority
// while they fit. Usage columns exist only when a metrics source does.
const WL_COLUMNS: ColumnDef[] = [
  { id: "lamp", width: 16, priority: 0 },
  { id: "kind", width: 64, priority: 2 },
  { id: "name", width: 240, priority: 0, flex: true },
  { id: "status", width: 168, priority: 1 },
  { id: "ready", width: 64, priority: 1 },
  { id: "cpu", width: 120, priority: 3 },
  { id: "mem", width: 120, priority: 3 },
  { id: "restarts", width: 64, priority: 4 },
  { id: "gitops", width: 150, priority: 5 },
  { id: "logs", width: 24, priority: 2 },
];
const WL_LABEL: Record<string, string> = { lamp: "", kind: "Kind", name: "Workload", status: "Status", ready: "Ready", cpu: "CPU", mem: "Memory", restarts: "Restarts", gitops: "GitOps owner", logs: "" };

export function WorkloadsView({ cluster }: { cluster: string }) {
  const wl = useFleet((s) => s.workloads);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const reloaderPresent = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.reloaderPresent ?? false);
  const reloaderHealthy = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.reloaderHealthy ?? false);
  const fleetSize = useFleet((s) => s.clusters.length);
  const [pending, setPending] = useState<Pending | null>(null);
  const [compareTarget, setCompareTarget] = useState<WorkloadDTO | null>(null);
  // Cursor and selection are stable keys (kind/namespace/name), never indexes:
  // live emits re-rank the rows.
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [unknownOnly, setUnknownOnly] = useState(false);
  const [logsTarget, setLogsTarget] = useState<{ namespace: string; name: string; kind: WorkloadKind } | null>(null);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const liveCleanupRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    // Live list subscription; usage metrics poll separately. Hydration fills
    // only fields still at their reset defaults, so an intent set just before
    // mount wins.
    const st = useFleet.getState();
    const prefs = loadClusterPrefs(cluster).workloads ?? {};
    if (prefs.needsAttention && !st.workloads.needsAttention) st.setWorkloadsNeedsAttention(true);
    if (prefs.nearLimitSort && !st.workloads.nearLimitSort) st.toggleNearLimitSort();
    const kindsAtDefault = KINDS.every((k) => st.workloads.kindFilter[k]);
    if (prefs.kinds && prefs.kinds.length > 0 && prefs.kinds.length < KINDS.length && kindsAtDefault) st.setWorkloadKindFilter(prefs.kinds);
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

  useEffect(() => {
    if (wl.cluster !== cluster) return;
    saveWorkloadsViewPrefs(cluster, { namespace: wl.namespace, needsAttention: wl.needsAttention, kinds: KINDS.filter((k) => wl.kindFilter[k]), nearLimitSort: wl.nearLimitSort });
    saveSearch(cluster, "workloads", wl.search);
  }, [cluster, wl.cluster, wl.namespace, wl.needsAttention, wl.kindFilter, wl.nearLimitSort, wl.search]);

  // Palette verbs / cross-links: select the workload, then open the verb's
  // confirm or scale input.
  useNavIntent(cluster, ["restart-workload", "scale-workload", "select-workload"], (intent) => {
    if (wl.loading || wl.items.length === 0) return "wait";
    if (!("workloadKind" in intent)) return "missing";
    const w = wl.items.find((x) => x.kind === intent.workloadKind && x.namespace === intent.namespace && x.name === intent.name);
    if (!w) return "missing";
    autoSelect.current = "done";
    setCursorKey(keyOf(w));
    setSelectedKey(keyOf(w));
    if (intent.kind === "restart-workload") setPending({ kind: "restart", w });
    else if (intent.kind === "scale-workload") setPending({ kind: "scale-input", w });
    return "consumed";
  }, [wl.items, wl.loading]);

  const onNamespace = (ns: string) => {
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLiveWorkloads(cluster, ns);
    void getWorkloadMetrics(cluster, ns);
  };
  const onRefresh = () => { listWorkloads(cluster, wl.namespace).then(() => getWorkloadMetrics(cluster, wl.namespace)); };

  const showMetrics = wl.metricsAvailable;
  const coverage = coverageOf(wl, cluster, wl.namespace);
  const scopedItems = useMemo(() => (coverage === "unavailable" ? [] : wl.items), [coverage, wl.items]);
  const counts = summarize(scopedItems, (w) => w.health);
  const receivedAt = useReceivedAt(coverage === "complete" || coverage === "partial" ? wl.items : null);
  const query = wl.search.trim().toLowerCase();
  const filtered = useMemo(() => scopedItems.filter((w) => {
    if (!wl.kindFilter[w.kind as WorkloadKind]) return false;
    if (wl.needsAttention && !isAttention(kindOf(w.health))) return false;
    if (unknownOnly && kindOf(w.health) !== "unknown") return false;
    if (!query) return true;
    const gitops = w.gitops ? `${w.gitops.kind} ${w.gitops.namespace} ${w.gitops.name}` : "";
    const pods = w.pods.map((p) => `${p.name} ${p.node} ${p.reason}`).join(" ");
    return [w.kind, w.namespace, w.name, labelOf(w.health), gitops, pods].some((v) => v.toLowerCase().includes(query));
  }), [scopedItems, wl.kindFilter, wl.needsAttention, unknownOnly, query]);
  const rows = showMetrics && wl.nearLimitSort ? nearLimitSort(filtered) : filtered;
  const cursorIdx = cursorKey === null ? -1 : rows.findIndex((w) => keyOf(w) === cursorKey);

  const closeInspector = useCallback(() => { autoSelect.current = "done"; setSelectedKey(null); }, []);
  const open = useCallback((w: WorkloadDTO) => { setCursorKey(keyOf(w)); setSelectedKey(keyOf(w)); }, []);
  useListKeys({
    count: rows.length,
    selected: cursorIdx,
    onSelect: (idx) => { const w = rows[idx]; if (w) { setCursorKey(keyOf(w)); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const w = rows[idx]; if (w) open(w); },
    onEscape: () => { if (selectedKey) closeInspector(); },
    searchRef,
    extraKeys: { l: (idx) => { const w = rows[idx]; if (w) setLogsTarget({ namespace: w.namespace, name: w.name, kind: w.kind as WorkloadKind }); } },
  });

  // One-shot auto-select of the first attention row from a FRESH read.
  const autoSelect = useRef<"pending" | "done">("pending");
  const itemsAtEntry = useRef<WorkloadDTO[] | null>(null);
  if (itemsAtEntry.current === null) itemsAtEntry.current = wl.items;
  useEffect(() => { autoSelect.current = "pending"; itemsAtEntry.current = useFleet.getState().workloads.items; setSelectedKey(null); setCursorKey(null); }, [cluster]);
  const freshRead = !wl.loading && !wl.error && wl.items !== itemsAtEntry.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead || (coverage !== "complete" && coverage !== "partial")) return;
    autoSelect.current = "done";
    if (selectedKey) return;
    const w = rows.find((x) => isAttention(kindOf(x.health)));
    if (w) open(w);
  }, [freshRead, coverage, rows, selectedKey, open]);

  // Selection lifecycle: a filter or scope excluding it closes the inspector;
  // a workload gone from a complete in-scope read is removed (last evidence,
  // no actions) and any pending verb for it is cancelled.
  const selected = selectedKey ? scopedItems.find((w) => keyOf(w) === selectedKey) : undefined;
  const lastSelected = useRef<WorkloadDTO | null>(null);
  if (selected) lastSelected.current = selected;
  const selNs = selectedKey ? selectedKey.split("/")[1] : "";
  const selInScope = !!selectedKey && (wl.namespace === "" || wl.namespace === selNs);
  const selRemoved = !!selectedKey && selInScope && coverage === "complete" && !selected;
  useEffect(() => {
    if (!selectedKey || coverage === "unavailable") return;
    if (!selInScope) { setSelectedKey(null); return; }
    if (selected && !rows.includes(selected)) setSelectedKey(null);
  }, [rows, selected, selectedKey, selInScope, coverage]);
  useEffect(() => {
    if (!selRemoved || !selectedKey) return;
    setPending((p) => (p && keyOf(p.w) === selectedKey ? null : p));
  }, [selRemoved, selectedKey]);

  const fitted = useMemo(
    () => fitColumns(WL_COLUMNS.filter((c) => showMetrics || (c.id !== "cpu" && c.id !== "mem")), bodyWidth),
    [bodyWidth, showMetrics],
  );
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const clearFilters = () => {
    const st = useFleet.getState();
    st.setWorkloadsSearch("");
    st.setWorkloadsNeedsAttention(false);
    st.setWorkloadKindFilter(KINDS);
    setUnknownOnly(false);
  };
  const oneKind = KINDS.filter((k) => wl.kindFilter[k]);
  const kindValue = oneKind.length === KINDS.length ? "" : oneKind.length === 1 ? oneKind[0] : "custom";

  let state: DatasetState = "ready";
  if (wl.loading && scopedItems.length === 0) state = "loading";
  else if (coverage === "unavailable" && wl.error) state = "unreadable";
  else if (coverage === "unavailable") state = "loading";
  else if (scopedItems.length === 0) state = "empty";
  else if (rows.length === 0) state = "no-match";
  const scope = wl.namespace || "all namespaces";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={8} label="loading workloads" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title={`Workloads in ${scope} could not be read.`} hint={wl.error ?? undefined} action={{ label: "Retry", onClick: onRefresh }} />
    : state === "empty" ? <EmptyState icon={<IconBox size={28} stroke={1.2} />} title={`No workloads in ${scope}.`} />
    : <EmptyState icon={<IconBox size={28} stroke={1.2} />} title="No workloads match the current filters."
        hint={wl.needsAttention && !wl.search && (counts.unknown > 0 || coverage !== "complete")
          ? `No known attention - but ${counts.unknown > 0 ? `${counts.unknown} workload${counts.unknown === 1 ? " is" : "s are"} unverified` : "the read was incomplete"}.`
          : `${scopedItems.length} workload${scopedItems.length === 1 ? "" : "s"} in ${scope}.`}
        action={{ label: "Clear filters", onClick: clearFilters }} />;

  const tone = attentionTone(counts);
  const partial = coverage === "partial" ? "+" : "";
  const filterBits = [wl.needsAttention ? "Attention" : unknownOnly ? "Unknown" : "", kindValue && kindValue !== "custom" ? kindShort[kindValue as WorkloadKind] : kindValue === "custom" ? "kinds" : "", wl.search ? "search" : ""].filter(Boolean);

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ flex: 1, minHeight: 0 }}>
        <LensLayout
          label="Workloads"
          title="Workloads"
          summary={coverage === "unavailable" ? null : (
            <>
              <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{counts.total}{partial}</span>
              {counts.attention > 0 ? (
                <span className="t-status" style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
                  {tone === "danger" ? "▲ " : ""}{counts.attention}{partial} need attention
                </span>
              ) : <span className="t-secondary">{counts.unknown > 0 || coverage !== "complete" ? "no known attention" : "none need attention"}</span>}
              <span className="t-secondary">
                {[counts.fault ? `${counts.fault} fault` : "", counts.degraded ? `${counts.degraded} degraded` : "", counts.unknown ? `${counts.unknown} unverified` : "", counts.progressing ? `${counts.progressing} rolling out` : "", counts.paused ? `${counts.paused} scaled to 0` : ""].filter(Boolean).join(" · ")}
              </span>
            </>
          )}
          headerEnd={
            <>
              <LiveIndicator live={wl.live} />
              <UpdatedAt at={receivedAt} />
              <button className="l-btn" onClick={onRefresh}>Refresh</button>
            </>
          }
          controls={
            <>
              <label style={{ display: "flex" }}>
                <span className="sr-only">Namespace</span>
                <select className="l-input" value={wl.namespace} onChange={(e) => onNamespace(e.target.value)}>
                  <option value="">All namespaces</option>
                  {wl.namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
                </select>
              </label>
              <label style={{ display: "flex" }}>
                <span className="sr-only">Kind</span>
                <select className="l-input" value={kindValue} onChange={(e) => useFleet.getState().setWorkloadKindFilter(e.target.value ? [e.target.value as WorkloadKind] : KINDS)}>
                  <option value="">All kinds</option>
                  {KINDS.map((k) => <option key={k} value={k}>{k}s</option>)}
                  {kindValue === "custom" && <option value="custom" disabled>Custom selection</option>}
                </select>
              </label>
              <button className="l-chip" aria-pressed={!wl.needsAttention && !unknownOnly} onClick={() => { setUnknownOnly(false); if (wl.needsAttention) useFleet.getState().toggleNeedsAttention(); }}>
                All <span style={{ color: "var(--color-text-tertiary)" }}>{counts.total || ""}</span>
              </button>
              <button className="l-chip" aria-pressed={wl.needsAttention} onClick={() => { setUnknownOnly(false); if (!wl.needsAttention) useFleet.getState().toggleNeedsAttention(); }}>
                Attention {counts.attention > 0 && <span style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{counts.attention}</span>}
              </button>
              {(counts.unknown > 0 || unknownOnly) && (
                <button className="l-chip" aria-pressed={unknownOnly} onClick={() => { if (wl.needsAttention) useFleet.getState().toggleNeedsAttention(); setUnknownOnly(!unknownOnly); }}>
                  Unknown <span style={{ color: "var(--color-text-tertiary)" }}>{counts.unknown || ""}</span>
                </button>
              )}
              <label style={{ display: "flex", flex: "0 1 280px", minWidth: 160 }}>
                <span className="sr-only">Search workloads</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} value={wl.search} placeholder="Search workloads" onChange={(e) => useFleet.getState().setWorkloadsSearch(e.target.value)} />
              </label>
              {showMetrics && (
                <button className="l-chip" aria-pressed={wl.nearLimitSort} onClick={() => useFleet.getState().toggleNearLimitSort()} title="Sort by how close usage is to the limit">
                  Sort: {wl.nearLimitSort ? "near limit" : "faults first"}
                </button>
              )}
            </>
          }
          notice={(wl.error && coverage !== "unavailable") || wl.truncated ? (
            <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              {wl.error && coverage !== "unavailable" && <WatchErrorBanner tool="Workloads" failure="read failed - showing the last good read" error={wl.error} />}
              {wl.truncated && (
                <div role="status" className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 8, padding: "0 10px", borderRadius: 4, background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)", color: "var(--color-text-warning)" }}>
                  <IconAlertTriangle size={14} stroke={1.6} aria-hidden="true" />
                  <span style={{ flex: 1, minWidth: 0 }}>The list hit the 5000 cap: counts are a lower bound and a fault past the cap is not shown. Scope by namespace for the full picture.</span>
                </div>
              )}
            </div>
          ) : undefined}
          state={state}
          stateView={stateView}
          body={
            <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
              <div className="lens-table-head" role="row" style={gridStyle}>
                {WL_COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{WL_LABEL[c.id]}</span>)}
              </div>
              <VirtualList
                ref={listRef}
                items={rows}
                rowHeight={ROW_HEIGHT}
                rowKey={(w) => keyOf(w)}
                style={{ flex: 1, minHeight: 0 }}
                render={(w) => {
                  const k = keyOf(w);
                  const quietStale = w.stale && !isAttention(kindOf(w.health));
                  return (
                    <div
                      key={k}
                      role="button"
                      tabIndex={0}
                      aria-selected={k === cursorKey}
                      data-cursor={k === cursorKey ? "true" : undefined}
                      data-selected={k === selectedKey ? "true" : undefined}
                      data-rowhost="true"
                      className="lens-row"
                      style={{ ...gridStyle, ...staleStyle(quietStale) }}
                      onClick={() => open(w)}
                      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(w); } }}
                    >
                      {show("lamp") && <VerdictLamp health={w.health} size={8} />}
                      {show("kind") && <span style={{ color: "var(--color-text-tertiary)" }}>{kindShort[w.kind as WorkloadKind]}</span>}
                      {show("name") && (
                        <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                          <span className="lens-cell-name" title={`${w.namespace}/${w.name}`}>
                            <span style={{ color: "var(--color-text-tertiary)" }}>{w.namespace} / </span>
                            <span style={{ fontWeight: k === selectedKey ? 600 : 400 }}>{w.name}</span>
                          </span>
                          <CopyButton text={w.name} label={`copy workload name ${w.name}`} appearOnRowHover />
                        </span>
                      )}
                      {show("status") && <span style={ellipsis}><VerdictLabel health={w.health} /></span>}
                      {show("ready") && <span style={{ color: "var(--color-text-secondary)" }}>{w.ready} / {w.desired}</span>}
                      {show("cpu") && <ResourceCellView resource="cpu" cell={w.resources.cpu} hasPods={w.pods.length > 0} />}
                      {show("mem") && <ResourceCellView resource="mem" cell={w.resources.mem} hasPods={w.pods.length > 0} />}
                      {show("restarts") && <span style={{ color: w.restarts > 0 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{w.restarts}</span>}
                      {show("gitops") && (
                        <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={w.gitops ? `Flux ownership label: ${w.gitops.kind} ${w.gitops.namespace}/${w.gitops.name}` : undefined}>
                          {w.gitops ? `flux ${w.gitops.kind === "HelmRelease" ? "hr" : "ks"}/${w.gitops.name}` : "-"}
                        </span>
                      )}
                      {show("logs") && (
                        <LogsIconButton label={`aggregate logs for ${w.namespace}/${w.name}`} onClick={() => setLogsTarget({ namespace: w.namespace, name: w.name, kind: w.kind as WorkloadKind })} />
                      )}
                    </div>
                  );
                }}
              />
            </LensTable>
          }
          footer={<span className="t-legend">j k navigate · ↵ inspect · l logs · / search</span>}
          footerEnd={
            <>
              <span className="t-secondary">{rows.length} of {scopedItems.length}{partial} workloads{filterBits.length ? ` · filter: ${filterBits.join(", ")}` : ""}</span>
              {filterBits.length > 0 && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all {scopedItems.length}{partial} workloads</button>}
            </>
          }
          inspector={selectedKey && wl.cluster === cluster && (selected || selRemoved) && lastSelected.current ? (
            <WorkloadInspector
              key={`${cluster}/${selectedKey}`}
              cluster={cluster}
              workload={selected ?? lastSelected.current}
              removed={selRemoved}
              showMetrics={showMetrics}
              isProtected={isProtected}
              reloaderPresent={reloaderPresent}
              reloaderHealthy={reloaderHealthy}
              pending={pending}
              onClose={closeInspector}
              onLogs={(w) => setLogsTarget({ namespace: w.namespace, name: w.name, kind: w.kind as WorkloadKind })}
              onRestart={(w) => setPending({ kind: "restart", w })}
              onScale={(w) => setPending({ kind: "scale-input", w })}
              onScaleConfirm={(w, replicas) => {
                if (isProtected) setPending({ kind: "scale", w, replicas });
                else { setPending(null); void scaleWorkload(cluster, w.kind, w.namespace, w.name, replicas); }
              }}
              onCancelScale={() => setPending(null)}
              onDeletePod={(w, pod) => setPending({ kind: "delete-pod", w, pod })}
              onOpenPod={(w, pod) => { useFleet.getState().setSection("pods"); void openPodDetail(cluster, w.namespace, pod.name); }}
              onOpenNode={(node) => { useFleet.getState().setSection("nodes"); useFleet.getState().requestNavIntent({ kind: "select-node", cluster, node }); }}
              onOpenFluxOwner={(w) => {
                if (!w.gitops) return;
                useFleet.getState().setSection("gitops");
                useFleet.getState().requestNavIntent({ kind: "select-flux", cluster, fluxKind: w.gitops.kind, namespace: w.gitops.namespace, name: w.gitops.name });
              }}
              onCompare={fleetSize > 1 ? (w) => setCompareTarget(w) : undefined}
              onUndo={(w, rev) => setPending({ kind: "undo", w, rev })}
              onPause={(w, pause) => setPending({ kind: "pause", w, pause })}
            />
          ) : undefined}
          inspectorLabel="Workload details"
          onCloseInspector={closeInspector}
        />
      </div>

      {compareTarget && (
        <FleetCompare origin={cluster} kind={compareTarget.kind} namespace={compareTarget.namespace} name={compareTarget.name} onClose={() => setCompareTarget(null)} />
      )}

      {logsTarget && (
        <WorkloadLogsDock
          cluster={cluster}
          target={logsTarget}
          onClose={() => setLogsTarget(null)}
          onPopOut={async (container) => {
            const ok = await openWorkloadLogsWindow(cluster, logsTarget.namespace, logsTarget.kind, logsTarget.name, container);
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
          onConfirm={() => { const { w } = pending; setPending(null); void rolloutRestart(cluster, w.kind, w.namespace, w.name); }}
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
          onConfirm={() => { const { w, replicas } = pending; setPending(null); void scaleWorkload(cluster, w.kind, w.namespace, w.name, replicas); }}
        />
      )}
      {pending?.kind === "undo" && (
        <ConfirmDialog
          title="roll back"
          cluster={cluster}
          detail={
            `${pending.w.kind.toLowerCase()} ${pending.w.namespace}/${pending.w.name} → revision ${pending.rev.revision}` +
            (pending.rev.images.length > 0 ? ` (${pending.rev.images.join(", ")})` : "") +
            (pending.w.gitops ? ` — GitOps-owned (${pending.w.gitops.kind}/${pending.w.gitops.name}): Flux will revert this on its next reconcile. Undo is incident response; make the real fix in Git.` : "")
          }
          protected={isProtected}
          danger
          confirmLabel="Roll back"
          onCancel={() => setPending(null)}
          onConfirm={() => { const { w, rev } = pending; setPending(null); void rolloutUndo(cluster, w.kind, w.namespace, w.name, rev.revision); }}
        />
      )}
      {pending?.kind === "pause" && (
        <ConfirmDialog
          title={pending.pause ? "pause rollouts" : "resume rollouts"}
          cluster={cluster}
          detail={`deployment ${pending.w.namespace}/${pending.w.name} — ${pending.pause ? "hold rollouts (running pods untouched)" : "apply any held template changes"}`}
          protected={isProtected}
          confirmLabel={pending.pause ? "Pause" : "Resume"}
          onCancel={() => setPending(null)}
          onConfirm={() => { const { w, pause } = pending; setPending(null); void rolloutPause(cluster, w.namespace, w.name, pause); }}
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
          onConfirm={() => { const { w, pod } = pending; setPending(null); void deletePod(cluster, w.namespace, pod.name).then(() => listWorkloads(cluster, wl.namespace)); }}
        />
      )}
    </div>
  );
}

// WorkloadInspector - investigation order: verdict; Logs (primary), Restart
// (secondary), More (scale, pause/resume, compare, manifest, delete a pod);
// pods, rollout history (undo), usage, reloads-on, then controller facts.
function WorkloadInspector({
  cluster, workload, removed, showMetrics, isProtected, reloaderPresent, reloaderHealthy, pending,
  onClose, onLogs, onRestart, onScale, onScaleConfirm, onCancelScale, onDeletePod, onOpenPod, onOpenNode, onOpenFluxOwner, onCompare, onUndo, onPause,
}: {
  cluster: string;
  workload: WorkloadDTO;
  removed: boolean;
  showMetrics: boolean;
  isProtected: boolean;
  reloaderPresent: boolean;
  reloaderHealthy: boolean;
  pending: Pending | null;
  onClose: () => void;
  onLogs: (w: WorkloadDTO) => void;
  onRestart: (w: WorkloadDTO) => void;
  onScale: (w: WorkloadDTO) => void;
  onScaleConfirm: (w: WorkloadDTO, replicas: number) => void;
  onCancelScale: () => void;
  onDeletePod: (w: WorkloadDTO, pod: PodDTO) => void;
  onOpenPod: (w: WorkloadDTO, pod: PodDTO) => void;
  onOpenNode: (node: string) => void;
  onOpenFluxOwner: (w: WorkloadDTO) => void;
  onCompare?: (w: WorkloadDTO) => void;
  onUndo: (w: WorkloadDTO, rev: RolloutRevisionDTO) => void;
  onPause: (w: WorkloadDTO, pause: boolean) => void;
}) {
  const w = workload;
  const live = !removed;
  // Selector-matched pods carry no verdict of their own: native facts only.
  const notReadyPods = w.pods.filter((p) => !p.ready).length;
  const deleteTarget = w.pods.find((p) => !p.ready) ?? w.pods[0] ?? null;
  const scaling = pending?.kind === "scale-input" && keyOf(pending.w) === keyOf(w);
  const manifestRef = wellKnownResourceRef("apps", w.kind);
  const scalable = w.kind === "Deployment" || w.kind === "StatefulSet";

  const more: MenuItem[] = [
    ...(scalable ? [{ label: `Scale (${w.desired} replicas)…`, onSelect: () => onScale(w) }] : []),
    ...(w.kind === "Deployment" ? [{ label: w.paused ? "Resume rollouts" : "Pause rollouts", onSelect: () => onPause(w, !w.paused), hint: w.paused ? "spec.paused=false" : "Hold rollouts without touching pods (spec.paused=true)" }] : []),
    ...(onCompare ? [{ label: "Compare across the fleet", onSelect: () => onCompare(w) }] : []),
    ...(manifestRef ? [{ label: "Edit manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(w.namespace, w.name); } }] : []),
    ...(deleteTarget ? [{ kind: "separator" as const }, { label: `Delete pod ${deleteTarget.name}`, danger: true, onSelect: () => onDeletePod(w, deleteTarget) }] : []),
  ];

  const ownerText = w.gitops ? `${w.gitops.kind} ${w.gitops.namespace}/${w.gitops.name}` : "";

  return (
    <div data-testid="workload-detail-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind={w.kind}
        namespace={w.namespace}
        name={w.name}
        meta={
          <>
            {isProtected && <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>protected · </span>}
            {w.gitops ? (
              <button onClick={() => onOpenFluxOwner(w)} title={`open ${ownerText} in the Flux lens`} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}>owned by {ownerText}</button>
            ) : "not GitOps-owned"}
            {w.paused ? " · rollouts paused" : ""}
          </>
        }
        onClose={onClose}
        verdict={removed ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">Workload removed.</span> It is no longer in the current read of {w.namespace}. The details below are its last evidence; actions are disabled.
          </div>
        ) : <VerdictStatus health={w.health} detail={`${w.ready}/${w.desired} ready · ${w.updated} updated · ${w.available} available${w.restarts ? ` · ${w.restarts} restarts` : ""}`} />}
        primary={live ? { label: "Logs", kbd: "l", onSelect: () => onLogs(w) } : undefined}
        secondary={live ? { label: "Restart", onSelect: () => onRestart(w) } : undefined}
        more={live ? more : []}
        tab="overview"
        onTab={() => { const nav = useFleet.getState(); if (manifestRef) { nav.openResource(manifestRef); nav.openInstance(w.namespace, w.name); } }}
        manifest={null}
      >
        {live && scaling && (
          <InspectorBlock title="Scale">
            <ScalePopover initial={w.desired} onConfirm={(n) => onScaleConfirm(w, n)} onCancel={onCancelScale} />
          </InspectorBlock>
        )}

        <InspectorBlock title="Pods · selector match" count={w.pods.length}
          extra={notReadyPods > 0 ? <span className="t-secondary">{notReadyPods} not ready</span> : undefined}>
          {w.pods.length === 0 ? <span className="t-secondary">No pods match this workload's selector.</span> : (
            <div style={{ display: "flex", flexDirection: "column" }}>
              {w.pods.slice(0, 8).map((p) => (
                <div key={p.name} className="t-identifier" style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) auto auto", gap: 10, padding: "3px 0", alignItems: "baseline" }}>
                  <button onClick={() => live && onOpenPod(w, p)} aria-label={`open pod ${p.name}`} title="open in Pods"
                    style={{ minWidth: 0, background: "none", border: 0, padding: 0, textAlign: "left", cursor: "pointer", color: "var(--color-text-info)", font: "inherit", overflowWrap: "anywhere" }}>{p.name}</button>
                  <span style={{ color: "var(--color-text-secondary)" }}>{p.ready ? "ready" : p.reason || "not ready"}{p.restarts ? ` · ${p.restarts}×` : ""}</span>
                  <button onClick={() => onOpenNode(p.node)} title={`open node ${p.node}`} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-tertiary)", font: "inherit" }}>{p.node || "-"}</button>
                </div>
              ))}
              {w.pods.length > 8 && <span className="t-secondary">+{w.pods.length - 8} more pods</span>}
            </div>
          )}
        </InspectorBlock>

        <RolloutSection cluster={cluster} workload={w} onUndo={live ? onUndo : undefined} />

        {showMetrics && (
          <InspectorBlock title="Usage">
            <div className="insp-kv">
              <span>CPU</span><span><ResourceCellView resource="cpu" cell={w.resources.cpu} hasPods={w.pods.length > 0} /></span>
              <span>memory</span><span><ResourceCellView resource="mem" cell={w.resources.mem} hasPods={w.pods.length > 0} /></span>
            </div>
            <div className="t-secondary">
              CPU {w.pods.length === 0 ? "-" : `usage ${w.resources.cpu.usage == null ? "-" : fmtCpu(w.resources.cpu.usage)} · req ${w.resources.cpu.request == null ? "-" : fmtCpu(w.resources.cpu.request)} · ${w.resources.cpu.limit == null ? "no limit" : `lim ${fmtCpu(w.resources.cpu.limit)}`}`} {riskLabel("cpu", w.resources.cpu)}
              <br />
              Memory {w.pods.length === 0 ? "-" : `usage ${w.resources.mem.usage == null ? "-" : fmtMem(w.resources.mem.usage)} · req ${w.resources.mem.request == null ? "-" : fmtMem(w.resources.mem.request)} · ${w.resources.mem.limit == null ? "no limit" : `lim ${fmtMem(w.resources.mem.limit)}`}`} {riskLabel("mem", w.resources.mem)}
            </div>
            <WorkloadSparkRow cluster={cluster} kind={w.kind} namespace={w.namespace} name={w.name} />
          </InspectorBlock>
        )}

        {w.reloader && (
          <InspectorBlock title="Reloads on" extra={w.reloader.auto ? <span className="t-secondary" title="reloader.stakater.com/auto: reloads on any mounted ConfigMap/Secret">auto</span> : undefined}>
            {!(reloaderPresent && reloaderHealthy) && (
              <div className="t-secondary" style={{ color: "var(--color-text-warning)", display: "flex", gap: 6, alignItems: "center" }}>
                <IconAlertTriangle size={12} stroke={1.6} aria-hidden="true" />
                {reloaderPresent ? "Reloader is present but not ready - reloads may not fire until it recovers." : "Reloader not detected on this cluster - these annotations may be inert."}
              </div>
            )}
            <div style={{ display: "flex", flexWrap: "wrap", gap: 5 }}>
              {w.reloader.configMaps.map((n) => <ReloadChip key={`cm-${n}`} kind="ConfigMap" name={n} namespace={w.namespace} />)}
              {w.reloader.secrets.map((n) => <ReloadChip key={`sec-${n}`} kind="Secret" name={n} namespace={w.namespace} />)}
              {w.reloader.configMaps.length === 0 && w.reloader.secrets.length === 0 && <span className="t-secondary">{w.reloader.auto ? "no mounted config" : "-"}</span>}
            </div>
          </InspectorBlock>
        )}

        <InspectorBlock title="Controller" defaultOpen={false}>
          <div className="insp-kv">
            <span>reason</span><span>{w.reason || "-"}</span>
            <span>ready</span><span>{w.ready} / {w.desired}</span>
            <span>updated</span><span>{w.updated}</span>
            <span>available</span><span>{w.available}</span>
            <span>restarts</span><span>{w.restarts}</span>
            {w.generation ? <><span>generation</span><span>{w.observedGeneration ?? "-"} observed / {w.generation}</span></> : null}
          </div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
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
// RolloutSection: the workload's revision history (newest first) with per-row
// undo. History is fetched on selection; a failed read renders its error, an
// empty history says so - never a silently missing section. The undo button
// only OPENS the confirm (danger + prd arming + the GitOps-owned warning);
// nothing fires from here.
const ROLLOUT_SHOWN = 8;

function RolloutSection({ cluster, workload, onUndo }: {
  cluster: string;
  workload: WorkloadDTO;
  onUndo?: (w: WorkloadDTO, rev: RolloutRevisionDTO) => void; // absent = read-only (removed object)
}) {
  const [history, setHistory] = useState<{ key: string; revisions: RolloutRevisionDTO[]; error?: string } | null>(null);
  const key = keyOf(workload);
  useEffect(() => {
    let live = true;
    setHistory(null);
    void rolloutHistory(cluster, workload.kind, workload.namespace, workload.name).then((r) => {
      if (live) setHistory({ key, revisions: r.revisions, error: r.error });
    });
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, key]);

  const rows = history?.key === key ? history.revisions : null;
  return (
    <div data-testid="rollout-section">
      <InspectorBlock title="Rollout history" count={rows ? rows.length : undefined}>
        {history?.error ? (
          <div className="t-body" style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{history.error}</div>
        ) : rows === null ? (
          <span className="t-secondary">Reading revisions…</span>
        ) : rows.length === 0 ? (
          <span className="t-secondary">No revision history recorded.</span>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
            {rows.slice(0, ROLLOUT_SHOWN).map((r) => (
              <div key={r.revision} data-rowhost="true" className="t-identifier" style={{ display: "flex", alignItems: "baseline", gap: 8, minHeight: 22 }}>
                <span style={{ color: "var(--color-text-secondary)", minWidth: 28 }}>#{r.revision}</span>
                <span style={{ color: "var(--color-text-tertiary)", minWidth: 34 }}>{ago(r.ageSeconds)}</span>
                <span
                  style={{ ...ellipsis, flex: 1, color: "var(--color-text-secondary)" }}
                  title={[...r.images, r.changeCause ? `cause: ${r.changeCause}` : ""].filter(Boolean).join("\n")}
                >
                  {r.images.length > 0 ? r.images.join(" · ") : "-"}
                </span>
                {r.current ? (
                  <span className="t-legend" style={{ color: "var(--color-text-success)", flexShrink: 0 }}>current</span>
                ) : onUndo ? (
                  <button
                    className="l-btn"
                    onClick={() => onUndo(workload, r)}
                    title={`Roll back to revision ${r.revision}`}
                    style={{ height: 22, color: "var(--color-text-danger)", flexShrink: 0 }}
                  >undo →</button>
                ) : null}
              </div>
            ))}
            {rows.length > ROLLOUT_SHOWN && <span className="t-secondary">+{rows.length - ROLLOUT_SHOWN} older revisions</span>}
          </div>
        )}
      </InspectorBlock>
    </div>
  );
}

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


