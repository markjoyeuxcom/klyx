import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { formatAge } from "../chrome/Age";
import { LensParts } from "../chrome/LensParts";
import type { ReactNode } from "react";
import { useFleet } from "../store/fleet";
import type { EventRowDTO, FluxResourceDTO, FluxSourceDTO, InventoryEntryDTO, ResourceDetailDTO, RuleDTO } from "../store/fleet";
import { getResourceDetail, reconcile, reconcileWithSource, setSuspend, resolveGitLink, fluxDiff } from "../bridge/gitops";
import { FLUX_PART_KINDS, useFluxRows } from "../health/useFluxRows";
import { useNow } from "../chrome/Age";
import { clock, combineEvidence, coverageGaps, joinCoverage, kindEvidence, lastGoodAt, type JoinCoverage, type KindEvidence } from "../layout/coverage";
import type { FluxDiffDTO } from "../bridge/gitops";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { kindOfRef } from "../nav/reveal";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { IconAlertTriangle, IconGitBranch } from "@tabler/icons-react";
import { wellKnownResourceRef, FLUX_KIND_GROUPS } from "./resourceRefs";
import { StatusDot, ellipsis, type Tone } from "../chrome/primitives";
import { compactCount } from "../chrome/format";
import { InspectorConditions, InspectorStatus } from "./ResourceInspector";
import { ROW_MARK, VerdictLamp, VerdictMark } from "../health/VerdictView";
import { CountSentence } from "../layout/CountSentence";
import { SyncingChip, staleProps } from "./staleness";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useInspectorAI } from "../ai/AIExplain";
import { DependencyGraphSection } from "./DependencyGraph";
import { ClampedText } from "../chrome/ClampedText";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone, expectsOther } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

const keyOf = (r: { kind: string; namespace: string; name: string }) => `${r.kind}/${r.namespace}/${r.name}`;

const sourceKindLabel: Record<string, string> = {
  GitRepository: "git",
  OCIRepository: "oci",
  Bucket: "bucket",
  HelmRepository: "helmrepo",
  HelmChart: "chart",
};

function shortRev(rev: string): string {
  if (!rev) return "";
  const s = rev.replace(/^refs\/heads\//, "");
  const at = s.indexOf("@");
  if (at < 0) return s;
  const branch = s.slice(0, at);
  const sha = s.slice(at + 1).replace(/^sha1:/, "").replace(/^sha256:/, "");
  return `${branch}@${sha.slice(0, 7)}`;
}

// sinceOf ticks the age since the Ready transition from its absolute time
// (shared rows are pushed only when they change - slice 3 §3); the
// capture-relative age is the fallback for a payload without it.
const sinceOf = (r: FluxResourceDTO, nowS: number) => (r.readySinceUnix ? Math.max(0, nowS - r.readySinceUnix) : r.readySinceSeconds);

// ago is the shared compact age in prose ("5d ago"); a table cell says the
// age alone, as every lens does (slice 13b.4)
function ago(sec: number): string {
  return sec <= 0 ? "" : `${formatAge(sec)} ago`;
}


// Broken first. Suspension is checked BEFORE the (historical) Ready state: a
// deliberately paused resource keeps its last Failed condition, and must not
// outrank an active failure.
function rank(r: { ready: string; suspended: boolean }): number {
  if (r.suspended) return 3;
  if (r.ready === "Failed") return 0;
  if (r.ready === "Reconciling") return 1;
  if (r.ready !== "Ready") return 2;
  return 4;
}
const srcRank = rank;

// sourceText names the bound source; a cross-namespace source carries its
// namespace so two same-named sources never read as one.
function sourceText(r: FluxResourceDTO): string {
  const name = r.sourceNamespace && r.sourceNamespace !== r.namespace ? `${r.sourceNamespace}/${r.sourceName}` : r.sourceName;
  return [r.sourceKind, name].filter(Boolean).join(" ");
}

// boundTo: the authoritative consumer join - kind, resolved namespace and
// name. Without a resolved namespace there is no evidence, so no match.
function boundTo(r: FluxResourceDTO, s: FluxSourceDTO): boolean {
  return !!r.sourceNamespace && r.sourceKind === s.kind && r.sourceNamespace === s.namespace && r.sourceName === s.name;
}

function matchesQuery(r: FluxResourceDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    r.kind,
    r.namespace,
    r.name,
    r.message,
    r.revision,
    r.sourceKind,
    r.sourceName,
  ].some((v) => v.toLowerCase().includes(q));
}

// Flux lens on the workspace template (spec §6 step 4). Two destinations in
// one lens, per the IA: Reconciliation (the aggregate KS/HR view; its kind
// filter lives inside it) and Sources (source-type filter lives there). Flux
// vocabulary is kept: Ready / Reconciling / Failed, Suspended is an intended
// pause (never attention), and generation > observedGeneration reads
// "Syncing…" - never a confident Ready. Primary verb Reconcile, secondary
// Suspend/Resume (spec §3 defaults); both open their existing confirm flow.

type FluxView = "reconciliation" | "sources";
type StatusFilter = "all" | "attention" | "suspended";
type KindFilter = "" | "Kustomization" | "HelmRelease";
type PinnedVerb =
  | { cluster: string; kind: "reconcile"; r: FluxResourceDTO }
  | { cluster: string; kind: "suspend" | "resume"; r: FluxResourceDTO };

const FLUX_COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "kind", label: "Kind", width: 100, priority: 2 },
  { id: "name", label: "Resource", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 200, priority: 1 },
  { id: "source", label: "Source", width: 170, priority: 3 },
  { id: "revision", label: "Revision", width: 130, priority: 4 },
  { id: "since", label: "Since", width: 84, priority: 5 },
];
const SOURCE_COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "kind", label: "Type", width: 72, priority: 1 },
  { id: "name", label: "Source", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 200, priority: 1 },
  { id: "revision", label: "Revision", width: 150, priority: 2 },
];

// The verdict is the relocated rule computed in Go (internal/gitops/flux
// rules.go, 1c addendum §2): rule.included is the attention filter (not
// suspended and not Ready - the legacy count, which includes progressing),
// rule.role/label the presentation (Suspended = paused, never attention).
const roleOf = (x: { rule: RuleDTO }) => ({ kind: x.rule.role, label: x.rule.label });
const included = (x: { rule: RuleDTO }) => x.rule.included;
// faultsOf: the counted rows that are faults - the rows' reading, used only
// where no summary count is current (slice 11b review).
const faultsOf = (xs: { rule: RuleDTO }[]) => xs.filter((x) => x.rule.included && x.rule.role === "fault").length;

const STATUS_COLOR: Record<string, string> = {
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  progressing: "var(--color-text-secondary)",
  unknown: "var(--color-text-tertiary)",
  paused: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)",
};


const ellipsisCell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };

export function GitOps({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.gitopsTier ?? "Unknown");
  const gitops = useFleet((s) => s.gitops);
  const expand = useFleet((s) => s.expand);
  const collapse = useFleet((s) => s.collapse);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  // The view is the route's (the navigation band's Reconciliation / Sources
  // destinations), so it survives restore and history like any destination.
  const view: FluxView = useFleet((s) => (s.route.name === "cluster" && s.route.fluxView === "sources" ? "sources" : "reconciliation"));
  const setView = useCallback((v: FluxView) => useFleet.getState().openFlux(v), []);
  const [status, setStatus] = useState<StatusFilter>("all");
  const [kindFilter, setKindFilter] = useState<KindFilter>("");
  const [sourceKind, setSourceKind] = useState("");
  const [query, setQuery] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const [pinned, setPinnedState] = useState<PinnedVerb | null>(null);
  const [sourceKey, setSourceKey] = useClusterKey(cluster);
  const sourceListRef = useRef<VirtualListHandle>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const absent = tier === "Absent";

  // The data are the Observer's shared rows (1c slice 3 §5.3): the same
  // capture as the cluster summary's flux task, never a watch of the lens's own.
  useFluxRows(absent ? null : cluster);
  const nowS = Math.floor(useNow() / 1000);

  const mine = gitops.cluster === cluster;
  const rows = useMemo(() => (mine ? gitops.resources : []), [mine, gitops.resources]);
  const allSources = useMemo(() => (mine ? (gitops.sources ?? []) : []), [mine, gitops.sources]);
  const watchError = mine ? gitops.watchError : null;
  const loaded = mine && !gitops.loading;
  // Evidence per kind (layout/coverage.ts): removal, a verb's target and the
  // headlines read their OWN kinds - a live Kustomization read never vouches
  // for a failing HelmRelease or source read, or the reverse.
  const evidenceOf = useCallback((kind: string): KindEvidence => kindEvidence(loaded, gitops.coverage, kind, watchError), [loaded, gitops.coverage, watchError]);
  const recsEv = combineEvidence(FLUX_PART_KINDS.reconciliations.map(evidenceOf));
  const srcEv = combineEvidence(FLUX_PART_KINDS.sources.map(evidenceOf));
  // A4 (slice 3 §4): past the deadline the rows are evidence as of their
  // capture, and the totals are the summary's until a re-read lands.
  const rowsRefreshing = mine && !!gitops.rowsState?.refreshing;
  const rowsAsOf = gitops.rowsState?.evaluatedAt ? clock(gitops.rowsState.evaluatedAt) : "the last read";
  const globalCounts = mine ? gitops.partCounts : undefined;
  const lastGood = mine ? lastGoodAt(gitops.coverage, [...FLUX_PART_KINDS.reconciliations, ...FLUX_PART_KINDS.sources]) : null;
  const discoveryGap = mine ? coverageGaps(gitops.coverage ?? []) : null;

  // Pending verbs are pinned to the cluster + OBJECT they were raised on (its
  // UID, design §5b): dropped when the cluster changes, when a same-name
  // replacement takes the row, or when the target leaves a complete read of
  // its kind. The backend writes only that UID, so a replacement after the
  // last publication is refused there too.
  const setPinned = useCallback((v: Omit<PinnedVerb, "cluster"> | null) => setPinnedState(v ? ({ ...v, cluster } as PinnedVerb) : null), [cluster]);
  const targetLive = (v: PinnedVerb) => {
    if (v.cluster !== cluster) return false;
    const cur = rows.find((x) => keyOf(x) === keyOf(v.r));
    if (cur) return cur.uid === v.r.uid;
    return evidenceOf(v.r.kind) !== "complete";
  };
  // dispatch re-checks the armed object against the current rows
  const dispatchArmed = (v: PinnedVerb, send: (uid: string) => void) => {
    setPinnedState(null);
    if (!targetLive(v)) {
      useFleet.getState().setActionStatus({ kind: "error", message: `${v.r.kind} ${v.r.namespace}/${v.r.name} was replaced since the action was armed - nothing was sent` });
      return;
    }
    send(v.r.uid ?? "");
  };
  const verb = pinned && targetLive(pinned) ? pinned : null;
  useEffect(() => { if (pinned && !verb) setPinnedState(null); }, [pinned, verb]);

  // Palette verbs / cross-links: select the resource, then open its confirm.
  // A target not in the rows is missing only on a complete read of its own
  // kind; until then it waits (slice 4 review of 4d8b545 #1).
  useNavIntent(cluster, ["reconcile-flux", "suspend-flux", "select-flux"], (intent) => {
    if (!("fluxKind" in intent)) return "missing";
    const r = rows.find((x) => x.kind === intent.fluxKind && x.namespace === intent.namespace && x.name === intent.name);
    // An opening by UID (slice 9 note §4.2) selects at once, pinned to that
    // UID: a different or missing row is said, never substituted.
    if (intent.kind === "select-flux" && intent.uid) expectPin(cluster, "Flux", keyOf({ kind: intent.fluxKind, namespace: intent.namespace, name: intent.name }), intent.uid);
    if (intent.kind === "select-flux" && expectsOther(r, intent.uid)) {
      autoSelect.current = "done";
      const key = keyOf({ kind: intent.fluxKind, namespace: intent.namespace, name: intent.name });
      setView("reconciliation");
      setCursorKey(key);
      expand(key);
      return "consumed";
    }
    if (!r) return evidenceOf(intent.fluxKind) === "complete" ? "missing" : "wait";
    autoSelect.current = "done";
    setView("reconciliation");
    // Direct navigation wins over pre-existing filters; a later operator edit
    // that excludes it still closes the inspector.
    const passes = (!kindFilter || r.kind === kindFilter) && (status === "all" || (status === "attention" ? included(r) : r.suspended)) && matchesQuery(r, query);
    if (!passes) { setStatus("all"); setKindFilter(""); setQuery(""); }
    setCursorKey(keyOf(r));
    expand(keyOf(r));
    if (intent.kind === "reconcile-flux") setPinned({ kind: "reconcile", r });
    else if (intent.kind === "suspend-flux") setPinned({ kind: intent.suspend ? "suspend" : "resume", r });
    return "consumed";
  }, [rows, evidenceOf, kindFilter, status, query]);

  // A summary Top link to a source (health 1c slice 4 §5.4): select it in
  // the Sources view past the filters - selection only.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "flux" || intent.part !== "sources") return "wait";
    const kind = kindOfRef(intent.ref);
    const key = `${kind}/${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const found = allSources.find((x) => keyOf(x) === key);
    if (intent.ref.uid) expectPin(cluster, "FluxSource", key, intent.ref.uid);
    else if (!found) return evidenceOf(kind) === "complete" ? "missing" : "wait";
    setView("sources");
    setStatus("all"); setSourceKind(""); setQuery("");
    setCursorKey(key);
    setSourceKey(key);
    return "consumed";
  }, [allSources, evidenceOf]);

  const orderedRows = useMemo(
    () => [...rows].sort((a, b) => rank(a) - rank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [rows],
  );
  const visibleRows = useMemo(() => orderedRows.filter((r) => {
    if (kindFilter && r.kind !== kindFilter) return false;
    if (status === "attention" && !included(r)) return false;
    if (status === "suspended" && !r.suspended) return false;
    return matchesQuery(r, query);
  }), [orderedRows, kindFilter, status, query]);
  const orderedSources = useMemo(
    () => [...allSources].sort((a, b) => srcRank(a) - srcRank(b) || a.kind.localeCompare(b.kind) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [allSources],
  );
  const visibleSources = useMemo(() => orderedSources.filter((s) => {
    if (sourceKind && s.kind !== sourceKind) return false;
    if (status === "attention" && !included(s)) return false;
    if (status === "suspended" && !s.suspended) return false;
    const q = query.trim().toLowerCase();
    return !q || [s.kind, s.namespace, s.name, s.message, s.revision].some((v) => v.toLowerCase().includes(q));
  }), [orderedSources, sourceKind, status, query]);
  const openSource = useCallback((s: FluxSourceDTO) => { setCursorKey(keyOf(s)); setSourceKey(keyOf(s)); }, [setSourceKey]);
  const selectedSource = sourceKey ? allSources.find((x) => keyOf(x) === sourceKey) : undefined;
  // Pinned to the source's UID (The Line slice 4 §6.3): removed (or
  // replaced) only on a complete read of the source's own kind; otherwise
  // kept with its last row, unverified (slice 3 §5a).
  const sourcePin = usePinnedSubject(cluster, sourceKey, selectedSource, !!sourceKey && evidenceOf(sourceKey.split("/")[0]) === "complete", "FluxSource");
  const sourceReplaced = sourcePin.gone === "replaced";
  const sourceRemoved = sourcePin.gone === "removed" || sourceReplaced; // the inspector gates on removed
  const sourceUnverified = sourcePin.gone === "unverified";
  const sourceSubject = sourcePin.subject;
  useEffect(() => {
    if (selectedSource && !visibleSources.includes(selectedSource)) setSourceKey(null);
  }, [selectedSource, visibleSources, setSourceKey]);
  useEffect(() => { setSourceKey(null); }, [cluster, setSourceKey]);
  const byKey = useMemo(() => {
    const m = new Map<string, FluxResourceDTO>();
    for (const r of rows) m.set(keyOf(r), r);
    return m;
  }, [rows]);

  // Selection lifecycle (expandedKey lives in the store). A filter excluding
  // the selection closes it; a resource gone from a complete read is removed.
  const selKey = mine ? gitops.expandedKey : null;
  const selected = selKey ? byKey.get(selKey) : undefined;
  // Pinned to the resource's UID (The Line slice 4 §6.3): a same-name
  // resource with a new UID on a complete read is "replaced", never
  // silently followed or read into the pinned inspector.
  const fluxPin = usePinnedSubject(cluster, selKey, selected, !!selKey && evidenceOf(selKey.split("/")[0]) === "complete", "Flux");
  const replaced = fluxPin.gone === "replaced";
  const removed = fluxPin.gone === "removed" || replaced; // the inspector gates on removed
  const unverified = fluxPin.gone === "unverified";
  const subject = fluxPin.subject;
  const livePinned = fluxPin.gone ? undefined : selected;
  // Detail is evidence for the selection only with matching cluster AND key,
  // and - with a known UID - the same object: a same-name recreation's
  // detail never becomes the old row's, nor the old detail the new row's
  // (design §5b). A failed read is the request's outcome, shown apart.
  const forSelection = mine && gitops.detail && gitops.detailCluster === cluster && keyOf(gitops.detail) === selKey ? gitops.detail : null;
  const detailError = forSelection?.error || null;
  const sameObject = (d: ResourceDetailDTO) => !subject?.uid || d.uid === subject.uid;
  const liveDetail = forSelection && !forSelection.error && sameObject(forSelection) ? forSelection : null;
  const detailOfOther = !!forSelection && !forSelection.error && !liveDetail;
  // the last detail read for an object, by UID: kept for a removed row or a
  // failed re-read of the same object, never for a recreation
  const lastDetail = useRef<{ cluster: string; uid: string; d: ResourceDetailDTO } | null>(null);
  if (liveDetail?.uid) lastDetail.current = { cluster, uid: liveDetail.uid, d: liveDetail };
  const priorDetail = !liveDetail && (removed || detailError) && subject?.uid && lastDetail.current?.cluster === cluster && lastDetail.current.uid === subject.uid ? lastDetail.current.d : null;
  const shownDetail = liveDetail ?? priorDetail;
  useEffect(() => {
    if (selected && view === "reconciliation" && !visibleRows.includes(selected)) collapse();
  }, [selected, visibleRows, view, collapse]);
  useEffect(() => {
    if (!livePinned) return;
    void getResourceDetail(cluster, livePinned.kind, livePinned.namespace, livePinned.name);
    // re-read on every live emit of the pinned resource
  }, [cluster, livePinned]);

  // One-shot auto-select of the first attention row from a fresh read.
  const autoSelect = useRef<"pending" | "done">("pending");
  const entryRows = useRef<FluxResourceDTO[] | null>(null);
  if (entryRows.current === null) entryRows.current = gitops.resources;
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    autoSelect.current = "pending";
    entryRows.current = useFleet.getState().gitops.resources;
    setCursorKey(null);
  }, [cluster]);
  const freshRead = recsEv === "complete" && gitops.resources !== entryRows.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead) return;
    autoSelect.current = "done";
    if (useFleet.getState().gitops.expandedKey) return;
    const r = visibleRows.find(included);
    if (r) { setCursorKey(keyOf(r)); expand(keyOf(r)); }
  }, [freshRead, visibleRows, expand]);

  const openRow = useCallback((r: FluxResourceDTO) => { autoSelect.current = "done"; setCursorKey(keyOf(r)); expand(keyOf(r)); }, [expand]);
  const close = useCallback(() => { autoSelect.current = "done"; collapse(); }, [collapse]);
  // Each destination keeps only its own filters: entering Sources drops the
  // KS/HR kind filter and the open resource; entering Reconciliation drops
  // the source-type filter.
  const lastView = useRef(view);
  useEffect(() => {
    if (lastView.current === view) return;
    lastView.current = view;
    if (view === "sources") { setKindFilter(""); close(); } else setSourceKind("");
  }, [view, close]);
  const navRows: { key: string; open: () => void }[] = view === "sources"
    ? visibleSources.map((s) => ({ key: keyOf(s), open: () => openSource(s) }))
    : visibleRows.map((r) => ({ key: keyOf(r), open: () => openRow(r) }));
  const cursorIdx = cursorKey === null ? -1 : navRows.findIndex((n) => n.key === cursorKey);
  // r / s drive the SAME confirm flows the inspector opens - keys never
  // bypass a confirm. Inert while no row has the cursor.
  useListKeys({
    count: navRows.length,
    selected: cursorIdx,
    onSelect: (idx) => { const n = navRows[idx]; if (n) { setCursorKey(n.key); (view === "sources" ? sourceListRef : listRef).current?.scrollToIndex(idx); } },
    onActivate: (idx) => navRows[idx]?.open(),
    onEscape: () => { if (view === "sources") setSourceKey(null); else if (selKey) close(); },
    searchRef,
    extraKeys: view === "sources" ? {} : {
      r: (idx) => { const r = visibleRows[idx]; if (r) setPinned({ kind: "reconcile", r }); },
      s: (idx) => { const r = visibleRows[idx]; if (r) setPinned({ kind: r.suspended ? "resume" : "suspend", r }); },
    },
  });

  const columns = view === "sources" ? SOURCE_COLUMNS : FLUX_COLUMNS;
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  // global: the count selector's (slice 3 §4a); row-scoped: the breakdown,
  // filters and footer ("as of" while refreshing)
  const recTotal = globalCounts?.reconciliations?.total ?? rows.length;
  const attention = globalCounts?.reconciliations?.included ?? rows.filter(included).length;
  const srcTotal = globalCounts?.sources?.total ?? allSources.length;
  const srcAttention = globalCounts?.sources?.included ?? allSources.filter(included).length;
  // each header's mark from the reading that gave its count
  const recFault = globalCounts?.reconciliations ? globalCounts.reconciliations.fault : faultsOf(rows);
  const srcFault = globalCounts?.sources ? globalCounts.sources.fault : faultsOf(allSources);
  const ks = rows.filter((r) => r.kind === "Kustomization").length;
  const hr = rows.filter((r) => r.kind === "HelmRelease").length;
  const failed = rows.filter((r) => !r.suspended && r.ready === "Failed").length;
  const suspended = rows.filter((r) => r.suspended).length;
  const reconciling = rows.filter((r) => !r.suspended && r.ready === "Reconciling").length;
  const syncing = rows.filter((r) => !r.suspended && r.stale).length;
  const srcSuspended = allSources.filter((s) => s.suspended).length;
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const sourceKinds = [...new Set(allSources.map((s) => s.kind))].sort();

  const clearFilters = () => { setStatus("all"); setKindFilter(""); setSourceKind(""); setQuery(""); };
  // total: the rows in hand (dataset state, filters, footer); the headline
  // reads the global count
  const total = view === "sources" ? allSources.length : rows.length;
  const globalTotal = view === "sources" ? srcTotal : recTotal;
  const shown = view === "sources" ? visibleSources.length : visibleRows.length;
  const viewEv = view === "sources" ? srcEv : recsEv;
  let state: DatasetState = "ready";
  if (absent) state = "empty";
  else if (viewEv === "failed" && total === 0) state = "unreadable";
  else if (viewEv === "pending" && total === 0) state = "loading";
  else if (total === 0) state = "empty";
  else if (shown === 0) state = "no-match";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={7} label="loading reconciliation state" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Flux state could not be read." hint={watchError ?? undefined} />
    : state === "empty" ? (absent
      ? <EmptyState icon={<IconGitBranch size={28} stroke={1.2} />} title="Flux is not installed on this cluster." hint="Klyx detected no Flux controllers or CRDs here." />
      : rowsRefreshing && globalTotal > 0 ? <EmptyState icon={<IconGitBranch size={28} stroke={1.2} />} title={`No ${view === "sources" ? "Flux sources" : "Flux resources"} as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${globalTotal}.`} />
      : <EmptyState icon={<IconGitBranch size={28} stroke={1.2} />} title={view === "sources" ? "No Flux sources on this cluster." : "No Flux resources on this cluster."} hint={view === "sources" ? undefined : "Flux is installed but no Kustomizations or HelmReleases exist yet."} />)
    : <EmptyState icon={<IconGitBranch size={28} stroke={1.2} />} title={view === "sources" ? "No Flux sources match the current filters." : "No Flux resources match the current filters."} hint={`${total} ${view === "sources" ? "sources" : "resources"} on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // pending/failed = a lower bound ("+"); stale = the last observed set ("*")
  const partial = viewEv === "complete" ? "" : viewEv === "stale" ? "*" : "+";
  const notKnown = (ev: KindEvidence) => (ev === "stale" ? "none known not ready (last observed)" : "none known not ready");

  // Notices stack: each is its own evidence, none hides another.
  const statusLine = { minHeight: 28, display: "flex", alignItems: "center" } as const;
  const notices: ReactNode[] = [];
  if (watchError && total > 0) notices.push(<WatchErrorBanner key="watch" tool="Flux" failure={`watch failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={watchError} />);
  else if (viewEv === "pending" && total > 0) notices.push(<div key="pending" role="status" className="t-secondary" style={statusLine}>Still reading - this {view === "sources" ? "source" : "Kustomization / HelmRelease"} list is incomplete.</div>);
  if (rowsRefreshing && !absent) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{gitops.rowsState?.refreshError ? ` Last re-read failed: ${gitops.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap && !absent) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  const filterBits = [status !== "all" ? status : "", view === "reconciliation" && kindFilter ? (kindFilter === "Kustomization" ? "ks" : "hr") : "", view === "sources" && sourceKind ? (sourceKindLabel[sourceKind] ?? sourceKind) : "", query ? "search" : ""].filter(Boolean);
  const attnHere = view === "sources" ? srcAttention : attention;

  return (
    <>
      <LensLayout
        label="Flux"
        title="Flux CD"
        part={view === "sources" ? "flux-sources" : "flux"}
        summary={state === "loading" || state === "unreadable" || absent ? null : view === "sources" ? (
          <>
            <CountSentence total={srcTotal} marker={partial} noun={["source", "sources"]}
              attention={{ count: srcAttention, phrase: "not ready", fault: srcFault > 0 }}
              clear={srcEv !== "complete" ? notKnown(srcEv) : srcSuspended > 0 ? "all active sources ready" : "all sources ready"} />
            {srcSuspended > 0 && <span className="t-secondary">{srcSuspended} suspended{asOf}</span>}
          </>
        ) : (
          <>
            {/* "not ready", not "need attention": this legacy count still
                includes neutral progressing/stale/unknown rows until the shared
                Flux health slice aligns backend summary, tape and lens together. */}
            <CountSentence total={recTotal} marker={partial} noun={["reconciliation", "reconciliations"]}
              attention={{ count: attention, phrase: "not ready", fault: recFault > 0 }}
              clear={recsEv !== "complete" ? notKnown(recsEv) : "all ready"} />
            <span className="t-secondary" data-testid="flux-breakdown">
              {[`${ks} Kustomization${ks === 1 ? "" : "s"} · ${hr} HelmRelease${hr === 1 ? "" : "s"}`, failed ? `${failed} failed` : "", reconciling ? `${reconciling} reconciling` : "", syncing ? `${syncing} syncing` : "", suspended ? `${suspended} suspended` : ""].filter(Boolean).join(" · ")}{asOf}
            </span>
          </>
        )}
        controls={absent ? undefined : (
          <>
            <LensParts cluster={cluster} groupId="flux" />
            {view === "reconciliation" ? (
              <label style={{ display: "flex" }}>
                <span className="sr-only">Kind</span>
                <select className="l-input" value={kindFilter} onChange={(e) => setKindFilter(e.target.value as KindFilter)}>
                  <option value="">All kinds ({rows.length})</option>
                  <option value="Kustomization">Kustomizations ({ks})</option>
                  <option value="HelmRelease">HelmReleases ({hr})</option>
                </select>
              </label>
            ) : (
              <label style={{ display: "flex" }}>
                <span className="sr-only">Source type</span>
                <select className="l-input" value={sourceKind} onChange={(e) => setSourceKind(e.target.value)}>
                  <option value="">All source types ({allSources.length})</option>
                  {sourceKinds.map((k) => <option key={k} value={k}>{k} ({allSources.filter((s) => s.kind === k).length})</option>)}
                </select>
              </label>
            )}
            <div role="group" aria-label="Status" className="l-seg">
              <button className="l-chip" aria-pressed={status === "all"} onClick={() => setStatus("all")}>All</button>
              <button className="l-chip" aria-pressed={status === "attention"} onClick={() => setStatus("attention")}>
                Not ready {attnHere > 0 && <span style={{ color: "var(--color-text-warning)" }}>{attnHere}</span>}
              </button>
              {(view === "sources" ? srcSuspended : suspended) > 0 && (
                <button className="l-chip" aria-pressed={status === "suspended"} onClick={() => setStatus("suspended")}>
                  Suspended <span style={{ color: "var(--color-text-tertiary)" }}>{view === "sources" ? srcSuspended : suspended}</span>
                </button>
              )}
            </div>
            <label className="lens-search">
              <span className="sr-only">Search Flux</span>
              <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={view === "sources" ? "Search sources" : "Search name, message, revision"} />
            </label>
          </>
        )}
        notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
        state={state}
        stateView={stateView}
        body={view === "sources" ? (
          <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
            <div className="lens-table-head" role="row" style={gridStyle}>
              {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
            </div>
            <VirtualList
              ref={sourceListRef}
              items={visibleSources}
              rowHeight={TABLE_ROW_HEIGHT}
              rowKey={(s) => keyOf(s)}
              style={{ flex: 1, minHeight: 0 }}
              render={(s) => (
                <div
                  key={keyOf(s)}
                  role="button"
                  tabIndex={0}
                  className="lens-row"
                  data-testid="flux-source-row"
                  aria-selected={keyOf(s) === cursorKey}
                  data-cursor={keyOf(s) === cursorKey ? "true" : undefined}
                  data-selected={keyOf(s) === sourceKey ? "true" : undefined}
                  style={gridStyle}
                  onClick={() => (keyOf(s) === sourceKey ? setSourceKey(null) : openSource(s))}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openSource(s); } }}
                >
                  {show("mark") && <VerdictMark health={roleOf(s)} />}
                  {show("kind") && <span style={{ color: "var(--color-text-tertiary)" }}>{sourceKindLabel[s.kind] ?? s.kind}</span>}
                  {show("name") && <span className="lens-cell-name" title={`${s.namespace}/${s.name}`}><span style={{ color: "var(--color-text-tertiary)" }}>{s.namespace} / </span>{s.name}</span>}
                  {show("status") && (
                    <span style={{ ...ellipsisCell, color: STATUS_COLOR[roleOf(s).kind], fontFamily: "var(--font-sans)" }}>
                      {roleOf(s).label}{included(s) && s.message ? ` · ${s.message}` : ""}
                    </span>
                  )}
                  {show("revision") && <span style={{ ...ellipsisCell, color: "var(--color-text-secondary)" }} title={s.revision}>{shortRev(s.revision) || "-"}</span>}
                </div>
              )}
            />
          </LensTable>
        ) : (
          <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
            <div className="lens-table-head" role="row" style={gridStyle}>
              {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
            </div>
            <VirtualList
              ref={listRef}
              items={visibleRows}
              rowHeight={TABLE_ROW_HEIGHT}
              rowKey={(r) => keyOf(r)}
              style={{ flex: 1, minHeight: 0 }}
              render={(r) => {
                const k = keyOf(r);
                const h = roleOf(r);
                return (
                  <div
                    key={k}
                    role="button"
                    tabIndex={0}
                    className="lens-row"
                    aria-selected={k === cursorKey}
                    data-cursor={k === cursorKey ? "true" : undefined}
                    data-selected={k === selKey ? "true" : undefined}
                    {...staleProps(!r.suspended && r.stale)}
                    style={gridStyle}
                    onClick={() => (k === selKey ? close() : openRow(r))}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openRow(r); } }}
                  >
                    {show("mark") && <VerdictMark health={roleOf(r)} stale={!r.suspended && r.stale} />}
                    {show("kind") && <span style={{ ...ellipsisCell, color: "var(--color-text-tertiary)", fontFamily: "var(--font-sans)" }} title={r.kind}>{r.kind}</span>}
                    {show("name") && (
                      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                        <span className="lens-cell-name" title={`${r.namespace}/${r.name}`}>
                          <span style={{ color: "var(--color-text-tertiary)" }}>{r.namespace} / </span>
                          <span style={{ fontWeight: k === selKey ? 600 : 400 }}>{r.name}</span>
                        </span>
                        <CopyButton text={r.name} label={`copy name ${r.name}`} appearOnRowHover />
                      </span>
                    )}
                    {show("status") && (
                      <span style={ellipsisCell} title={included(r) && r.message ? r.message : undefined}>
                        {h.kind === "paused" ? <span style={{ color: "var(--color-text-secondary)" }}>{h.label}</span>
                          : h.label === "Syncing…" ? <SyncingChip generation={r.generation} observedGeneration={r.observedGeneration} />
                          : <span style={{ color: STATUS_COLOR[h.kind], fontFamily: "var(--font-sans)" }}>
                              {h.label}{included(r) && r.reason ? ` · ${r.reason}` : ""}
                            </span>}
                      </span>
                    )}
                    {show("source") && <span style={{ ...ellipsisCell, color: "var(--color-text-secondary)" }} title={sourceText(r)}>{sourceText(r) || "-"}</span>}
                    {show("revision") && <span style={{ ...ellipsisCell, color: "var(--color-text-secondary)" }} title={r.revision}>{shortRev(r.revision) || "-"}</span>}
                    {show("since") && <span style={{ color: "var(--color-text-tertiary)" }} title={ago(sinceOf(r, nowS)) || undefined}>{sinceOf(r, nowS) > 0 ? formatAge(sinceOf(r, nowS)) : "-"}</span>}
                  </div>
                );
              }}
            />
          </LensTable>
        )}
        footer={<span className="t-legend">{view === "sources" ? "j k navigate · ↵ inspect · / search" : "j k navigate · ↵ inspect · r reconcile · s suspend · / search"}</span>}
        footerEnd={
          <>
            <span className="t-secondary">{shown} of {total}{partial} {view === "sources" ? "sources" : "resources"}{asOf} · broken first{filterBits.length ? ` · filter: ${filterBits.join(", ")}` : ""}</span>
            {filterBits.length > 0 && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all</button>}
          </>
        }
        inspector={view === "sources" ? (sourceKey && sourceSubject ? (
          <SourceInspector
            cluster={cluster}
            key={`${cluster}/${sourceKey}`}
            source={sourceSubject}
            removed={sourceRemoved}
            unverified={sourceUnverified}
            replaced={sourceReplaced ? sourcePin.adopt : undefined}
            usedBy={rows.filter((r) => boundTo(r, sourceSubject))}
            usageCov={{ Kustomization: joinCoverage(evidenceOf("Kustomization")), HelmRelease: joinCoverage(evidenceOf("HelmRelease")) }}
            onClose={() => setSourceKey(null)}
            onOpenResource={(r) => { setView("reconciliation"); setSourceKey(null); openRow(r); }}
          />
        ) : sourceKey && sourcePin.expected && sourcePin.gone ? (
          <PinnedGone cluster={cluster} kind={sourceKey.split("/")[0]} namespace={sourceKey.split("/")[1]} name={sourceKey.split("/").slice(2).join("/")} uid={sourcePin.expected} gone={sourcePin.gone}
            onAdopt={sourcePin.next ? sourcePin.adopt : undefined} onClose={() => setSourceKey(null)} />
        ) : undefined) : view === "reconciliation" && selKey && subject ? (
          <FluxInspector
            key={`${cluster}/${selKey}`}
            cluster={cluster}
            resource={subject}
            nowS={nowS}
            removed={removed}
            unverified={unverified}
            replaced={replaced ? fluxPin.adopt : undefined}
            isProtected={isProtected}
            detail={shownDetail}
            detailPrior={shownDetail !== null && (removed || shownDetail === priorDetail)}
            detailError={detailError}
            detailOfOther={detailOfOther}
            onClose={close}
            onReconcile={(r) => setPinned({ kind: "reconcile", r })}
            onToggleSuspend={(r, isSuspended) => setPinned({ kind: isSuspended ? "resume" : "suspend", r })}
          />
        ) : view === "reconciliation" && selKey && fluxPin.expected && fluxPin.gone ? (
          <PinnedGone cluster={cluster} kind={selKey.split("/")[0]} namespace={selKey.split("/")[1]} name={selKey.split("/").slice(2).join("/")} uid={fluxPin.expected} gone={fluxPin.gone}
            onAdopt={fluxPin.next ? fluxPin.adopt : undefined} onClose={close} />
        ) : undefined}
        inspectorLabel={view === "sources" ? "Flux source details" : "Flux resource details"}
        onCloseInspector={view === "sources" ? () => setSourceKey(null) : close}
      />

      {verb && (verb.kind === "suspend" || verb.kind === "resume") && (
        <ConfirmDialog
          title={verb.kind === "suspend" ? "Suspend reconciliation" : "Resume reconciliation"}
          cluster={verb.cluster}
          detail={`${verb.r.kind} ${verb.r.namespace}/${verb.r.name}`}
          protected={isProtected}
          danger={verb.kind === "suspend"}
          confirmLabel={verb.kind === "suspend" ? "Suspend" : "Resume"}
          onCancel={() => setPinnedState(null)}
          onConfirm={() => {
            const { cluster: target, kind, r } = verb;
            dispatchArmed(verb, (uid) => void setSuspend(target, r.kind, r.namespace, r.name, uid, kind === "suspend"));
          }}
        />
      )}
      {verb && verb.kind === "reconcile" && (
        <ReconcileDialog
          cluster={verb.cluster}
          resource={verb.r}
          isProtected={isProtected}
          onCancel={() => setPinnedState(null)}
          onConfirm={(withSource) => {
            const { cluster: target, r } = verb;
            dispatchArmed(verb, (uid) => void (withSource ? reconcileWithSource(target, r.kind, r.namespace, r.name, uid) : reconcile(target, r.kind, r.namespace, r.name, uid)));
          }}
        />
      )}
    </>
  );
}

// SourceInspector - a source's full diagnosis (wrapped message, URL,
// revision) and the resources bound to it, which is how a source failure
// reaches the operator's attention. Manifest opens the guarded editor.
function SourceInspector({ cluster, source, removed, unverified, replaced, usedBy, usageCov, onClose, onOpenResource }: {
  cluster: string;
  source: FluxSourceDTO;
  removed: boolean;
  unverified: boolean;
  replaced?: () => void;
  usedBy: FluxResourceDTO[];
  // the consumer join is qualified by the reconciliation kinds it reads:
  // unknown is unverified (never zero), stale is last observed
  usageCov: Record<"Kustomization" | "HelmRelease", JoinCoverage>;
  onClose: () => void;
  onOpenResource: (r: FluxResourceDTO) => void;
}) {
  const ai = useInspectorAI(source.uid ? { kind: "row" as const, task: "flux", part: "sources", uid: source.uid } : null, `${source.kind}/${source.namespace}/${source.name}`, !removed && !unverified, cluster);
  const s = source;
  const h = roleOf(s);
  const usage = usageQualifier(usageCov);
  const ref = wellKnownResourceRef(FLUX_KIND_GROUPS[s.kind] ?? "", s.kind);
  const view = ref ? () => { const nav = useFleet.getState(); nav.openResource(ref); nav.openInstance(s.namespace, s.name); } : null;
  return (
    <div data-testid="flux-source-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: s.uid || undefined }} replaced={replaced}
        kind={s.kind}
        namespace={s.namespace}
        name={s.name}
        meta={[sourceKindLabel[s.kind] ?? s.kind, s.url].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={removed ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">{s.kind} removed.</span> It is no longer in the current read. The details below are its last evidence.
          </div>
        ) : unverified ? <UnverifiedNotice kind={s.kind} /> : (
          <div>
            <InspectorStatus tone={roleTone(h.kind)} kind={h.kind} label={h.label} reason={included(s) ? s.reason || undefined : undefined} detail={shortRev(s.revision) || undefined} />
            {s.message && included(s) && (
              <div className="verdict-message" style={{ overflowWrap: "anywhere" }}>{s.message}</div>
            )}
          </div>
        )}
        primary={view ? { label: "View manifest in Resources", onSelect: view } : undefined}
        more={ai.menu}
      >
        {ai.block}
        <InspectorBlock title="Used by" count={usage.mark ? undefined : usedBy.length} extra={usage.mark ? <span className="t-secondary" data-testid="used-by-count">{usage.allUnknown ? "?" : `${usedBy.length}${usage.mark}`}</span> : undefined}>
          {usage.note && <Muted>{usage.note}</Muted>}
          {usedBy.length === 0 ? (usage.allUnknown ? null : <Muted>{usage.unknown.length ? `None among the ${usage.read.join(" and ")} read.` : usage.stale.length ? `None in the last observed ${usage.stale.join(" and ")} list.` : "No Kustomization or HelmRelease in this cluster names this source."}</Muted>) : (
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              {usedBy.map((r) => (
                <button key={keyOf(r)} onClick={() => onOpenResource(r)} className="t-identifier"
                  style={{ display: "flex", gap: 8, alignItems: "center", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)" }}>
                  <VerdictLamp health={roleOf(r)} size={7} />
                  <span style={{ overflowWrap: "anywhere" }}>{r.kind === "Kustomization" ? "ks" : "hr"} {r.namespace}/{r.name}</span>
                </button>
              ))}
            </div>
          )}
        </InspectorBlock>
        <InspectorBlock title="Artifact">
          <div className="insp-kv">
            <span>revision</span><span className="t-identifier" style={{ overflowWrap: "anywhere" }}>{s.revision || "-"}</span>
            <span>url</span><span className="t-identifier" style={{ overflowWrap: "anywhere" }}>{s.url || "-"}</span>
            <span>state</span><span>{s.suspended ? "suspended (intended pause)" : s.ready}</span>
          </div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}

// usageQualifier qualifies a source's consumer count by the reconciliation
// kinds behind it (Codex review of 7d389eb #2): a kind without a read makes
// the count a lower bound ("+") and the usage unverified, never zero; a
// stale kind makes it last observed ("*").
function usageQualifier(cov: Record<string, JoinCoverage>) {
  const kinds = Object.keys(cov);
  const plural = (k: string) => `${k}s`;
  const unknown = kinds.filter((k) => cov[k] === "unknown");
  const stale = kinds.filter((k) => cov[k] === "stale");
  const read = kinds.filter((k) => cov[k] !== "unknown").map(plural);
  const allUnknown = unknown.length === kinds.length;
  const mark = unknown.length ? "+" : stale.length ? "*" : "";
  const note = allUnknown ? `${unknown.join(" and ")} reads unavailable - usage unverified, not zero.`
    : unknown.length ? `${unknown.join(" and ")} read unavailable - usage unverified beyond the ${read.join(" and ")}.`
    : stale.length ? `From the last observed ${stale.join(" and ")} list.` : "";
  return { unknown, stale, read, allUnknown, mark, note };
}

// sinceWord: readySinceSeconds is the Ready condition's LAST TRANSITION, so
// its meaning follows the current state - "ready since" only when Ready.
function sinceWord(r: FluxResourceDTO): string {
  // Ready's lastTransitionTime says nothing about when spec.suspend changed.
  if (r.suspended) return "last observed Ready condition change";
  if (r.ready === "Ready") return "ready";
  if (r.ready === "Failed") return "failing";
  return "in this state";
}

// FluxInspector - investigation order: the Flux verdict with the controller's
// full message; Reconcile (primary), Suspend/Resume (secondary), More (view in
// Git, manifest); then the bound source (the usual root cause), dependsOn
// blockers, drift/events, conditions, inventory, and the gated live diff.
function FluxInspector({ cluster, resource, nowS, removed, unverified, replaced, isProtected, detail, detailPrior, detailError, detailOfOther, onClose, onReconcile, onToggleSuspend }: {
  cluster: string;
  resource: FluxResourceDTO;
  nowS: number;
  removed: boolean;
  unverified: boolean;
  replaced?: () => void;
  isProtected: boolean;
  detail: ResourceDetailDTO | null;
  // detailPrior: the detail is this object's last good read, not the current one
  detailPrior: boolean;
  detailError: string | null;
  // detailOfOther: the last detail read was of an earlier object with this name
  detailOfOther: boolean;
  onClose: () => void;
  onReconcile: (r: FluxResourceDTO) => void;
  onToggleSuspend: (r: FluxResourceDTO, suspended: boolean) => void;
}) {
  const ai = useInspectorAI(resource.uid ? { kind: "row" as const, task: "flux", part: "reconciliations", uid: resource.uid } : null, `${resource.kind}/${resource.namespace}/${resource.name}`, !removed && !unverified, cluster);
  const r = resource;
  const role = roleOf(r);
  // A generation lag is evidence about the last observation, shown under the
  // verdict; it is never a verdict of its own.
  const lagging = !r.suspended && r.stale;
  const live = !removed;
  const isSuspended = detail?.suspended ?? r.suspended;
  const manifestRef = wellKnownResourceRef(FLUX_KIND_GROUPS[r.kind] ?? "", r.kind);
  const viewManifest: MenuItem | null = manifestRef
    ? { label: "View manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(manifestRef.scope === "Namespaced" ? r.namespace : "", r.name); } }
    : null;
  const more: MenuItem[] = [
    ...(live && r.kind === "Kustomization" ? [{ label: "View in Git", onSelect: () => void resolveGitLink(cluster, r.kind, r.namespace, r.name) }] : []),
    ...(viewManifest ? [viewManifest] : []),
  ];
  // M10-f: a real diff only helps when Flux isn't auto-healing (suspended) or
  // is stuck (apply-failing); a healthy resource's diff is empty/misleading.
  const canDiff = live && !!detail && r.kind === "Kustomization" && (detail.suspended || r.ready === "Failed");

  const verdict = removed ? (
    <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
      <span className="t-status">{r.kind} removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
    </div>
  ) : unverified ? <UnverifiedNotice kind={r.kind} /> : (
    <div>
      {/* The headline is the Go rule's verdict; the generation lag and the
          controller's own words below it are secondary evidence. */}
      <InspectorStatus
        tone={roleTone(role.kind)}
        kind={role.kind}
        label={role.label}
        reason={lagging ? `generation ${r.generation} not yet observed (observed ${r.observedGeneration})` : included(r) ? r.reason || undefined : undefined}
        detail={lagging ? undefined : shortRev(r.revision) || undefined}
      />
      {lagging && r.message && (
        <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>
          <span className="t-section-label">Last observed (generation {r.observedGeneration}): </span>{r.ready}{r.reason ? ` · ${r.reason}` : ""} - {r.message}
        </div>
      )}
      {r.message && included(r) && !lagging && (
        <ClampedText key={r.message} className="verdict-message" text={r.message} style={{ overflowWrap: "anywhere" }} />
      )}
    </div>
  );

  return (
    <div data-testid="flux-detail-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: r.uid || undefined }} replaced={replaced}
        kind={r.kind}
        namespace={r.namespace}
        name={r.name}
        meta={
          <>
            {isProtected && <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>protected · </span>}
            {[sourceText(r) ? `source ${sourceText(r)}` : "", sinceOf(r, nowS) > 0 ? (r.suspended ? `${sinceWord(r)} ${ago(sinceOf(r, nowS))}` : `${sinceWord(r)} since ${ago(sinceOf(r, nowS))}`) : ""].filter(Boolean).join(" · ") || "Flux"}
          </>
        }
        onClose={onClose}
        verdict={verdict}
        drawing={r.uid && !removed ? <DependencyGraphSection cluster={cluster} kind={r.kind} namespace={r.namespace} name={r.name} uid={r.uid} /> : undefined}
        primary={live ? { label: "Reconcile", kbd: "r", onSelect: () => onReconcile(r) } : undefined}
        secondary={live ? { label: isSuspended ? "Resume" : "Suspend", kbd: "s", onSelect: () => onToggleSuspend(r, isSuspended) } : undefined}
        more={[...(more), ...ai.menu]}
      >
        {ai.block}
        {detailError && !removed && (
          <div role="alert" data-testid="detail-error" className="t-body" style={{ display: "flex", alignItems: "flex-start", gap: 5, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>
            <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
            <span>Detail read failed: {detailError}{detail ? "" : " - the detail is unverified, not empty."}</span>
          </div>
        )}
        {!detail ? (
          detailError && !removed ? null
            : <span className="t-secondary" data-testid="detail-unloaded">{removed ? "No detail was loaded before it was removed." : detailOfOther ? "Detail not loaded for this object - the last read was of an earlier object with this name." : "Loading detail…"}</span>
        ) : (
          <>
            {detailPrior && <span className="t-secondary">{detail.asOfUnix ? `Last read ${new Date(detail.asOfUnix * 1000).toLocaleTimeString()}` : "Last read detail"}{removed ? " - before it was removed." : "."}</span>}
            {detail.applyFailed && (
              <div className="t-body" style={{ color: "var(--color-text-danger)" }}>
                Apply failed at <span className="t-identifier">{shortRev(detail.attemptedRevision)}</span>
              </div>
            )}
            {detail.source ? <SourceSection source={detail.source} />
              : detail.sourceWarning ? (
                <InspectorBlock title="Source">
                  <div data-testid="source-warning" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>{detail.sourceWarning}</div>
                </InspectorBlock>
              ) : null}
            {detail.events && <EventsSection events={detail.events} />}
            <InspectorBlock title="Conditions" count={detail.conditions.length} defaultOpen={included(r)}>
              <InspectorConditions conditions={detail.conditions} />
            </InspectorBlock>
            {r.kind === "Kustomization" ? (
              <InspectorBlock title="Inventory" count={detail.inventory.length}>
                {detail.inventory.length === 0 ? <Muted>No managed objects.</Muted> : <InventoryList entries={detail.inventory} />}
              </InspectorBlock>
            ) : (
              <InspectorBlock title="Inventory"><Muted>A HelmRelease CR carries no inventory.</Muted></InspectorBlock>
            )}
            {canDiff && <DiffSection cluster={cluster} namespace={r.namespace} name={r.name} />}
          </>
        )}
      </InspectorLayout>
    </div>
  );
}

// ReconcileDialog is the one reconcile verb: the former separate "reconcile
// with source" action is now a per-run option here, because whether the
// source needs a re-fetch is a property of this reconcile, not a different
// operation.
function ReconcileDialog({ cluster, resource, isProtected, onCancel, onConfirm }: {
  cluster: string;
  resource: FluxResourceDTO;
  isProtected: boolean;
  onCancel: () => void;
  onConfirm: (withSource: boolean) => void;
}) {
  const [withSource, setWithSource] = useState(false);
  return (
    <OperationDialog
      title={`Reconcile "${resource.namespace}/${resource.name}"`}
      ariaLabel={`Reconcile ${resource.namespace}/${resource.name}`}
      subtitle="Annotates the resource so Flux reconciles it now instead of waiting for the next interval."
      cluster={cluster}
      protected={isProtected}
      confirmLabel="Reconcile"
      onCancel={onCancel}
      onConfirm={() => onConfirm(withSource)}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <MetaTile label="resource" value={`${resource.kind} ${resource.namespace}/${resource.name}`} />
        <MetaTile label="bound source" value={sourceText(resource) || "—"} />
      </div>
      <OptionTile
        active={withSource}
        title="Re-fetch source first"
        body="Also annotate the bound source so Flux pulls a fresh artifact before reconciling; use when the source itself is stale."
        onClick={() => setWithSource((v) => !v)}
      />
    </OperationDialog>
  );
}

// Inventory entries carry no health in the Kustomization CR (group/kind/ns/name
// only), so the list truncates without claiming "all healthy" and the dots stay
// unlit - a health suffix or green lamp here would be fabricated.
const INVENTORY_SHOWN = 8;

function InventoryList({ entries }: { entries: InventoryEntryDTO[] }) {
  const shown = entries.slice(0, INVENTORY_SHOWN);
  const hidden = entries.length - shown.length;
  return (
    <>
      {shown.map((e) => {
        const key = `${e.kind}/${e.namespace}/${e.name}`;
        const name = `${e.namespace ? `${e.namespace}/` : ""}${e.name}`;
        // Argo's app resources walk to the live object; Flux's inventory
        // does the same for every kind Klyx can route to.
        const ref = wellKnownResourceRef(e.group, e.kind);
        return (
          <div key={key} style={{ display: "flex", gap: 7, alignItems: "center", minWidth: 0 }}>
            <StatusDot size={6} color="var(--color-text-tertiary)" />
            <span style={{ color: "var(--color-text-tertiary)", fontSize: 11, flexShrink: 0 }}>{e.kind}</span>
            {ref ? (
              <button
                onClick={() => {
                  const nav = useFleet.getState();
                  nav.openResource(ref);
                  nav.openInstance(ref.scope === "Cluster" ? "" : e.namespace, e.name);
                }}
                title={`open ${e.kind} ${name}`}
                style={{
                  background: "transparent", border: "none", padding: 0, cursor: "pointer",
                  textAlign: "left", fontFamily: "var(--font-mono)", fontSize: "inherit", minWidth: 0,
                  color: "var(--color-text-info)", ...ellipsis,
                }}
              >
                {name}
              </button>
            ) : (
              <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-secondary)", ...ellipsis }}>{name}</span>
            )}
          </div>
        );
      })}
      {hidden > 0 && (
        <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-tertiary)" }}>+{hidden} more</div>
      )}
    </>
  );
}

function DiffSection({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }) {
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<FluxDiffDTO | null>(null);
  const run = () => {
    setRunning(true);
    fluxDiff(cluster, namespace, name, "")
      .then((r) => setResult(r))
      .catch((e) => setResult({ available: true, hasChanges: false, output: "", error: String(e) }))
      .finally(() => setRunning(false));
  };
  return (
    <InspectorBlock title="Live diff">
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <button className="l-btn" onClick={run} disabled={running} style={{ opacity: running ? 0.6 : 1 }}>
          {running ? "Computing…" : "Compute diff"}
        </button>
        <span data-testid="diff-via" style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>
          {!result || !result.via
            ? <>builds from the Git cache when the repository is approved in Settings &gt; Git, else shells out to <span style={{ fontFamily: "var(--font-mono)" }}>flux diff</span></>
            : result.via === "cache"
              ? <>built from the Git cache{result.revision ? <> at <span style={{ fontFamily: "var(--font-mono)" }}>{result.revision.slice(0, 7)}</span></> : null}, dry-run on this cluster&apos;s connection</>
              : <>computed by <span style={{ fontFamily: "var(--font-mono)" }}>flux diff</span> with your local credentials</>}
        </span>
      </div>
      {result?.notes?.map((n) => (
        <div key={n} style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginTop: 4 }}>{n}</div>
      ))}
      {result && result.error && (
        <div style={{ color: "var(--color-text-danger)", marginTop: 6, overflowWrap: "anywhere" }} title={result.error}>{result.error}</div>
      )}
      {result && !result.error && !result.hasChanges && (
        <div style={{ color: "var(--color-text-success)", marginTop: 6 }}>{result.via === "cache" ? "no changes - the dry-run matches the live objects" : "no changes - live matches Git"}</div>
      )}
      {result && result.hasChanges && (
        <pre style={{ marginTop: 6, padding: 8, maxHeight: 280, overflow: "auto", background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, fontFamily: "var(--font-mono)", fontSize: 11, whiteSpace: "pre", lineHeight: 1.4 }}>{result.output}</pre>
      )}
    </InspectorBlock>
  );
}

function SourceSection({ source }: { source: FluxSourceDTO }) {
  // A suspended source is paused, not failed: its Ready condition goes stale by
  // design, so it must not surface the red "source not ready" root-cause headline
  // (the status dot already shows paused). Suspended-is-not-a-fault invariant.
  const ready = !included(source);
  const role = roleOf(source);
  const label = sourceKindLabel[source.kind] ?? source.kind;
  return (
    <InspectorBlock title="Source">
      {!ready && (
        <div style={{ color: role.kind === "fault" ? "var(--color-text-danger)" : "var(--color-text-warning)", fontWeight: 500, marginBottom: 4 }}>
          source not ready{source.reason ? `: ${source.reason}` : ""}
        </div>
      )}
      <div style={{ display: "grid", gridTemplateColumns: "9px minmax(0,1fr)", gap: 8, alignItems: "baseline" }}>
        <span role="img" aria-label={role.label} style={{ display: "inline-flex" }}><VerdictLamp health={role} size={7} /></span>
        <div style={{ minWidth: 0 }}>
          <span style={{ fontFamily: "var(--font-mono)" }}>{label} · {source.namespace}/{source.name}</span>
          {source.revision && (
            <span style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginLeft: 6, fontFamily: "var(--font-mono)" }}>{shortRev(source.revision)}</span>
          )}
          {source.message && !ready && (
            <div style={{ color: "var(--color-text-secondary)", fontSize: 11, marginTop: 2, overflowWrap: "anywhere" }}>{source.message}</div>
          )}
        </div>
      </div>
    </InspectorBlock>
  );
}

function isDriftEvent(e: EventRowDTO): boolean {
  const s = `${e.reason} ${e.message}`.toLowerCase();
  return s.includes("drift") || s.includes("configured");
}

// EventsSection mirrors the InspectorEvents evline (dot, reason, ellipsized
// message, ×count, age right) but cannot reuse it: EventRowDTO ages are unix
// (not ISO) and drift events carry the M10 drift chip, which the shared
// component has no slot for.
function EventsSection({ events }: { events: EventRowDTO[] }) {
  const nowSec = Math.floor(Date.now() / 1000);
  return (
    <InspectorBlock title="Drift / events">
      {events.length === 0 ? <Muted>no recent events</Muted> : events.map((e, i) => {
        const warn = e.type === "Warning";
        const drift = isDriftEvent(e);
        const dot = warn ? "var(--color-text-danger)" : drift ? "var(--color-text-info)" : "var(--color-text-tertiary)";
        const sec = e.lastSeenUnix > 0 ? Math.max(0, nowSec - e.lastSeenUnix) : 0;
        return (
          <div key={i} style={{ display: "flex", gap: 6, alignItems: "baseline", fontSize: 11, padding: "1px 0", color: warn ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
            <span style={{ width: 7, height: 7, borderRadius: "50%", background: dot, display: "inline-block", flexShrink: 0, alignSelf: "center" }} />
            <span style={{ fontWeight: 500, flexShrink: 0, maxWidth: 130, ...ellipsis }} title={e.reason}>{e.reason}</span>
            {drift && <span style={{ fontFamily: "var(--font-mono)", fontSize: 9, padding: "1px 5px", borderRadius: 3, border: "0.5px solid var(--color-border-info)", color: "var(--color-text-info)", background: "var(--color-background-info)", flexShrink: 0, alignSelf: "center" }}>drift</span>}
            <span style={{ ...ellipsis, flex: 1, minWidth: 0 }} title={e.message}>{e.message}</span>
            {e.count > 1 && <span title={`${e.count.toLocaleString()} occurrences`} style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>×{compactCount(e.count)}</span>}
            <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{ago(sec) || "now"}</span>
          </div>
        );
      })}
    </InspectorBlock>
  );
}

// UnverifiedNotice: the selection is missing from a read that is not live
// and complete for its kind - kept with its last row, never called removed.
function UnverifiedNotice({ kind }: { kind: string }) {
  return (
    <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
      <span className="t-status">Not in the current {kind} read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
    </div>
  );
}

// roleTone maps the Go rule's presentation role onto the verdict tone, as
// every other lens does. Progressing stays grey: the ratified staleness
// language is a grey "Syncing…", and paused is an intended pause, not a fault.
function roleTone(k: string): Tone {
  if (k === "fault") return "danger";
  if (k === "degraded") return "warning";
  if (k === "ok") return "success";
  return "muted";
}

function Muted({ children }: { children: ReactNode }) {
  return <span style={{ color: "var(--color-text-tertiary)" }}>{children}</span>;
}
