import { useEffect, useMemo, useState, useCallback, useRef } from "react";
import { IconTerminal2, IconExternalLink, IconBox, IconX, IconAlertTriangle, IconTrash } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { WorkloadDTO, PodDTO, WorkloadKind, ResourceCellDTO, RolloutRevisionDTO, KindCoverageDTO } from "../store/fleet";
import { rolloutRestart, rolloutHistory, rolloutUndo, rolloutPause, scaleWorkload } from "../bridge/workloads";
import { deletePod } from "../bridge/pods";
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
import { btn, ellipsis } from "../chrome/primitives";
import { loadClusterPrefs, loadSearch, saveSearch, saveWorkloadsViewPrefs } from "../store/persist";
import { staleStyle } from "./staleness";
import { wellKnownResourceRef } from "./resourceRefs";
import { FleetCompare } from "./FleetCompare";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { useInspectorAI } from "../ai/AIExplain";

import { VerdictLabel, VerdictLamp, VerdictStatus } from "../health/VerdictView";
import { type VerdictCounts, attentionTone, isAttention, kindOf, labelOf, orderOf, summarize } from "../health/verdict";
import { useWorkloadRows } from "../health/useWorkloadRows";
import { clock, combineEvidence, coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
import { UpdatedAt, useReceivedAt } from "../chrome/Age";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
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
const ROW_HEIGHT = TABLE_ROW_HEIGHT;

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
// A pending verb is pinned to the cluster it was raised on, independent of
// selection: it dispatches there, and is dropped when its target leaves scope
// or a complete read.
type PinnedPending = Pending & { cluster: string };

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

const byTriage = (a: WorkloadDTO, b: WorkloadDTO) =>
  orderOf(kindOf(a.health)) - orderOf(kindOf(b.health)) || (b.health?.rank ?? 0) - (a.health?.rank ?? 0)
  || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name);

// withUsage joins the live usage fetch to a row (separate from the rows, so a
// rows push never erases it).
function withUsage(w: WorkloadDTO, usage: Record<string, { cpu: number | null; mem: number | null }>): WorkloadDTO {
  const u = usage[keyOf(w)];
  if (!u) return w;
  return { ...w, resources: { cpu: { ...w.resources.cpu, usage: u.cpu }, mem: { ...w.resources.mem, usage: u.mem } } };
}

export function WorkloadsView({ cluster }: { cluster: string }) {
  const wl = useFleet((s) => s.workloads);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const reloaderPresent = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.reloaderPresent ?? false);
  const reloaderHealthy = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.reloaderHealthy ?? false);
  const fleetSize = useFleet((s) => s.clusters.length);
  const [pinned, setPinned] = useState<PinnedPending | null>(null);
  const setPending = useCallback((p: Pending | null) => setPinned(p ? { ...p, cluster } : null), [cluster]);
  // Auxiliary surfaces carry their origin cluster and render only on it, so a
  // cluster switch never queries A's object name against B.
  const [compareTarget, setCompareTarget] = useState<{ cluster: string; w: WorkloadDTO } | null>(null);
  // Cursor and selection are stable keys (kind/namespace/name), never indexes:
  // live emits re-rank the rows.
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [unknownOnly, setUnknownOnly] = useState(false);
  const [logsPinned, setLogsPinned] = useState<{ cluster: string; namespace: string; name: string; kind: WorkloadKind } | null>(null);
  const setLogsTarget = useCallback((t: { namespace: string; name: string; kind: WorkloadKind } | null) => setLogsPinned(t ? { ...t, cluster } : null), [cluster]);
  const logsTarget = logsPinned && logsPinned.cluster === cluster ? logsPinned : null;
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  // The workloads are the Observer's shared rows, all namespaces (1c slice 3
  // §5.8) - live; the namespace is a list filter. Usage is a separate fetch.
  useWorkloadRows(cluster);
  const mine = wl.cluster === cluster;
  useEffect(() => {
    // Hydration fills only fields still at their reset defaults, so an intent
    // set just before mount wins.
    const st = useFleet.getState();
    const prefs = loadClusterPrefs(cluster).workloads ?? {};
    if (prefs.needsAttention && !st.workloads.needsAttention) st.setWorkloadsNeedsAttention(true);
    if (prefs.nearLimitSort && !st.workloads.nearLimitSort) st.toggleNearLimitSort();
    // Saved kinds fill only an unchosen default: an explicit All workloads
    // (navigation or history) is not overwritten.
    const kindsAtDefault = KINDS.every((k) => st.workloads.kindFilter[k]) && !st.workloads.kindExplicit;
    if (prefs.kinds && prefs.kinds.length > 0 && prefs.kinds.length < KINDS.length && kindsAtDefault) st.hydrateWorkloadKinds(prefs.kinds);
    const savedSearch = loadSearch(cluster, "workloads");
    if (savedSearch && st.workloads.search === "") st.setWorkloadsSearch(savedSearch);
    if (prefs.namespace && st.workloads.namespace === "") st.setWorkloadsNamespace(prefs.namespace);
  }, [cluster]);
  useEffect(() => {
    if (!mine) return;
    const ns = wl.namespace;
    void getWorkloadMetrics(cluster, ns);
    const id = setInterval(() => {
      const cur = useFleet.getState().workloads;
      if (cur.cluster === cluster && cur.namespace === ns) void getWorkloadMetrics(cluster, ns);
    }, 30000);
    return () => clearInterval(id);
  }, [cluster, mine, wl.namespace]);

  useEffect(() => {
    if (!mine) return;
    saveWorkloadsViewPrefs(cluster, { namespace: wl.namespace, needsAttention: wl.needsAttention, kinds: KINDS.filter((k) => wl.kindFilter[k]), nearLimitSort: wl.nearLimitSort });
    saveSearch(cluster, "workloads", wl.search);
  }, [cluster, mine, wl.namespace, wl.needsAttention, wl.kindFilter, wl.nearLimitSort, wl.search]);

  // Evidence is each counted kind's own coverage; what pod attribution read
  // (Pods, ReplicaSets) qualifies the verdicts, never the list.
  const kindEv = useCallback((k: string) => kindEvidence(mine && !wl.loading, wl.coverage, k, wl.error), [mine, wl.loading, wl.coverage, wl.error]);
  const ev = combineEvidence(KINDS.map(kindEv));
  const depEv = (k: string) => (mine && wl.dependencies?.some((d) => d.kind === k) ? kindEvidence(true, wl.dependencies, k, null) : "pending");
  const podsEv = depEv("Pod");
  // what pod attribution read: Pods for every kind, ReplicaSets to join a
  // Deployment's pods - each qualifies the verdicts with its own freshness
  const attribution = (["Pod", "ReplicaSet"] as const).map((k) => ({ kind: k, ev: depEv(k) }));
  const attributionComplete = attribution.every((a) => a.ev === "complete");
  const rowsRefreshing = mine && !!wl.rowsState?.refreshing;
  const rowsAsOf = mine && wl.rowsState?.evaluatedAt ? clock(wl.rowsState.evaluatedAt) : "the last read";
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = mine ? lastGoodAt(wl.coverage, KINDS) : null;
  const discoveryGap = mine ? coverageGaps([...(wl.coverage ?? []), ...(wl.dependencies ?? [])]) : null;
  const mark = ev === "complete" ? "" : ev === "stale" ? "*" : "+";

  const showMetrics = wl.metricsAvailable;
  // faults first, then severity, namespace and name - the lens's own order,
  // never the capture's (slice 3 §5.8)
  const allItems = useMemo(() => (mine ? wl.items.map((w) => withUsage(w, wl.usage)).sort(byTriage) : []), [mine, wl.items, wl.usage]);
  // the namespace filter scopes the list; the global counts stay cluster-wide
  const scopedItems = useMemo(() => (wl.namespace ? allItems.filter((w) => w.namespace === wl.namespace) : allItems), [allItems, wl.namespace]);
  const rowCounts = summarize(scopedItems, (w) => w.health);
  const globalCounts = mine ? wl.counts : null;
  const counts: VerdictCounts = globalCounts ? { ...globalCounts, attention: globalCounts.fault + globalCounts.degraded } : summarize(allItems, (w) => w.health);
  const receivedAt = useReceivedAt(allItems.length > 0 ? wl.items : null);
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
  // "none need attention" is a claim about the present: a complete, current
  // read - of the workloads and of the attribution evidence their verdicts need
  const current = ev === "complete" && attributionComplete && !wl.rowsState?.updating && !rowsRefreshing;

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

  // Palette verbs / cross-links: select the workload (revealing it past the
  // filters), then open the verb's confirm or scale input.
  // A target not in the rows is missing only on a complete read of its own
  // kind; until then it waits (slice 4 review of 4d8b545 #1).
  useNavIntent(cluster, ["restart-workload", "scale-workload", "select-workload"], (intent) => {
    if (!("workloadKind" in intent)) return "missing";
    const w = allItems.find((x) => x.kind === intent.workloadKind && x.namespace === intent.namespace && x.name === intent.name);
    if (!w) return kindEv(intent.workloadKind) === "complete" ? "missing" : "wait";
    autoSelect.current = "done";
    if (!rows.some((x) => keyOf(x) === keyOf(w))) {
      const st = useFleet.getState();
      st.setWorkloadsNamespace(""); st.setWorkloadsSearch(""); st.setWorkloadsNeedsAttention(false); st.setWorkloadKindFilter(KINDS); setUnknownOnly(false);
    }
    setCursorKey(keyOf(w));
    setSelectedKey(keyOf(w));
    if (intent.kind === "restart-workload") setPending({ kind: "restart", w });
    else if (intent.kind === "scale-workload") setPending({ kind: "scale-input", w });
    return "consumed";
  }, [allItems, rows, kindEv]);

  // One-shot auto-select of the first attention row from a FRESH complete read.
  const autoSelect = useRef<"pending" | "done">("pending");
  const itemsAtEntry = useRef<WorkloadDTO[] | null>(null);
  if (itemsAtEntry.current === null) itemsAtEntry.current = wl.items;
  // Reset on a real cluster change only: on mount this would undo a palette
  // intent that already selected its workload.
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    autoSelect.current = "pending";
    itemsAtEntry.current = useFleet.getState().workloads.items;
    setSelectedKey(null);
    setCursorKey(null);
    setLogsPinned(null);
    setCompareTarget(null);
  }, [cluster]);
  const freshRead = mine && ev === "complete" && wl.items !== itemsAtEntry.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead) return;
    autoSelect.current = "done";
    if (selectedKey) return;
    const w = rows.find((x) => isAttention(kindOf(x.health)));
    if (w) open(w);
  }, [freshRead, rows, selectedKey, open]);

  // Selection follows the key; verbs follow the UID. A workload missing from
  // the rows keeps its last row: removed only on a complete read of its own
  // kind, otherwise unverified (slice 3 §5a).
  const selected = selectedKey ? allItems.find((w) => keyOf(w) === selectedKey) : undefined;
  const lastSelected = useRef<{ cluster: string; w: WorkloadDTO } | null>(null);
  if (selected) lastSelected.current = { cluster, w: selected };
  const prior = selectedKey && lastSelected.current?.cluster === cluster && keyOf(lastSelected.current.w) === selectedKey ? lastSelected.current.w : null;
  const subject = selected ?? prior;
  const selKind = selectedKey ? selectedKey.split("/")[0] : "";
  const gone: "removed" | "unverified" | null = selectedKey && !selected && subject ? (kindEv(selKind) === "complete" ? "removed" : "unverified") : null;
  // Operator filter edits win: one that hides the selection closes it. A
  // read change never does - the selection keeps its last evidence.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !rows.includes(selected)) setSelectedKey(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wl.namespace, wl.kindFilter, wl.needsAttention, unknownOnly, query]);

  // A pending verb is pinned to the workload OBJECT (and, for a pod delete,
  // the pod object) it was armed on: dropped on a same-name replacement or a
  // removal from a complete read of its kind, and re-checked at dispatch. The
  // backend writes only those UIDs (design §5b).
  const targetLive = (p: PinnedPending) => {
    if (p.cluster !== cluster) return false;
    const cur = allItems.find((w) => keyOf(w) === keyOf(p.w));
    if (!cur) return kindEv(p.w.kind) !== "complete";
    if (cur.uid !== p.w.uid) return false;
    if (p.kind !== "delete-pod") return true;
    const pod = cur.pods.find((x) => x.name === p.pod.name);
    if (pod) return pod.uid === p.pod.uid;
    return podsEv !== "complete";
  };
  const pending = pinned && targetLive(pinned) ? pinned : null;
  useEffect(() => { if (pinned && !pending) setPinned(null); }, [pinned, pending]);
  const dispatchArmed = (p: PinnedPending, send: () => void) => {
    setPinned(null);
    if (!targetLive(p)) {
      useFleet.getState().setActionStatus({ kind: "error", message: `${p.kind === "delete-pod" ? `pod ${p.w.namespace}/${p.pod.name}` : `${p.w.kind.toLowerCase()} ${p.w.namespace}/${p.w.name}`} was replaced or removed since the action was armed - nothing was sent` });
      return;
    }
    send();
  };

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
    st.setWorkloadsNamespace("");
    setUnknownOnly(false);
  };
  const oneKind = KINDS.filter((k) => wl.kindFilter[k]);
  const kindValue = oneKind.length === KINDS.length ? "" : oneKind.length === 1 ? oneKind[0] : "custom";

  let state: DatasetState = "ready";
  if (ev === "failed" && allItems.length === 0) state = "unreadable";
  else if (ev === "pending" && allItems.length === 0) state = "loading";
  else if (scopedItems.length === 0) state = "empty";
  else if (rows.length === 0) state = "no-match";
  const scope = wl.namespace || "all namespaces";
  const icon = <IconBox size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={8} label="loading workloads" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Workloads could not be read." hint={wl.error ?? undefined} />
    : state === "empty" && rowsRefreshing && counts.total > 0 && !wl.namespace ? <EmptyState icon={icon} title={`No workloads as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${counts.total}.`} />
    : state === "empty" && ev === "stale" ? <EmptyState icon={icon} title={`No workloads in ${scope} in the last good read.`} hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${wl.error ?? "unverified"}.`} />
    : state === "empty" && ev !== "complete" ? <EmptyState icon={icon} title={`No workloads in ${scope} in the read so far.`} hint={`Not every workload kind has been read: ${wl.error ?? "a read is still in progress"}.`} />
    : state === "empty" ? <EmptyState icon={icon} title={`No workloads in ${scope}.`} action={wl.namespace ? { label: "All namespaces", onClick: () => useFleet.getState().setWorkloadsNamespace("") } : undefined} />
    : <EmptyState icon={icon} title="No workloads match the current filters."
        hint={wl.needsAttention && !wl.search && (rowCounts.unknown > 0 || ev !== "complete")
          ? `No known attention - but ${rowCounts.unknown > 0 ? `${rowCounts.unknown} workload${rowCounts.unknown === 1 ? " is" : "s are"} unverified` : "the read was incomplete"}.`
          : `${scopedItems.length} workload${scopedItems.length === 1 ? "" : "s"} in ${scope}${asOf}.`}
        action={{ label: "Clear filters", onClick: clearFilters }} />;

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (wl.error && allItems.length > 0) notices.push(<WatchErrorBanner key="read" tool="Workloads" failure={`read failed - showing the last good read${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={wl.error} />);
  for (const a of attribution) {
    if (allItems.length === 0 || a.ev === "complete") continue;
    notices.push(
      <div key={`deps-${a.kind}`} role="status" data-testid={`attribution-evidence-${a.kind}`} className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
        {attributionText(a.kind, a.ev, wl.dependencies)}
      </div>,
    );
  }
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{wl.rowsState?.refreshError ? ` Last re-read failed: ${wl.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);

  const tone = attentionTone(counts);
  const rowTone = attentionTone(rowCounts);
  const filterBits = [wl.namespace ? `namespace ${wl.namespace}` : "", wl.needsAttention ? "Attention" : unknownOnly ? "Unknown" : "", kindValue && kindValue !== "custom" ? kindShort[kindValue as WorkloadKind] : kindValue === "custom" ? "kinds" : "", wl.search ? "search" : ""].filter(Boolean);

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ flex: 1, minHeight: 0 }}>
        <LensLayout
          label="Workloads"
          title="Workloads"
          summary={state === "loading" || state === "unreadable" ? null : (
            <>
              <span className="t-number" data-testid="workloads-total" style={{ color: "var(--color-text-secondary)" }}>{counts.total}{mark}</span>
              {counts.attention > 0 ? (
                <span className="t-status" data-testid="workloads-attention" style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
                  {tone === "danger" ? "▲ " : ""}{counts.attention}{mark} need attention
                </span>
              ) : <span className="t-secondary">{counts.unknown > 0 || !current ? "no known attention" : "none need attention"}</span>}
              <span className="t-secondary">
                {[counts.fault ? `${counts.fault}${mark} fault` : "", counts.degraded ? `${counts.degraded}${mark} degraded` : "", counts.unknown ? `${counts.unknown}${mark} unverified` : "", counts.progressing ? `${counts.progressing}${mark} rolling out` : "", counts.paused ? `${counts.paused}${mark} paused` : ""].filter(Boolean).join(" · ")}
              </span>
              {wl.namespace && (
                <span className="t-secondary" data-testid="workloads-scoped">
                  in {wl.namespace}: {rowCounts.total}{rowCounts.attention > 0 ? ` · ${rowCounts.attention} need attention` : ""}{asOf}
                </span>
              )}
            </>
          )}
          headerEnd={
            <>
              <UpdatedAt at={receivedAt} />
              <button className="l-btn" onClick={() => { void getWorkloadMetrics(cluster, wl.namespace); }} title="The workloads are live; this re-reads the usage columns">Refresh usage</button>
            </>
          }
          controls={
            <>
              <label style={{ display: "flex" }}>
                <span className="sr-only">Namespace</span>
                <select className="l-input" value={wl.namespace} onChange={(e) => useFleet.getState().setWorkloadsNamespace(e.target.value)}>
                  <option value="">All namespaces</option>
                  {(wl.namespace && !wl.namespaces.includes(wl.namespace) ? [...wl.namespaces, wl.namespace] : wl.namespaces).map((ns) => <option key={ns} value={ns}>{ns}</option>)}
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
                All <span style={{ color: "var(--color-text-tertiary)" }}>{rowCounts.total || ""}</span>
              </button>
              <button className="l-chip" aria-pressed={wl.needsAttention} onClick={() => { setUnknownOnly(false); if (!wl.needsAttention) useFleet.getState().toggleNeedsAttention(); }}>
                Attention {rowCounts.attention > 0 && <span style={{ color: rowTone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{rowCounts.attention}</span>}
              </button>
              {(rowCounts.unknown > 0 || unknownOnly) && (
                <button className="l-chip" aria-pressed={unknownOnly} onClick={() => { if (wl.needsAttention) useFleet.getState().toggleNeedsAttention(); setUnknownOnly(!unknownOnly); }}>
                  Unknown <span style={{ color: "var(--color-text-tertiary)" }}>{rowCounts.unknown || ""}</span>
                </button>
              )}
              <label style={{ display: "flex", flex: "0 1 280px", minWidth: 160 }}>
                <span className="sr-only">Search workloads</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={wl.search} placeholder="Search workloads" onChange={(e) => useFleet.getState().setWorkloadsSearch(e.target.value)} />
              </label>
              {showMetrics && (
                <button className="l-chip" aria-pressed={wl.nearLimitSort} onClick={() => useFleet.getState().toggleNearLimitSort()} title="Sort by how close usage is to the limit">
                  Sort: {wl.nearLimitSort ? "near limit" : "faults first"}
                </button>
              )}
            </>
          }
          notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
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
                        <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={w.gitops ? `Flux ownership label: ${w.gitops.kind} ${w.gitops.namespace}/${w.gitops.name}` : wl.fluxPresent ? undefined : "Flux was not detected when these rows were evaluated"}>
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
              <span className="t-secondary">{rows.length} of {scopedItems.length}{mark} workloads shown{filterBits.length ? ` · filter: ${filterBits.join(", ")}` : ""}{asOf}</span>
              {filterBits.length > 0 && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all {allItems.length}{mark} workloads</button>}
            </>
          }
          inspector={selectedKey && mine && subject ? (
            <WorkloadInspector
              key={`${cluster}/${selectedKey}`}
              cluster={cluster}
              workload={subject}
              gone={gone}
              podsEv={podsEv}
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
                else dispatchArmed({ kind: "scale", w, replicas, cluster }, () => void scaleWorkload(cluster, w.kind, w.namespace, w.name, w.uid ?? "", replicas));
              }}
              onCancelScale={() => setPending(null)}
              onDeletePod={(w, pod) => setPending({ kind: "delete-pod", w, pod })}
              onOpenPod={(w, pod) => { useFleet.getState().setSection("pods"); useFleet.getState().requestNavIntent({ kind: "select-pod", cluster, namespace: w.namespace, name: pod.name }); }}
              onOpenNode={(node) => { useFleet.getState().setSection("nodes"); useFleet.getState().requestNavIntent({ kind: "select-node", cluster, node }); }}
              onOpenFluxOwner={(w) => {
                if (!w.gitops) return;
                useFleet.getState().setSection("gitops");
                useFleet.getState().requestNavIntent({ kind: "select-flux", cluster, fluxKind: w.gitops.kind, namespace: w.gitops.namespace, name: w.gitops.name });
              }}
              onCompare={fleetSize > 1 ? (w) => setCompareTarget({ cluster, w }) : undefined}
              onUndo={(w, rev) => setPending({ kind: "undo", w, rev })}
              onPause={(w, pause) => setPending({ kind: "pause", w, pause })}
            />
          ) : undefined}
          inspectorLabel="Workload details"
          onCloseInspector={closeInspector}
        />
      </div>

      {compareTarget && compareTarget.cluster === cluster && (
        <FleetCompare origin={compareTarget.cluster} kind={compareTarget.w.kind} namespace={compareTarget.w.namespace} name={compareTarget.w.name} onClose={() => setCompareTarget(null)} />
      )}

      {logsTarget && (
        <WorkloadLogsDock
          cluster={logsTarget.cluster}
          target={logsTarget}
          onClose={() => setLogsTarget(null)}
          onPopOut={async (container) => {
            const ok = await openWorkloadLogsWindow(logsTarget.cluster, logsTarget.namespace, logsTarget.kind, logsTarget.name, container);
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
          danger
          confirmLabel="Restart"
          onCancel={() => setPending(null)}
          onConfirm={() => { const p = pending; dispatchArmed(p, () => void rolloutRestart(p.cluster, p.w.kind, p.w.namespace, p.w.name, p.w.uid ?? "")); }}
        />
      )}
      {pending?.kind === "scale" && (
        <ConfirmDialog
          title="scale"
          cluster={cluster}
          detail={`${pending.w.kind.toLowerCase()} ${pending.w.namespace}/${pending.w.name} → ${pending.replicas} replicas`}
          protected={isProtected}
          danger={pending.replicas < pending.w.desired}
          confirmLabel="Scale"
          onCancel={() => setPending(null)}
          onConfirm={() => { const p = pending; dispatchArmed(p, () => void scaleWorkload(p.cluster, p.w.kind, p.w.namespace, p.w.name, p.w.uid ?? "", p.replicas)); }}
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
          onConfirm={() => { const p = pending; dispatchArmed(p, () => void rolloutUndo(p.cluster, p.w.kind, p.w.namespace, p.w.name, p.w.uid ?? "", p.rev.revision)); }}
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
          onConfirm={() => { const p = pending; dispatchArmed(p, () => void rolloutPause(p.cluster, p.w.namespace, p.w.name, p.w.uid ?? "", p.pause)); }}
        />
      )}
      {pending?.kind === "delete-pod" && (
        <ConfirmDialog
          title="delete pod"
          cluster={pending.cluster}
          detail={`pod ${pending.w.namespace}/${pending.pod.name} - matched by ${pending.w.kind.toLowerCase()} ${pending.w.name}'s selector (a selector match, not proof of ownership)`}
          protected={isProtected}
          danger
          confirmLabel="Delete"
          onCancel={() => setPending(null)}
          onConfirm={() => { const p = pending; dispatchArmed(p, () => void deletePod(p.cluster, p.w.namespace, p.pod.name, p.pod.uid ?? "")); }}
        />
      )}
    </div>
  );
}

const statusLine: React.CSSProperties = { minHeight: 24, display: "flex", alignItems: "center", padding: "0 10px" };
// attributionText names one attribution dependency's evidence: what it is
// for, its freshness (last good time when stale) and its error.
function attributionText(kind: "Pod" | "ReplicaSet", ev: KindEvidence, deps: KindCoverageDTO[] | undefined): string {
  const d = deps?.find((x) => x.kind === kind);
  const err = d?.error ? ` (${d.error})` : "";
  const what = kind === "Pod" ? "verdicts that need pod evidence" : "Deployment pod attribution (via ReplicaSets)";
  const noun = kind === "Pod" ? "Pods" : "ReplicaSets";
  if (ev === "stale") return `${kind} read failing${d?.lastGoodAt ? ` - last good ${clock(d.lastGoodAt)}` : ""} - ${what} ${kind === "Pod" ? "are" : "is"} as last observed${err}.`;
  return `${noun} ${ev === "failed" ? "could not be read" : "are not read yet"} - ${what} ${kind === "Pod" ? "are" : "is"} unverified${err}.`;
}

// WorkloadInspector - investigation order: verdict; Logs (primary), Restart
// (secondary), More (scale, pause/resume, compare, manifest, delete a pod);
// pods, rollout history (undo), usage, reloads-on, then controller facts.
function WorkloadInspector({
  cluster, workload, gone, podsEv, showMetrics, isProtected, reloaderPresent, reloaderHealthy, pending,
  onClose, onLogs, onRestart, onScale, onScaleConfirm, onCancelScale, onDeletePod, onOpenPod, onOpenNode, onOpenFluxOwner, onCompare, onUndo, onPause,
}: {
  cluster: string;
  workload: WorkloadDTO;
  gone: "removed" | "unverified" | null;
  podsEv: KindEvidence; // what the pod list was read from
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
  const ai = useInspectorAI(workload.uid ? { kind: "row" as const, task: "workloads", part: "workloads", uid: workload.uid } : null, `${workload.kind}/${workload.namespace}/${workload.name}`, !gone, cluster);
  const w = workload;
  const live = gone !== "removed";
  // Selector-matched pods carry no verdict of their own: native facts only.
  const notReadyPods = w.pods.filter((p) => !p.ready).length;
  const scaling = pending?.kind === "scale-input" && keyOf(pending.w) === keyOf(w);
  const manifestRef = wellKnownResourceRef("apps", w.kind);
  const scalable = w.kind === "Deployment" || w.kind === "StatefulSet";

  const viewManifest: MenuItem | null = manifestRef
    ? { label: "View manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(w.namespace, w.name); } }
    : null;
  const more: MenuItem[] = [
    ...(scalable ? [{ label: `Scale (${w.desired} replicas)…`, onSelect: () => onScale(w) }] : []),
    ...(w.kind === "Deployment" ? [{ label: w.paused ? "Resume rollouts" : "Pause rollouts", onSelect: () => onPause(w, !w.paused), hint: w.paused ? "spec.paused=false" : "Hold rollouts without touching pods (spec.paused=true)" }] : []),
    ...(onCompare ? [{ label: "Compare across the fleet", onSelect: () => onCompare(w) }] : []),
    ...(viewManifest ? [viewManifest] : []),
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
        verdict={gone === "removed" ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">Workload removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
          </div>
        ) : gone === "unverified" ? (
          <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <span className="t-status">Not in the current {w.kind} read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
          </div>
        ) : <VerdictStatus
          health={w.health}
          // The native line already carries ready/updated/available (and
          // generation); print counts once - only add what it lacks.
          detail={[w.health?.native ? "" : `${w.ready}/${w.desired} ready · ${w.updated} updated · ${w.available} available`, w.restarts ? `${w.restarts} restarts` : ""].filter(Boolean).join(" · ") || undefined}
        />}
        primary={live ? { label: "Logs", kbd: "l", onSelect: () => onLogs(w) } : undefined}
        secondary={live ? { label: "Restart", onSelect: () => onRestart(w) } : undefined}
        more={[...(live ? more : viewManifest ? [viewManifest] : []), ...ai.menu]}
      >
        {ai.block}
        {live && scaling && (
          <InspectorBlock title="Scale">
            <ScalePopover initial={w.desired} onConfirm={(n) => onScaleConfirm(w, n)} onCancel={onCancelScale} />
          </InspectorBlock>
        )}

        <InspectorBlock title="Pods · selector match" count={w.pods.length}
          extra={podsEv !== "complete" ? <span className="t-secondary" data-testid="pods-evidence" style={{ color: "var(--color-text-warning)" }}>{podsEv === "stale" ? "as last observed" : "pod read unverified"}</span>
            : notReadyPods > 0 ? <span className="t-secondary">{notReadyPods} not ready</span> : undefined}>
          {w.pods.length === 0 ? <span className="t-secondary">{podsEv === "complete" ? "No pods match this workload's selector." : podsEv === "stale" ? "No pods matched in the last good pod read." : "Pods are not read - the selector match is unverified."}</span> : (
            <div style={{ display: "flex", flexDirection: "column" }}>
              {w.pods.slice(0, 8).map((p) => (
                <div key={p.name} className="t-identifier" style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) auto", columnGap: 8, padding: "3px 0" }}>
                  {/* Two lines in a 360px inspector: identity owns line one; status
                      and node wrap on line two instead of squeezing the name. */}
                  <button onClick={() => live && onOpenPod(w, p)} aria-label={`open pod ${p.name}`} title="open in Pods"
                    style={{ minWidth: 0, background: "none", border: 0, padding: 0, textAlign: "left", cursor: "pointer", color: "var(--color-text-info)", font: "inherit", overflowWrap: "anywhere" }}>{p.name}</button>
                  {live ? (
                    <button onClick={() => onDeletePod(w, p)} aria-label={`delete pod ${p.name}`} title={`delete pod ${p.name}`}
                      style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-tertiary)", display: "inline-flex", alignSelf: "start", marginTop: 2 }}>
                      <IconTrash size={13} stroke={1.6} />
                    </button>
                  ) : <span />}
                  <span style={{ gridColumn: "1 / -1", minWidth: 0, display: "flex", flexWrap: "wrap", columnGap: 8, color: "var(--color-text-secondary)" }}>
                    <span style={{ overflowWrap: "anywhere", minWidth: 0 }}>{p.ready ? "ready" : p.reason || "not ready"}{p.restarts ? ` · ${p.restarts}×` : ""}</span>
                    <button onClick={() => onOpenNode(p.node)} title={`open node ${p.node}`} style={{ minWidth: 0, background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-tertiary)", font: "inherit", overflowWrap: "anywhere", textAlign: "left" }}>{p.node ? `on ${p.node}` : "unscheduled"}</button>
                  </span>
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
  // The history is the selected OBJECT's (design §5b): requested per
  // cluster + UID, accepted only when it was read from that UID, and read
  // afresh on a same-name replacement - an earlier object's revisions (and an
  // undo bound to them) never show under the new one.
  const [history, setHistory] = useState<{ key: string; uid?: string; revisions: RolloutRevisionDTO[]; error?: string } | null>(null);
  const key = `${cluster}|${keyOf(workload)}|${workload.uid ?? ""}`;
  useEffect(() => {
    let live = true;
    setHistory(null);
    void rolloutHistory(cluster, workload.kind, workload.namespace, workload.name).then((r) => {
      if (live) setHistory({ key, uid: r.uid, revisions: r.revisions, error: r.error });
    });
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const current = history?.key === key ? history : null;
  const ofOther = !!current && !current.error && !!workload.uid && current.uid !== workload.uid;
  const rows = current && !ofOther ? current.revisions : null;
  return (
    <div data-testid="rollout-section">
      <InspectorBlock title="Rollout history" count={rows ? rows.length : undefined}>
        {current?.error ? (
          <div className="t-body" style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{current.error}</div>
        ) : ofOther ? (
          <span className="t-secondary" data-testid="history-of-other" style={{ color: "var(--color-text-warning)" }}>
            The history read found another object with this name (UID {current?.uid || "unknown"}, not {workload.uid}) - no revision is shown or undoable until the list catches up.
          </span>
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


