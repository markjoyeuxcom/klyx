import { useEffect, useState, useCallback, useMemo, useRef } from "react";
import { IconExternalLink, IconX, IconAlertTriangle, IconTerminal2, IconCircleDot } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { PodDetailDTO, PodSummaryDTO, ContainerSummaryDTO, ContainerUsageDTO, PodUsageResultDTO } from "../store/fleet";
import { listPods, openLivePods, openPodDetail, deletePod } from "../bridge/pods";
import { getPodContainerUsage } from "../bridge/metrics";
import { rightsizing, fmtCpu, fmtMem } from "./saturation";
import { openLogsWindow } from "../bridge/windows";
import { rolloutRestart } from "../bridge/workloads";
import { armProtected } from "../bridge/configsvc";
import { copyExecCommand, openExecTerminal, openDebugTerminal } from "../bridge/exec";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { DescribePane } from "../chrome/DescribePane";
import { LogsPane } from "./LogsPane";
import { ForwardPopover } from "./ForwardPopover";
import { VirtualList } from "../chrome/VirtualList";
import type { VirtualListHandle } from "../chrome/VirtualList";
import { useResizableDock } from "../chrome/useResizableDock";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { EmptyState, LiveIndicator, SkeletonRows, btn, ellipsis } from "../chrome/primitives";
import { CopyButton } from "../chrome/CopyButton";
import { ScratchPanel } from "./ScratchPanel";
import { YamlBlock } from "../chrome/YamlBlock";
import { InspectorConditions, InspectorEvents } from "./ResourceInspector";
import { loadClusterPrefs, loadSearch, savePodsViewPrefs, saveSearch } from "../store/persist";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { UpdatedAt, useReceivedAt } from "../chrome/Age";
import { wellKnownResourceRef } from "./resourceRefs";
import { LensLayout, LensTable, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { VerdictLabel, VerdictLamp, VerdictStatus } from "../health/VerdictView";
import { attentionTone, coverageOf, isAttention, kindOf, labelOf, summarize } from "../health/verdict";

// Pods lens on the shared workspace template (2026-10 redesign pilot). The
// template owns geometry; this file owns pod data, filters, selection and the
// day-2 operations, which route through the same confirm/arming as before.

function readyOf(p: PodSummaryDTO): string {
  const nonInit = p.containers.filter((c) => !c.init);
  return `${nonInit.filter((c) => c.ready).length}/${nonInit.length}`;
}

function ago(s: number): string {
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`;
}

// imageShort strips the registry/repository path from an image ref, keeping the
// final segment with its tag; long sha256 digests shorten to 12 hex chars.
export function imageShort(image: string): string {
  const lastSlash = image.lastIndexOf("/");
  let s = lastSlash >= 0 ? image.slice(lastSlash + 1) : image;
  const at = s.indexOf("@sha256:");
  if (at >= 0) s = s.slice(0, at + "@sha256:".length + 12) + "…";
  return s;
}

type LogsTarget = { namespace: string; name: string; containers: ContainerSummaryDTO[] };

export function PodsView({ cluster }: { cluster: string }) {
  const pods = useFleet((s) => s.pods);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  const [cursor, setCursor] = useState(-1);
  const [unknownOnly, setUnknownOnly] = useState(false);
  const [logsTarget, setLogsTarget] = useState<LogsTarget | null>(null);
  const [bulk, setBulk] = useState<Set<string>>(new Set());
  const [bulkConfirm, setBulkConfirm] = useState(false);
  const [scratchOpen, setScratchOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const liveCleanupRef = useRef<(() => void) | null>(null);

  // Live subscription for the persisted namespace (or all namespaces).
  useEffect(() => {
    const st = useFleet.getState();
    const prefs = loadClusterPrefs(cluster).pods ?? {};
    if (prefs.needsAttention && !st.pods.needsAttention) st.setPodsNeedsAttention(true);
    const savedSearch = loadSearch(cluster, "pods");
    if (savedSearch && st.pods.search === "") st.setPodsSearch(savedSearch);
    liveCleanupRef.current = openLivePods(cluster, st.pods.namespace || (prefs.namespace ?? ""));
    return () => {
      if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
      useFleet.getState().clearPods();
    };
  }, [cluster]);

  useEffect(() => {
    if (pods.cluster !== cluster) return;
    savePodsViewPrefs(cluster, { namespace: pods.namespace, needsAttention: pods.needsAttention });
    saveSearch(cluster, "pods", pods.search);
  }, [cluster, pods.cluster, pods.namespace, pods.needsAttention, pods.search]);

  const nsRequest = useFleet((s) => s.pods.nsRequest);
  useEffect(() => {
    if (!nsRequest) return;
    useFleet.getState().clearPodsNsRequest();
    if (nsRequest.cluster !== cluster) return;
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLivePods(cluster, nsRequest.namespace);
  }, [nsRequest, cluster]);

  useNavIntent(cluster, ["logs-pod", "select-pod"], (intent) => {
    if (pods.loading || pods.items.length === 0) return "wait";
    if (!("namespace" in intent)) return "missing";
    const p = pods.items.find((x) => x.namespace === intent.namespace && x.name === intent.name);
    if (!p) return "missing";
    if (intent.kind === "logs-pod") setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers });
    else void openPodDetail(cluster, p.namespace, p.name);
    return "consumed";
  }, [pods.items, pods.loading]);

  const onNamespace = (ns: string) => {
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLivePods(cluster, ns);
  };
  const onRefresh = () => { void listPods(cluster, pods.namespace); };

  // Rows count only when read for THIS cluster+namespace (health model 1b).
  const coverage = coverageOf(pods, cluster, pods.namespace);
  const scopedItems = useMemo(() => (coverage === "unavailable" ? [] : pods.items), [coverage, pods.items]);
  const counts = summarize(scopedItems, (p) => p.health);
  const receivedAt = useReceivedAt(coverage === "complete" || coverage === "partial" ? pods.items : null);

  const filtered = useMemo(() => scopedItems.filter((p) => {
    const k = kindOf(p.health);
    if (pods.needsAttention && !isAttention(k)) return false;
    if (unknownOnly && k !== "unknown") return false;
    if (pods.search) {
      const q = pods.search.toLowerCase();
      return p.name.toLowerCase().includes(q) || p.namespace.toLowerCase().includes(q) || labelOf(p.health).toLowerCase().includes(q);
    }
    return true;
  }), [scopedItems, pods.needsAttention, pods.search, unknownOnly]);

  const effectiveCursor = filtered.length === 0 ? -1 : Math.min(cursor, filtered.length - 1);

  // Selection (spec §3): on the first successful read RECEIVED after this
  // route entry (never the cached rows present at entry, never a pending or
  // failed read), auto-select the first visible attention row once - never
  // again after Escape/close, on later watches or filter changes - and never
  // move focus.
  const autoSelect = useRef<"pending" | "done">("pending");
  const itemsAtEntry = useRef<PodSummaryDTO[] | null>(null);
  if (itemsAtEntry.current === null) itemsAtEntry.current = pods.items;
  useEffect(() => { autoSelect.current = "pending"; itemsAtEntry.current = useFleet.getState().pods.items; }, [cluster]);
  const freshRead = !pods.loading && !pods.error && pods.items !== itemsAtEntry.current;
  useEffect(() => {
    if (autoSelect.current === "done") return;
    if (!freshRead || (coverage !== "complete" && coverage !== "partial")) return;
    autoSelect.current = "done";
    if (useFleet.getState().pods.selected) return;
    const idx = filtered.findIndex((p) => isAttention(kindOf(p.health)));
    if (idx < 0) return;
    setCursor(idx);
    void openPodDetail(cluster, filtered[idx].namespace, filtered[idx].name);
  }, [coverage, filtered, cluster, freshRead]);

  // Selection exclusion: a filter or a namespace scope that excludes the
  // selected pod closes the inspector. A pod that the complete, current read
  // no longer contains (in a scope that would include it) was REMOVED: the
  // inspector keeps its identity and last evidence but disables operations.
  const sel = pods.selected;
  const selInScope = !!sel && (pods.namespace === "" || pods.namespace === sel.namespace);
  const selRow = sel ? scopedItems.find((p) => p.namespace === sel.namespace && p.name === sel.name) : undefined;
  const selRemoved = !!sel && selInScope && coverage === "complete" && !selRow;
  useEffect(() => {
    if (!sel || coverage === "unavailable") return;
    if (!selInScope) { useFleet.getState().selectPod(null); return; }
    if (selRow && !filtered.includes(selRow)) useFleet.getState().selectPod(null);
  }, [filtered, selRow, sel, selInScope, coverage]);

  const select = useCallback((idx: number) => { setCursor(idx); listRef.current?.scrollToIndex(idx); }, []);
  const activate = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) void openPodDetail(cluster, p.namespace, p.name);
  }, [filtered, cluster]);
  const closeInspector = useCallback(() => {
    autoSelect.current = "done";
    useFleet.getState().selectPod(null);
  }, []);
  const logsFor = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers });
  }, [filtered]);
  const toggleBulk = useCallback((key: string) => {
    setBulk((prev) => { const next = new Set(prev); if (next.has(key)) next.delete(key); else next.add(key); return next; });
  }, []);
  const bulkKey = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) toggleBulk(`${p.namespace}/${p.name}`);
  }, [filtered, toggleBulk]);
  useEffect(() => { setBulk(new Set()); }, [cluster]);

  useListKeys({
    count: filtered.length,
    selected: effectiveCursor,
    onSelect: select,
    onActivate: activate,
    onEscape: () => { if (pods.selected) closeInspector(); },
    searchRef,
    extraKeys: { l: logsFor, x: bulkKey },
  });

  const bulkTargets = pods.items.filter((p) => bulk.has(`${p.namespace}/${p.name}`));
  const runBulkDelete = useCallback(async (typed: string) => {
    const targets = useFleet.getState().pods.items.filter((p) => bulk.has(`${p.namespace}/${p.name}`));
    setBulkConfirm(false);
    setBulk(new Set());
    for (let i = 0; i < targets.length; i++) {
      const p = targets[i];
      // Each destructive call on a protected cluster after the first re-arms
      // with the same typed confirmation; a failed re-arm stops honestly.
      if (isProtected && i > 0) {
        const r = await armProtected(cluster, typed);
        if (!r.ok) {
          useFleet.getState().setActionStatus({ kind: "error", message: `bulk delete stopped at ${p.namespace}/${p.name}: ${r.error || "could not re-arm protected action"}` });
          return;
        }
      }
      await deletePod(cluster, p.namespace, p.name);
    }
  }, [bulk, cluster, isProtected]);

  const clearFilters = () => {
    useFleet.getState().setPodsSearch("");
    useFleet.getState().setPodsNeedsAttention(false);
    setUnknownOnly(false);
  };

  // ---- dataset state ------------------------------------------------------
  let state: DatasetState = "ready";
  if (pods.loading && scopedItems.length === 0) state = "loading";
  else if (coverage === "unavailable" && pods.error) state = "unreadable";
  else if (coverage === "unavailable") state = "loading";
  else if (scopedItems.length === 0) state = "empty";
  else if (filtered.length === 0) state = "no-match";
  const scope = pods.namespace || "all namespaces";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={8} label="loading pods" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title={`Pods in ${scope} could not be read.`} hint={pods.error ?? undefined} action={{ label: "Retry", onClick: onRefresh }} />
    : state === "empty" ? <EmptyState icon={<IconCircleDot size={28} stroke={1.2} />} title={`No pods in ${scope}.`} hint={receivedAt ? "The read succeeded and returned no pods." : undefined} />
    : <EmptyState icon={<IconCircleDot size={28} stroke={1.2} />} title="No pods match the current filters." hint={`${scopedItems.length} pod${scopedItems.length === 1 ? "" : "s"} in ${scope}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // ---- header: one entity-scoped total and one attention summary ----------
  const tone = attentionTone(counts);
  const partial = coverage === "partial" ? "+" : "";
  const summary = coverage === "unavailable" ? null : (
    <>
      <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{counts.total}{partial}</span>
      {counts.attention > 0 ? (
        <span className="t-status" style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
          {tone === "danger" ? "▲ " : ""}{counts.attention}{partial} need attention
        </span>
      ) : (
        <span className="t-secondary">{counts.unknown > 0 || coverage !== "complete" ? "no known attention" : "none need attention"}</span>
      )}
      {(counts.fault > 0 || counts.degraded > 0 || counts.unknown > 0) && (
        <span className="t-secondary">
          {[counts.fault ? `${counts.fault} fault` : "", counts.degraded ? `${counts.degraded} degraded` : "", counts.unknown ? `${counts.unknown} unverified` : ""].filter(Boolean).join(" · ")}
        </span>
      )}
    </>
  );

  const filterLabel = pods.needsAttention ? "Attention" : unknownOnly ? "Unknown" : "";

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ flex: 1, minHeight: 0 }}>
        <LensLayout
          label="Pods"
          title="Pods"
          summary={summary}
          headerEnd={
            <>
              <LiveIndicator live={pods.live} />
              <UpdatedAt at={receivedAt} />
              <button className="l-btn" onClick={() => setScratchOpen(true)} title="Launch a TTL-bounded debug pod into klyx-scratch">Debug pod</button>
              <button className="l-btn" onClick={onRefresh}>Refresh</button>
            </>
          }
          controls={
            <>
              <label style={{ display: "flex" }}>
                <span className="sr-only">Namespace</span>
                <select className="l-input" value={pods.namespace} onChange={(e) => onNamespace(e.target.value)}>
                  <option value="">All namespaces</option>
                  {pods.namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
                </select>
              </label>
              <button className="l-chip" aria-pressed={!pods.needsAttention && !unknownOnly} onClick={() => { setUnknownOnly(false); if (pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }}>
                All <span style={{ color: "var(--color-text-tertiary)" }}>{counts.total || ""}</span>
              </button>
              <button className="l-chip" aria-pressed={pods.needsAttention} onClick={() => { setUnknownOnly(false); if (!pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }}>
                Attention {counts.attention > 0 && <span style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{counts.attention}</span>}
              </button>
              {(counts.unknown > 0 || unknownOnly) && (
                <button className="l-chip" aria-pressed={unknownOnly} onClick={() => { setUnknownOnly(true); if (pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }}>
                  Unknown <span style={{ color: "var(--color-text-tertiary)" }}>{counts.unknown || ""}</span>
                </button>
              )}
              <label style={{ display: "flex", flex: "0 1 280px", minWidth: 160 }}>
                <span className="sr-only">Search pods</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={pods.search} placeholder="Search pods" onChange={(e) => useFleet.getState().setPodsSearch(e.target.value)} />
              </label>
            </>
          }
          notice={(pods.error || pods.truncated || bulk.size > 0) ? (
            <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              {pods.error && coverage !== "unavailable" && <WatchErrorBanner tool="Pods" failure="read failed - showing the last good read" error={pods.error} />}
              {pods.truncated && (
                <div role="status" className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 8, padding: "0 10px", borderRadius: 4, background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)", color: "var(--color-text-warning)" }}>
                  <IconAlertTriangle size={14} stroke={1.6} aria-hidden="true" />
                  <span style={{ flex: 1, minWidth: 0 }}>The pod list hit the 5000 cap: counts are a lower bound and a fault past the cap is not shown. Scope by namespace for the full picture.</span>
                </div>
              )}
              {bulk.size > 0 && (
                <div className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 10, padding: "0 10px", borderRadius: 4, background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
                  <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>{bulkTargets.length} pod{bulkTargets.length === 1 ? "" : "s"} selected</span>
                  <button className="l-btn l-btn-danger" onClick={() => setBulkConfirm(true)} disabled={bulkTargets.length === 0}>Delete selected</button>
                  <button className="l-btn" onClick={() => setBulk(new Set())}>Clear</button>
                  <span className="t-secondary">x toggles the highlighted row</span>
                </div>
              )}
            </div>
          ) : undefined}
          state={state}
          stateView={stateView}
          body={
            <LensTable minWidth={560}>
              <div className="lens-table-head pods-grid" role="row">
                <span /><span>Pod</span><span>Status</span><span>Ready</span><span className="col-restarts">Restarts</span><span className="col-node">Node</span><span className="col-age">Age</span><span />
              </div>
              <VirtualList
                ref={listRef}
                items={filtered}
                rowHeight={32}
                rowKey={(p) => `${p.namespace}/${p.name}`}
                style={{ flex: 1, minHeight: 0 }}
                render={(p, i) => (
                  <PodRow
                    key={`${p.namespace}/${p.name}`}
                    p={p}
                    cursor={i === effectiveCursor}
                    selected={pods.selected?.namespace === p.namespace && pods.selected?.name === p.name}
                    bulkSelected={bulk.has(`${p.namespace}/${p.name}`)}
                    onToggleBulk={() => toggleBulk(`${p.namespace}/${p.name}`)}
                    onOpen={() => { setCursor(i); void openPodDetail(cluster, p.namespace, p.name); }}
                    onLogs={() => setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers })}
                  />
                )}
              />
            </LensTable>
          }
          footer={<span className="t-legend">j k navigate · ↵ inspect · l logs · x select · / search</span>}
          footerEnd={
            <>
              <span className="t-secondary">{filtered.length} of {scopedItems.length}{partial} pods{filterLabel ? ` · filter: ${filterLabel}` : ""}</span>
              {filterLabel && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all {scopedItems.length}{partial} pods</button>}
            </>
          }
          inspector={pods.selected && pods.cluster === cluster ? (
            <PodInspector
              // Object-scoped state (tab, confirms, popovers) resets per object.
              key={`${cluster}/${pods.selected.namespace}/${pods.selected.name}`}
              removed={selRemoved}
              cluster={cluster}
              namespace={pods.selected.namespace}
              name={pods.selected.name}
              detail={pods.detail}
              loading={pods.detailLoading}
              isProtected={isProtected}
              onClose={closeInspector}
              onOpenLogs={setLogsTarget}
            />
          ) : undefined}
          inspectorLabel="Pod details"
          onCloseInspector={closeInspector}
        />
      </div>

      {scratchOpen && (
        <ScratchPanel
          cluster={cluster}
          onClose={() => setScratchOpen(false)}
          onLaunched={(ns) => { useFleet.getState().requestPodsNamespace(cluster, ns); setScratchOpen(false); }}
        />
      )}

      {bulkConfirm && bulkTargets.length > 0 && (
        <ConfirmDialog
          title={`delete ${bulkTargets.length} pod${bulkTargets.length === 1 ? "" : "s"}`}
          cluster={cluster}
          detail={`${bulkTargets.slice(0, 5).map((p) => `${p.namespace}/${p.name}`).join(", ")}${bulkTargets.length > 5 ? ` and ${bulkTargets.length - 5} more` : ""} — controllers recreate owned pods; bare pods are NOT recreated.`}
          protected={isProtected}
          danger
          confirmLabel={`Delete ${bulkTargets.length}`}
          onConfirm={() => void runBulkDelete("")}
          onConfirmTyped={(typed) => void runBulkDelete(typed)}
          onCancel={() => setBulkConfirm(false)}
        />
      )}

      {logsTarget && (
        <LogsDock
          cluster={cluster}
          target={logsTarget}
          onClose={() => setLogsTarget(null)}
          onPopOut={async (container) => {
            const ok = await openLogsWindow(cluster, logsTarget.namespace, logsTarget.name, container);
            if (ok) setLogsTarget(null);
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// PodRow
// ---------------------------------------------------------------------------

function PodRow({ p, cursor, selected, bulkSelected, onToggleBulk, onOpen, onLogs }: {
  p: PodSummaryDTO;
  cursor: boolean;
  selected: boolean;
  bulkSelected: boolean;
  onToggleBulk: () => void;
  onOpen: () => void;
  onLogs: () => void;
}) {
  const nonInit = p.containers.filter((c) => !c.init);
  const ready = nonInit.filter((c) => c.ready).length;
  return (
    <div
      role="button"
      tabIndex={0}
      aria-selected={cursor}
      data-cursor={cursor ? "true" : undefined}
      data-selected={selected ? "true" : undefined}
      data-rowhost="true"
      className="lens-row pods-grid"
      onClick={onOpen}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(); } }}
    >
      <VerdictLamp health={p.health} size={8} />
      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
        <input
          type="checkbox"
          checked={bulkSelected}
          data-rowcopy={bulkSelected ? undefined : "true"}
          aria-label={`select pod ${p.name} for bulk actions`}
          onClick={(e) => e.stopPropagation()}
          onChange={onToggleBulk}
          style={{ margin: 0, flexShrink: 0, accentColor: "var(--color-text-info)" }}
        />
        <span className="lens-cell-name" title={`${p.namespace}/${p.name}`}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{p.namespace} / </span>
          <span style={{ fontWeight: selected ? 600 : 400 }}>{p.name}</span>
        </span>
        <CopyButton text={p.name} label={`copy pod name ${p.name}`} appearOnRowHover />
      </span>
      <span style={{ ...ellipsis }}><VerdictLabel health={p.health} /></span>
      <span style={{ color: "var(--color-text-secondary)" }}>{ready}/{nonInit.length}</span>
      <span className="col-restarts" style={{ color: p.restarts > 0 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{p.restarts}</span>
      <span className="col-node" style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={p.node}>{p.node || "-"}</span>
      <span className="col-age" style={{ color: "var(--color-text-tertiary)" }}>{ago(p.ageSeconds)}</span>
      <button
        aria-label={`logs for ${p.namespace}/${p.name}`}
        title="logs"
        onClick={(e) => { e.stopPropagation(); onLogs(); }}
        style={{ display: "flex", alignItems: "center", justifyContent: "center", width: 20, height: 20, padding: 0, border: "none", background: "transparent", cursor: "pointer", color: "var(--color-text-tertiary)", borderRadius: 3 }}
      >
        <IconTerminal2 size={14} />
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// PodInspector - investigation order: verdict, actions, runtime, events,
// placement, then collapsed metadata.
// ---------------------------------------------------------------------------

type Pending = { verb: "restart" | "delete"; summary: PodSummaryDTO } | { verb: "debug"; summary: PodSummaryDTO; container: string };

function PodInspector({ cluster, namespace, name, detail, loading, removed, isProtected, onClose, onOpenLogs }: {
  removed: boolean;
  cluster: string;
  namespace: string;
  name: string;
  detail: PodDetailDTO | null;
  loading: boolean;
  isProtected: boolean;
  onClose: () => void;
  onOpenLogs: (t: LogsTarget) => void;
}) {
  const [tab, setTab] = useState<"overview" | "manifest">("overview");
  const [pending, setPending] = useState<Pending | null>(null);
  const [forwarding, setForwarding] = useState(false);
  const [describing, setDescribing] = useState(false);
  const rowHealth = useFleet((s) => s.pods.items.find((r) => r.namespace === namespace && r.name === name)?.health);

  // Escape with a confirm open cancels only the confirm.
  useEffect(() => {
    if (!pending) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") { e.stopPropagation(); setPending(null); } };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [pending]);

  const [ctUsage, setCtUsage] = useState<PodUsageResultDTO | null>(null);
  useEffect(() => {
    let live = true;
    setCtUsage(null);
    void getPodContainerUsage(cluster, namespace, name).then((r) => { if (live) setCtUsage(r); });
    return () => { live = false; };
  }, [cluster, namespace, name]);
  const usageByContainer = useMemo(() => {
    const m = new Map<string, ContainerUsageDTO>();
    for (const it of ctUsage?.items ?? []) m.set(it.container, it);
    return m;
  }, [ctUsage]);

  // A removed pod keeps its identity and last evidence; no operation targets
  // it - removal also cancels any confirm or popover already open for it.
  const p = detail && !detail.error ? detail.summary : null;
  const live = !removed;
  useEffect(() => {
    if (!removed) return;
    setPending(null);
    setForwarding(false);
  }, [removed]);
  const containers = p ? p.containers.filter((c) => !c.init) : [];
  const per = (fn: (container: string) => void) => containers.map((c) => ({ label: c.name, onSelect: () => fn(c.name) }));
  const showRestart = !!p && (p.ownerKind === "Deployment" || p.ownerKind === "StatefulSet" || p.ownerKind === "DaemonSet");
  const manifestRef = wellKnownResourceRef("", "Pod");

  const more: MenuItem[] = p ? [
    ...(showRestart ? [{ label: `Restart ${p.ownerKind.toLowerCase()} ${p.ownerName}`, onSelect: () => setPending({ verb: "restart", summary: p }) }] : []),
    { label: "Port-forward…", onSelect: () => setForwarding(true) },
    { kind: "choose", label: "Copy kubectl exec command", options: per((c) => void copyExecCommand(cluster, namespace, name, c).then((r) => useFleet.getState().setActionStatus(r === "copied" ? { kind: "success", message: "kubectl exec command copied" } : { kind: "error", message: "could not copy the exec command" }))) },
    { kind: "choose", label: "Debug shell (ephemeral container)", hint: "kubectl debug adds an ephemeral busybox container to this pod; it stays until the pod is recreated", options: per((c) => setPending({ verb: "debug", summary: p, container: c })) },
    { label: "Describe", onSelect: () => setDescribing(true) },
    ...(manifestRef ? [{ label: "Edit manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(namespace, name); } }] : []),
    { kind: "separator" },
    { label: "Delete pod", danger: true, onSelect: () => setPending({ verb: "delete", summary: p }) },
  ] : [];

  const meta = p ? (
    <>
      {p.node && (
        <button className="t-identifier" style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", fontSize: 11 }}
          onClick={() => { useFleet.getState().setSection("nodes"); useFleet.getState().requestNavIntent({ kind: "select-node", cluster, node: p.node }); }}
          title="open in Nodes">{p.node}</button>
      )}
      {p.node ? " · " : ""}{ago(p.ageSeconds)} old{p.ip ? ` · ${p.ip}` : ""}
    </>
  ) : undefined;

  const failedConditions = detail?.conditions.filter((c) => c.status !== "True").length ?? 0;

  return (
    <div data-testid="pod-detail-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind="Pod"
        namespace={namespace}
        name={name}
        meta={meta}
        onClose={onClose}
        verdict={removed ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">Pod removed.</span> It is no longer in the current read of {namespace}. The details below are its last evidence; actions are disabled.
          </div>
        ) : p ? <VerdictStatus health={rowHealth ?? p.health} detail={`${readyOf(p)} ready`} /> : undefined}
        primary={p && live ? { label: "Logs", kbd: "l", onSelect: () => onOpenLogs({ namespace, name, containers: p.containers }) } : undefined}
        secondary={p && live && containers.length > 0 ? { label: "Shell", onSelect: () => void openExecTerminal(cluster, namespace, name, containers[0].name), options: per((c) => void openExecTerminal(cluster, namespace, name, c)) } : undefined}
        more={live ? more : []}
        tab={tab}
        onTab={setTab}
        manifest={detail && !detail.error ? <YamlBlock text={detail.yaml} copyLabel="pod YAML" /> : null}
      >
        {loading && !detail ? (
          <div className="t-body" style={{ color: "var(--color-text-tertiary)" }}>Loading pod detail…</div>
        ) : !detail ? (
          <div className="t-body" style={{ color: "var(--color-text-tertiary)" }}>This pod is no longer available.</div>
        ) : detail.error ? (
          <div role="alert" className="t-body" style={{ color: "var(--color-text-danger)" }}>Could not load pod detail: {detail.error}</div>
        ) : p && (
          <>
            {forwarding && live && (
              <ForwardPopover
                cluster={cluster}
                namespace={namespace}
                kind="Pod"
                name={name}
                ports={containers.flatMap((c) => (c.ports ?? []).map((pt) => ({ name: pt.name, port: pt.port })))}
                onClose={() => setForwarding(false)}
              />
            )}
            {describing && <DescribePane cluster={cluster} kindArg="pod" namespace={namespace} name={name} onClose={() => setDescribing(false)} />}

            <InspectorBlock title="Containers" count={p.containers.length}>
              {ctUsage !== null && !ctUsage.available && (
                <div className="t-secondary">Live usage unavailable - {ctUsage.message || "no metrics source"}</div>
              )}
              {p.containers.map((c) => <ContainerBlock key={c.name} c={c} u={usageByContainer.get(c.name)} />)}
            </InspectorBlock>

            <InspectorBlock title="Recent events" count={detail.events.length}>
              <InspectorEvents events={detail.events} emptyText="No events for this pod." wrap />
            </InspectorBlock>

            <InspectorBlock title="Placement">
              <div className="insp-kv">
                <span>owner</span>
                {p.ownerKind ? (
                  showRestart ? (
                    <button style={{ background: "none", border: 0, padding: 0, textAlign: "left", cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}
                      onClick={() => { useFleet.getState().setSection("workloads"); useFleet.getState().requestNavIntent({ kind: "select-workload", cluster, workloadKind: p.ownerKind, namespace: p.namespace, name: p.ownerName }); }}>
                      {p.ownerKind} {p.namespace}/{p.ownerName}
                    </button>
                  ) : <span>{p.ownerKind} {p.ownerName}</span>
                ) : <span>none - not recreated if deleted</span>}
                <span>node</span><span>{p.node || "not scheduled"}</span>
                <span>IP</span><span>{p.ip || "-"}</span>
                {detail.qosClass && <><span>QoS</span><span>{detail.qosClass}</span></>}
                {detail.serviceAccount && <><span>account</span><span>{detail.serviceAccount}</span></>}
              </div>
            </InspectorBlock>

            {detail.conditions.length > 0 && (
              <InspectorBlock title="Conditions" count={detail.conditions.length} defaultOpen={false}
                extra={failedConditions > 0 ? <span className="t-secondary" style={{ color: "var(--color-text-danger)" }}>{failedConditions} not true</span> : undefined}>
                <InspectorConditions conditions={detail.conditions} />
              </InspectorBlock>
            )}
            <InspectorBlock title="Labels" count={Object.keys(detail.labels ?? {}).length} defaultOpen={false}>
              <Chips values={detail.labels} />
            </InspectorBlock>
            <InspectorBlock title="Annotations" count={Object.keys(detail.annotations ?? {}).length} defaultOpen={false}>
              <Chips values={detail.annotations} />
            </InspectorBlock>
          </>
        )}
      </InspectorLayout>

      {pending && live && (
        <ConfirmDialog
          title={pending.verb === "delete" ? "delete pod" : pending.verb === "debug" ? "debug shell" : "restart owner"}
          cluster={cluster}
          detail={pending.verb === "delete"
            ? (pending.summary.ownerKind === ""
              ? `${pending.summary.namespace}/${pending.summary.name} — this pod has no controller; it will NOT be recreated.`
              : `${pending.summary.namespace}/${pending.summary.name} — the controller will recreate it.`)
            : pending.verb === "debug"
              ? `${pending.summary.namespace}/${pending.summary.name}${pending.container ? ` (target ${pending.container})` : ""} - kubectl debug adds an ephemeral busybox container to this pod. It stays on the pod spec until the pod is recreated; Kubernetes cannot remove it.`
              : `${pending.summary.ownerKind} ${pending.summary.namespace}/${pending.summary.ownerName}`}
          protected={isProtected}
          danger
          confirmLabel={pending.verb === "delete" ? "Delete" : pending.verb === "debug" ? "Attach" : "Restart"}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const pd = pending;
            setPending(null);
            // Re-check at dispatch: the pod may have gone since the dialog opened.
            if (removed) return;
            if (pd.verb === "delete") void deletePod(cluster, pd.summary.namespace, pd.summary.name);
            else if (pd.verb === "debug") void openDebugTerminal(cluster, pd.summary.namespace, pd.summary.name, pd.container);
            // The backend resolves the controller chain into ownerKind/ownerName.
            else void rolloutRestart(cluster, pd.summary.ownerKind, pd.summary.namespace, pd.summary.ownerName);
          }}
        />
      )}
    </div>
  );
}

function Chips({ values }: { values?: Record<string, string> }) {
  const entries = Object.entries(values ?? {});
  if (entries.length === 0) return <span className="t-secondary">None.</span>;
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
      {entries.map(([k, v]) => (
        <span key={k} className="t-identifier" style={{ fontSize: 11, padding: "1px 6px", borderRadius: 3, background: "var(--color-background-secondary)", overflowWrap: "anywhere", maxWidth: "100%" }}>{k}={v}</span>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// LogsDock - full-width resizable dock at the bottom of the lens
// ---------------------------------------------------------------------------

function LogsDock({ cluster, target, onClose, onPopOut }: {
  cluster: string;
  target: LogsTarget;
  onClose: () => void;
  onPopOut: (container: string) => void;
}) {
  const { height, handleProps } = useResizableDock();
  const [container, setContainer] = useState("");
  return (
    <div data-testid="logs-dock" style={{ flexShrink: 0, height, position: "relative", borderTop: "0.5px solid var(--color-border-tertiary)", display: "flex", flexDirection: "column", background: "var(--color-background-primary)", fontFamily: "var(--font-mono)", fontSize: 12 }}>
      <div {...handleProps} />
      <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "4px 12px 4px 14px", borderBottom: "0.5px solid var(--color-border-tertiary)", flexShrink: 0 }}>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: 500 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{target.namespace}</span>/{target.name}
        </span>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>logs</span>
        <div style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 4 }}>
          <button onClick={() => onPopOut(container)} style={{ ...btn, padding: "2px 6px", fontSize: 12, display: "flex", alignItems: "center" }} aria-label="open logs in window" title="Open logs in a separate window">
            <IconExternalLink size={13} stroke={1.5} />
          </button>
          <button onClick={onClose} style={{ ...btn, padding: "2px 8px", display: "inline-flex", alignItems: "center" }} aria-label="close logs dock"><IconX size={13} stroke={1.5} /></button>
        </div>
      </div>
      <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", padding: "8px 12px" }}>
        <LogsPane key={`${target.namespace}/${target.name}`} cluster={cluster} pod={target} onContainerChange={setContainer} />
      </div>
    </div>
  );
}

// containerStateParts splits "running" | "waiting:<Reason>" | "terminated:<Reason>".
function containerStateParts(state: string): { word: string; reason: string } {
  const i = state.indexOf(":");
  return i < 0 ? { word: state, reason: "" } : { word: state.slice(0, i), reason: state.slice(i + 1) };
}

function ContainerBlock({ c, u }: { c: ContainerSummaryDTO; u?: ContainerUsageDTO }) {
  const { word, reason } = containerStateParts(c.state);
  const findings = [
    rightsizing("cpu", u?.cpu ?? null, c.cpuRequest, c.cpuLimit),
    rightsizing("mem", u?.mem ?? null, c.memRequest, c.memLimit),
  ].filter((f): f is NonNullable<typeof f> => f !== null);
  const fmtRes = (v: number | null | undefined, format: (n: number) => string) => (v == null ? "—" : format(v));
  const faulted = word !== "" && word !== "running" && reason !== "Completed";
  const lastFinished = c.lastFinishedUnix ?? 0;
  const lastAgo = lastFinished > 0 ? ago(Math.max(0, Math.floor(Date.now() / 1000) - lastFinished)) : "";
  return (
    <div data-rowhost="true" className="insp-kv">
      <span>{c.name}{c.init ? " (init)" : ""}</span>
      <span style={{ color: faulted ? "var(--color-text-danger)" : "var(--color-text-secondary)", fontWeight: faulted ? 600 : undefined }}>
        {word || "—"}{reason ? ` · ${reason}` : ""}
      </span>
      <span>image</span>
      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
        <span title={c.image} style={{ minWidth: 0, overflowWrap: "anywhere" }}>{imageShort(c.image)}</span>
        <CopyButton text={c.image} label={`copy image ref ${imageShort(c.image)}`} size={11} appearOnRowHover />
      </span>
      {c.lastReason && (
        <>
          <span>last exit</span>
          <span>
            {c.lastReason}{c.lastExitCode ? ` (exit ${c.lastExitCode})` : ""}
            {lastAgo && <span style={{ color: "var(--color-text-tertiary)" }}>{` · ${lastAgo} ago`}</span>}
          </span>
        </>
      )}
      <span>restarts</span><span>{c.restarts}</span>
      {u && (u.cpu != null || u.mem != null) && (
        <><span>usage</span><span>cpu {fmtRes(u.cpu, fmtCpu)} · mem {fmtRes(u.mem, fmtMem)}</span></>
      )}
      {(c.cpuRequest != null || c.memRequest != null || c.cpuLimit != null || c.memLimit != null) && (
        <><span>req / lim</span><span>cpu {fmtRes(c.cpuRequest, fmtCpu)}/{fmtRes(c.cpuLimit, fmtCpu)} · mem {fmtRes(c.memRequest, fmtMem)}/{fmtRes(c.memLimit, fmtMem)}</span></>
      )}
      {findings.map((f) => (
        <span key={f.label} style={{ gridColumn: "1 / -1", fontSize: 11, color: f.tier === "danger" ? "var(--color-text-danger)" : f.tier === "warn" ? "var(--color-text-warning)" : "var(--color-text-info)" }}>{f.label}</span>
      ))}
    </div>
  );
}
