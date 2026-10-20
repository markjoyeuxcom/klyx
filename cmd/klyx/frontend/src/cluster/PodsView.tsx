import { useEffect, useState, useCallback, useMemo, useRef } from "react";
import { useInspectorAlerts } from "./AlertsBlock";
import { IconExternalLink, IconX, IconAlertTriangle, IconTerminal2, IconCircleDot } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { PodDetailDTO, PodRowDTO, PodRowContainerDTO, ContainerSummaryDTO, ContainerUsageDTO, PodUsageResultDTO, KindCoverageDTO } from "../store/fleet";
import { openPodDetail, deletePod } from "../bridge/pods";
import { getPodContainerUsage } from "../bridge/metrics";
import { rightsizing, fmtCpu, fmtMem } from "./saturation";
import { openLogHistoryWindow, openLogsWindow } from "../bridge/windows";
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
import { EmptyState, SkeletonRows, btn, ellipsis } from "../chrome/primitives";
import { CopyButton } from "../chrome/CopyButton";
import { ScratchPanel } from "./ScratchPanel";
import { YamlBlock } from "../chrome/YamlBlock";
import { InspectorConditions, InspectorEvents } from "./ResourceInspector";
import { loadClusterPrefs, loadSearch, savePodsViewPrefs, saveSearch } from "../store/persist";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { UpdatedAt, formatAge, useNow, useReceivedAt } from "../chrome/Age";
import { wellKnownResourceRef } from "./resourceRefs";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { CountSentence } from "../layout/CountSentence";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { VerdictLabel, VerdictMark, VerdictStatus } from "../health/VerdictView";
import { type VerdictCounts, attentionTone, isAttention, kindOf, labelOf, orderOf, summarize } from "../health/verdict";
import { usePodRows } from "../health/usePodRows";
import { useCommitProbe } from "../health/perfHooks";
import { clock, coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
import { useInspectorAI } from "../ai/AIExplain";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone, expectsOther } from "./PinnedGone";
import { LogHistoryPane } from "./LogHistoryPane";
import { expectPin } from "../nav/inspectorPin";
import { PodIdentity } from "./IdentityBlocks";
import { DebugChooser, DebugContainersBlock, PodResourcesBlock, PodExtendedBlock, debugConfirmText, podStale, type DebugChoice } from "./PodS6";
import { PodClaimsBlock } from "./DRABlocks";
import { SyncingChip } from "./staleness";

// Pods lens on the shared workspace template (2026-10 redesign pilot). The
// template owns geometry; this file owns pod data, filters, selection and the
// day-2 operations, which route through the same confirm/arming as before.

function readyOf(p: PodRowDTO): string {
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

// uid, createdUnix and gone pin a History view to the pod (coverage S8b §4);
// history opens the dock in History mode (a gone pod has no Live).
type LogsTarget = { namespace: string; name: string; containers: { name: string; init?: boolean }[]; uid?: string; createdUnix?: number; gone?: boolean; history?: boolean };

// The age ticks from the creation time (1c slice 3 §3): the rows carry no
// relative age (addendum §3.1).
const ageText = (p: PodRowDTO, nowS: number) => (p.createdUnix ? formatAge(Math.max(0, nowS - p.createdUnix)) : "-");
const keyOf = (p: { namespace: string; name: string }) => `${p.namespace}/${p.name}`;
// faults first, then severity, namespace and name - the lens's own order,
// never the capture's
const byTriage = (a: PodRowDTO, b: PodRowDTO) =>
  orderOf(kindOf(a.health)) - orderOf(kindOf(b.health)) || (b.health?.rank ?? 0) - (a.health?.rank ?? 0)
  || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name);
// What a selected row's detail is re-read on: its identity and the facts the
// detail explains. A capture that changes none of them re-reads nothing.
// evidenceRev revises the resize and debug evidence (coverage S6 review 1):
// an acknowledgement, an actuation or a debug container exiting changes no
// other field, and must still re-read the detail.
const detailSig = (cluster: string, p: PodRowDTO) =>
  [cluster, p.namespace, p.name, p.uid, p.generation ?? 0, kindOf(p.health), p.health?.label ?? "", p.health?.reason ?? "", p.ready, p.restarts, p.node,
    p.observedGeneration ?? 0, JSON.stringify(p.resize ?? null), p.evidenceRev ?? ""].join("|");

// A verb's target: the pod OBJECT (its UID) it was armed against.
type PodTarget = { namespace: string; name: string; uid: string };

export function PodsView({ cluster }: { cluster: string }) {
  const pods = useFleet((s) => s.pods);
  const logHistory = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.logHistoryPresent ?? false); // S8b: a Loki configured
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const nowS = Math.floor(useNow() / 1000);

  const [cursor, setCursor] = useState(-1);
  const [unknownOnly, setUnknownOnly] = useState(false);
  // The logs dock is pinned to the cluster it was opened on: it renders only
  // there, closes on a cluster change, and a late pop-out never closes a
  // later dock - DEV/STG/PRD share namespace/name.
  const [logsPinned, setLogsPinned] = useState<(LogsTarget & { cluster: string }) | null>(null);
  const setLogsTarget = useCallback((t: LogsTarget | null) => setLogsPinned(t ? { ...t, cluster } : null), [cluster]);
  const logsTarget = logsPinned && logsPinned.cluster === cluster ? logsPinned : null;
  useEffect(() => { setLogsPinned((cur) => (cur && cur.cluster !== cluster ? null : cur)); }, [cluster]);
  const [bulk, setBulk] = useState<Set<string>>(new Set());
  // the bulk delete is pinned to the pod OBJECTS it was confirmed for
  const [bulkConfirm, setBulkConfirm] = useState<PodTarget[] | null>(null);
  const [scratchOpen, setScratchOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  // The pods are the Observer's shared rows, all namespaces and uncapped
  // (1c slice 3 §5.9) - live; the namespace is a list filter.
  usePodRows(cluster);
  const mine = pods.cluster === cluster;
  useCommitProbe("pods/pods", mine ? pods.rowsVer : null);
  useEffect(() => {
    // Hydration fills only fields still at their reset defaults.
    const st = useFleet.getState();
    const prefs = loadClusterPrefs(cluster).pods ?? {};
    if (prefs.needsAttention && !st.pods.needsAttention) st.setPodsNeedsAttention(true);
    const savedSearch = loadSearch(cluster, "pods");
    if (savedSearch && st.pods.search === "") st.setPodsSearch(savedSearch);
    if (prefs.namespace && st.pods.namespace === "") st.setPodsNamespace(prefs.namespace);
  }, [cluster]);

  useEffect(() => {
    if (!mine) return;
    savePodsViewPrefs(cluster, { namespace: pods.namespace, needsAttention: pods.needsAttention });
    saveSearch(cluster, "pods", pods.search);
  }, [cluster, mine, pods.namespace, pods.needsAttention, pods.search]);

  // Palette "jump to namespace" and a scratch pod launch set the filter.
  const nsRequest = useFleet((s) => s.pods.nsRequest);
  useEffect(() => {
    if (!nsRequest) return;
    useFleet.getState().clearPodsNsRequest();
    if (nsRequest.cluster !== cluster) return;
    useFleet.getState().setPodsNamespace(nsRequest.namespace);
  }, [nsRequest, cluster]);

  // Evidence is the Pod kind's own coverage; what owner resolution read
  // (ReplicaSets) qualifies the owners, never the list.
  const ev = kindEvidence(mine && !pods.loading, pods.coverage, "Pod", pods.error);
  const rsEv = mine && pods.dependencies?.some((d) => d.kind === "ReplicaSet") ? kindEvidence(true, pods.dependencies, "ReplicaSet", null) : "pending";
  const error = mine ? pods.error ?? null : null;
  const rowsRefreshing = mine && !!pods.rowsState?.refreshing;
  const rowsAsOf = mine && pods.rowsState?.evaluatedAt ? clock(pods.rowsState.evaluatedAt) : "the last read";
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = mine ? lastGoodAt(pods.coverage, ["Pod"]) : null;
  const discoveryGap = mine ? coverageGaps([...(pods.coverage ?? []), ...(pods.dependencies ?? [])]) : null;
  const mark = ev === "complete" ? "" : ev === "stale" ? "*" : "+";

  const allItems = useMemo(() => (mine ? [...pods.items].sort(byTriage) : []), [mine, pods.items]);
  const scopedItems = useMemo(() => (pods.namespace ? allItems.filter((p) => p.namespace === pods.namespace) : allItems), [allItems, pods.namespace]);
  // global: the count selector's verdict counts (slice 3 §4a); row-scoped:
  // the chips, the namespace line and the footer ("as of" while refreshing)
  const globalCounts = mine ? pods.counts : null;
  const counts: VerdictCounts = globalCounts ? { ...globalCounts, attention: globalCounts.fault + globalCounts.degraded } : summarize(allItems, (p) => p.health);
  const rowCounts = summarize(scopedItems, (p) => p.health);
  const receivedAt = useReceivedAt(allItems.length > 0 ? pods.items : null);
  // "none need attention" is a claim about the present: a complete, current read
  const current = ev === "complete" && !pods.rowsState?.updating && !rowsRefreshing;

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

  // The selection is pinned to its pod's UID (The Line slice 4 §6.3): a pod
  // missing from the rows keeps its last row - removed only on a complete
  // read of the Pod kind, otherwise unverified (slice 3 §5a) - and a
  // same-name pod with a new UID on a complete read is "replaced", never
  // silently followed. Its detail and verbs follow the pinned UID.
  const sel = mine ? pods.selected : null;
  const found = sel ? allItems.find((p) => p.namespace === sel.namespace && p.name === sel.name) : undefined;
  const pin = usePinnedSubject(cluster, sel ? keyOf(sel) : null, found, ev === "complete", "Pod");
  const subject = pin.subject;
  const live = pin.gone ? undefined : found; // the pinned pod, while the rows hold it
  // the inspector gates on "removed": a replaced pod runs no action either
  const gone: "removed" | "unverified" | null = pin.gone === "replaced" ? "removed" : pin.gone;

  // Opening a row reads its detail; the row's later changes re-read it, and a
  // same-name replacement is read afresh - the old object's detail never
  // becomes the new one's (design §5b).
  const readSig = useRef<string | null>(null);
  const openRow = useCallback((p: PodRowDTO) => {
    readSig.current = detailSig(cluster, p);
    void openPodDetail(cluster, p.namespace, p.name, p.uid ?? "");
  }, [cluster]);
  const liveSig = live ? detailSig(cluster, live) : null;
  useEffect(() => {
    if (!sel || !live || !liveSig || readSig.current === liveSig) return;
    readSig.current = liveSig;
    void openPodDetail(cluster, live.namespace, live.name, live.uid ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, sel, liveSig]);

  // Selection (spec §3): on the first fresh complete read after this route
  // entry, auto-select the first visible attention row once - never again
  // after Escape/close, on later pushes or filter changes - and never move
  // focus.
  const autoSelect = useRef<"pending" | "done">("pending");
  const itemsAtEntry = useRef<PodRowDTO[] | null>(null);
  if (itemsAtEntry.current === null) itemsAtEntry.current = pods.items;
  useEffect(() => { autoSelect.current = "pending"; itemsAtEntry.current = useFleet.getState().pods.items; }, [cluster]);
  const freshRead = mine && ev === "complete" && pods.items !== itemsAtEntry.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead) return;
    autoSelect.current = "done";
    if (useFleet.getState().pods.selected) return;
    const idx = filtered.findIndex((p) => isAttention(kindOf(p.health)));
    if (idx < 0) return;
    setCursor(idx);
    openRow(filtered[idx]);
  }, [filtered, freshRead, openRow]);

  // Operator filter edits win: one that hides the selection closes it. A
  // read change never does - the selection keeps its last evidence.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (live && !filtered.includes(live)) useFleet.getState().selectPod(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pods.namespace, pods.needsAttention, pods.search, unknownOnly]);

  // The palette's Scratch entry opens the existing Scratch panel here.
  useNavIntent(cluster, ["open-scratch"], () => {
    setScratchOpen(true);
    return "consumed";
  }, []);

  // Cross-links name a pod: resolve it to its row's object, revealing it past
  // the filters.
  useNavIntent(cluster, ["logs-pod", "select-pod"], (intent) => {
    if (!("namespace" in intent)) return "missing";
    const p = allItems.find((x) => x.namespace === intent.namespace && x.name === intent.name);
    // An opening by UID (slice 9 note §4.2) selects at once, pinned to that
    // UID: a different or missing row is said, never substituted.
    if (intent.kind === "select-pod" && intent.uid) expectPin(cluster, "Pod", keyOf(intent), intent.uid);
    if (intent.kind === "select-pod" && expectsOther(p, intent.uid)) {
      autoSelect.current = "done";
      useFleet.getState().selectPod({ namespace: intent.namespace, name: intent.name, uid: intent.uid! });
      return "consumed";
    }
    if (allItems.length === 0) return ev === "complete" ? "missing" : "wait";
    if (!p) return ev === "complete" ? "missing" : "wait";
    if (intent.kind === "logs-pod") { setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers, uid: p.uid, createdUnix: p.createdUnix }); return "consumed"; }
    autoSelect.current = "done";
    if (!filtered.includes(p)) {
      const st = useFleet.getState();
      st.setPodsNamespace(""); st.setPodsSearch(""); st.setPodsNeedsAttention(false); setUnknownOnly(false);
    }
    openRow(p);
    return "consumed";
  }, [allItems, filtered, ev]);

  const select = useCallback((idx: number) => { setCursor(idx); listRef.current?.scrollToIndex(idx); }, []);
  const activate = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) { autoSelect.current = "done"; openRow(p); }
  }, [filtered, openRow]);
  const closeInspector = useCallback(() => {
    autoSelect.current = "done";
    useFleet.getState().selectPod(null);
  }, []);
  const logsFor = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers, uid: p.uid, createdUnix: p.createdUnix });
  }, [filtered, setLogsTarget]);
  const toggleBulk = useCallback((key: string) => {
    setBulk((prev) => { const next = new Set(prev); if (next.has(key)) next.delete(key); else next.add(key); return next; });
  }, []);
  const bulkKey = useCallback((idx: number) => {
    const p = filtered[idx];
    if (p) toggleBulk(keyOf(p));
  }, [filtered, toggleBulk]);
  useEffect(() => { setBulk(new Set()); setBulkConfirm(null); }, [cluster]);

  useListKeys({
    count: filtered.length,
    selected: effectiveCursor,
    onSelect: select,
    onActivate: activate,
    onEscape: () => { if (pods.selected) closeInspector(); },
    searchRef,
    extraKeys: { l: logsFor, x: bulkKey },
  });

  // A pod target is live while the rows hold that OBJECT, or while an
  // incomplete read cannot say it is gone (the backend's UID precondition
  // decides then).
  const targetLive = useCallback((t: PodTarget) => {
    const cur = useFleet.getState().pods;
    if (cur.cluster !== cluster) return false;
    const row = cur.items.find((p) => p.namespace === t.namespace && p.name === t.name);
    if (row) return row.uid === t.uid;
    return kindEvidence(!cur.loading, cur.coverage, "Pod", cur.error) !== "complete";
  }, [cluster]);
  const bulkTargets = allItems.filter((p) => bulk.has(keyOf(p)));
  const runBulkDelete = useCallback(async (targets: PodTarget[], typed: string) => {
    setBulkConfirm(null);
    setBulk(new Set());
    const skipped: string[] = [];
    let sent = 0;
    for (const t of targets) {
      if (!targetLive(t)) { skipped.push(keyOf(t)); continue; }
      // Each destructive call on a protected cluster after the first re-arms
      // with the same typed confirmation; a failed re-arm stops honestly.
      if (isProtected && sent > 0) {
        const r = await armProtected(cluster, typed);
        if (!r.ok) {
          useFleet.getState().setActionStatus({ kind: "error", message: `bulk delete stopped at ${keyOf(t)}: ${r.error || "could not re-arm protected action"}` });
          return;
        }
      }
      sent++;
      await deletePod(cluster, t.namespace, t.name, t.uid);
    }
    if (skipped.length > 0) {
      useFleet.getState().setActionStatus({ kind: "error", message: `${skipped.length} pod${skipped.length === 1 ? " was" : "s were"} replaced or removed since the delete was armed - nothing was sent for ${skipped.slice(0, 3).join(", ")}${skipped.length > 3 ? ` and ${skipped.length - 3} more` : ""}` });
    }
  }, [cluster, isProtected, targetLive]);
  // a confirm whose targets have all been replaced or removed is dropped
  useEffect(() => {
    if (bulkConfirm && !bulkConfirm.some(targetLive)) setBulkConfirm(null);
  }, [bulkConfirm, targetLive, pods.items]);

  const clearFilters = () => {
    useFleet.getState().setPodsSearch("");
    useFleet.getState().setPodsNeedsAttention(false);
    useFleet.getState().setPodsNamespace("");
    setUnknownOnly(false);
  };

  // ---- dataset state ------------------------------------------------------
  let state: DatasetState = "ready";
  if (ev === "failed" && allItems.length === 0) state = "unreadable";
  else if (ev === "pending" && allItems.length === 0) state = "loading";
  else if (scopedItems.length === 0) state = "empty";
  else if (filtered.length === 0) state = "no-match";
  const scope = pods.namespace || "all namespaces";
  const icon = <IconCircleDot size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={8} label="loading pods" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Pods could not be read." hint={error ?? undefined} />
    : state === "empty" && rowsRefreshing && counts.total > 0 && !pods.namespace ? <EmptyState icon={icon} title={`No pods as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${counts.total}.`} />
    : state === "empty" && ev === "stale" ? <EmptyState icon={icon} title={`No pods in ${scope} in the last good read.`} hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${error ?? "unverified"}.`} />
    : state === "empty" && ev !== "complete" ? <EmptyState icon={icon} title={`No pods in ${scope} in the read so far.`} hint={`The pod read is incomplete: ${error ?? "a read is still in progress"}.`} />
    : state === "empty" ? <EmptyState icon={icon} title={`No pods in ${scope}.`} hint="The read succeeded and returned no pods." action={pods.namespace ? { label: "All namespaces", onClick: () => useFleet.getState().setPodsNamespace("") } : undefined} />
    : <EmptyState icon={icon} title="No pods match the current filters." hint={`${scopedItems.length} pod${scopedItems.length === 1 ? "" : "s"} in ${scope}${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // ---- header: the global verdict counts and one attention summary --------
  const tone = attentionTone(counts);
  const rowTone = attentionTone(rowCounts);
  const summary = state === "loading" || state === "unreadable" ? null : (
    <>
      <CountSentence total={counts.total} marker={mark} noun={["pod", "pods"]} totalTestid="pods-total"
        attention={{ count: counts.attention, marker: mark, phrase: "need attention", fault: tone === "danger", testid: "pods-attention" }}
        clear={counts.unknown > 0 || !current ? "no known attention" : "none need attention"} />
      {(counts.fault > 0 || counts.degraded > 0 || counts.unknown > 0) && (
        <span className="t-secondary">
          {[counts.fault ? `${counts.fault}${mark} fault` : "", counts.degraded ? `${counts.degraded}${mark} degraded` : "", counts.unknown ? `${counts.unknown}${mark} unverified` : ""].filter(Boolean).join(" · ")}
        </span>
      )}
      {pods.namespace && (
        <span className="t-secondary" data-testid="pods-scoped">
          in {pods.namespace}: {rowCounts.total}{rowCounts.attention > 0 ? ` · ${rowCounts.attention} need attention` : ""}{asOf}
        </span>
      )}
    </>
  );

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (error && allItems.length > 0) notices.push(<WatchErrorBanner key="read" tool="Pods" failure={`read failed - showing the last good read${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={error} />);
  if (allItems.length > 0 && rsEv !== "complete") notices.push(
    <div key="owners" role="status" data-testid="owner-evidence" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
      {ownerText(rsEv, pods.dependencies)}
    </div>,
  );
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{pods.rowsState?.refreshError ? ` Last re-read failed: ${pods.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  if (bulk.size > 0) notices.push(
    <div key="bulk" className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 10, padding: "0 10px", borderRadius: 4, background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
      <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>{bulkTargets.length} pod{bulkTargets.length === 1 ? "" : "s"} selected</span>
      <button className="l-btn l-btn-danger" onClick={() => setBulkConfirm(bulkTargets.map((p) => ({ namespace: p.namespace, name: p.name, uid: p.uid ?? "" })))} disabled={bulkTargets.length === 0}>Delete selected</button>
      <button className="l-btn" onClick={() => setBulk(new Set())}>Clear</button>
      <span className="t-secondary">x toggles the highlighted row</span>
    </div>,
  );

  const filterLabel = [pods.namespace ? `namespace ${pods.namespace}` : "", pods.needsAttention ? "Attention" : unknownOnly ? "Unknown" : "", pods.search ? "search" : ""].filter(Boolean).join(", ");

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ flex: 1, minHeight: 0 }}>
        <LensLayout
          label="Pods"
          title="Pods"
          summary={summary}
          headerEnd={
            <>
              <UpdatedAt at={receivedAt} />
              <button className="l-btn" onClick={() => setScratchOpen(true)} title="Launch a TTL-bounded debug pod into klyx-scratch">Debug pod</button>
            </>
          }
          controls={
            <>
              <label style={{ display: "flex" }}>
                <span className="sr-only">Namespace</span>
                <select className="l-input" value={pods.namespace} onChange={(e) => useFleet.getState().setPodsNamespace(e.target.value)}>
                  <option value="">All namespaces</option>
                  {(pods.namespace && !pods.namespaces.includes(pods.namespace) ? [...pods.namespaces, pods.namespace] : pods.namespaces).map((ns) => <option key={ns} value={ns}>{ns}</option>)}
                </select>
              </label>
              <div role="group" aria-label="Status" className="l-seg">
                <button className="l-chip" aria-pressed={!pods.needsAttention && !unknownOnly} onClick={() => { setUnknownOnly(false); if (pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }}>
                  All <span style={{ color: "var(--color-text-tertiary)" }}>{rowCounts.total || ""}</span>
                </button>
                <button className="l-chip" aria-pressed={pods.needsAttention} onClick={() => { setUnknownOnly(false); if (!pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }}>
                  Attention {rowCounts.attention > 0 && <span style={{ color: rowTone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{rowCounts.attention}</span>}
                </button>
                {(rowCounts.unknown > 0 || unknownOnly) && (
                  <button className="l-chip" aria-pressed={unknownOnly} onClick={() => { setUnknownOnly(true); if (pods.needsAttention) useFleet.getState().togglePodsNeedsAttention(); }}>
                    Unknown <span style={{ color: "var(--color-text-tertiary)" }}>{rowCounts.unknown || ""}</span>
                  </button>
                )}
              </div>
              <label className="lens-search">
                <span className="sr-only">Search pods</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={pods.search} placeholder="Search pods" onChange={(e) => useFleet.getState().setPodsSearch(e.target.value)} />
              </label>
            </>
          }
          notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
          state={state}
          stateView={stateView}
          body={
            <LensTable minWidth={552}>
              <div className="lens-table-head pods-grid" role="row">
                <span /><span>Pod</span><span>Status</span><span>Ready</span><span className="col-restarts">Restarts</span><span className="col-node">Node</span><span className="col-age">Age</span><span />
              </div>
              <VirtualList
                ref={listRef}
                items={filtered}
                rowHeight={TABLE_ROW_HEIGHT}
                rowKey={(p) => keyOf(p)}
                style={{ flex: 1, minHeight: 0 }}
                render={(p, i) => (
                  <PodRow
                    key={keyOf(p)}
                    p={p}
                    nowS={nowS}
                    cursor={i === effectiveCursor}
                    selected={sel?.namespace === p.namespace && sel?.name === p.name}
                    bulkSelected={bulk.has(keyOf(p))}
                    onToggleBulk={() => toggleBulk(keyOf(p))}
                    onOpen={() => { setCursor(i); autoSelect.current = "done"; openRow(p); }}
                    onLogs={() => setLogsTarget({ namespace: p.namespace, name: p.name, containers: p.containers, uid: p.uid, createdUnix: p.createdUnix })}
                  />
                )}
              />
            </LensTable>
          }
          footer={<span className="t-legend">j k navigate · ↵ inspect · l logs · x select · / search</span>}
          footerEnd={
            <>
              <span className="t-secondary">{filtered.length} of {scopedItems.length}{mark} pods shown{filterLabel ? ` · filter: ${filterLabel}` : ""}{asOf}</span>
              {filterLabel && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all {allItems.length}{mark} pods</button>}
            </>
          }
          inspector={sel && subject ? (
            <PodInspector
              // Object-scoped state (tab, confirms, popovers) resets per name.
              key={`${cluster}/${sel.namespace}/${sel.name}`}
              cluster={cluster}
              row={subject}
              uid={subject.uid ?? sel.uid}
              nowS={nowS}
              gone={gone}
              replaced={pin.gone === "replaced" && pin.next ? () => { const n = pin.next as PodRowDTO; pin.adopt(); openRow(n); } : undefined}
              rsEv={rsEv}
              detail={pods.detail}
              loading={pods.detailLoading}
              isProtected={isProtected}
              onClose={closeInspector}
              onOpenLogs={setLogsTarget}
            />
          ) : sel && pin.expected && pin.gone ? (
            <PinnedGone cluster={cluster} kind="Pod" namespace={sel.namespace} name={sel.name} uid={pin.expected} gone={pin.gone}
              onAdopt={pin.next ? () => { const n = pin.next as PodRowDTO; pin.adopt(); openRow(n); } : undefined}
              onHistory={logHistory ? () => setLogsTarget({ namespace: sel.namespace, name: sel.name, containers: [], uid: pin.expected!, gone: true, history: true }) : undefined}
              onClose={closeInspector} />
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

      {bulkConfirm && bulkConfirm.length > 0 && (
        <ConfirmDialog
          title={`delete ${bulkConfirm.length} pod${bulkConfirm.length === 1 ? "" : "s"}`}
          cluster={cluster}
          detail={`${bulkConfirm.slice(0, 5).map(keyOf).join(", ")}${bulkConfirm.length > 5 ? ` and ${bulkConfirm.length - 5} more` : ""} - controllers recreate owned pods; bare pods are NOT recreated.`}
          protected={isProtected}
          danger
          confirmLabel={`Delete ${bulkConfirm.length}`}
          onConfirm={() => void runBulkDelete(bulkConfirm, "")}
          onConfirmTyped={(typed) => void runBulkDelete(bulkConfirm, typed)}
          onCancel={() => setBulkConfirm(null)}
        />
      )}

      {logsTarget && (
        <LogsDock
          cluster={logsTarget.cluster}
          target={logsTarget}
          onClose={() => setLogsTarget(null)}
          onPopOut={async (container) => {
            const t = logsTarget; // rendered only on its own cluster
            const ok = await openLogsWindow(t.cluster, t.namespace, t.name, container);
            // close only the dock that popped out, never a later one
            if (ok) setLogsPinned((cur) => (cur === t ? null : cur));
          }}
        />
      )}
    </div>
  );
}

const statusLine: React.CSSProperties = { minHeight: 24, display: "flex", alignItems: "center", padding: "0 10px" };

// ownerText names the owner-resolution evidence: a pod under a Deployment is
// joined to it through its ReplicaSet.
function ownerText(ev: KindEvidence, deps: KindCoverageDTO[] | undefined): string {
  const d = deps?.find((x) => x.kind === "ReplicaSet");
  const err = d?.error ? ` (${d.error})` : "";
  if (ev === "stale") return `ReplicaSet read failing${d?.lastGoodAt ? ` - last good ${clock(d.lastGoodAt)}` : ""} - pod owners are as last observed${err}.`;
  return `ReplicaSets ${ev === "failed" ? "could not be read" : "are not read yet"} - a pod under a Deployment shows its ReplicaSet; owners are unresolved${err}.`;
}

// ---------------------------------------------------------------------------
// PodRow
// ---------------------------------------------------------------------------

function PodRow({ p, nowS, cursor, selected, bulkSelected, onToggleBulk, onOpen, onLogs }: {
  p: PodRowDTO;
  nowS: number;
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
      <VerdictMark health={p.health} stale={podStale(p)} />
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
      {/* coverage S6 §4: the kubelet has not acknowledged the latest spec;
          only where the verdict draws no mark - a lag never hides a fault */}
      <span style={{ ...ellipsis }}>{podStale(p) && kindOf(p.health) === "ok"
        ? <SyncingChip generation={p.generation} observedGeneration={p.observedGeneration} />
        : <VerdictLabel health={p.health} />}</span>
      <span style={{ color: "var(--color-text-secondary)" }}>{ready}/{nonInit.length}</span>
      <span className="col-restarts" style={{ color: p.restarts > 0 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{p.restarts}</span>
      <span className="col-node" style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={p.node}>{p.node || "-"}</span>
      <span className="col-age" style={{ color: "var(--color-text-tertiary)" }}>{ageText(p, nowS)}</span>
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

// A pending verb is pinned to the pod OBJECT (and, for an owner restart, the
// owner object) it was armed on.
type Pending = { verb: "restart" | "delete"; summary: PodRowDTO } | { verb: "debug"; summary: PodRowDTO; container: string; choice?: DebugChoice };

// PodInspector - investigation order: verdict, actions, runtime, events,
// placement, then collapsed metadata. The row (live, or the selection's last)
// is the verdict and the facts; the detail is shown only for the row's UID
// (design §5b) - a same-name replacement's detail never becomes this one's.
function PodInspector({ cluster, row, uid, nowS, gone, replaced, rsEv, detail, loading, isProtected, onClose, onOpenLogs }: {
  cluster: string;
  row: PodRowDTO;
  uid: string;
  nowS: number;
  gone: "removed" | "unverified" | null;
  replaced?: () => void;
  rsEv: KindEvidence; // what owner resolution read
  detail: PodDetailDTO | null;
  loading: boolean;
  isProtected: boolean;
  onClose: () => void;
  onOpenLogs: (t: LogsTarget) => void;
}) {
  const logHistory = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.logHistoryPresent ?? false);
  const ai = useInspectorAI(uid ? { kind: "row" as const, task: "pods", part: "pods", uid: uid } : null, `Pod/${row.namespace}/${row.name}`, !gone, cluster);
  // coverage S8a: Alertmanager's alerts naming this pod
  const alertTarget = useMemo(() => (uid ? { uid, kind: "Pod", namespace: row.namespace, name: row.name } : null), [uid, row.namespace, row.name]);
  const alertsBlock = useInspectorAlerts(cluster, alertTarget);
  const { namespace, name } = row;
  const [tab, setTab] = useState<"overview" | "manifest">("overview");
  const [pending, setPending] = useState<Pending | null>(null);
  const [forwarding, setForwarding] = useState(false);
  const [describing, setDescribing] = useState(false);

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

  const detailError = detail?.error || null;
  const forObject = detail && !detail.error && detail.summary.uid === uid ? detail : null;
  const detailOfOther = !!detail && !detail.error && !forObject;
  // the last detail read for this object, by UID - when the pod leaves the
  // read, the re-read fails or finds another object, it is the object's last
  // evidence, said as such; never a recreation's
  const lastDetail = useRef<{ uid: string; d: PodDetailDTO } | null>(null);
  if (forObject) lastDetail.current = { uid, d: forObject };
  const shown = forObject ?? (lastDetail.current?.uid === uid ? lastDetail.current.d : null);
  const lastEvidence = !!gone || !!detailError || detailOfOther;
  const readAt = shown?.asOfUnix ? new Date(shown.asOfUnix * 1000).toLocaleTimeString() : null;

  // A removed pod keeps its identity and last evidence; no operation targets
  // it. A pending verb is dropped when its pod (or, for a restart, its owner)
  // is no longer the armed object - removal also closes the forward popover.
  const live = gone !== "removed";
  const pendingLive = (pd: Pending) => live && pd.summary.uid === row.uid && (pd.verb !== "restart" || pd.summary.ownerUid === row.ownerUid);
  useEffect(() => {
    if (pending && !pendingLive(pending)) setPending(null);
    if (!live) setForwarding(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, row.uid, row.ownerUid, pending]);
  const p = row;
  // The row carries no verdict message and no container detail (addendum
  // §3.2): both are this UID's detail - "loading" until it is read, and the
  // message only while the detail's verdict is the row's.
  const sameVerdict = !!shown && kindOf(shown.summary.health) === kindOf(p.health) && labelOf(shown.summary.health) === labelOf(p.health)
    && (shown.summary.health?.reason ?? "") === (p.health?.reason ?? "");
  const message = sameVerdict ? shown?.summary.health?.message : undefined;
  const detailContainers = new Map((shown?.summary.containers ?? []).map((c) => [c.name, c]));
  const detailState: "read" | "loading" | "unread" = shown ? "read" : loading ? "loading" : "unread";
  const pendingMessage = sameVerdict ? undefined : loading ? "Reading the verdict message…" : detailError ? undefined : "Verdict message not read.";
  const containers = p.containers.filter((c) => !c.init);
  const per = (fn: (container: string) => void) => containers.map((c) => ({ label: c.name, onSelect: () => fn(c.name) }));
  const showRestart = p.ownerKind === "Deployment" || p.ownerKind === "StatefulSet" || p.ownerKind === "DaemonSet";
  const manifestRef = wellKnownResourceRef("", "Pod");
  // a pod under a Deployment shows its ReplicaSet until owner resolution reads
  const ownerUnresolved = p.ownerKind === "ReplicaSet" && rsEv !== "complete";

  const more: MenuItem[] = [
    ...(showRestart ? [{ label: `Restart ${p.ownerKind.toLowerCase()} ${p.ownerName}`, onSelect: () => setPending({ verb: "restart", summary: p }) }] : []),
    { label: "Port-forward…", onSelect: () => setForwarding(true) },
    { kind: "choose", label: "Copy kubectl exec command", options: per((c) => void copyExecCommand(cluster, namespace, name, c).then((r) => useFleet.getState().setActionStatus(r === "copied" ? { kind: "success", message: "kubectl exec command copied" } : { kind: "error", message: "could not copy the exec command" }))) },
    { kind: "choose", label: "Debug shell (ephemeral container)", hint: "kubectl debug adds an ephemeral container (image and profile chosen next); it stays until the pod is recreated", options: per((c) => setPending({ verb: "debug", summary: p, container: c })) },
    { label: "Describe", onSelect: () => setDescribing(true) },
    ...(manifestRef ? [{ label: "Edit manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(namespace, name); } }] : []),
    { kind: "separator" },
    { label: "Delete pod", danger: true, onSelect: () => setPending({ verb: "delete", summary: p }) },
  ];

  const meta = (
    <>
      {p.node && (
        <button className="t-identifier" style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", fontSize: 11 }}
          onClick={() => { useFleet.getState().setSection("nodes"); useFleet.getState().requestNavIntent({ kind: "select-node", cluster, node: p.node }); }}
          title="open in Nodes">{p.node}</button>
      )}
      {p.node ? " · " : ""}{ageText(p, nowS)} old{p.ip ? ` · ${p.ip}` : ""}
    </>
  );

  const failedConditions = shown?.conditions.filter((c) => c.status !== "True").length ?? 0;

  return (
    <div data-testid="pod-detail-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: uid || undefined }} replaced={replaced}
        kind="Pod"
        namespace={namespace}
        name={name}
        meta={meta}
        onClose={onClose}
        verdict={gone === "removed" ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">Pod removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
          </div>
        ) : gone === "unverified" ? (
          <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <span className="t-status">Not in the current Pod read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
          </div>
        ) : <VerdictStatus health={message ? { ...p.health, message } : p.health} detail={`${readyOf(p)} ready`}
          pendingMessage={pendingMessage} />}
        primary={live ? { label: "Logs", kbd: "l", onSelect: () => onOpenLogs({ namespace, name, containers: p.containers, uid: p.uid, createdUnix: p.createdUnix }) }
          : logHistory && p.uid ? { label: "Log history", onSelect: () => onOpenLogs({ namespace, name, containers: p.containers, uid: p.uid, createdUnix: p.createdUnix, gone: true, history: true }) }
          : undefined}
        secondary={live && containers.length > 0 ? { label: "Shell", onSelect: () => void openExecTerminal(cluster, namespace, name, containers[0].name), options: per((c) => void openExecTerminal(cluster, namespace, name, c)) } : undefined}
        more={[...(live ? more : []), ...ai.menu]}
        tab={tab}
        onTab={setTab}
        manifest={shown ? <YamlBlock text={shown.yaml} copyLabel="pod YAML" /> : null}
      >
        {ai.block}
        {alertsBlock}
        {forwarding && live && (
          <ForwardPopover
            cluster={cluster}
            namespace={namespace}
            kind="Pod"
            name={name}
            ports={[...detailContainers.values()].filter((c) => !c.init).flatMap((c) => (c.ports ?? []).map((pt) => ({ name: pt.name, port: pt.port })))}
            onClose={() => setForwarding(false)}
          />
        )}
        {describing && live && <DescribePane cluster={cluster} kindArg="pod" kind="Pod" namespace={namespace} name={name} expectUid={uid || undefined} onClose={() => setDescribing(false)} />}

        <InspectorBlock title="Containers" count={p.containers.length}>
          {ctUsage !== null && !ctUsage.available && (
            <div className="t-secondary">Live usage unavailable - {(ctUsage.message || "no metrics source").replace(/^metrics unavailable:\s*/, "")}</div>
          )}
          {p.containers.map((c) => <ContainerBlock key={c.name} c={c} d={detailContainers.get(c.name)} detailState={detailState} u={usageByContainer.get(c.name)} />)}
        </InspectorBlock>

        {uid && !gone && <PodIdentity cluster={cluster} namespace={namespace} name={name} uid={uid} active />}

        {shown && <PodResourcesBlock detail={shown} />}
        {shown && <PodExtendedBlock detail={shown} />}
        {shown && <PodClaimsBlock cluster={cluster} namespace={namespace} claims={shown.claims ?? []} health={shown.deviceHealth ?? []} unbound={!shown.summary.node && shown.summary.phase === "Pending"} />}
        {shown && <DebugContainersBlock detail={shown} />}

        <InspectorBlock title="Placement">
          <div className="insp-kv">
            <span>owner</span>
            {p.ownerKind ? (
              showRestart ? (
                <button style={{ background: "none", border: 0, padding: 0, textAlign: "left", cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}
                  onClick={() => { useFleet.getState().setSection("workloads"); useFleet.getState().requestNavIntent({ kind: "select-workload", cluster, workloadKind: p.ownerKind, namespace: p.namespace, name: p.ownerName }); }}>
                  {p.ownerKind} {p.namespace}/{p.ownerName}
                </button>
              ) : <span>{p.ownerKind} {p.ownerName}{ownerUnresolved ? <span data-testid="owner-unresolved" style={{ color: "var(--color-text-warning)" }}> - owner unresolved (ReplicaSet read {rsEv === "stale" ? "stale" : "unavailable"})</span> : null}</span>
            ) : <span>none - not recreated if deleted</span>}
            <span>node</span><span>{p.node || "not scheduled"}</span>
            <span>IP</span><span>{p.ip || "-"}</span>
            {shown?.qosClass && <><span>QoS</span><span>{shown.qosClass}</span></>}
            {shown?.serviceAccount && <><span>account</span><span>{shown.serviceAccount}</span></>}
          </div>
        </InspectorBlock>

        {!shown ? (
          detailError && !loading ? (
            <div role="alert" className="t-body" style={{ color: "var(--color-text-danger)" }}>Could not load pod detail: {detailError}</div>
          ) : (
            <div className="t-body" data-testid="pod-detail-unloaded" style={{ color: "var(--color-text-tertiary)" }}>
              {gone === "removed" ? "No detail was loaded before it was removed."
                : detailOfOther && !loading ? "Detail not loaded for this object - the last read found another object with this name."
                : "Loading pod detail…"}
            </div>
          )
        ) : (
          <>
            {readAt && (
              <span className="t-secondary" data-testid="pod-detail-asof" style={{ color: detailError || detailOfOther ? "var(--color-text-warning)" : undefined }}>
                {lastEvidence
                  ? `Last read ${readAt}${gone === "removed" ? " - before it was removed."
                    : detailError ? ` - the re-read failed: ${detailError}`
                    : detailOfOther ? ` - the latest read found another object with this name (UID ${detail?.summary.uid || "unknown"}, not ${uid}).`
                    : "."}`
                  : `Detail read ${readAt}`}
              </span>
            )}
            <InspectorBlock title="Recent events" count={shown.events.length}>
              <InspectorEvents events={shown.events} emptyText="No events for this pod." wrap />
            </InspectorBlock>

            {shown.conditions.length > 0 && (
              <InspectorBlock title="Conditions" count={shown.conditions.length} defaultOpen={false}
                extra={failedConditions > 0 ? <span className="t-secondary" style={{ color: "var(--color-text-danger)" }}>{failedConditions} not true</span> : undefined}>
                <InspectorConditions conditions={shown.conditions} />
              </InspectorBlock>
            )}
            <InspectorBlock title="Labels" count={Object.keys(shown.labels ?? {}).length} defaultOpen={false}>
              <Chips values={shown.labels} />
            </InspectorBlock>
            <InspectorBlock title="Annotations" count={Object.keys(shown.annotations ?? {}).length} defaultOpen={false}>
              <Chips values={shown.annotations} />
            </InspectorBlock>
          </>
        )}
      </InspectorLayout>

      {pending && pendingLive(pending) && pending.verb === "debug" && !pending.choice && (
        <DebugChooser cluster={cluster} target={pending.container} onCancel={() => setPending(null)}
          onContinue={(choice) => setPending({ ...pending, choice })} />
      )}
      {pending && pendingLive(pending) && (pending.verb !== "debug" || pending.choice) && (
        <ConfirmDialog
          title={pending.verb === "delete" ? "delete pod" : pending.verb === "debug" ? "debug shell" : "restart owner"}
          cluster={cluster}
          detail={pending.verb === "delete"
            ? (pending.summary.ownerKind === ""
              ? `${pending.summary.namespace}/${pending.summary.name} - this pod has no controller; it will NOT be recreated.`
              : `${pending.summary.namespace}/${pending.summary.name} - the controller will recreate it.`)
            : pending.verb === "debug" && pending.choice
              ? debugConfirmText(pending.summary.namespace, pending.summary.name, pending.container, pending.choice)
              : `${pending.summary.ownerKind} ${pending.summary.namespace}/${pending.summary.ownerName}`}
          // a privileged debug container takes a typed interlock even off a
          // protected cluster (S6 §6); a protected cluster's own lock covers it
          {...(pending.verb === "debug" && pending.choice?.profile === "sysadmin" && !isProtected
            ? { protected: true, cluster: "privileged", lockName: "privileged profile", showScope: false, arm: async () => ({ ok: true, error: "" }) }
            : { protected: isProtected })}
          danger
          confirmLabel={pending.verb === "delete" ? "Delete" : pending.verb === "debug" ? "Attach" : "Restart"}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const pd = pending;
            setPending(null);
            // Re-check at dispatch: the pod (or its owner) may have been
            // replaced or removed since the dialog opened.
            if (!pendingLive(pd)) {
              useFleet.getState().setActionStatus({ kind: "error", message: `pod ${pd.summary.namespace}/${pd.summary.name} was replaced or removed since the action was armed - nothing was sent` });
              return;
            }
            if (pd.verb === "delete") void deletePod(cluster, pd.summary.namespace, pd.summary.name, pd.summary.uid ?? "");
            else if (pd.verb === "debug") void openDebugTerminal(cluster, pd.summary.namespace, pd.summary.name, pd.summary.uid ?? "", pd.container, pd.choice?.image ?? "", pd.choice?.profile ?? "");
            // The backend resolves the controller chain into ownerKind/ownerName/ownerUid.
            else void rolloutRestart(cluster, pd.summary.ownerKind, pd.summary.namespace, pd.summary.ownerName, pd.summary.ownerUid ?? "");
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

export function LogsDock({ cluster, target, onClose, onPopOut }: {
  cluster: string;
  target: LogsTarget;
  onClose: () => void;
  onPopOut: (container: string) => void;
}) {
  const { height, handleProps } = useResizableDock();
  const [container, setContainer] = useState("");
  // coverage S8b: Live (the kube API) | History (Loki), History pinned to the
  // pod's UID; a gone pod has History only
  const logHistory = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.logHistoryPresent ?? false);
  const canHistory = logHistory && !!target.uid;
  const [mode, setMode] = useState<"live" | "history">(target.history || target.gone ? "history" : "live");
  const history = canHistory && mode === "history";
  return (
    <div data-testid="logs-dock" style={{ flexShrink: 0, height, position: "relative", borderTop: "0.5px solid var(--color-border-tertiary)", display: "flex", flexDirection: "column", background: "var(--color-background-primary)", fontFamily: "var(--font-mono)", fontSize: 12 }}>
      <div {...handleProps} />
      <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "4px 12px 4px 14px", borderBottom: "0.5px solid var(--color-border-tertiary)", flexShrink: 0 }}>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: 500 }}>
          <span style={{ color: "var(--color-text-tertiary)" }}>{target.namespace}</span>/{target.name}
        </span>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>logs</span>
        {canHistory && (
          <div role="group" aria-label="Logs source" className="l-seg" data-testid="logs-mode">
            <button className="l-chip" aria-pressed={!history} disabled={!!target.gone} title={target.gone ? "The pod is gone: no live logs" : "Live logs from the API server"}
              onClick={() => setMode("live")}>Live</button>
            <button className="l-chip" aria-pressed={history} onClick={() => setMode("history")} title="History from the cluster's Loki">History</button>
          </div>
        )}
        <div style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 4 }}>
          {history ? <button onClick={() => void openLogHistoryWindow(cluster, target.namespace, target.name, target.uid!, target.createdUnix, target.gone)}
            style={{ ...btn, padding: "2px 6px", fontSize: 12, display: "flex", alignItems: "center" }} aria-label="open log history in window" title="Open the log history in a separate window">
            <IconExternalLink size={13} stroke={1.5} />
          </button> : <button onClick={() => onPopOut(container)} style={{ ...btn, padding: "2px 6px", fontSize: 12, display: "flex", alignItems: "center" }} aria-label="open logs in window" title="Open logs in a separate window">
            <IconExternalLink size={13} stroke={1.5} />
          </button>}
          <button onClick={onClose} style={{ ...btn, padding: "2px 8px", display: "inline-flex", alignItems: "center" }} aria-label="close logs dock"><IconX size={13} stroke={1.5} /></button>
        </div>
      </div>
      <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", padding: "8px 12px" }}>
        {history ? (
          <LogHistoryPane key={`${target.namespace}/${target.name}/${target.uid}`} cluster={cluster}
            target={{ namespace: target.namespace, name: target.name, uid: target.uid!, createdUnix: target.createdUnix, gone: target.gone, containers: target.containers }} />
        ) : (
          <LogsPane key={`${target.namespace}/${target.name}`} cluster={cluster} pod={target} onContainerChange={setContainer} />
        )}
      </div>
    </div>
  );
}

// containerStateParts splits "running" | "waiting:<Reason>" | "terminated:<Reason>".
function containerStateParts(state: string): { word: string; reason: string } {
  const i = state.indexOf(":");
  return i < 0 ? { word: state, reason: "" } : { word: state.slice(0, i), reason: state.slice(i + 1) };
}

// ContainerBlock: the live state is the row's (c); the image, last
// termination and declared resources are the pod detail's (d), read for this
// UID - "loading" until then, never an empty claim.
function ContainerBlock({ c, d, detailState, u }: { c: PodRowContainerDTO; d?: ContainerSummaryDTO; detailState: "read" | "loading" | "unread"; u?: ContainerUsageDTO }) {
  const { word, reason } = containerStateParts(c.state);
  const findings = d ? [
    rightsizing("cpu", u?.cpu ?? null, d.cpuRequest, d.cpuLimit),
    rightsizing("mem", u?.mem ?? null, d.memRequest, d.memLimit),
  ].filter((f): f is NonNullable<typeof f> => f !== null) : [];
  const fmtRes = (v: number | null | undefined, format: (n: number) => string) => (v == null ? "—" : format(v));
  const faulted = word !== "" && word !== "running" && reason !== "Completed";
  const lastFinished = d?.lastFinishedUnix ?? 0;
  const lastAgo = lastFinished > 0 ? ago(Math.max(0, Math.floor(Date.now() / 1000) - lastFinished)) : "";
  return (
    <div data-rowhost="true" className="insp-kv">
      <span>{c.name}{c.init ? " (init)" : ""}</span>
      <span style={{ color: faulted ? "var(--color-text-danger)" : "var(--color-text-secondary)", fontWeight: faulted ? 600 : undefined }}>
        {word || "—"}{reason ? ` · ${reason}` : ""}
      </span>
      <span>image</span>
      {d ? (
        <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
          <span title={d.image} style={{ minWidth: 0, overflowWrap: "anywhere" }}>{imageShort(d.image)}</span>
          <CopyButton text={d.image} label={`copy image ref ${imageShort(d.image)}`} size={11} appearOnRowHover />
        </span>
      ) : <span data-testid="container-detail-pending" style={{ color: "var(--color-text-tertiary)" }}>{detailState === "loading" ? "loading…" : "not read"}</span>}
      {d?.lastReason && (
        <>
          <span>last exit</span>
          <span>
            {d.lastReason}{d.lastExitCode ? ` (exit ${d.lastExitCode})` : ""}
            {lastAgo && <span style={{ color: "var(--color-text-tertiary)" }}>{` · ${lastAgo} ago`}</span>}
          </span>
        </>
      )}
      <span>restarts</span><span>{c.restarts}</span>
      {u && (u.cpu != null || u.mem != null) && (
        <><span>usage</span><span>cpu {fmtRes(u.cpu, fmtCpu)} · mem {fmtRes(u.mem, fmtMem)}</span></>
      )}
      {d && (d.cpuRequest != null || d.memRequest != null || d.cpuLimit != null || d.memLimit != null) && (
        <><span>desired req / lim</span><span>cpu {fmtRes(d.cpuRequest, fmtCpu)}/{fmtRes(d.cpuLimit, fmtCpu)} · mem {fmtRes(d.memRequest, fmtMem)}/{fmtRes(d.memLimit, fmtMem)}</span></>
      )}
      {findings.map((f) => (
        <span key={f.label} style={{ gridColumn: "1 / -1", fontSize: 11, color: f.tier === "danger" ? "var(--color-text-danger)" : f.tier === "warn" ? "var(--color-text-warning)" : "var(--color-text-info)" }}>{f.label}</span>
      ))}
    </div>
  );
}
