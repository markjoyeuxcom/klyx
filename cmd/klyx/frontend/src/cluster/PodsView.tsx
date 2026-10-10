import { useEffect, useState, useCallback, useMemo, useRef } from "react";
import { IconTerminal2, IconExternalLink, IconCircleDot, IconChevronDown, IconX, IconAlertTriangle } from "@tabler/icons-react";
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
import { useResizablePanel } from "../chrome/useResizablePanel";
import { useResizableDock } from "../chrome/useResizableDock";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { FKeysBar } from "../chrome/FKeysBar";
import { inputWell, selectWell, FilterButton, MetersStrip, well, EmptyState, LiveIndicator, SkeletonRows, btn, ellipsis } from "../chrome/primitives";
import { CopyButton } from "../chrome/CopyButton";
import { ManifestButton } from "./ManifestButton";
import { ScratchPanel } from "./ScratchPanel";
import { YamlBlock } from "../chrome/YamlBlock";
import { InspectorFacts, InspectorIdle, InspectorConditions, InspectorEvents, InspectorSection, InspectorTabButton, Kbd, MetadataChips, inspectorStateBox } from "./ResourceInspector";
import { loadClusterPrefs, loadSearch, savePodsViewPrefs, saveSearch } from "../store/persist";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";

import { VerdictLabel, VerdictLamp, VerdictStatus } from "../health/VerdictView";
import { coverageOf, isAttention, kindOf, labelOf, summarize } from "../health/verdict";
import { verdictMeters } from "../health/meters";

// readyOf renders the panel's "N/M ready" from non-init containers.
function readyOf(p: PodSummaryDTO): string {
  const nonInit = p.containers.filter((c) => !c.init);
  return `${nonInit.filter((c) => c.ready).length}/${nonInit.length}`;
}

function ago(s: number): string {
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`;
}

const gridCols = "12px minmax(0,1.2fr) 60px minmax(110px,0.8fr) 55px minmax(0,1.1fr) 52px 28px";

// kv grid shared by the panel's per-container blocks - labels in a fixed 88px
// column, values left-adjacent (never right-aligned across the panel width).
const kvGridCols = "88px 1fr";

// imageShort strips the registry/repository path from an image ref, keeping the
// final segment with its tag ("docker.io/grafana/grafana:12.4.1" -> "grafana:12.4.1").
// The registry prefix is the least informative part at a glance; the full ref
// stays available via the cell's title attribute. Long sha256 digests are
// shortened to their first 12 hex chars.
export function imageShort(image: string): string {
  const lastSlash = image.lastIndexOf("/");
  let s = lastSlash >= 0 ? image.slice(lastSlash + 1) : image;
  const at = s.indexOf("@sha256:");
  if (at >= 0) s = s.slice(0, at + "@sha256:".length + 12) + "…";
  return s;
}

export function PodsView({ cluster }: { cluster: string }) {
  const pods = useFleet((s) => s.pods);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  const [selectedIdx, setSelectedIdx] = useState(-1);
  // "unknown" filter: offered only while some pod's health cannot be told.
  const [unknownOnly, setUnknownOnly] = useState(false);
  const [logsTarget, setLogsTarget] = useState<{ namespace: string; name: string; containers: ContainerSummaryDTO[] } | null>(null);
  // Bulk selection ("ns/name" keys): deleting three crash-looping pods should
  // be one confirm, not three select->confirm cycles. x toggles the keyboard
  // row; the checkbox reveals on row hover and stays put once checked.
  const [bulk, setBulk] = useState<Set<string>>(new Set());
  const [bulkConfirm, setBulkConfirm] = useState(false);
  const [scratchOpen, setScratchOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  // Holds the cleanup for the current live subscription so namespace changes
  // can close the old sub before opening the new one.
  const liveCleanupRef = useRef<(() => void) | null>(null);

  // Live subscription effect — opens the sub on mount for the persisted
  // namespace (or all namespaces). Namespace changes are handled inline in
  // onNamespace. Hydration fills only fields still at their reset defaults, so
  // an Overview/TriageTape/palette intent set just before mount always wins.
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

  // Mirror filters into per-cluster prefs (namespace + toggles survive a
  // relaunch; search survives section switches within this run).
  useEffect(() => {
    if (pods.cluster !== cluster) return; // transient state during open/switch
    savePodsViewPrefs(cluster, { namespace: pods.namespace, needsAttention: pods.needsAttention });
    saveSearch(cluster, "pods", pods.search);
  }, [cluster, pods.cluster, pods.namespace, pods.needsAttention, pods.search]);

  // Palette "jump to namespace": honor the request by re-opening the live
  // subscription for it (same path as the namespace select). Runs after the
  // mount effect above, so the request wins over the initial all-namespaces sub.
  const nsRequest = useFleet((s) => s.pods.nsRequest);
  useEffect(() => {
    if (!nsRequest) return;
    useFleet.getState().clearPodsNsRequest();
    if (nsRequest.cluster !== cluster) return; // stale request from another cluster
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLivePods(cluster, nsRequest.namespace);
  }, [nsRequest, cluster]);

  // Palette verbs / cross-links land here as one-shot intents once the pod
  // list is loaded.
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
    // Close the current live sub, then open the new one for the selected namespace.
    // The backend immediate emit means the view updates without a separate listPods call.
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLivePods(cluster, ns);
  };
  const onRefresh = () => { void listPods(cluster, pods.namespace); };

  // One verdict per pod, classified by the backend (internal/health). Every
  // number on this lens - meters, filter counts, the idle inspector - comes
  // from this one summary, so they cannot disagree.
  // Rows count only when they were read for THIS cluster+namespace; during a
  // scope switch (or after a failed first read) the store still holds another
  // scope's rows, which must not be presented - or counted - as this one.
  const coverage = coverageOf(pods, cluster, pods.namespace);
  const scopedItems = coverage === "unavailable" ? [] : pods.items;
  const counts = summarize(scopedItems, (p) => p.health);

  const filtered = scopedItems.filter((p) => {
    const k = kindOf(p.health);
    if (pods.needsAttention && !isAttention(k)) return false;
    if (unknownOnly && k !== "unknown") return false;
    if (pods.search) {
      const q = pods.search.toLowerCase();
      return p.name.toLowerCase().includes(q) || p.namespace.toLowerCase().includes(q) || labelOf(p.health).toLowerCase().includes(q);
    }
    return true;
  });

  // Clamp selectedIdx when list changes.
  const clampedIdx = filtered.length === 0 ? -1 : selectedIdx >= filtered.length ? filtered.length - 1 : selectedIdx;
  const effectiveIdx = clampedIdx;

  const handleSelect = useCallback((idx: number) => {
    setSelectedIdx(idx);
    listRef.current?.scrollToIndex(idx);
  }, []);

  const handleActivate = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) void openPodDetail(cluster, p.namespace, p.name);
  }, [filtered, cluster]);

  const handleEscape = useCallback(() => {
    if (pods.selected) {
      useFleet.getState().selectPod(null);
    }
  }, [pods.selected]);

  const handleLogsKey = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers });
  }, [filtered]);

  const toggleBulk = useCallback((key: string) => {
    setBulk((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const handleBulkKey = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) toggleBulk(`${p.namespace}/${p.name}`);
  }, [filtered, toggleBulk]);

  // Bulk selection never survives a cluster switch.
  useEffect(() => {
    setBulk(new Set());
  }, [cluster]);

  useListKeys({
    count: filtered.length,
    selected: effectiveIdx,
    onSelect: handleSelect,
    onActivate: handleActivate,
    onEscape: handleEscape,
    searchRef,
    extraKeys: { l: handleLogsKey, x: handleBulkKey },
  });

  // Resolve bulk keys against the LIVE list at confirm time - pods deleted or
  // rescheduled since selection silently drop out instead of erroring.
  const bulkTargets = pods.items.filter((p) => bulk.has(`${p.namespace}/${p.name}`));

  const runBulkDelete = useCallback(async (typed: string) => {
    const targets = useFleet.getState().pods.items.filter((p) => bulk.has(`${p.namespace}/${p.name}`));
    setBulkConfirm(false);
    setBulk(new Set());
    for (let i = 0; i < targets.length; i++) {
      const p = targets[i];
      // The dialog's own arming covers exactly one backend grant; every
      // subsequent destructive call on a protected cluster re-arms with the
      // same typed confirmation. A failed re-arm stops the batch honestly.
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

  // Reset selection when filter changes.
  useEffect(() => {
    setSelectedIdx((prev) => {
      if (filtered.length === 0) return -1;
      return prev >= filtered.length ? filtered.length - 1 : prev;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pods.search, pods.needsAttention, pods.namespace]);

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", boxSizing: "border-box" }}>
      {/* Main area: list + detail panel side by side */}
      <div style={{ flex: 1, minHeight: 0, padding: "16px 20px", display: "flex", gap: 0, position: "relative" }}>
        <div style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column", minHeight: 0 }}>
          {/* Controls row */}
          {/* Meters (Signal Bench): the pod population by severity, faults only
            when they exist - 209 healthy rows stay dark. */}
        <MetersStrip
          note="faults first"
          meters={verdictMeters({ noun: "pods", scope: pods.namespace, counts, coverage, extra: [counts.progressing > 0 && { label: "starting", value: counts.progressing }] })}
        />
        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12, flexWrap: "wrap", flexShrink: 0 }}>

            <select
              value={pods.namespace}
              onChange={(e) => onNamespace(e.target.value)}
              style={selectWell}
            >
              <option value="">all namespaces</option>
              {pods.namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
            </select>
            <FilterButton label="all" count={counts.total} active={!pods.needsAttention && !unknownOnly} onClick={() => { setUnknownOnly(false); if (pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }} />
            <FilterButton label="attention" count={counts.attention} active={pods.needsAttention} onClick={() => { setUnknownOnly(false); if (!pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }} />
            {(counts.unknown > 0 || unknownOnly) && (
              <FilterButton label="unknown" count={counts.unknown} active={unknownOnly} onClick={() => { setUnknownOnly(true); if (pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }} />
            )}
            <input
              ref={searchRef}
              value={pods.search}
              onChange={(e) => useFleet.getState().setPodsSearch(e.target.value)}
              placeholder="filter pods"
              style={{ ...inputWell, width: 160 }}
            />
            <button onClick={onRefresh} style={btn}>refresh</button>
            <button onClick={() => setScratchOpen(true)} style={btn} title="Launch a TTL-bounded debug pod into klyx-scratch">debug pod</button>
            <LiveIndicator live={pods.live} />
          </div>
          {scratchOpen && (
            <ScratchPanel
              cluster={cluster}
              onClose={() => setScratchOpen(false)}
              onLaunched={(ns) => {
                // Jump the live pods subscription to the scratch namespace so
                // the new pod is immediately visible.
                useFleet.getState().requestPodsNamespace(cluster, ns);
                setScratchOpen(false);
              }}
            />
          )}

          {/* Read failure - the rows shown (if any) are the last good read; an
              error must never pass as "no pods" (review 2026-10-03 #10). */}
          {pods.error && (
            <div style={{ marginBottom: 10, flexShrink: 0 }}>
              <WatchErrorBanner tool="Pods" failure="read failed" error={pods.error} />
            </div>
          )}

          {/* Truncation banner - LOUD, not a chip: broken-first ranking happens
              after the list, so a truncated all-namespaces view can hide a broken
              pod past the cap. Never imply the shown set is complete. */}
          {pods.truncated && (
            <div role="status" style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 10, padding: "7px 11px", border: "0.5px solid var(--color-border-warning)", background: "var(--color-background-warning)", borderRadius: 4, fontSize: 12, color: "var(--color-text-warning)", flexShrink: 0 }}>
              <IconAlertTriangle size={14} stroke={1.5} />
              <span>Showing the first 5000 pods — this list is incomplete, so broken-first triage may miss a fault. Scope by namespace for the full picture.</span>
            </div>
          )}

          {/* Bulk action bar - appears only while a selection exists */}
          {bulk.size > 0 && (
            <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 10, padding: "6px 10px", border: "0.5px solid var(--color-border-warning)", background: "var(--color-background-warning)", borderRadius: 4, fontSize: 12, flexShrink: 0 }}>
              <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>
                {bulkTargets.length} pod{bulkTargets.length === 1 ? "" : "s"} selected
              </span>
              <button
                onClick={() => setBulkConfirm(true)}
                disabled={bulkTargets.length === 0}
                style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-border-danger)", background: "var(--color-background-danger)" }}
              >delete selected</button>
              <button onClick={() => setBulk(new Set())} style={btn}>clear</button>
              <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>x toggles the highlighted row</span>
            </div>
          )}

          {/* Table */}
          {pods.loading && scopedItems.length === 0 ? (
            <SkeletonRows rows={8} label="loading pods" />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<IconCircleDot size={28} stroke={1.2} />}
              title={coverage === "unavailable" ? `No pod data for ${pods.namespace || "all namespaces"} yet.` : scopedItems.length === 0 ? `No pods${pods.namespace ? ` in ${pods.namespace}` : ""}.` : "No pods match the current filter."}
              action={scopedItems.length > 0 ? {
                label: "Clear filters",
                onClick: () => {
                  useFleet.getState().setPodsSearch("");
                  useFleet.getState().setPodsNeedsAttention(false);
                },
              } : undefined}
            />
          ) : (
            <div style={{ ...well, fontFamily: "var(--font-mono)", fontSize: 12, display: "flex", flexDirection: "column", flex: 1, minHeight: 0, overflow: "hidden" }}>
              {/* Header */}
              <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "0 8px 6px", fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)", flexShrink: 0 }}>
                <span /><span>pod</span><span>ready</span><span>status</span><span>restarts</span><span>node</span><span>age</span><span />
              </div>
              {/* Rows — VirtualList for >=100 items, plain render for smaller lists */}
              <VirtualList
                ref={listRef}
                items={filtered}
                rowHeight={32}
                rowKey={(p) => `${p.namespace}/${p.name}`}
                style={{ flex: 1, minHeight: 0 }}
                render={(p, i) => {
                  const isKbSelected = i === effectiveIdx;
                  const isSelected = pods.selected?.namespace === p.namespace && pods.selected?.name === p.name;
                  const nonInitContainers = p.containers.filter((c) => !c.init);
                  const readyCount = nonInitContainers.filter((c) => c.ready).length;
                  return (
                    <PodRow
                      key={`${p.namespace}/${p.name}`}
                      p={p}
                      isKbSelected={isKbSelected}
                      isSelected={isSelected}
                      bulkSelected={bulk.has(`${p.namespace}/${p.name}`)}
                      onToggleBulk={() => toggleBulk(`${p.namespace}/${p.name}`)}
                      readyCount={readyCount}
                      nonInitCount={nonInitContainers.length}
                      onRowClick={() => {
                        setSelectedIdx(i);
                        void openPodDetail(cluster, p.namespace, p.name);
                      }}
                      onLogsClick={() => setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers })}
                    />
                  );
                }}
              />
              <FKeysBar
                keys={[["j k", "nav"], ["↵", "inspect"], ["l", "logs"], ["x", "select"], ["/", "filter"]]}
                status={`${filtered.length} of ${scopedItems.length} · faults first`}
              />
            </div>
          )}
        </div>

        {/* Detail panel - always present; idle when nothing is selected */}
        {!pods.selected && (
          <InspectorIdle
            subject="pods"
            attention={counts.attention}
            hint="Select a pod for containers, conditions, events, logs, and day-2 actions."
          />
        )}
        {pods.selected && (
          <PodDetailPanel
            cluster={cluster}
            namespace={pods.selected.namespace}
            name={pods.selected.name}
            detail={pods.detail}
            loading={pods.detailLoading}
            isProtected={isProtected}
            onClose={() => useFleet.getState().selectPod(null)}
            onOpenLogs={(target) => setLogsTarget(target)}
          />
        )}
      </div>

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

      {/* Logs dock — persists across pod selection changes; close via ✕ only */}
      {logsTarget && (
        <LogsDock
          cluster={cluster}
          target={logsTarget}
          onClose={() => setLogsTarget(null)}
          onPopOut={async (container) => {
            const ok = await openLogsWindow(cluster, logsTarget.namespace, logsTarget.name, container);
            // On success the tail moved to the native window; close the dock so we
            // don't double-tail the same container. On failure keep the dock open.
            if (ok) setLogsTarget(null);
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// PodRow — single row in the triage list
// ---------------------------------------------------------------------------

function PodRow({
  p, isKbSelected, isSelected, bulkSelected, onToggleBulk, readyCount, nonInitCount, onRowClick, onLogsClick,
}: {
  p: PodSummaryDTO;
  isKbSelected: boolean;
  isSelected: boolean;
  bulkSelected: boolean;
  onToggleBulk: () => void;
  readyCount: number;
  nonInitCount: number;
  onRowClick: () => void;
  onLogsClick: () => void;
}) {
  const [logsHovered, setLogsHovered] = useState(false);
  return (
    <div
      role="button"
      tabIndex={0}
      aria-selected={isKbSelected}
      data-rowhost="true"
      onClick={onRowClick}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onRowClick();
        }
      }}
      style={{
        display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center",
        padding: "7px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)",
        cursor: "pointer",
        background: isKbSelected
          ? "var(--color-background-secondary)"
          : isSelected ? "var(--color-background-secondary)" : undefined,
        boxShadow: isKbSelected ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
        outline: "none",
      }}
    >
      <VerdictLamp health={p.health} size={8} />
      <span style={{ display: "flex", alignItems: "center", gap: 2, minWidth: 0 }}>
        <input
          type="checkbox"
          checked={bulkSelected}
          data-rowcopy={bulkSelected ? undefined : "true"}
          aria-label={`select pod ${p.name} for bulk actions`}
          onClick={(e) => e.stopPropagation()}
          onChange={onToggleBulk}
          style={{ margin: 0, flexShrink: 0, accentColor: "var(--color-text-info)" }}
        />
        <span style={ellipsis}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{p.namespace}</span>
          {" / "}
          <span style={{ fontWeight: 500 }}>{p.name}</span>
        </span>
        <CopyButton text={p.name} label={`copy pod name ${p.name}`} appearOnRowHover />
      </span>
      {/* Ready and restarts are facts, not verdicts: neutral. The verdict
          column carries the colour. */}
      <span style={{ color: "var(--color-text-secondary)" }}>
        {readyCount}/{nonInitCount}
      </span>
      <span style={{ ...ellipsis, fontSize: 11 }}>
        <VerdictLabel health={p.health} mono />
      </span>
      <span style={{ color: p.restarts > 0 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>
        {p.restarts}
      </span>
      <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={p.node}>{p.node}</span>
      <span style={{ color: "var(--color-text-tertiary)" }}>{ago(p.ageSeconds)}</span>
      {/* Logs icon button — subtle, full opacity on own hover */}
      <button
        aria-label={`logs for ${p.namespace}/${p.name}`}
        title="logs"
        onClick={(e) => {
          e.stopPropagation();
          onLogsClick();
        }}
        onMouseEnter={() => setLogsHovered(true)}
        onMouseLeave={() => setLogsHovered(false)}
        style={{
          display: "flex", alignItems: "center", justifyContent: "center",
          width: 20, height: 20,
          padding: 0, border: "none", background: "transparent", cursor: "pointer",
          color: "var(--color-text-tertiary)",
          opacity: logsHovered ? 1 : 0.45,
          borderRadius: 3,
        }}
      >
        <IconTerminal2 size={14} />
      </button>
    </div>
  );
}

type PodPendingAction = { verb: "restart" | "delete"; summary: PodSummaryDTO };

function PodDetailPanel({
  cluster, namespace, name, detail, loading, isProtected, onClose, onOpenLogs,
}: {
  cluster: string;
  namespace: string;
  name: string;
  detail: PodDetailDTO | null;
  loading: boolean;
  isProtected: boolean;
  onClose: () => void;
  onOpenLogs: (target: { namespace: string; name: string; containers: ContainerSummaryDTO[] }) => void;
}) {
  const [tab, setTab] = useState<"info" | "yaml">("info");
  const [pending, setPending] = useState<PodPendingAction | null>(null);
  const { width, handleProps } = useResizablePanel();

  // Close on Escape. With a confirm pending, Esc cancels ONLY the confirm:
  // stopPropagation keeps the event from reaching the window-level useListKeys
  // handler (which would also close the whole panel). Without a pending confirm
  // both handlers resolve to the same idempotent selectPod(null).
  const handleKey = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape") {
      if (pending) {
        e.stopPropagation();
        setPending(null);
      } else {
        onClose();
      }
    }
  }, [onClose, pending]);
  useEffect(() => {
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [handleKey]);

  return (
    <div data-testid="pod-detail-panel" style={{
      width, flexShrink: 0, position: "relative",
      background: "var(--bench-well-bg, var(--color-background-secondary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))", borderRadius: "var(--radius-sm)", padding: "12px 16px",
      overflowY: "auto",
      fontFamily: "var(--font-mono)",
      fontSize: 12,
      paddingLeft: 16,
      marginLeft: 16,
    }}>
      {/* Resize handle — drag left edge */}
      <div {...handleProps} />

      {/* Panel header */}
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 10, position: "sticky", top: 0, background: "var(--color-background-primary)", paddingTop: 2, paddingBottom: 6, borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
        <span style={{ minWidth: 0, flex: 1 }}>
          <span style={{ display: "block", fontSize: 10, color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)" }}>Pod / {namespace}</span>
          <span style={{ display: "block", fontWeight: 600, ...ellipsis }} title={`${namespace}/${name}`}>{name}</span>
          {/* One mono meta line: where the pod lives. Node keeps its Nodes
              cross-link here - it left the Summary kv to avoid duplication. */}
          {detail && (
            <span style={{ display: "block", fontSize: 10, color: "var(--color-text-secondary)", fontFamily: "var(--font-mono)", marginTop: 2, ...ellipsis }}>
              {namespace}
              {detail.summary.node && (
                <>
                  {" · "}
                  <button
                    onClick={() => {
                      useFleet.getState().setSection("nodes");
                      useFleet.getState().requestNavIntent({ kind: "select-node", cluster, node: detail.summary.node });
                    }}
                    title="open in Nodes"
                    style={{
                      background: "transparent", border: "none", padding: 0, cursor: "pointer",
                      color: "var(--color-text-info)", fontFamily: "inherit", fontSize: "inherit",
                      textDecoration: "underline", textDecorationColor: "var(--color-border-secondary)", textUnderlineOffset: 2,
                    }}
                  >{detail.summary.node}</button>
                </>
              )}
              {` · ${ago(detail.summary.ageSeconds)}`}
              {detail.summary.ip ? ` · ${detail.summary.ip}` : ""}
            </span>
          )}
        </span>
        <CopyButton text={name} label="copy pod name" />
        <ManifestButton group="" kind="Pod" namespace={namespace} name={name} />

        {/* Logs button — opens / re-targets the dock; l is live via useListKeys */}
        <button
          onClick={() => {
            if (detail) {
              onOpenLogs({ namespace, name, containers: detail.summary.containers });
            }
          }}
          style={{ ...btn, padding: "2px 8px", fontSize: 11, display: "inline-flex", alignItems: "center", gap: 5 }}
          aria-label="open logs dock"
          title="Open logs in bottom dock"
        >logs <Kbd k="l" /></button>
        <button onClick={onClose} style={{ ...btn, padding: "2px 8px", display: "inline-flex", alignItems: "center" }} aria-label="close pod detail panel"><IconX size={13} stroke={1.5} /></button>
      </div>

      {/* Tabs: info | yaml (logs moved to dock) */}
      <div style={{ display: "flex", gap: 2, marginBottom: 12 }}>
        <InspectorTabButton active={tab === "info"} onClick={() => setTab("info")}>info</InspectorTabButton>
        <InspectorTabButton active={tab === "yaml"} onClick={() => setTab("yaml")}>yaml</InspectorTabButton>
      </div>

      {loading && !detail ? (
        <div style={inspectorStateBox}>Loading detail...</div>
      ) : !detail ? (
        <div style={inspectorStateBox}>Could not load pod detail.</div>
      ) : detail.error ? (
        // The backend returns a zero DTO WITH the reason on a failed read;
        // never render that as a real (empty) pod.
        <div role="alert" style={{ ...inspectorStateBox, color: "var(--color-text-danger)" }}>Could not load pod detail: {detail.error}</div>
      ) : (
        <>
          {tab === "info" && (
            <InfoTab
              detail={detail}
              cluster={cluster}
              namespace={namespace}
              name={name}
              onRestart={(s) => setPending({ verb: "restart", summary: s })}
              onDelete={(s) => setPending({ verb: "delete", summary: s })}
            />
          )}
          {tab === "yaml" && <YamlBlock text={detail.yaml} copyLabel="pod YAML" />}
        </>
      )}

      {pending && (
        <ConfirmDialog
          title={pending.verb === "delete" ? "delete pod" : "restart owner"}
          cluster={cluster}
          detail={pending.verb === "delete"
            ? (pending.summary.ownerKind === ""
              ? `${pending.summary.namespace}/${pending.summary.name} — this pod has no controller; it will NOT be recreated.`
              : `${pending.summary.namespace}/${pending.summary.name} — the controller will recreate it.`)
            : `${pending.summary.namespace}/${pending.summary.name}`}
          protected={isProtected}
          danger={pending.verb === "delete"}
          confirmLabel={pending.verb === "delete" ? "Delete" : "Restart"}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const { verb, summary } = pending;
            setPending(null);
            if (verb === "delete") {
              void deletePod(cluster, summary.namespace, summary.name);
            } else {
              // Restart the owner workload. The backend resolves the real
              // controller chain (ReplicaSet -> Deployment) into ownerKind/
              // ownerName, so no name guessing happens here.
              void rolloutRestart(cluster, summary.ownerKind, summary.namespace, summary.ownerName);
            }
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// LogsDock — full-width resizable dock at the bottom of PodsView
// ---------------------------------------------------------------------------

function LogsDock({
  cluster,
  target,
  onClose,
  onPopOut,
}: {
  cluster: string;
  target: { namespace: string; name: string; containers: ContainerSummaryDTO[] };
  onClose: () => void;
  onPopOut: (container: string) => void;
}) {
  const { height, handleProps } = useResizableDock();
  // Track the container currently selected inside LogsPane so the pop-out opens
  // the same tail. LogsPane owns container state; it reports via onContainerChange.
  const [container, setContainer] = useState("");

  return (
    <div
      data-testid="logs-dock"
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
      <div
        {...handleProps}
        style={{
          ...handleProps.style,
          // Darken on hover via CSS. Inline styles can't do :hover — use a
          // background that is visible on interaction feedback via pointer-capture.
        }}
      />

      {/* Dock header */}
      <div style={{
        display: "flex", alignItems: "center", gap: 8,
        padding: "4px 12px 4px 14px",
        borderBottom: "0.5px solid var(--color-border-tertiary)",
        flexShrink: 0,
      }}>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: 500 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{target.namespace}</span>/{target.name}
        </span>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>logs</span>
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

      {/* LogsPane body — host-driven sizing: flex: 1, min-height: 0 gives LogsPane
          its height. LogsPane's own normal-layout root is height:100%/flex-column
          so it fills this container without any modification. */}
      <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", padding: "8px 12px" }}>
        <LogsPane
          key={`${target.namespace}/${target.name}`}
          cluster={cluster}
          pod={target}
          onContainerChange={setContainer}
        />
      </div>
    </div>
  );
}

function InfoTab({ detail, cluster, namespace, name, onRestart, onDelete }: {
  detail: PodDetailDTO;
  cluster: string;
  namespace: string;
  name: string;
  onRestart: (s: PodSummaryDTO) => void;
  onDelete: (s: PodSummaryDTO) => void;
}) {
  const p = detail.summary;
  // Verdict provenance: the headline is the SAME verdict as the selected list
  // row (one observation, one answer). The detail read's own summary is a
  // later observation and is used only if the row has left the list.
  const rowHealth = useFleet((s) => s.pods.items.find((r) => r.namespace === p.namespace && r.name === p.name)?.health);
  const [forwarding, setForwarding] = useState(false);
  const [describing, setDescribing] = useState(false);

  // Live per-container usage for the rightsizing readout (usage vs the
  // container's declared requests/limits). Unavailable metrics degrade to a
  // caption on the Containers section - the block still renders.
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
  // Restart is meaningful only for owners rollout-restart supports. The
  // backend resolves ReplicaSet -> Deployment, so a pod still showing
  // ownerKind=ReplicaSet is under a BARE ReplicaSet (nothing to restart), and
  // Argo Rollouts surface as kind Rollout (restart is Argo's verb, not ours).
  const showRestart = p.ownerKind === "Deployment" || p.ownerKind === "StatefulSet" || p.ownerKind === "DaemonSet";

  return (
    <>
      {/* State first: the phase/rank/reason headline, scannable before any
          key-value rows. */}
      <VerdictStatus health={rowHealth ?? p.health} detail={`${readyOf(p)} ready`} />

      {/* Action buttons */}
      <div style={{ display: "flex", gap: 8, marginBottom: 12, alignItems: "center", flexWrap: "wrap" }}>
        {showRestart && (
          <button onClick={() => onRestart(p)} style={btn}>restart owner</button>
        )}
        {forwarding ? (
          <ForwardPopover
            cluster={cluster}
            namespace={namespace}
            kind="Pod"
            name={name}
            ports={p.containers.filter((c) => !c.init).flatMap((c) => (c.ports ?? []).map((pt) => ({ name: pt.name, port: pt.port })))}
            onClose={() => setForwarding(false)}
          />
        ) : (
          <button onClick={() => setForwarding(true)} style={btn}>forward</button>
        )}
        <ExecButtons
          cluster={cluster}
          namespace={namespace}
          pod={name}
          containers={p.containers.filter((c) => !c.init)}
        />
        <DebugShellButton
          cluster={cluster}
          namespace={namespace}
          pod={name}
          containers={p.containers.filter((c) => !c.init)}
        />
        <button onClick={() => setDescribing(true)} style={btn}>describe</button>
        <button
          onClick={() => onDelete(p)}
          style={{ ...btn, color: "var(--color-text-danger)" }}
        >delete pod</button>
      </div>

      {describing && (
        <DescribePane
          cluster={cluster}
          kindArg="pod"
          namespace={namespace}
          name={name}
          onClose={() => setDescribing(false)}
        />
      )}

      {/* Numbers get physical form; prose stays in the Summary kv rows. */}
      <div style={{ marginBottom: 12 }}>
        <InspectorFacts
          facts={[
            { label: "restarts", value: p.restarts, tone: p.restarts > 0 ? "warning" : undefined },
            { label: "containers", value: readyOf(p) },
            { label: "age", value: ago(p.ageSeconds) },
          ]}
        />
      </div>

      {/* Summary header */}
      <InspectorSection title="Summary">
        {p.ownerKind && (
          <InfoRow label="owner">
            {showRestart ? (
              <InfoLink
                label={`${p.ownerKind}/${p.ownerName}`}
                title="open in Workloads"
                onClick={() => {
                  useFleet.getState().setSection("workloads");
                  useFleet.getState().requestNavIntent({ kind: "select-workload", cluster, workloadKind: p.ownerKind, namespace: p.namespace, name: p.ownerName });
                }}
              />
            ) : (
              `${p.ownerKind}/${p.ownerName}`
            )}
          </InfoRow>
        )}
        {/* node + ip live on the header meta line - repeating them here would
            be the duplication the meta line exists to remove */}
        {detail.qosClass && <InfoRow label="qos">{detail.qosClass}</InfoRow>}
        {detail.serviceAccount && <InfoRow label="sa">{detail.serviceAccount}</InfoRow>}
      </InspectorSection>

      <MetadataChips title="Labels" values={detail.labels} />
      <MetadataChips title="Annotations" values={detail.annotations} defaultCollapsed />

      {/* Containers — a kv block per container, not a table: the state word
          with its reason is the headline fact and needs a full line. */}
      <InspectorSection title="Containers" count={p.containers.length} collapsible>
        {ctUsage !== null && !ctUsage.available && (
          <div style={{ fontSize: 9, color: "var(--color-text-tertiary)", paddingBottom: 4 }}>
            live usage unavailable — {ctUsage.message || "no metrics source"}
          </div>
        )}
        {p.containers.map((c) => <ContainerBlock key={c.name} c={c} u={usageByContainer.get(c.name)} />)}
      </InspectorSection>

      {/* Conditions */}
      {detail.conditions.length > 0 && (
        <InspectorSection title="Conditions" collapsible>
          <InspectorConditions conditions={detail.conditions} />
        </InspectorSection>
      )}

      {/* Events */}
      <InspectorSection title="Events" count={detail.events.length} collapsible>
        <InspectorEvents events={detail.events} emptyText="No events for this pod." />
      </InspectorSection>
    </>
  );
}

// ExecButtons renders "open shell" and "copy kubectl exec" buttons for a pod.
// When the pod has more than one regular (non-init) container, clicking "open shell"
// first shows a tiny container picker; with a single container it fires immediately.
function ExecButtons({
  cluster, namespace, pod, containers,
}: {
  cluster: string;
  namespace: string;
  pod: string;
  containers: ContainerSummaryDTO[];
}) {
  const [pickerOpen, setPickerOpen] = useState(false);
  // copyState: "idle" | "copied" | "error"
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">("idle");
  const pickerRef = useRef<HTMLDivElement>(null);

  // Close picker on outside click.
  useEffect(() => {
    if (!pickerOpen) return;
    const handler = (e: MouseEvent) => {
      if (pickerRef.current && !pickerRef.current.contains(e.target as Node)) {
        setPickerOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [pickerOpen]);

  const handleOpen = (container: string) => {
    setPickerOpen(false);
    void openExecTerminal(cluster, namespace, pod, container);
  };

  const handleCopy = async () => {
    // Use first container (or "" for implicit single container) for the copy command.
    const container = containers.length === 1 ? containers[0].name : containers[0]?.name ?? "";
    const result = await copyExecCommand(cluster, namespace, pod, container);
    setCopyState(result);
    setTimeout(() => setCopyState("idle"), 1500);
  };

  const firstContainer = containers[0]?.name ?? "";

  return (
    <div style={{ display: "flex", gap: 4, alignItems: "center", position: "relative" }}>
      {/* open shell button — container picker when >1 container */}
      {containers.length > 1 ? (
        <div ref={pickerRef} style={{ position: "relative" }}>
          <button
            onClick={() => setPickerOpen((v) => !v)}
            style={btn}
            title="Open interactive shell in container"
          >
            open shell <IconChevronDown size={11} stroke={1.7} style={{ verticalAlign: "-1px" }} />
          </button>
          {pickerOpen && (
            <div style={{
              position: "absolute", top: "100%", left: 0, zIndex: 20, marginTop: 2,
              background: "var(--color-background-primary)",
              border: "0.5px solid var(--color-border-secondary)",
              borderRadius: 4,
              minWidth: 160,
              boxShadow: "0 2px 8px rgba(0,0,0,0.18)",
            }}>
              {containers.map((c) => (
                <button
                  key={c.name}
                  onClick={() => handleOpen(c.name)}
                  style={{
                    display: "block", width: "100%", textAlign: "left",
                    padding: "5px 10px", fontSize: 11,
                    background: "transparent",
                    border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)",
                    color: "var(--color-text-secondary)", cursor: "pointer",
                  }}
                  title={`exec into ${c.name}`}
                >
                  {c.name}
                </button>
              ))}
            </div>
          )}
        </div>
      ) : (
        <button
          onClick={() => handleOpen(firstContainer)}
          style={btn}
          title="Open interactive shell"
        >
          open shell
        </button>
      )}
      {/* copy kubectl exec command */}
      <button
        onClick={() => void handleCopy()}
        style={btn}
        title="Copy kubectl exec command to clipboard"
      >
        {copyState === "copied" ? "copied" : copyState === "error" ? "copy error" : "copy exec"}
      </button>
    </div>
  );
}

// DebugShellButton opens an ephemeral busybox container attached to the pod
// (kubectl debug -it --image=busybox --target=<container>) in the external
// terminal - the shell for DISTROLESS containers where "open shell" has
// nothing to exec. With more than one container a picker chooses the
// --target (whose process namespace the shell joins).
//
// kubectl debug MUTATES the pod: the ephemeral container is added to its spec
// and Kubernetes has no API to remove it until the pod is recreated. So it
// goes through the shared ConfirmDialog (typed arming on protected clusters),
// and the backend refuses it on a protected cluster without that grant.
function DebugShellButton({
  cluster, namespace, pod, containers,
}: {
  cluster: string;
  namespace: string;
  pod: string;
  containers: ContainerSummaryDTO[];
}) {
  const [pickerOpen, setPickerOpen] = useState(false);
  // The container a confirmed launch will target ("" = default container);
  // null while no confirmation is pending.
  const [pendingTarget, setPendingTarget] = useState<string | null>(null);
  const pickerRef = useRef<HTMLDivElement>(null);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  useEffect(() => {
    if (!pickerOpen) return;
    const handler = (e: MouseEvent) => {
      if (pickerRef.current && !pickerRef.current.contains(e.target as Node)) {
        setPickerOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [pickerOpen]);

  const open = (container: string) => {
    setPickerOpen(false);
    setPendingTarget(container);
  };

  const confirmDialog = pendingTarget !== null && (
    <ConfirmDialog
      title="debug shell"
      cluster={cluster}
      detail={`${namespace}/${pod}${pendingTarget ? ` (target ${pendingTarget})` : ""} - kubectl debug adds an ephemeral busybox container to this pod. It stays on the pod spec until the pod is recreated; Kubernetes cannot remove it.`}
      protected={isProtected}
      confirmLabel="Attach"
      onCancel={() => setPendingTarget(null)}
      onConfirm={() => {
        const target = pendingTarget;
        setPendingTarget(null);
        void openDebugTerminal(cluster, namespace, pod, target);
      }}
    />
  );

  const tip = "Attach an ephemeral busybox shell (kubectl debug) - works on distroless images where open shell cannot. The ephemeral container stays listed on the pod until it is recreated.";

  if (containers.length <= 1) {
    return (
      <>
        <button onClick={() => open(containers[0]?.name ?? "")} style={btn} title={tip}>
          debug shell
        </button>
        {confirmDialog}
      </>
    );
  }
  return (
    <div ref={pickerRef} style={{ position: "relative" }}>
      {confirmDialog}
      <button onClick={() => setPickerOpen((v) => !v)} style={btn} title={tip}>
        debug shell <IconChevronDown size={11} stroke={1.7} style={{ verticalAlign: "-1px" }} />
      </button>
      {pickerOpen && (
        <div style={{
          position: "absolute", top: "100%", left: 0, zIndex: 20, marginTop: 2,
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: 4,
          minWidth: 160,
          boxShadow: "0 2px 8px rgba(0,0,0,0.18)",
        }}>
          {containers.map((c) => (
            <button
              key={c.name}
              onClick={() => open(c.name)}
              style={{
                display: "block", width: "100%", textAlign: "left",
                padding: "5px 10px", fontSize: 11,
                background: "transparent",
                border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)",
                color: "var(--color-text-secondary)", cursor: "pointer",
              }}
              title={`debug shell targeting ${c.name}`}
            >
              {c.name}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// containerStateParts splits the backend's canonical state string ("running" |
// "waiting:<Reason>" | "terminated:<Reason>" | "") into word + reason.
function containerStateParts(state: string): { word: string; reason: string } {
  const i = state.indexOf(":");
  return i < 0 ? { word: state, reason: "" } : { word: state.slice(0, i), reason: state.slice(i + 1) };
}

function ContainerBlock({ c, u }: { c: ContainerSummaryDTO; u?: ContainerUsageDTO }) {
  const { word, reason } = containerStateParts(c.state);
  // Rightsizing findings: usage vs the container's declared requests/limits.
  // Diagnosis framing - throttle/OOM proximity first, oversized request second.
  const findings = [
    rightsizing("cpu", u?.cpu ?? null, c.cpuRequest, c.cpuLimit),
    rightsizing("mem", u?.mem ?? null, c.memRequest, c.memLimit),
  ].filter((f): f is NonNullable<typeof f> => f !== null);
  const fmtRes = (v: number | null | undefined, format: (n: number) => string) => (v == null ? "—" : format(v));
  // terminated:Completed is the normal init-container end state, not a fault.
  const faulted = word !== "" && word !== "running" && reason !== "Completed";
  const stateColor = word === "running"
    ? "var(--color-text-success)"
    : faulted ? "var(--color-text-danger)" : "var(--color-text-tertiary)";
  // Last termination: lastReason "" means the container never terminated - the
  // row only exists when the backend has a real record, never fabricated.
  const lastFinished = c.lastFinishedUnix ?? 0;
  const lastAgo = lastFinished > 0 ? ago(Math.max(0, Math.floor(Date.now() / 1000) - lastFinished)) : "";
  return (
    <div data-rowhost="true" style={{ display: "grid", gridTemplateColumns: kvGridCols, gap: "2px 8px", fontSize: 11, padding: "3px 0", alignItems: "baseline" }}>
      <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={c.name}>
        {c.name}
        {c.init && <span style={{ marginLeft: 4, fontSize: 9, background: "var(--color-background-secondary)", padding: "0 4px", borderRadius: 3 }}>init</span>}
      </span>
      <span style={{ ...ellipsis, color: stateColor, fontWeight: faulted ? 600 : undefined }} title={c.state}>
        {word || "—"}{reason ? ` · ${reason}` : ""}
      </span>
      <span style={{ color: "var(--color-text-tertiary)" }}>image</span>
      <span style={{ display: "flex", alignItems: "center", gap: 2, minWidth: 0 }}>
        <span style={{ ...ellipsis, color: "var(--color-text-secondary)" }} title={c.image}>{imageShort(c.image)}</span>
        <CopyButton text={c.image} label={`copy image ref ${imageShort(c.image)}`} size={11} appearOnRowHover />
      </span>
      <span style={{ color: "var(--color-text-tertiary)" }}>restarts</span>
      <span style={{ color: c.restarts > 0 ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{c.restarts}</span>
      {c.lastReason && (
        <>
          <span style={{ color: "var(--color-text-tertiary)" }}>last state</span>
          <span style={{ ...ellipsis, minWidth: 0 }}>
            <span style={{ color: "var(--color-text-danger)", fontWeight: 600 }}>
              {c.lastReason}{c.lastExitCode ? ` (exit ${c.lastExitCode})` : ""}
            </span>
            {lastAgo && <span style={{ color: "var(--color-text-tertiary)" }}>{` · ${lastAgo} ago`}</span>}
          </span>
        </>
      )}
      {u && (u.cpu != null || u.mem != null) && (
        <>
          <span style={{ color: "var(--color-text-tertiary)" }}>usage</span>
          <span style={{ ...ellipsis, color: "var(--color-text-secondary)" }}>
            cpu {fmtRes(u.cpu, fmtCpu)} · mem {fmtRes(u.mem, fmtMem)}
          </span>
        </>
      )}
      {(c.cpuRequest != null || c.memRequest != null || c.cpuLimit != null || c.memLimit != null) && (
        <>
          <span style={{ color: "var(--color-text-tertiary)" }}>req / lim</span>
          <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }}>
            cpu {fmtRes(c.cpuRequest, fmtCpu)}/{fmtRes(c.cpuLimit, fmtCpu)} · mem {fmtRes(c.memRequest, fmtMem)}/{fmtRes(c.memLimit, fmtMem)}
          </span>
        </>
      )}
      {findings.map((f) => (
        <span key={f.label} style={{ gridColumn: "1 / -1", fontSize: 10, fontWeight: f.tier === "danger" ? 600 : undefined, color: f.tier === "danger" ? "var(--color-text-danger)" : f.tier === "warn" ? "var(--color-text-warning)" : "var(--color-text-info)" }}>
          {f.label}
        </span>
      ))}
    </div>
  );
}

function InfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: "flex", gap: 8, fontSize: 11 }}>
      <span style={{ color: "var(--color-text-tertiary)", width: 60, flexShrink: 0 }}>{label}</span>
      <span style={{ color: "var(--color-text-primary)" }}>{children}</span>
    </div>
  );
}

// InfoLink: an InfoRow value that navigates (owner -> Workloads, node -> Nodes).
function InfoLink({ label, title, onClick }: { label: string; title: string; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      title={title}
      style={{
        background: "transparent", border: "none", padding: 0, cursor: "pointer",
        color: "var(--color-text-info)", fontFamily: "var(--font-mono)", fontSize: 11,
        textAlign: "left", overflowWrap: "anywhere",
        textDecoration: "underline", textDecorationColor: "var(--color-border-secondary)", textUnderlineOffset: 2,
      }}
    >{label}</button>
  );
}
