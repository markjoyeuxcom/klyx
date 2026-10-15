import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "../store/fleet";
import type { ArgoApplicationSetDTO, ArgoAppDTO, ArgoProjectDTO, ArgoResourceDTO, ArgoResourceRuleDTO, ArgoViewMode, CRDGroupDTO, KindCoverageDTO, ResourceRef, RuleDTO } from "../store/fleet";
import { listArgoProjects, refreshArgoApp, syncArgoApp } from "../bridge/argo";
import { useArgoRows } from "../health/useArgoRows";
import { clock, coverageGaps, lastGoodAt } from "../layout/coverage";
import type { ArgoSyncOptions } from "../bridge/argo";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { EmptyState, SkeletonRows, ellipsis } from "../chrome/primitives";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { IconAlertTriangle, IconGitMerge } from "@tabler/icons-react";
import { Age, UpdatedAt, useReceivedAt } from "../chrome/Age";
import { InspectorSection, InspectorStatus } from "./ResourceInspector";
import { wellKnownResourceRef } from "./resourceRefs";
import { VerdictLamp } from "../health/VerdictView";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useInspectorAI } from "../ai/AIExplain";
import { ClampedText } from "../chrome/ClampedText";
import { useClusterKey } from "./useClusterKey";

// ArgoView is the Argo CD lens, speaking Argo's vocabulary (synced/degraded,
// never translated into Flux terms). Applications and ApplicationSets are the
// health Observer's shared rows (1c slice 3 §5.4), sorted broken-first here;
// AppProjects stay a fetch. The two imperative verbs (refresh, sync)
// mirror the Flux reconcile pattern: ConfirmDialog + Protected gating, never
// authoring desired state, never pruning.

const keyOf = (a: ArgoAppDTO) => `${a.namespace}/${a.name}`;
const arrayOf = <T,>(value: T[] | null | undefined): T[] => (Array.isArray(value) ? value : []);


const short = (rev: string) => (rev.length > 8 ? rev.slice(0, 8) : rev);


export function resolveArgoResourceRef(resource: ArgoResourceDTO, crdGroups: CRDGroupDTO[] = []): ResourceRef | null {
  const crdGroup = crdGroups.find((g) => g.group === resource.group);
  const crdKind = crdGroup?.kinds.find((k) => k.kind === resource.kind);
  if (crdGroup && crdKind) {
    return {
      group: crdGroup.group,
      version: crdKind.version,
      plural: crdKind.plural,
      kind: crdKind.kind,
      scope: crdKind.scope,
    };
  }
  return wellKnownResourceRef(resource.group, resource.kind);
}

// ArgoView - the Argo CD lens on the workspace template (spec §6 step 4b).
// Three views of one lens: Applications, Projects, ApplicationSets (the route
// keeps the mode). Argo's own vocabulary is kept - Synced/OutOfSync,
// Healthy/Progressing/Degraded/Missing/Suspended - never translated into Flux
// terms. Attention follows the ratified backend rule (the Go-computed rule):
// Suspended is an intended pause, never attention. Primary verb Refresh,
// secondary Sync (safeguarded composer, prune off by default) per spec §3.

type ArgoPinned = { cluster: string; verb: "refresh" | "sync"; app: ArgoAppDTO };
type AppFilter = "all" | "attention" | "outofsync";
type ProjectFilter = "all" | "risky" | "unused";
type AppSetFilter = "all" | "attention" | "missing";

// The verdict is the relocated rule computed in Go (internal/gitops/argo
// rules.go, 1c addendum §2): rule.included is the ratified backend rule (not
// Synced or unhealthy; Progressing and Suspended are not broken) - or, for an
// ApplicationSet, not healthy - and rule.role/label the presentation in Argo's
// own vocabulary.
const roleOf = (x: { rule: RuleDTO }) => ({ kind: x.rule.role, label: x.rule.label });
const ROLE_COLOR: Record<string, string> = {
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  progressing: "var(--color-text-secondary)",
  paused: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)",
  unknown: "var(--color-text-tertiary)",
};

// A project's policy risk is governance evidence, not a running fault: it
// may warn (amber) but never glows as an outage or carries the fault ▲.
function riskRole(risks: Risk[]): { kind: string; label: string } {
  if (risks.some((r) => r.tone === "danger")) return { kind: "degraded", label: "risky policy" };
  if (risks.some((r) => r.tone === "warning")) return { kind: "degraded", label: "policy warning" };
  return { kind: "ok", label: "restricted" };
}

function RowLamp({ role }: { role: { kind: string; label: string } }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}

const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };

const APP_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Application", width: 240, priority: 0, flex: true },
  { id: "sync", label: "Sync", width: 92, priority: 1 },
  { id: "health", label: "Health", width: 104, priority: 1 },
  { id: "dest", label: "Destination", width: 140, priority: 3, grow: 0.6 },
  { id: "revision", label: "Revision", width: 84, priority: 4 },
  { id: "reconciled", label: "Reconciled", width: 92, priority: 5 },
];
const PROJECT_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Project", width: 240, priority: 0, flex: true },
  { id: "policy", label: "Policy signal", width: 220, priority: 1, grow: 0.8 },
  { id: "apps", label: "Apps", width: 56, priority: 2 },
  { id: "appsets", label: "AppSets", width: 64, priority: 3 },
];
const APPSET_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "ApplicationSet", width: 240, priority: 0, flex: true },
  { id: "generators", label: "Generators", width: 130, priority: 2 },
  { id: "apps", label: "Apps", width: 56, priority: 1 },
  { id: "source", label: "Source", width: 180, priority: 4, grow: 0.8 },
  { id: "project", label: "Project", width: 110, priority: 3 },
];

export function ArgoView({ cluster, mode = "applications" }: { cluster: string; mode?: ArgoViewMode }) {
  const argo = useFleet((s) => s.argo);
  const openArgo = useFleet((s) => s.openArgo);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [pinned, setPinnedState] = useState<ArgoPinned | null>(null);
  const [appKey, setAppKey] = useClusterKey(cluster);
  const [projKey, setProjKey] = useClusterKey(cluster);
  const [setKey, setSetKey] = useClusterKey(cluster);
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [appFilter, setAppFilter] = useState<AppFilter>("all");
  const [projectFilter, setProjectFilter] = useState<ProjectFilter>("all");
  const [appSetFilter, setAppSetFilter] = useState<AppSetFilter>("all");
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  // Applications and ApplicationSets: the Observer's shared rows; projects
  // stay a fetch (re-read when the view changes, as before).
  useArgoRows(cluster);
  useEffect(() => { void listArgoProjects(cluster); }, [cluster, mode]);

  const mine = argo.cluster === cluster;
  // Applications and ApplicationSets carry the Observer's per-kind coverage
  // (Codex review of 0777b3b): live and absent are complete, stale is the
  // last good read - empty included - and checking or unreadable is
  // unverified even with retained rows. The fetched AppProjects keep the
  // loading/error inference.
  const appsCov = observedCoverage(mine, argo.coverage, "Application", coverageOf(mine, argo.loading, argo.error, argo.apps.length));
  const projectsCov = coverageOf(mine, argo.projectsLoading, argo.projectsError, argo.projects.length);
  const appSetsCov = observedCoverage(mine, argo.coverage, "ApplicationSet", coverageOf(mine, argo.applicationSetsLoading, argo.applicationSetsError, argo.applicationSets.length));
  // broken first, then namespace/name - the rows arrive in capture order
  const apps = useMemo(() => (mine ? [...argo.apps].sort((a, b) => Number(b.broken) - Number(a.broken) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)) : []), [mine, argo.apps]);
  const projects = useMemo(() => (mine ? argo.projects : []), [mine, argo.projects]);
  const appSets = useMemo(() => (mine ? argo.applicationSets : []), [mine, argo.applicationSets]);
  // A4 (slice 3 §4): past the deadline the rows are evidence as of their
  // capture, and the totals are the summary's until a re-read lands.
  const rowsRefreshing = mine && !!argo.rowsState?.refreshing;
  const rowsAsOf = argo.rowsState?.evaluatedAt ? clock(argo.rowsState.evaluatedAt) : "the last read";
  const globalCounts = mine ? argo.partCounts : undefined;
  const lastGood = mine ? lastGoodAt(argo.coverage, ["Application", "ApplicationSet"]) : null;
  const discoveryGap = mine ? coverageGaps(argo.coverage ?? []) : null;

  // Pending verbs are pinned to the cluster + Application OBJECT (its UID,
  // design §5b) they were raised on: dropped when the cluster changes, when a
  // same-name replacement takes the row, or when the app leaves a complete
  // read. The backend writes only that UID.
  const appsComplete = mine && !argo.loading && !argo.error;
  const setPinned = useCallback((v: Omit<ArgoPinned, "cluster"> | null) => setPinnedState(v ? { ...v, cluster } : null), [cluster]);
  const verbLive = (v: ArgoPinned) => {
    if (v.cluster !== cluster) return false;
    const cur = apps.find((a) => keyOf(a) === keyOf(v.app));
    if (cur) return cur.uid === v.app.uid;
    return !appsComplete;
  };
  const verb = pinned && verbLive(pinned) ? pinned : null;
  useEffect(() => { if (pinned && !verb) setPinnedState(null); }, [pinned, verb]);
  // dispatch re-checks the armed Application against the current rows
  const dispatchArmed = (v: ArgoPinned, send: (uid: string) => void) => {
    setPinnedState(null);
    if (!verbLive(v)) {
      useFleet.getState().setActionStatus({ kind: "error", message: `Application ${keyOf(v.app)} was replaced since the action was armed - nothing was sent` });
      return;
    }
    send(v.app.uid ?? "");
  };

  // Filtering per view.
  const q = query.trim().toLowerCase();
  const visibleApps = useMemo(() => apps.filter((a) => {
    if (appFilter === "attention" && !a.rule.included) return false;
    if (appFilter === "outofsync" && a.syncStatus !== "OutOfSync") return false;
    return matchesAppQuery(a, q);
  }), [apps, appFilter, q]);
  const visibleProjects = useMemo(() => [...projects]
    .filter((p) => matchesProjectQuery(p, q))
    .filter((p) => projectFilter !== "risky" || projectRiskBadges(p, appsForProject(apps, p.name), appsCov).some((r) => r.tone === "danger" || r.tone === "warning"))
    .filter((p) => projectFilter !== "unused" || (appsCov !== "unknown" && appsForProject(apps, p.name).length === 0))
    .sort((a, b) => projectRiskScore(b, apps, appsCov) - projectRiskScore(a, apps, appsCov) || a.name.localeCompare(b.name)), [projects, apps, q, projectFilter, appsCov]);
  const visibleAppSets = useMemo(() => [...appSets]
    .filter((a) => matchesAppSetQuery(a, q))
    .filter((a) => appSetFilter !== "attention" || a.rule.included)
    .filter((a) => appSetFilter !== "missing" || (appsCov !== "unknown" && missingGeneratedNames(a, apps).length > 0))
    .sort((a, b) => Number(b.rule.included) - Number(a.rule.included) || a.name.localeCompare(b.name)), [appSets, apps, q, appSetFilter, appsCov]);

  // Selection lifecycle per view: filter exclusion closes; absent from a
  // complete read = removed (last evidence, no actions).
  const selectedApp = appKey ? apps.find((a) => keyOf(a) === appKey) : undefined;
  const selectedProject = projKey ? projects.find((p) => projectKey(p) === projKey) : undefined;
  const selectedSet = setKey ? appSets.find((a) => appSetKey(a) === setKey) : undefined;
  // the selection's last evidence, for this cluster and key only
  const lastApp = useRef<{ cluster: string; v: ArgoAppDTO } | null>(null);
  const lastProject = useRef<{ cluster: string; v: ArgoProjectDTO } | null>(null);
  const lastSet = useRef<{ cluster: string; v: ArgoApplicationSetDTO } | null>(null);
  if (selectedApp) lastApp.current = { cluster, v: selectedApp };
  if (selectedProject) lastProject.current = { cluster, v: selectedProject };
  if (selectedSet) lastSet.current = { cluster, v: selectedSet };
  const appRemoved = !!appKey && appsComplete && !selectedApp;
  const projRemoved = !!projKey && mine && !argo.projectsLoading && !argo.projectsError && !selectedProject;
  const setRemoved = !!setKey && mine && !argo.applicationSetsLoading && !argo.applicationSetsError && !selectedSet;
  // missing without a complete read of its own kind: kept with its last row,
  // unverified - never removed (slice 3 §5a)
  const priorApp = lastApp.current?.cluster === cluster && keyOf(lastApp.current.v) === appKey ? lastApp.current.v : null;
  const priorProject = lastProject.current?.cluster === cluster && projectKey(lastProject.current.v) === projKey ? lastProject.current.v : null;
  const priorSet = lastSet.current?.cluster === cluster && appSetKey(lastSet.current.v) === setKey ? lastSet.current.v : null;
  const appSubject = selectedApp ?? priorApp;
  const projectSubject = selectedProject ?? priorProject;
  const setSubject = selectedSet ?? priorSet;
  const appUnverified = !!appKey && !selectedApp && !appRemoved && !!priorApp;
  const projUnverified = !!projKey && !selectedProject && !projRemoved && !!priorProject;
  const setUnverified = !!setKey && !selectedSet && !setRemoved && !!priorSet;
  useEffect(() => { if (selectedApp && !visibleApps.includes(selectedApp)) setAppKey(null); }, [selectedApp, visibleApps, setAppKey]);
  useEffect(() => { if (selectedProject && !visibleProjects.includes(selectedProject)) setProjKey(null); }, [selectedProject, visibleProjects, setProjKey]);
  useEffect(() => { if (selectedSet && !visibleAppSets.includes(selectedSet)) setSetKey(null); }, [selectedSet, visibleAppSets, setSetKey]);
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    setAppKey(null); setProjKey(null); setSetKey(null); setCursorKey(null);
    autoSelect.current = "pending";
    entryApps.current = useFleet.getState().argo.apps;
  }, [cluster, setAppKey, setProjKey, setSetKey]);

  // Palette verbs / cross-links: select the app, reveal it past stale
  // filters, then open its confirm - never bypassing the composer.
  useNavIntent(cluster, ["sync-argo", "refresh-argo"], (intent) => {
    if (argo.loading || apps.length === 0) return "wait";
    if (!("namespace" in intent)) return "missing";
    const app = apps.find((a) => a.namespace === intent.namespace && a.name === intent.name);
    if (!app) return "missing";
    autoSelect.current = "done";
    if (mode !== "applications") openArgo("applications");
    if (!visibleApps.includes(app)) { setAppFilter("all"); setQuery(""); }
    setCursorKey(keyOf(app));
    setAppKey(keyOf(app));
    setPinned({ verb: intent.kind === "sync-argo" ? "sync" : "refresh", app });
    return "consumed";
  }, [apps, argo.loading, visibleApps, mode]);

  // A summary Top link (health 1c slice 4 §5.4): select the Application or
  // ApplicationSet past the filters - selection only, never an action. The
  // router already opened the part's view.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "argo") return "wait";
    const sets = intent.part === "applicationsets";
    const key = `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const found = sets ? appSets.some((a) => appSetKey(a) === key) : apps.some((a) => keyOf(a) === key);
    if (!found) return (sets ? appSetsCov : appsCov) === "complete" ? "missing" : "wait";
    autoSelect.current = "done";
    clearFilters();
    setCursorKey(key);
    if (sets) setSetKey(key); else setAppKey(key);
    return "consumed";
  }, [apps, appSets, appsCov, appSetsCov]);

  // One-shot auto-select of the first broken Application from a fresh read.
  const autoSelect = useRef<"pending" | "done">("pending");
  const entryApps = useRef<ArgoAppDTO[] | null>(null);
  if (entryApps.current === null) entryApps.current = argo.apps;
  const freshRead = mine && !argo.loading && !argo.error && argo.apps !== entryApps.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead || mode !== "applications") return;
    autoSelect.current = "done";
    if (appKey) return;
    const a = visibleApps.find((x) => x.rule.included);
    if (a) { setCursorKey(keyOf(a)); setAppKey(keyOf(a)); }
  }, [freshRead, visibleApps, appKey, mode, setAppKey]);

  // Keyboard: j/k/Enter/Esc in every view; r/s (refresh/sync) on apps only.
  const nav: { key: string; open: () => void }[] = mode === "projects"
    ? visibleProjects.map((p) => ({ key: projectKey(p), open: () => setProjKey(projectKey(p)) }))
    : mode === "applicationsets"
      ? visibleAppSets.map((a) => ({ key: appSetKey(a), open: () => setSetKey(appSetKey(a)) }))
      : visibleApps.map((a) => ({ key: keyOf(a), open: () => { autoSelect.current = "done"; setAppKey(keyOf(a)); } }));
  const cursorIdx = cursorKey === null ? -1 : nav.findIndex((n) => n.key === cursorKey);
  const closeSelected = () => { autoSelect.current = "done"; if (mode === "projects") setProjKey(null); else if (mode === "applicationsets") setSetKey(null); else setAppKey(null); };
  useListKeys({
    count: nav.length,
    selected: cursorIdx,
    onSelect: (idx) => { const n = nav[idx]; if (n) { setCursorKey(n.key); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const n = nav[idx]; if (n) { setCursorKey(n.key); n.open(); } },
    onEscape: closeSelected,
    searchRef,
    extraKeys: mode === "applications" ? {
      r: (idx) => { const a = visibleApps[idx]; if (a) setPinned({ verb: "refresh", app: a }); },
      s: (idx) => { const a = visibleApps[idx]; if (a) setPinned({ verb: "sync", app: a }); },
    } : {},
  });

  const columns = mode === "projects" ? PROJECT_COLUMNS : mode === "applicationsets" ? APPSET_COLUMNS : APP_COLUMNS;
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  // Counts in Argo vocabulary. global: the count selector's (slice 3 §4a);
  // row-scoped: the breakdown and filters ("as of" while refreshing).
  const appTotal = globalCounts?.applications?.total ?? apps.length;
  const broken = globalCounts?.applications?.included ?? apps.filter((a) => a.rule.included).length;
  const asOf = rowsRefreshing && mode !== "projects" ? ` (as of ${rowsAsOf})` : "";
  const byHealth = (h: string) => apps.filter((a) => a.healthStatus === h).length;
  const outOfSync = apps.filter((a) => a.syncStatus === "OutOfSync").length;
  const riskyProjects = projects.filter((p) => projectRiskBadges(p, appsForProject(apps, p.name), appsCov).some((r) => r.tone === "danger" || r.tone === "warning")).length;
  const unusedProjects = appsCov === "unknown" ? null : projects.filter((p) => appsForProject(apps, p.name).length === 0).length;
  const setTotal = globalCounts?.applicationsets?.total ?? appSets.length;
  const unhealthySets = globalCounts?.applicationsets?.included ?? appSets.filter((a) => a.rule.included).length;
  const missingSets = appsCov === "unknown" ? null : appSets.filter((a) => missingGeneratedNames(a, apps).length > 0).length;

  const view = mode === "projects"
    ? { rows: projects.length, shown: visibleProjects.length, loading: argo.projectsLoading, error: argo.projectsError, available: argo.projectsAvailable, message: argo.projectsMessage, noun: "projects" }
    : mode === "applicationsets"
      ? { rows: appSets.length, shown: visibleAppSets.length, loading: argo.applicationSetsLoading, error: argo.applicationSetsError, available: argo.applicationSetsAvailable, message: argo.applicationSetsMessage, noun: "ApplicationSets" }
      : { rows: apps.length, shown: visibleApps.length, loading: argo.loading, error: argo.error, available: argo.available, message: argo.message, noun: "applications" };
  const clearFilters = () => { setQuery(""); setAppFilter("all"); setProjectFilter("all"); setAppSetFilter("all"); };
  const viewCov = mode === "projects" ? projectsCov : mode === "applicationsets" ? appSetsCov : appsCov;
  let state: DatasetState = "ready";
  if (!mine || (view.loading && view.rows === 0)) state = "loading";
  // a stale read that was empty is last-good empty evidence, never unreadable
  else if (view.error && view.rows === 0 && viewCov !== "stale") state = "unreadable";
  else if (view.rows === 0) state = "empty";
  else if (view.shown === 0) state = "no-match";
  // only projects are a fetch to repeat; the rows retry on their own
  const reload = mode === "projects" ? () => void listArgoProjects(cluster) : null;
  const globalTotal = mode === "projects" ? view.rows : mode === "applicationsets" ? setTotal : appTotal;
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={6} label={`loading ${view.noun}`} /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title={`Argo CD ${view.noun} could not be read.`} hint={view.error ?? undefined} action={reload ? { label: "Retry", onClick: reload } : undefined} />
    : state === "empty" ? (!view.available
      ? <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title={`Argo CD ${view.noun} are not served on this cluster.`} hint={view.message || undefined} />
      : rowsRefreshing && mode !== "projects" && globalTotal > 0 ? <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title={`No Argo CD ${view.noun} as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${globalTotal}.`} />
      : viewCov === "stale" ? <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title={`No Argo CD ${view.noun} in the last good read.`} hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${view.error ?? "unverified"}.`} />
      : <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title={`No Argo CD ${view.noun} on this cluster.`} hint={mode === "applications" ? "Argo CD is installed but no Application objects exist yet." : mode === "projects" ? "Projects define allowed repos, destinations, and roles." : "ApplicationSets generate Applications from clusters, Git, pull requests, and other generators."} />)
    : <EmptyState icon={<IconGitMerge size={28} stroke={1.2} />} title={`No ${view.noun} match the current filters.`} hint={`${view.rows} ${view.noun} on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;
  // freshness follows the CURRENT view's own read, not Applications for all
  const viewRows: unknown[] = mode === "projects" ? projects : mode === "applicationsets" ? appSets : apps;
  const receivedAt = useReceivedAt(mine && !view.loading && !view.error && viewRows.length > 0 ? viewRows : null);
  // stale = the last observed set ("*"); pending/failed = a lower bound ("+")
  const partial = viewCov === "stale" ? "*" : view.error || viewCov === "unknown" ? "+" : "";

  // Notices stack: each is its own evidence, none hides another.
  const statusLine = { minHeight: 28, display: "flex", alignItems: "center" } as const;
  const notices: React.ReactNode[] = [];
  if (view.error && view.rows > 0) notices.push(<WatchErrorBanner key="read" tool="Argo CD" failure={`read failed - showing the last good read${mode !== "projects" && lastGood ? ` from ${clock(lastGood)}` : ""}`} error={view.error} />);
  if (rowsRefreshing && mode !== "projects") notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{argo.rowsState?.refreshError ? ` Last re-read failed: ${argo.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap && mode !== "projects") notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  const projectFor = (name: string) => projects.find((p) => p.name === name) ?? null;

  return (
    <>
      <LensLayout
        label="Argo CD"
        title={mode === "projects" ? "Argo CD projects" : mode === "applicationsets" ? "Argo CD ApplicationSets" : "Argo CD applications"}
        summary={state === "loading" || state === "unreadable" ? null : mode === "applications" ? (
          <>
            <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{appTotal}{partial}</span>
            {broken > 0
              ? <span className="t-status" style={{ color: byHealth("Degraded") + byHealth("Missing") > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{byHealth("Degraded") + byHealth("Missing") > 0 ? "▲ " : ""}{broken} out of sync or unhealthy</span>
              : apps.length === 0 ? null
              : <span className="t-secondary">{view.error ? "none known out of sync or unhealthy" : "none out of sync or unhealthy reported"}</span>}
            <span className="t-secondary" data-testid="argo-breakdown">
              {[byHealth("Degraded") ? `${byHealth("Degraded")} degraded` : "", byHealth("Missing") ? `${byHealth("Missing")} missing` : "", outOfSync ? `${outOfSync} out of sync` : "", byHealth("Progressing") ? `${byHealth("Progressing")} progressing` : "", byHealth("Suspended") ? `${byHealth("Suspended")} suspended` : ""].filter(Boolean).join(" · ")}{apps.length ? asOf : ""}
            </span>
          </>
        ) : mode === "projects" ? (
          <>
            <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{projects.length}{partial}</span>
            <span className="t-secondary">{[riskyProjects ? `${riskyProjects} with policy risk` : "no policy risk", unusedProjects === null ? "app usage unverified (Application read unavailable)" : unusedProjects ? `${unusedProjects} unused${appsCov === "stale" ? " (last good Application read)" : ""}` : ""].filter(Boolean).join(" · ")}</span>
          </>
        ) : (
          <>
            <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{setTotal}{partial}</span>
            {unhealthySets > 0
              ? <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{unhealthySets} need attention</span>
              : <span className="t-secondary">none need attention</span>}
            {missingSets === null ? <span className="t-secondary">generated apps unverified (Application read unavailable)</span> : missingSets > 0 && <span className="t-secondary">{missingSets} missing generated apps{appsCov === "stale" ? " (last good Application read)" : ""}</span>}
          </>
        )}
        headerEnd={
          <>
            <UpdatedAt at={receivedAt} />
            {reload && <button className="l-btn" onClick={reload}>Refresh list</button>}
          </>
        }
        controls={
          <>
            {/* The view (Applications, Projects, ApplicationSets) is chosen in
                the navigation band - one switch, not two (UI review 2026-10-13);
                this row holds only filters and search. */}
            {mode === "applications" && (
              <>
                <button className="l-chip" aria-pressed={appFilter === "all"} onClick={() => setAppFilter("all")}>All</button>
                <button className="l-chip" aria-pressed={appFilter === "attention"} onClick={() => setAppFilter("attention")}>Attention {broken > 0 && <span style={{ color: "var(--color-text-warning)" }}>{broken}</span>}</button>
                {outOfSync > 0 && <button className="l-chip" aria-pressed={appFilter === "outofsync"} onClick={() => setAppFilter("outofsync")}>OutOfSync <span style={{ color: "var(--color-text-tertiary)" }}>{outOfSync}</span></button>}
              </>
            )}
            {mode === "projects" && (
              <>
                <button className="l-chip" aria-pressed={projectFilter === "all"} onClick={() => setProjectFilter("all")}>All</button>
                <button className="l-chip" aria-pressed={projectFilter === "risky"} onClick={() => setProjectFilter("risky")}>Policy risk <span style={{ color: "var(--color-text-tertiary)" }}>{riskyProjects}</span></button>
                {unusedProjects !== null && <button className="l-chip" aria-pressed={projectFilter === "unused"} onClick={() => setProjectFilter("unused")} title={appsCov === "stale" ? "from the last good Application read" : undefined}>Unused <span style={{ color: "var(--color-text-tertiary)" }}>{countText(unusedProjects, appsCov)}</span></button>}
              </>
            )}
            {mode === "applicationsets" && (
              <>
                <button className="l-chip" aria-pressed={appSetFilter === "all"} onClick={() => setAppSetFilter("all")}>All</button>
                <button className="l-chip" aria-pressed={appSetFilter === "attention"} onClick={() => setAppSetFilter("attention")}>Attention {unhealthySets > 0 && <span style={{ color: "var(--color-text-warning)" }}>{unhealthySets}</span>}</button>
                {missingSets !== null && <button className="l-chip" aria-pressed={appSetFilter === "missing"} onClick={() => setAppSetFilter("missing")} title={appsCov === "stale" ? "from the last good Application read" : undefined}>Missing apps <span style={{ color: "var(--color-text-tertiary)" }}>{countText(missingSets, appsCov)}</span></button>}
              </>
            )}
            <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
              <span className="sr-only">Search Argo CD</span>
              <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={`Search ${view.noun}`} />
            </label>
          </>
        }
        notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
        state={state}
        stateView={stateView}
        body={
          <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
            <div className="lens-table-head" role="row" style={gridStyle}>
              {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
            </div>
            {mode === "applications" ? (
              <VirtualList
                ref={listRef}
                items={visibleApps}
                rowHeight={TABLE_ROW_HEIGHT}
                rowKey={(a) => keyOf(a)}
                style={{ flex: 1, minHeight: 0 }}
                render={(a) => {
                  const k = keyOf(a);
                  const role = roleOf(a);
                  return (
                    <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                      aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === appKey ? "true" : undefined}
                      onClick={() => { setCursorKey(k); autoSelect.current = "done"; setAppKey(k === appKey ? null : k); }}
                      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setAppKey(k); } }}>
                      {show("lamp") && <RowLamp role={role} />}
                      {show("name") && (
                        <span style={{ display: "flex", alignItems: "center", gap: 6, minWidth: 0 }}>
                          <span className="lens-cell-name" title={k}><span style={{ fontWeight: k === appKey ? 600 : 400 }}>{a.name}</span></span>
                          {a.project && a.project !== "default" && <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{a.project}</span>}
                          {a.autoSync && <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }} title="automated sync policy">auto</span>}
                        </span>
                      )}
                      {show("sync") && <span style={{ ...cell, color: a.syncStatus === "OutOfSync" ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{a.syncStatus || "-"}</span>}
                      {show("health") && <span style={{ ...cell, color: ROLE_COLOR[a.healthStatus === "Degraded" || a.healthStatus === "Missing" ? "fault" : "ok"], fontWeight: a.healthStatus === "Degraded" || a.healthStatus === "Missing" ? 600 : 400 }}>{a.healthStatus === "Degraded" || a.healthStatus === "Missing" ? "▲ " : ""}{a.healthStatus || "-"}</span>}
                      {show("dest") && <span style={{ ...cell, color: "var(--color-text-secondary)" }}>{a.destNamespace || "-"}</span>}
                      {show("revision") && <span style={{ ...cell, color: "var(--color-text-tertiary)" }}>{a.revision ? short(a.revision) : "-"}</span>}
                      {show("reconciled") && <span style={{ color: "var(--color-text-tertiary)" }}><Age unixSeconds={a.reconciledUnix} suffix=" ago" fallback="never" /></span>}
                    </div>
                  );
                }}
              />
            ) : mode === "projects" ? (
              <VirtualList
                ref={listRef}
                items={visibleProjects}
                rowHeight={TABLE_ROW_HEIGHT}
                rowKey={(p) => projectKey(p)}
                style={{ flex: 1, minHeight: 0 }}
                render={(p) => {
                  const k = projectKey(p);
                  const pApps = appsForProject(apps, p.name);
                  const risks = projectRiskBadges(p, pApps, appsCov);
                  return (
                    <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                      aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === projKey ? "true" : undefined}
                      onClick={() => { setCursorKey(k); setProjKey(k === projKey ? null : k); }}
                      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setProjKey(k); } }}>
                      {show("lamp") && <RowLamp role={riskRole(risks)} />}
                      {show("name") && <span className="lens-cell-name" title={p.description || p.name}>{p.name}{p.description && <span style={{ color: "var(--color-text-tertiary)" }}> · {p.description}</span>}</span>}
                      {show("policy") && <span style={{ ...cell, display: "flex", gap: 4 }}>{risks.length === 0 ? <span style={{ color: "var(--color-text-tertiary)" }}>restricted</span> : risks.slice(0, 3).map((r) => <RiskBadge key={r.label} tone={r.tone}>{r.label}</RiskBadge>)}</span>}
                      {show("apps") && <span title={appsCov === "unknown" ? "Application read unavailable" : undefined}>{countText(pApps.length, appsCov)}</span>}
                      {show("appsets") && <span title={appSetsCov === "unknown" ? "ApplicationSet read unavailable" : appSetsCov === "stale" ? "from the last good ApplicationSet read" : undefined}>{countText(appSetsForProject(appSets, p.name).length, appSetsCov)}</span>}
                    </div>
                  );
                }}
              />
            ) : (
              <VirtualList
                ref={listRef}
                items={visibleAppSets}
                rowHeight={TABLE_ROW_HEIGHT}
                rowKey={(a) => appSetKey(a)}
                style={{ flex: 1, minHeight: 0 }}
                render={(a) => {
                  const k = appSetKey(a);
                  return (
                    <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                      aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === setKey ? "true" : undefined}
                      onClick={() => { setCursorKey(k); setSetKey(k === setKey ? null : k); }}
                      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSetKey(k); } }}>
                      {show("lamp") && <RowLamp role={roleOf(a)} />}
                      {show("name") && <span className="lens-cell-name" title={k}><span style={{ color: "var(--color-text-tertiary)" }}>{a.namespace} / </span>{a.name}</span>}
                      {show("generators") && <span style={{ ...cell, color: "var(--color-text-secondary)" }}>{a.generators.length === 0 ? "-" : a.generators.join(", ")}</span>}
                      {show("apps") && <span>{a.generatedApps || (appsCov === "unknown" ? "?" : generatedAppsForSet(apps, a).length || "-")}</span>}
                      {show("source") && <span style={{ ...cell, color: "var(--color-text-tertiary)" }}>{a.repoURL ? `${a.repoURL}${a.path ? ` · ${a.path}` : ""}` : "-"}</span>}
                      {show("project") && <span style={{ ...cell, color: "var(--color-text-secondary)" }}>{a.project || "-"}</span>}
                    </div>
                  );
                }}
              />
            )}
          </LensTable>
        }
        footer={<span className="t-legend">{mode === "applications" ? "j k navigate · ↵ inspect · r refresh · s sync · / search" : "j k navigate · ↵ inspect · / search"}</span>}
        footerEnd={<span className="t-secondary">{view.shown} of {view.rows}{partial} {view.noun}{asOf}{mode === "applications" ? " · broken first" : ""}</span>}
        inspector={
          mode === "applications" && appKey && appSubject ? (
            <ApplicationInspector
              key={`${cluster}/${appKey}`}
              cluster={cluster}
              app={appSubject}
              removed={appRemoved}
              unverified={appUnverified}
              isProtected={isProtected}
              project={projectFor(appSubject.project)}
              projectsCov={projectsCov}
              appSet={applicationSetForApp(appSets, appSubject)}
              appSetsCov={appSetsCov}
              onClose={() => { autoSelect.current = "done"; setAppKey(null); }}
              onRefresh={(a) => setPinned({ verb: "refresh", app: a })}
              onSync={(a) => setPinned({ verb: "sync", app: a })}
            />
          ) : mode === "projects" && projKey && projectSubject ? (
            <ProjectInspector
              cluster={cluster}
              key={`${cluster}/${projKey}`}
              project={projectSubject}
              removed={projRemoved}
              unverified={projUnverified}
              apps={appsForProject(apps, projectSubject.name)}
              appsCov={appsCov}
              appSets={appSetsForProject(appSets, projectSubject.name)}
              appSetsCov={appSetsCov}
              onClose={() => setProjKey(null)}
            />
          ) : mode === "applicationsets" && setKey && setSubject ? (
            <ApplicationSetInspector
              cluster={cluster}
              key={`${cluster}/${setKey}`}
              appSet={setSubject}
              removed={setRemoved}
              unverified={setUnverified}
              allApps={apps}
              appsCov={appsCov}
              project={projectFor(setSubject.project)}
              onClose={() => setSetKey(null)}
            />
          ) : undefined
        }
        inspectorLabel={mode === "projects" ? "Argo CD project details" : mode === "applicationsets" ? "ApplicationSet details" : "Argo CD application details"}
        onCloseInspector={closeSelected}
      />

      {verb?.verb === "refresh" && (
        <ConfirmDialog
          title="refresh application"
          cluster={verb.cluster}
          detail={`${keyOf(verb.app)} - re-compare against ${verb.app.targetRevision || "HEAD"}`}
          protected={isProtected}
          confirmLabel="Refresh"
          onCancel={() => setPinnedState(null)}
          onConfirm={() => {
            const { cluster: target, app } = verb;
            dispatchArmed(verb, (uid) => void refreshArgoApp(target, app.namespace, app.name, uid));
          }}
        />
      )}
      {verb?.verb === "sync" && (
        <SyncApplicationDialog
          cluster={verb.cluster}
          app={verb.app}
          isProtected={isProtected}
          onCancel={() => setPinnedState(null)}
          onConfirm={(options) => {
            const { cluster: target, app } = verb;
            dispatchArmed(verb, (uid) => void syncArgoApp(target, app.namespace, app.name, uid, options));
          }}
        />
      )}
    </>
  );
}

// Argo Application condition types carry their own severity: *Error types
// (ComparisonError, SyncError, InvalidSpecError, UnknownError) are confirmed
// errors; *Warning types (SharedResourceWarning, OrphanedResourceWarning ...)
// are warnings; anything else is unclassified native evidence - never a
// manufactured fault.
function conditionSeverityColor(type: string): string {
  if (/Error$/.test(type)) return "var(--color-text-danger)";
  if (/Warning$/.test(type)) return "var(--color-text-warning)";
  return "var(--color-text-secondary)";
}

// destinationOf: an Application's inventory lives on its destination
// cluster. Only affirmative in-cluster evidence (Argo's default server URL
// and/or the "in-cluster" name, not contradicting each other) makes it this
// cluster. No evidence, or contradicting evidence, is unverified - fail closed:
// never open a same-named control-plane object as the managed one.
const IN_CLUSTER_SERVER = "https://kubernetes.default.svc";
function destinationOf(a: ArgoAppDTO): "local" | "remote" | "unverified" {
  const server = a.destServer ?? "";
  const name = a.destName ?? "";
  if (!server && !name) return "unverified";
  const serverLocal = server ? server === IN_CLUSTER_SERVER : null;
  const nameLocal = name ? name === "in-cluster" : null;
  if (serverLocal !== null && nameLocal !== null && serverLocal !== nameLocal) return "unverified";
  return (serverLocal ?? nameLocal) ? "local" : "remote";
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

function RemovedNotice({ kind }: { kind: string }) {
  return (
    <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
      <span className="t-status">{kind} removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
    </div>
  );
}

// ApplicationInspector - Argo verdict (health · sync) with the operation and
// condition messages in full; Refresh (primary), Sync (secondary, composer
// with prune off by default), More = View manifest; then source, destination,
// operation, conditions, and the live resource inventory (each resolvable
// resource opens in Resources).
function ApplicationInspector({ cluster, app, removed, unverified, isProtected, project, projectsCov, appSet, appSetsCov, onClose, onRefresh, onSync }: {
  cluster: string;
  app: ArgoAppDTO;
  removed: boolean;
  unverified: boolean;
  isProtected: boolean;
  project: ArgoProjectDTO | null;
  projectsCov: AppCoverage;
  appSet: ArgoApplicationSetDTO | null;
  appSetsCov: AppCoverage;
  onClose: () => void;
  onRefresh: (a: ArgoAppDTO) => void;
  onSync: (a: ArgoAppDTO) => void;
}) {
  const ai = useInspectorAI(app.uid ? { kind: "row" as const, task: "argo", part: "applications", uid: app.uid } : null, `Application/${app.namespace}/${app.name}`, !removed && !unverified, cluster);
  const crd = useFleet((s) => s.crd);
  const crdGroups = crd.cluster === cluster ? crd.groups : [];
  const conditions = arrayOf(app.conditions);
  const resources = arrayOf(app.resources);
  const badResources = resources.filter((r) => r.status === "OutOfSync" || r.health === "Degraded" || r.health === "Missing");
  const role = roleOf(app);
  const tone = role.kind === "fault" ? "danger" : role.kind === "degraded" ? "warning" : role.kind === "ok" ? "success" : "muted";
  const live = !removed;
  const manifestRef = wellKnownResourceRef("argoproj.io", "Application");
  const dest = destinationOf(app);
  const local = dest === "local";
  const destLabel = [app.destName, app.destServer].filter(Boolean).join(" / ");
  const openLiveResource = (resource: ArgoResourceDTO) => {
    if (!local) return;
    const ref = resolveArgoResourceRef(resource, crdGroups);
    if (!ref) return;
    const nav = useFleet.getState();
    nav.openResource(ref);
    nav.openInstance(ref.scope === "Cluster" ? "" : (resource.namespace || app.destNamespace), resource.name);
  };
  const more: MenuItem[] = manifestRef
    ? [{ label: "View manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(app.namespace, app.name); } }]
    : [];
  const opFailed = !!app.opPhase && app.opPhase !== "Succeeded" && app.opPhase !== "Running";
  return (
    <div data-testid="argo-app-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: app.uid || undefined }}
        kind="Application"
        namespace={app.namespace}
        name={app.name}
        meta={
          <>
            {isProtected && <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>protected · </span>}
            {[`project ${app.project || "default"}`, app.destNamespace ? `→ ${app.destNamespace}` : "", dest === "remote" ? `on ${destLabel}` : dest === "unverified" ? "destination unverified" : "", app.autoSync ? "auto sync" : "manual sync"].filter(Boolean).join(" · ")}
          </>
        }
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind="Application" /> : unverified ? <UnverifiedNotice kind="Application" /> : (
          <div>
            <InspectorStatus tone={tone} kind={role.kind} label={`${app.healthStatus || "Unknown"} · ${app.syncStatus || "Unknown"}`} reason={badResources.length > 0 ? `${badResources.length} resource${badResources.length === 1 ? " needs" : "s need"} attention` : undefined} detail={app.revision ? short(app.revision) : undefined} />
            {conditions.map((c, i) => (
              <ClampedText key={`${c.type}/${i}/${c.message}`} className="t-body" lines={2} prefix={`${c.type}: `} text={c.message || "-"} style={{ color: conditionSeverityColor(c.type), overflowWrap: "anywhere", marginTop: i === 0 ? -4 : 2 }} />
            ))}
            {opFailed && app.opMessage && (
              <ClampedText key={app.opMessage} className="t-body" lines={2} prefix={`Last operation ${app.opPhase}: `} text={app.opMessage} style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere", marginTop: 2 }} />
            )}
          </div>
        )}
        primary={live ? { label: "Refresh", kbd: "r", onSelect: () => onRefresh(app) } : undefined}
        secondary={live ? { label: "Sync", kbd: "s", onSelect: () => onSync(app) } : undefined}
        more={[...(more), ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Resources" count={resources.length} extra={badResources.length > 0 ? <span className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{badResources.length} need attention</span> : undefined}>
          {dest === "remote" && <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>Managed on {destLabel} - these objects are not on this cluster, so they are not linked.</div>}
          {dest === "unverified" && <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>Destination unverified{destLabel ? ` (${destLabel} - conflicting server and name)` : " (no destination server or name reported)"} - objects are not linked.</div>}
          {resources.length === 0 ? <span className="t-secondary">Argo has not reported resource inventory for this Application.</span> : (
            <div style={{ display: "grid", gap: 4 }}>
              {[...badResources, ...resources.filter((r) => !badResources.includes(r))].map((r) => (
                <ArgoResourceRow key={resourceKey(r)} resource={r} resolvable={local && resolveArgoResourceRef(r, crdGroups) !== null} onOpen={() => openLiveResource(r)} />
              ))}
            </div>
          )}
        </InspectorBlock>
        <InspectorBlock title="Source">
          <div className="insp-kv">
            <span>repo</span><span className="t-identifier" style={{ overflowWrap: "anywhere" }}>{app.repoURL || "-"}</span>
            <span>{app.chart ? "chart" : "path"}</span><span className="t-identifier" style={{ overflowWrap: "anywhere" }}>{app.chart || app.path || "root"}</span>
            <span>target</span><span className="t-identifier">{app.targetRevision || "HEAD"}</span>
            <span>revision</span><span className="t-identifier" style={{ overflowWrap: "anywhere" }}>{app.revision || "-"}</span>
            {app.extraSources > 0 && <><span>extra</span><span>{app.extraSources} more source{app.extraSources === 1 ? "" : "s"}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Destination">
          <div className="insp-kv">
            <span>namespace</span><span className="t-identifier">{app.destNamespace || "-"}</span>
            <span>project</span><span className="t-identifier" style={{ color: project || projectsCov === "unknown" ? undefined : "var(--color-text-warning)" }}>
              {app.project || "default"}{projectsCov === "unknown" ? " (project read unavailable - unverified)" : project ? (projectsCov === "stale" ? " (last good read)" : "") : " (not found in the project read)"}
            </span>
            <span>appset</span><span className="t-identifier">
              {/* the Application's own owner record is authoritative; a name
                  join against the ApplicationSet list needs that list */}
              {app.applicationSet ? app.applicationSet : appSetsCov === "unknown" ? "unverified (ApplicationSet read unavailable)" : appSet ? `${appSet.name}${appSetsCov === "stale" ? " (last good read)" : ""}` : "-"}
            </span>
          </div>
        </InspectorBlock>
        {/* collapsed: a failed operation's message already reads in the verdict */}
        <InspectorBlock title="Operation" defaultOpen={false}>
          <div className="insp-kv">
            <span>last</span><span style={{ color: opFailed ? "var(--color-text-warning)" : undefined }}>{app.opPhase || "-"}</span>
            <span>message</span><span style={{ overflowWrap: "anywhere" }}>{app.opMessage || "-"}</span>
            <span>reconciled</span><span><Age unixSeconds={app.reconciledUnix} suffix=" ago" fallback="never" /></span>
          </div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}

function ProjectInspector({ cluster, project, removed, unverified, apps, appsCov, appSets, appSetsCov, onClose }: { cluster: string; project: ArgoProjectDTO; removed: boolean; unverified: boolean; apps: ArgoAppDTO[]; appsCov: AppCoverage; appSets: ArgoApplicationSetDTO[]; appSetsCov: AppCoverage; onClose: () => void }) {
  const risks = projectRiskBadges(project, apps, appsCov);
  const role = riskRole(risks);
  return (
    <div data-testid="argo-project-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster }}
        kind="AppProject"
        namespace={project.namespace}
        name={project.name}
        meta={`${appsCov === "unknown" ? "apps unverified" : `${apps.length} app${apps.length === 1 ? "" : "s"}${appsCov === "stale" ? " (last good read)" : ""}`} · ${appSetsCov === "unknown" ? "ApplicationSets unverified" : `${appSets.length} ApplicationSet${appSets.length === 1 ? "" : "s"}${appSetsCov === "stale" ? " (last good read)" : ""}`}${project.description ? ` · ${project.description}` : ""}`}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind="AppProject" /> : unverified ? <UnverifiedNotice kind="AppProject" /> : (
          <div style={{ display: "flex", flexDirection: "column", gap: 6, marginBottom: 12 }}>
            <InspectorStatus tone={role.kind === "degraded" ? "warning" : "success"} kind={role.kind} label={role.label} />
            {risks.length > 0 && <div style={{ display: "flex", flexWrap: "wrap", gap: 5 }}>{risks.map((r) => <RiskBadge key={r.label} tone={r.tone}>{r.label}</RiskBadge>)}</div>}
          </div>
        )}
      >
        <InspectorSection title="Sources"><ValueList values={project.sourceRepos} empty="no source repos declared" /></InspectorSection>
        <InspectorSection title="Destinations"><ValueList values={project.destinations} empty="no destinations declared" /></InspectorSection>
        <InspectorSection title="Resource policy" collapsible defaultCollapsed>
          <RuleList label="cluster allow" rules={project.clusterResourceWhitelist} />
          <RuleList label="cluster deny" rules={project.clusterResourceBlacklist} />
          <RuleList label="namespace allow" rules={project.namespaceResourceWhitelist} />
          <RuleList label="namespace deny" rules={project.namespaceResourceBlacklist} />
        </InspectorSection>
        <InspectorSection title="Roles" collapsible defaultCollapsed>
          <ValueList values={project.roleNames ?? []} empty={project.roles > 0 ? `${project.roles} role${project.roles === 1 ? "" : "s"}` : "no project roles"} />
        </InspectorSection>
        <InspectorSection title="Applications" count={appsCov === "unknown" ? undefined : apps.length}>
          {appsCov === "unknown" ? <div style={mutedLine}>Application read unavailable - links unverified, not zero.</div> : (
            <>
              {appsCov === "stale" && <div style={mutedLine}>From the last good Application read; the latest read failed.</div>}
              <LinkedApps apps={apps} />
            </>
          )}
        </InspectorSection>
        <InspectorSection title="ApplicationSets" count={appSetsCov === "unknown" ? undefined : appSets.length} collapsible defaultCollapsed>
          {appSetsCov === "unknown" ? <div style={mutedLine}>ApplicationSet read unavailable - unverified, not zero.</div> : (
            <>
              {appSetsCov === "stale" && <div style={mutedLine}>From the last good ApplicationSet read; the latest read failed.</div>}
              <ValueList values={appSets.map((a) => a.name)} empty="no ApplicationSets target this project" />
            </>
          )}
        </InspectorSection>
      </InspectorLayout>
    </div>
  );
}

// adverseAppSetConditions applies each ApplicationSet condition's known
// polarity, matching the backend's appSetConditionBroken (internal/gitops/argo):
// ErrorOccurred is adverse when True (it carries the real error);
// ParametersGenerated / ResourcesUpToDate are adverse when not True. Unknown
// types make no claim. The real error leads.
const APPSET_POLARITY: Record<string, "true-is-bad" | "false-is-bad"> = {
  ErrorOccurred: "true-is-bad",
  ParametersGenerated: "false-is-bad",
  ResourcesUpToDate: "false-is-bad",
};
function adverseAppSetConditions(appSet: ArgoApplicationSetDTO) {
  const conds = arrayOf(appSet.conditions);
  const adverse = conds.filter((c) =>
    (APPSET_POLARITY[c.type] === "true-is-bad" && c.status === "True") ||
    (APPSET_POLARITY[c.type] === "false-is-bad" && c.status === "False"));
  return adverse.sort((a, b) => Number(b.type === "ErrorOccurred") - Number(a.type === "ErrorOccurred"));
}
// A known-polarity condition whose status is neither True nor False is
// unverified evidence: shown neutrally, never as a diagnosis. (The backend's
// health flag counts it, so an unhealthy set can carry only unverified lines.)
function unverifiedAppSetConditions(appSet: ArgoApplicationSetDTO) {
  return arrayOf(appSet.conditions).filter((c) => APPSET_POLARITY[c.type] && c.status !== "True" && c.status !== "False");
}

function ApplicationSetInspector({ cluster, appSet, removed, unverified: selectionUnverified, allApps, appsCov, project, onClose }: { cluster: string; appSet: ArgoApplicationSetDTO; removed: boolean; unverified: boolean; allApps: ArgoAppDTO[]; appsCov: AppCoverage; project: ArgoProjectDTO | null; onClose: () => void }) {
  const ai = useInspectorAI(appSet.uid ? { kind: "row" as const, task: "argo", part: "applicationsets", uid: appSet.uid } : null, `ApplicationSet/${appSet.namespace}/${appSet.name}`, !removed && !selectionUnverified, cluster);
  const known = appsCov !== "unknown";
  const adverse = adverseAppSetConditions(appSet);
  const unverified = unverifiedAppSetConditions(appSet);
  const condition = adverse[0] ?? null;
  const generatedNames = arrayOf(appSet.generatedNames);
  const liveApps = generatedAppsForSet(allApps, appSet);
  const missing = missingGeneratedNames(appSet, allApps);
  const projectApps = appSet.project ? appsForProject(allApps, appSet.project) : [];
  const extraProjectApps = generatedNames.length > 0 ? projectApps.filter((a) => !generatedNames.includes(a.name)) : [];
  const expected = generatedNames.length > 0 ? generatedNames.length : appSet.generatedApps;
  return (
    <div data-testid="argo-appset-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: appSet.uid || undefined }}
        kind="ApplicationSet"
        namespace={appSet.namespace}
        name={appSet.name}
        meta={appSet.generators.join(", ") || "generator unknown"}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind="ApplicationSet" /> : selectionUnverified ? <UnverifiedNotice kind="ApplicationSet" /> : (
          <div>
            <InspectorStatus
              tone={!appSet.rule.included ? "success" : adverse.length > 0 ? "warning" : "muted"}
              label={!appSet.rule.included ? "Healthy" : adverse.length > 0 ? "Needs attention" : "Unverified"}
              reason={condition ? condition.reason || condition.type : undefined}
            />
            {adverse.map((c, i) => (
              <div key={`${c.type}/${i}`} className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere", marginTop: i === 0 ? -4 : 2 }}>{c.type}: {c.message || c.reason || c.status}</div>
            ))}
            {unverified.map((c, i) => (
              <div key={`u/${c.type}/${i}`} className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: 2 }}>{c.type} is {c.status || "unreported"} (unverified){c.message ? `: ${c.message}` : ""}</div>
            ))}
          </div>
        )}
        more={ai.menu}
      >
        {ai.block}
        <InspectorSection title="Generated drift">
          <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 8 }}>
            <MetricTile label="expected" value={String(expected)} tone={expected === 0 ? "warning" : undefined} />
            <MetricTile label="actual" value={known ? String(liveApps.length) : "?"} tone={known && liveApps.length < expected ? "warning" : undefined} />
            <MetricTile label="missing" value={known ? String(missing.length) : "?"} tone={known && missing.length > 0 ? "warning" : undefined} />
          </div>
          {!known && <div style={mutedLine}>Application read unavailable - actual and missing are unverified, not zero.</div>}
          {appsCov === "stale" && <div style={mutedLine}>Actual and missing come from the last good Application read; the latest read failed.</div>}
          {known && missing.length > 0 && <ValueList values={missing} empty="no missing generated Applications" />}
        </InspectorSection>
        <InspectorSection title="Live application state" count={known ? liveApps.length : undefined}>
          {known ? <LinkedApps apps={liveApps} /> : <div style={mutedLine}>Application read unavailable.</div>}
        </InspectorSection>
        <InspectorSection title="Template" collapsible defaultCollapsed>
          <MetaLine label="name" value={appSet.templateName || "-"} />
          <MetaLine label="project" value={appSet.project || "-"} tone={project ? undefined : "warning"} />
          <MetaLine label="source" value={appSet.repoURL ? `${appSet.repoURL}${appSet.path ? ` · ${appSet.path}` : ""}` : "-"} />
          <MetaLine label="revision" value={appSet.targetRevision || "HEAD"} />
          <MetaLine label="destination" value={appSet.destNamespace || appSet.destServer || "-"} />
        </InspectorSection>
        <InspectorSection title="Generated applications" collapsible defaultCollapsed>
          {generatedNames.length > 0 ? <ValueList values={generatedNames} /> : <ValueList values={liveApps.map((a) => a.name)} empty="Argo has not reported generated resource names yet" />}
        </InspectorSection>
        {extraProjectApps.length > 0 && (
          <InspectorSection title="Same-project applications" collapsible defaultCollapsed><LinkedApps apps={extraProjectApps} /></InspectorSection>
        )}
        {generatedNames.length === 0 && appSet.generatedApps > 0 && (
          <div style={mutedLine}>Argo reported {appSet.generatedApps} generated resource{appSet.generatedApps === 1 ? "" : "s"} but did not include generated Application names.</div>
        )}
      </InspectorLayout>
    </div>
  );
}

function MetricTile({ label, value, tone }: { label: string; value: string; tone?: "warning" }) {
  return (
    <div style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, padding: "7px 8px", background: "var(--color-background-secondary)", minWidth: 0 }}>
      <div style={fieldLabel}>{label}</div>
      <div style={{ marginTop: 4, fontFamily: "var(--font-mono)", fontSize: 16, color: tone === "warning" ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>{value}</div>
    </div>
  );
}

function ArgoResourceRow({ resource: r, resolvable, onOpen }: { resource: ArgoResourceDTO; resolvable: boolean; onOpen: () => void }) {
  const body = (
    <>
      <span style={{ color: "var(--color-text-secondary)", fontSize: 10 }}>{r.kind}</span>
      <span style={{ ...monoLine, ...ellipsis }} title={`${r.namespace}/${r.name}`}>{r.namespace ? `${r.namespace}/` : ""}{r.name}</span>
      <span style={{ color: resourceTone(r), fontSize: 10, textAlign: "right" }}>{r.status || r.health || "-"}</span>
    </>
  );
  const style: React.CSSProperties = {
    display: "grid",
    gridTemplateColumns: "82px minmax(0,1fr) 76px",
    gap: 7,
    alignItems: "center",
    width: "100%",
    padding: 0,
    border: "none",
    background: "transparent",
    color: "var(--color-text-primary)",
    textAlign: "left",
    fontFamily: "var(--font-mono)",
    cursor: resolvable ? "pointer" : "default",
  };

  if (!resolvable) return <div style={style}>{body}</div>;
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={`open ${r.kind} ${r.namespace ? `${r.namespace}/` : ""}${r.name}`}
      title="Open live Kubernetes resource"
      style={style}
    >
      {body}
    </button>
  );
}

type Risk = { label: string; tone: "success" | "warning" | "danger" | "muted" };

function projectKey(p: ArgoProjectDTO): string {
  return `${p.namespace}/${p.name}`;
}

function appSetKey(a: ArgoApplicationSetDTO): string {
  return `${a.namespace}/${a.name}`;
}

function appsForProject(apps: ArgoAppDTO[], project: string): ArgoAppDTO[] {
  return apps.filter((a) => a.project === project).sort((a, b) => Number(b.rule.included) - Number(a.rule.included) || a.name.localeCompare(b.name));
}

function appSetsForProject(appSets: ArgoApplicationSetDTO[], project: string): ArgoApplicationSetDTO[] {
  return appSets.filter((a) => a.project === project).sort((a, b) => Number(b.rule.included) - Number(a.rule.included) || a.name.localeCompare(b.name));
}

function generatedAppsForSet(apps: ArgoAppDTO[], appSet: ArgoApplicationSetDTO): ArgoAppDTO[] {
  const names = new Set(arrayOf(appSet.generatedNames));
  return apps
    .filter((a) => names.has(a.name) || a.applicationSet === appSet.name)
    .sort((a, b) => Number(b.rule.included) - Number(a.rule.included) || a.name.localeCompare(b.name));
}

function missingGeneratedNames(appSet: ArgoApplicationSetDTO, apps: ArgoAppDTO[]): string[] {
  const generatedNames = arrayOf(appSet.generatedNames);
  if (generatedNames.length === 0) return [];
  const liveNames = new Set(generatedAppsForSet(apps, appSet).map((a) => a.name));
  return generatedNames.filter((name) => !liveNames.has(name));
}

function applicationSetForApp(appSets: ArgoApplicationSetDTO[], app: ArgoAppDTO): ArgoApplicationSetDTO | null {
  return appSets.find((a) => a.name === app.applicationSet || arrayOf(a.generatedNames).includes(app.name)) ?? null;
}

function matchesText(values: Array<string | undefined>, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return values.some((v) => (v ?? "").toLowerCase().includes(q));
}

function matchesAppQuery(app: ArgoAppDTO, query: string): boolean {
  return matchesText([app.namespace, app.name, app.project, app.destNamespace, app.syncStatus, app.healthStatus, app.repoURL, app.path, app.chart, app.applicationSet], query);
}

function matchesProjectQuery(project: ArgoProjectDTO, query: string): boolean {
  return matchesText([project.namespace, project.name, project.description, ...arrayOf(project.sourceRepos), ...arrayOf(project.destinations), ...arrayOf(project.roleNames)], query);
}

function matchesAppSetQuery(appSet: ArgoApplicationSetDTO, query: string): boolean {
  return matchesText([appSet.namespace, appSet.name, appSet.project, appSet.repoURL, appSet.path, appSet.destNamespace, appSet.destServer, ...arrayOf(appSet.generators), ...arrayOf(appSet.generatedNames)], query);
}

function projectRiskScore(p: ArgoProjectDTO, apps: ArgoAppDTO[], appsCov: AppCoverage = "complete"): number {
  return projectRiskBadges(p, apps, appsCov).reduce((sum, r) => sum + (r.tone === "danger" ? 4 : r.tone === "warning" ? 2 : 1), 0);
}

// Application coverage for cross-kind joins: a project's "unused" or an
// ApplicationSet's "missing generated apps" are conclusions about the
// Application read, so they need that read to have succeeded. "stale" = the
// last good read is retained after a failure; "unknown" = no successful read.
type AppCoverage = "complete" | "stale" | "unknown";
// the same coverage notion for any of the three reads
// observedCoverage: a rows-backed kind's coverage from the Observer (the
// fallback is the legacy inference, for a slice seeded without coverage).
function observedCoverage(mine: boolean, coverage: KindCoverageDTO[] | undefined, kind: string, fallback: AppCoverage): AppCoverage {
  const k = mine ? coverage?.find((c) => c.kind === kind) : undefined;
  if (!k?.coverage) return fallback;
  return k.coverage === "live" || k.coverage === "absent" ? "complete" : k.coverage === "stale" ? "stale" : "unknown";
}
const coverageOf = (mine: boolean, loading: boolean, error: string | null | undefined, rows: number): AppCoverage =>
  !mine ? "unknown" : error ? (rows > 0 ? "stale" : "unknown") : loading && rows === 0 ? "unknown" : "complete";
// countText renders a join count honestly: "?" with no evidence, a trailing
// "*" when derived from a retained last-good read.
const countText = (n: number, cov: AppCoverage) => (cov === "unknown" ? "?" : `${n}${cov === "stale" ? "*" : ""}`);

function projectRiskBadges(p: ArgoProjectDTO, apps: ArgoAppDTO[], appsCov: AppCoverage = "complete"): Risk[] {
  const risks: Risk[] = [];
  const repos = arrayOf(p.sourceRepos);
  const destinations = arrayOf(p.destinations);
  if (repos.length === 0) risks.push({ label: "no repos", tone: "warning" });
  if (repos.some(hasWildcard)) risks.push({ label: "wildcard repo", tone: "danger" });
  if (destinations.length === 0) risks.push({ label: "no destinations", tone: "warning" });
  if (destinations.some(hasWildcard)) risks.push({ label: "all destinations", tone: "danger" });
  if (arrayOf(p.clusterResourceWhitelist).some(ruleWildcard)) risks.push({ label: "cluster wildcard", tone: "danger" });
  if (arrayOf(p.namespaceResourceWhitelist).some(ruleWildcard)) risks.push({ label: "namespace wildcard", tone: "warning" });
  if (appsCov !== "unknown" && !p.orphanWarn && apps.length > 0) risks.push({ label: "orphan warn off", tone: "warning" });
  if (appsCov !== "unknown" && apps.length === 0) risks.push({ label: "unused", tone: "muted" });
  return risks;
}

function hasWildcard(value: string): boolean {
  return value === "*" || value.includes("*");
}

function ruleWildcard(rule: ArgoResourceRuleDTO): boolean {
  return hasWildcard(rule.group || "") || hasWildcard(rule.kind || "");
}

function RiskBadge({ children, tone }: { children: React.ReactNode; tone: Risk["tone"] }) {
  const colors: Record<Risk["tone"], { color: string; border: string; bg: string }> = {
    success: { color: "var(--color-text-success)", border: "var(--color-border-success, var(--color-border-tertiary))", bg: "var(--color-background-secondary)" },
    warning: { color: "var(--color-text-warning)", border: "var(--color-border-warning, var(--color-border-tertiary))", bg: "var(--color-background-secondary)" },
    danger: { color: "var(--color-text-danger)", border: "var(--color-border-danger, var(--color-border-tertiary))", bg: "var(--color-background-secondary)" },
    muted: { color: "var(--color-text-tertiary)", border: "var(--color-border-tertiary)", bg: "var(--color-background-secondary)" },
  };
  const c = colors[tone];
  return <span style={{ display: "inline-flex", alignItems: "center", maxWidth: "100%", border: `0.5px solid ${c.border}`, borderRadius: 4, padding: "1px 5px", background: c.bg, color: c.color, fontSize: 10, lineHeight: 1.4, whiteSpace: "nowrap" }}>{children}</span>;
}

function ValueList({ values, empty }: { values: string[]; empty?: string }) {
  const items = arrayOf(values).filter(Boolean);
  if (items.length === 0) return <div style={mutedLine}>{empty ?? "-"}</div>;
  return (
    <div style={{ display: "grid", gap: 4 }}>
      {items.map((v) => <div key={v} style={{ ...monoLine, overflowWrap: "anywhere" }}>{v}</div>)}
    </div>
  );
}

function RuleList({ label, rules }: { label: string; rules?: ArgoResourceRuleDTO[] }) {
  const items = arrayOf(rules);
  return (
    <div style={{ display: "grid", gridTemplateColumns: "92px minmax(0,1fr)", gap: 8, alignItems: "start" }}>
      <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{label}</span>
      {items.length === 0 ? <span style={mutedLine}>-</span> : (
        <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
          {items.map((r, i) => <RiskBadge key={`${r.group}/${r.kind}/${i}`} tone={ruleWildcard(r) ? "warning" : "muted"}>{`${r.group || "core"}/${r.kind || "*"}`}</RiskBadge>)}
        </span>
      )}
    </div>
  );
}

function LinkedApps({ apps }: { apps: ArgoAppDTO[] }) {
  if (apps.length === 0) return <div style={mutedLine}>no linked Applications loaded</div>;
  return (
    <div style={{ display: "grid", gap: 4 }}>
      {apps.map((a) => {
        // The lamp is the evaluated presentation role, never inclusion:
        // Progressing and Suspended are excluded yet not healthy-green.
        const role = roleOf(a);
        return (
          <div key={keyOf(a)} data-testid="linked-app" style={{ display: "grid", gridTemplateColumns: "8px minmax(0,1fr) 82px", gap: 7, alignItems: "center" }}>
            <RowLamp role={role} />
            <span style={{ ...monoLine, ...ellipsis }} title={keyOf(a)}>{a.name}</span>
            <span title={role.label} style={{ color: ROLE_COLOR[role.kind] ?? "var(--color-text-tertiary)", fontSize: 10 }}>{a.syncStatus}</span>
          </div>
        );
      })}
    </div>
  );
}

function MetaLine({ label, value, tone }: { label: string; value: React.ReactNode; tone?: "warning" }) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "72px minmax(0,1fr)", gap: 8, alignItems: "center" }}>
      <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{label}</span>
      <span style={{ ...monoLine, overflowWrap: "anywhere", color: tone === "warning" ? "var(--color-text-warning)" : monoLine.color }}>{value}</span>
    </div>
  );
}

function SyncApplicationDialog({
  cluster,
  app,
  isProtected,
  onCancel,
  onConfirm,
}: {
  cluster: string;
  app: ArgoAppDTO;
  isProtected: boolean;
  onCancel: () => void;
  onConfirm: (options: ArgoSyncOptions) => void;
}) {
  const defaultRevision = app.targetRevision || "";
  const resources = app.resources ?? [];
  const [revision, setRevision] = useState(defaultRevision);
  const [strategy, setStrategy] = useState<"hook" | "apply">("hook");
  const [dryRun, setDryRun] = useState(false);
  // Prune deletes live resources - default OFF, matching Argo's own UI. The
  // M6 contract shipped "sync (never prune)"; the tile is a deliberate
  // post-M6 addition, but it must always be an explicit opt-in per sync.
  const [prune, setPrune] = useState(false);
  const [force, setForce] = useState(false);
  const [replace, setReplace] = useState(false);
  const [serverSideApply, setServerSideApply] = useState(false);
  const [selectedResources, setSelectedResources] = useState<string[]>([]);
  const source = app.chart ? `chart ${app.chart}` : app.path || "root";
  const selected = new Set(selectedResources);
  const syncResources = resources.filter((r) => selected.has(resourceKey(r)));

  return (
    <OperationDialog
      title={`Sync Application "${app.name}"`}
      ariaLabel={`Sync Application ${app.name}`}
      subtitle="Starts an Argo CD sync operation with explicit Argo options and fast Klyx defaults."
      cluster={cluster}
      protected={isProtected}
      danger={prune}
      confirmLabel="Synchronize"
      onCancel={onCancel}
      onConfirm={() => onConfirm({
        revision: revision.trim(),
        strategy,
        prune,
        dryRun,
        force,
        replace,
        serverSideApply,
        resources: syncResources,
      })}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <MetaTile label="application" value={`${app.namespace}/${app.name}`} />
        <MetaTile label="destination" value={app.destNamespace || "-"} />
        <MetaTile label="source" value={`${app.repoURL || "-"} - ${source}`} />
        <MetaTile label="current revision" value={app.revision ? short(app.revision) : "-"} title={app.revision} />
      </div>

      <label style={{ display: "grid", gap: 6 }}>
        <span style={fieldLabel}>revision (git ref)</span>
        <input
          autoFocus={!isProtected}
          aria-label="revision git ref"
          value={revision}
          onChange={(e) => setRevision(e.target.value)}
          placeholder="default: spec.source.targetRevision"
          style={inputStyle}
        />
      </label>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={fieldLabel}>strategy</div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
          <OptionTile
            active={strategy === "hook"}
            title="Hook (recommended)"
            body="Respect sync waves and pre/post-sync hooks. This matches Argo CD's normal operation."
            onClick={() => setStrategy("hook")}
          />
          <OptionTile
            active={strategy === "apply"}
            title="Apply"
            body="Apply resources directly. Skips hooks and is useful when hooks are the problem."
            onClick={() => setStrategy("apply")}
          />
        </div>
      </div>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={fieldLabel}>options</div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 10 }}>
          <OptionTile active={dryRun} title="Dry-run" body="Ask the apiserver what would happen without applying changes." onClick={() => setDryRun((v) => !v)} />
          <OptionTile active={prune} title="Prune" body="Delete resources that are in cluster but no longer in Git." onClick={() => setPrune((v) => !v)} />
          <OptionTile active={force} title="Force" body="Pass Force=true to Argo syncOptions for conflict override." onClick={() => setForce((v) => !v)} />
          <OptionTile active={replace} title="Replace" body="Use replace semantics via Replace=true syncOption." onClick={() => setReplace((v) => !v)} />
          <OptionTile active={serverSideApply} title="Server-side apply" body="Use ServerSideApply=true for field ownership-aware apply." onClick={() => setServerSideApply((v) => !v)} />
        </div>
      </div>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={fieldLabel}>selective sync {selectedResources.length > 0 ? `(${selectedResources.length})` : "(all)"}</div>
        {resources.length === 0 ? (
          <div style={{ color: "var(--color-text-tertiary)", fontSize: 12, border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, padding: "9px 10px", background: "var(--color-background-secondary)" }}>
            Argo has not reported resource inventory for this application yet. Synchronize applies all resources.
          </div>
        ) : (
          <div style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, overflow: "hidden", maxHeight: 168, overflowY: "auto", background: "var(--color-background-secondary)" }}>
            {resources.map((r) => {
              const key = resourceKey(r);
              const checked = selected.has(key);
              return (
                <label key={key} style={{ display: "grid", gridTemplateColumns: "18px 110px minmax(0, 1fr) 82px", gap: 8, alignItems: "center", padding: "7px 10px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontFamily: "var(--font-mono)", fontSize: 11, cursor: "pointer" }}>
                  <input
                    aria-label={`select ${r.kind} ${r.namespace}/${r.name}`}
                    type="checkbox"
                    checked={checked}
                    onChange={() => setSelectedResources((prev) => checked ? prev.filter((v) => v !== key) : [...prev, key])}
                  />
                  <span>{r.kind}</span>
                  <span style={ellipsis}>{r.namespace ? `${r.namespace}/` : ""}{r.name}</span>
                  <span style={{ color: resourceTone(r), textAlign: "right" }}>{r.status || r.health || "-"}</span>
                </label>
              );
            })}
          </div>
        )}
      </div>
    </OperationDialog>
  );
}

function resourceKey(r: ArgoResourceDTO): string {
  return `${r.group}/${r.kind}/${r.namespace}/${r.name}`;
}

function resourceTone(r: ArgoResourceDTO): string {
  if (r.status === "OutOfSync" || r.health === "Degraded" || r.health === "Missing") return "var(--color-text-warning)";
  if (r.status === "Synced" || r.health === "Healthy") return "var(--color-text-success)";
  return "var(--color-text-tertiary)";
}


const mutedLine: React.CSSProperties = { color: "var(--color-text-tertiary)", fontSize: 11 };
const monoLine: React.CSSProperties = { fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)" };
const fieldLabel: React.CSSProperties = { color: "var(--color-text-tertiary)", fontSize: 10, textTransform: "uppercase", fontWeight: 600, letterSpacing: 0.2 };
const inputStyle: React.CSSProperties = { width: "100%", boxSizing: "border-box", padding: "8px 10px", fontFamily: "var(--font-mono)", fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 5, color: "var(--color-text-primary)", outline: "none" };
