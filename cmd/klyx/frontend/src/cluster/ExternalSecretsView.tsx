import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "../store/fleet";
import { useNavIntent } from "../chrome/useNavIntent";
import { kindOfRef } from "../nav/reveal";
import type { ExternalSecretDTO, ExternalSecretStoreDTO, RuleDTO } from "../store/fleet";
import { useExternalSecretsRows } from "../health/useExternalSecretsRows";
import { useNow } from "../chrome/Age";
import { useListKeys } from "../chrome/useListKeys";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { IconAlertTriangle, IconShieldLock } from "@tabler/icons-react";
import { SECRET_REF, wellKnownResourceRef } from "./resourceRefs";
import { InspectorStatus } from "./ResourceInspector";
import { VerdictLamp } from "../health/VerdictView";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { clock, combineEvidence, coverageGaps, joinCoverage, kindEvidence, lastGoodAt, type JoinCoverage, type KindEvidence } from "../layout/coverage";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";

const keyOf = (e: { namespace: string; name: string }) => `${e.namespace}/${e.name}`;

// refreshText states the secret's refresh policy in ESO's own terms: only
// Periodic refreshes on the interval (the stale check applies to it alone).
function refreshText(e: ExternalSecretDTO): string {
  if (e.refreshPolicy === "CreatedOnce") return "created once - no periodic refresh (re-synced if the target Secret is changed or deleted)";
  if (e.refreshPolicy === "OnChange") return "on metadata or spec change - no periodic refresh";
  if (e.refreshPolicy && e.refreshPolicy !== "Periodic") return e.refreshPolicy;
  return e.refreshIntervalSeconds > 0 ? `every ${fmtDuration(e.refreshIntervalSeconds)}` : "disabled";
}

function fmtDuration(secs: number): string {
  if (secs <= 0) return "0s";
  if (secs < 60) return `${Math.floor(secs)}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`;
  return `${Math.floor(secs / 86400)}d`;
}

// Age 0 is both "never" and "under a second ago"; the presence fact decides.
// Older payloads without `refreshed` fall back to age > 0.
const hasRefreshed = (e: ExternalSecretDTO) => e.refreshed ?? e.lastRefreshAgeSeconds > 0;

// ageOf ticks the age since the last sync from status.refreshTime (shared
// rows are pushed only when they change - slice 3 §3); the capture-relative
// age is the fallback for a payload without the absolute time.
const ageOf = (e: ExternalSecretDTO, nowS: number) => (e.lastRefreshUnix ? Math.max(0, nowS - e.lastRefreshUnix) : e.lastRefreshAgeSeconds);

function refreshedText(e: ExternalSecretDTO, nowS: number): string {
  return hasRefreshed(e) ? fmtDuration(ageOf(e, nowS)) : "—";
}

function staleTitle(e: ExternalSecretDTO, nowS: number): string {
  return `last synced ${fmtDuration(ageOf(e, nowS))} ago - refresh interval ${fmtDuration(e.refreshIntervalSeconds)}; the controller has stopped refreshing`;
}


function secretRank(e: ExternalSecretDTO): number {
  if (e.state === "Failed") return 0;
  if (e.stale) return 1;
  if (e.state !== "Synced") return 2;
  return 3;
}

function storeRank(s: ExternalSecretStoreDTO): number {
  if (s.state === "Failed") return 0;
  if (s.state !== "Synced") return 1;
  return 2;
}

// resolveStore finds the bound (Cluster)SecretStore for an ExternalSecret. A
// ClusterSecretStore is cluster-scoped; a SecretStore is matched within the
// secret's namespace.
function resolveStore(e: ExternalSecretDTO, stores: ExternalSecretStoreDTO[]): ExternalSecretStoreDTO | undefined {
  return stores.find(
    (s) => s.kind === e.storeKind && s.name === e.storeName && (s.kind === "ClusterSecretStore" || s.namespace === e.namespace),
  );
}

function matchesQuery(e: ExternalSecretDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [e.namespace, e.name, e.storeName, e.targetSecret, e.reason].some((v) => v.toLowerCase().includes(q));
}

// ExternalSecretsView - the External Secrets Operator lens on the workspace
// template (spec §6 step 4d). Two views: ExternalSecrets (broken first, then
// staleness) and Stores. ESO-native refresh staleness is kept: a Synced
// secret whose last sync is older than 2x its refresh interval is "stale" -
// never shown as confidently synced. Read-only lens; the documented domain
// variant of the action band is Open target Secret (primary) with the
// manifest in More. The bound (Cluster)SecretStore's health is the headline.

type EsoView = "secrets" | "stores";
type EsoStatus = "all" | "attention" | "stale";

// The verdict is the relocated rule computed in Go (1c addendum §2):
// rule.included is the attention filter (not confidently Synced, or refresh
// overdue), rule.role/label the presentation.
const roleOf = (x: { rule: RuleDTO }) => ({ kind: x.rule.role, label: x.rule.label });
const included = (x: { rule: RuleDTO }) => x.rule.included;
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)",
  unknown: "var(--color-text-tertiary)",
};
const roleTone = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
function Lamp({ role }: { role: { kind: string; label: string } }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const storeKey = (s: ExternalSecretStoreDTO) => `${s.kind}/${s.namespace}/${s.name}`;

const SECRET_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "ExternalSecret", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 170, priority: 1 },
  { id: "refreshed", label: "Last sync", width: 76, priority: 1 },
  { id: "store", label: "Store", width: 150, priority: 2, grow: 0.6 },
  { id: "target", label: "Target Secret", width: 140, priority: 3, grow: 0.5 },
];
const STORE_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Store", width: 240, priority: 0, flex: true },
  { id: "kind", label: "Kind", width: 140, priority: 2 },
  { id: "provider", label: "Provider", width: 110, priority: 3 },
  { id: "status", label: "Status", width: 200, priority: 1, grow: 0.8 },
  { id: "secrets", label: "ExternalSecrets", width: 112, priority: 4 },
];

export function ExternalSecretsView({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.externalSecretsTier ?? "Unknown");
  const eso = useFleet((s) => s.externalsecrets);
  const selectSecret = useFleet((s) => s.selectExternalSecret);
  const [view, setView] = useState<EsoView>("secrets");
  const [status, setStatus] = useState<EsoStatus>("all");
  const [query, setQuery] = useState("");
  const [namespace, setNamespace] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [storeSel, setStoreSel] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const absent = tier === "Absent";

  // The data are the Observer's shared rows (1c slice 3 §5.2): the same
  // capture as the cluster summary's eso task, never a watch of the lens's own.
  useExternalSecretsRows(absent ? null : cluster);
  const nowS = Math.floor(useNow() / 1000);

  const mine = eso.cluster === cluster;
  const secrets = useMemo(() => (mine ? eso.secrets : []), [mine, eso.secrets]);
  const stores = useMemo(() => (mine ? eso.stores : []), [mine, eso.stores]);
  const watchError = mine ? eso.watchError : null;
  // A4 (slice 3 §4): past the deadline the rows are evidence as of their
  // capture, and the totals are the summary's until a re-read lands.
  const rowsRefreshing = mine && !!eso.rowsState?.refreshing;
  const rowsAsOf = eso.rowsState?.evaluatedAt ? clock(eso.rowsState.evaluatedAt) : "the last read";
  // The global counts are the one count selector's (slice 3 §4a); filtered
  // views and breakdowns stay row-scoped ("as of").
  const globalCounts = mine ? eso.partCounts : undefined;
  // Evidence beside the rows: a stale kind's last good time and a failing
  // discovery re-check (a capability gap, never a downgrade of live rows).
  const lastGood = mine ? lastGoodAt(eso.coverage, ["ExternalSecret", "SecretStore", "ClusterSecretStore"]) : null;
  const discoveryGap = mine ? coverageGaps(eso.coverage ?? []) : null;
  const loaded = mine && !eso.loading;
  // Evidence per kind (layout/coverage.ts): every dataset, claim, auto-select
  // and removal reads the coverage of ITS OWN kinds - a synced ExternalSecret
  // watch never vouches for a pending or forbidden store watch, or the reverse.
  const evidenceOf = useCallback((kind: string): KindEvidence => kindEvidence(loaded, eso.coverage, kind, watchError), [loaded, eso.coverage, watchError]);
  const kindCov = useCallback((kind: string): JoinCoverage => joinCoverage(evidenceOf(kind)), [evidenceOf]);
  const secretsEv = evidenceOf("ExternalSecret");
  const storesEv = combineEvidence([evidenceOf("SecretStore"), evidenceOf("ClusterSecretStore")]);
  const secretsCov = joinCoverage(secretsEv);

  const q = query.trim().toLowerCase();
  const orderedSecrets = useMemo(
    () => [...secrets].sort((a, b) => secretRank(a) - secretRank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [secrets],
  );
  const namespaces = useMemo(() => Array.from(new Set(secrets.map((e) => e.namespace))).sort(), [secrets]);
  const visibleSecrets = useMemo(() => orderedSecrets.filter((e) => {
    if (namespace && e.namespace !== namespace) return false;
    if (status === "attention" && !included(e)) return false;
    if (status === "stale" && !e.stale) return false;
    return matchesQuery(e, q);
  }), [orderedSecrets, namespace, status, q]);
  const orderedStores = useMemo(
    () => [...stores].sort((a, b) => storeRank(a) - storeRank(b) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)),
    [stores],
  );
  const visibleStores = useMemo(() => orderedStores.filter((s) => {
    if (namespace && s.kind === "SecretStore" && s.namespace !== namespace) return false;
    if (status === "attention" && !included(s)) return false;
    return !q || [s.kind, s.namespace, s.name, s.provider, s.reason, s.message].some((v) => (v ?? "").toLowerCase().includes(q));
  }), [orderedStores, namespace, status, q]);
  const secretsFor = useCallback((s: ExternalSecretStoreDTO) => secrets.filter((e) => resolveStore(e, [s]) === s), [secrets]);

  // Selection lifecycle (secrets keyed in the store, stores local).
  const selKey = mine ? eso.selectedKey : null;
  // The selection's last evidence, for this cluster and key only, pinned to
  // its object's UID (The Line slice 4 §6.3): a same-name object with a new
  // UID on a complete read is "replaced", never silently followed. Missing
  // without a complete read of its own kind it is kept with its last row,
  // unverified - never removed (slice 3 §5a).
  const selectedSecret = selKey ? secrets.find((e) => keyOf(e) === selKey) : undefined;
  const secretPin = usePinnedSubject(cluster, selKey, selectedSecret, secretsEv === "complete");
  const secretReplaced = secretPin.gone === "replaced";
  const secretRemoved = secretPin.gone === "removed" || secretReplaced; // the inspector gates on removed
  const secretUnverified = secretPin.gone === "unverified";
  const secretSubject = secretPin.subject;
  const selectedStore = storeSel ? stores.find((s) => storeKey(s) === storeSel) : undefined;
  // a store is removed (or replaced) only when ITS kind's read is complete
  const storePin = usePinnedSubject(cluster, storeSel, selectedStore, !!storeSel && evidenceOf(storeSel.split("/")[0]) === "complete");
  const storeReplaced = storePin.gone === "replaced";
  const storeRemoved = storePin.gone === "removed" || storeReplaced;
  const storeUnverified = storePin.gone === "unverified";
  const storeSubject = storePin.subject;
  useEffect(() => { if (selectedSecret && view === "secrets" && !visibleSecrets.includes(selectedSecret)) selectSecret(null); }, [selectedSecret, visibleSecrets, view, selectSecret]);
  useEffect(() => { if (selectedStore && view === "stores" && !visibleStores.includes(selectedStore)) setStoreSel(null); }, [selectedStore, visibleStores, view, setStoreSel]);
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    setStoreSel(null); setCursorKey(null);
    autoSelect.current = "pending";
    entrySecrets.current = useFleet.getState().externalsecrets.secrets;
  }, [cluster, setStoreSel]);

  // One-shot auto-select of the first secret needing attention.
  const autoSelect = useRef<"pending" | "done">("pending");
  const entrySecrets = useRef<ExternalSecretDTO[] | null>(null);
  if (entrySecrets.current === null) entrySecrets.current = eso.secrets;
  // auto-select waits for the ExternalSecret kind's first complete list
  const freshRead = secretsEv === "complete" && eso.secrets !== entrySecrets.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead || view !== "secrets") return;
    autoSelect.current = "done";
    if (useFleet.getState().externalsecrets.selectedKey) return;
    const e = visibleSecrets.find(included);
    if (e) { setCursorKey(keyOf(e)); selectSecret(keyOf(e)); }
  }, [freshRead, visibleSecrets, view, selectSecret]);

  // A summary Top link (health 1c slice 4 §5.4): open the part's view and
  // select the object past the filters - selection only, never an action.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "eso") return "wait";
    const store = intent.part === "stores";
    const key = store ? `${kindOfRef(intent.ref)}/${intent.ref.namespace ?? ""}/${intent.ref.name}` : `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const found = store ? stores.some((x) => storeKey(x) === key) : secrets.some((e) => keyOf(e) === key);
    if (!found) return (store ? storesEv : secretsEv) === "complete" ? "missing" : "wait";
    autoSelect.current = "done";
    setView(store ? "stores" : "secrets"); setStatus("all"); setQuery(""); setNamespace("");
    setCursorKey(key);
    if (store) setStoreSel(key); else selectSecret(key);
    return "consumed";
  }, [secrets, stores, secretsEv, storesEv]);

  const nav: { key: string; open: () => void }[] = view === "stores"
    ? visibleStores.map((s) => ({ key: storeKey(s), open: () => setStoreSel(storeKey(s)) }))
    : visibleSecrets.map((e) => ({ key: keyOf(e), open: () => { autoSelect.current = "done"; selectSecret(keyOf(e)); } }));
  const cursorIdx = cursorKey === null ? -1 : nav.findIndex((n) => n.key === cursorKey);
  const closeSelected = () => { autoSelect.current = "done"; if (view === "stores") setStoreSel(null); else selectSecret(null); };
  useListKeys({
    count: nav.length,
    selected: cursorIdx,
    onSelect: (idx) => { const n = nav[idx]; if (n) { setCursorKey(n.key); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const n = nav[idx]; if (n) { setCursorKey(n.key); n.open(); } },
    onEscape: closeSelected,
    searchRef,
  });

  const columns = view === "stores" ? STORE_COLUMNS : SECRET_COLUMNS;
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  // global: the count selector's; row-scoped: the breakdown and filters
  const secretTotal = globalCounts?.secrets?.total ?? secrets.length;
  const attention = globalCounts?.secrets?.included ?? secrets.filter(included).length;
  const storeTotal = globalCounts?.stores?.total ?? stores.length;
  const storesNotReady = globalCounts?.stores?.included ?? stores.filter(included).length;
  const failed = secrets.filter((e) => e.state === "Failed").length;
  const stale = secrets.filter((e) => e.stale).length;
  const storeProblems = secrets.filter((e) => { if (kindCov(e.storeKind) === "unknown") return false; const st = resolveStore(e, stores); return !st || st.rule.included; }).length;
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";

  const clearFilters = () => { setStatus("all"); setQuery(""); setNamespace(""); };
  // total: the rows in hand (dataset state, filters, footer); the headline
  // reads the global count
  const total = view === "stores" ? stores.length : secrets.length;
  const globalTotal = view === "stores" ? storeTotal : secretTotal;
  const shown = view === "stores" ? visibleStores.length : visibleSecrets.length;
  const viewEv = view === "stores" ? storesEv : secretsEv;
  let state: DatasetState = "ready";
  if (absent) state = "empty";
  else if (viewEv === "failed" && total === 0) state = "unreadable";
  else if (viewEv === "pending" && total === 0) state = "loading";
  else if (total === 0) state = "empty";
  else if (shown === 0) state = "no-match";
  const noun = view === "stores" ? "stores" : "ExternalSecrets";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={7} label="loading external secret state" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="External Secrets state could not be read." hint={watchError ?? undefined} />
    : state === "empty" ? (absent
      ? <EmptyState icon={<IconShieldLock size={28} stroke={1.2} />} title="External Secrets Operator is not installed on this cluster." hint="Klyx detected no external-secrets.io resources here." />
      : rowsRefreshing && globalTotal > 0 ? <EmptyState icon={<IconShieldLock size={28} stroke={1.2} />} title={`No ${noun} as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${globalTotal}.`} />
      : <EmptyState icon={<IconShieldLock size={28} stroke={1.2} />} title={view === "stores" ? "No secret stores on this cluster." : "No ExternalSecrets on this cluster."} hint={view === "stores" ? undefined : "The operator is installed but no ExternalSecret objects exist yet."} />)
    : <EmptyState icon={<IconShieldLock size={28} stroke={1.2} />} title={`No ${noun} match the current filters.`} hint={`${total} ${noun} on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;
  // pending/failed = a lower bound ("+"); stale = the last observed set ("*",
  // not necessarily a lower bound); never an unqualified count either way
  const partial = viewEv === "complete" ? "" : viewEv === "stale" ? "*" : "+";

  // Notices stack: each is its own evidence, none hides another.
  const statusLine = { minHeight: 28, display: "flex", alignItems: "center" } as const;
  const notices: React.ReactNode[] = [];
  if (watchError && total > 0) notices.push(<WatchErrorBanner key="watch" tool="External Secrets" failure={`watch failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={watchError} />);
  else if (viewEv === "pending" && total > 0) notices.push(<div key="pending" role="status" className="t-secondary" style={statusLine}>Still reading - this {view === "stores" ? "store" : "ExternalSecret"} list is incomplete.</div>);
  if (rowsRefreshing && !absent) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{eso.rowsState?.refreshError ? ` Last re-read failed: ${eso.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap && !absent) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);

  return (
    <LensLayout
      label="External secrets"
      title={view === "stores" ? "Secret stores" : "External secrets"}
      summary={state === "loading" || state === "unreadable" || absent ? null : view === "stores" ? (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{storeTotal}{partial}</span>
          {storesNotReady > 0 ? <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{storesNotReady} not ready</span>
            : <span className="t-secondary">{storesEv === "complete" ? "all stores ready" : storesEv === "stale" ? "none known not ready (last observed)" : "store list incomplete"}</span>}
        </>
      ) : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{secretTotal}{partial}</span>
          {attention > 0
            ? <span className="t-status" style={{ color: failed > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{failed > 0 ? "▲ " : ""}{attention} not synced or stale</span>
            : <span className="t-secondary">{secretsEv === "complete" ? "all synced and refreshing" : secretsEv === "stale" ? "none known not synced (last observed)" : "ExternalSecret list incomplete"}</span>}
          <span className="t-secondary" data-testid="eso-breakdown">{[failed ? `${failed} failed` : "", stale ? `${stale} stale` : "", storeProblems ? `${storeProblems} with store problem` : ""].filter(Boolean).join(" · ")}{(failed || stale || storeProblems) ? asOf : ""}</span>
        </>
      )}
      controls={absent ? undefined : (
        <>
          <div role="group" aria-label="External Secrets view" style={{ display: "inline-flex", gap: 4 }}>
            <button className="l-chip" aria-pressed={view === "secrets"} onClick={() => { setView("secrets"); setStatus("all"); }}>ExternalSecrets</button>
            <button className="l-chip" aria-pressed={view === "stores"} onClick={() => { setView("stores"); setStatus("all"); }}>
              Stores {storesNotReady > 0 && <span style={{ color: "var(--color-text-warning)" }}>{storesNotReady}</span>}
            </button>
          </div>
          <label style={{ display: "flex" }}>
            <span className="sr-only">Namespace</span>
            <select className="l-input" value={namespace} onChange={(e) => setNamespace(e.target.value)}>
              <option value="">All namespaces</option>
              {namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
            </select>
          </label>
          <button className="l-chip" aria-pressed={status === "all"} onClick={() => setStatus("all")}>All</button>
          <button className="l-chip" aria-pressed={status === "attention"} onClick={() => setStatus("attention")}>{view === "stores" ? "Not ready" : "Not synced or refresh overdue"} {(view === "stores" ? storesNotReady : attention) > 0 && <span style={{ color: "var(--color-text-warning)" }}>{view === "stores" ? storesNotReady : attention}</span>}</button>
          {view === "secrets" && stale > 0 && <button className="l-chip" aria-pressed={status === "stale"} onClick={() => setStatus("stale")}>Stale <span style={{ color: "var(--color-text-warning)" }}>{stale}</span></button>}
          <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
            <span className="sr-only">Search External Secrets</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={view === "stores" ? "Search stores" : "Search name, store, target"} />
          </label>
        </>
      )}
      notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          {view === "stores" ? (
            <VirtualList
              ref={listRef}
              items={visibleStores}
              rowHeight={TABLE_ROW_HEIGHT}
              rowKey={(s) => storeKey(s)}
              style={{ flex: 1, minHeight: 0 }}
              render={(s) => {
                const k = storeKey(s);
                const role = roleOf(s);
                return (
                  <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                    aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === storeSel ? "true" : undefined}
                    onClick={() => { setCursorKey(k); setStoreSel(k === storeSel ? null : k); }}
                    onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setStoreSel(k); } }}>
                    {show("lamp") && <Lamp role={role} />}
                    {show("name") && <span className="lens-cell-name" title={`${s.namespace ? `${s.namespace}/` : ""}${s.name}`}>{s.namespace && <span style={{ color: "var(--color-text-tertiary)" }}>{s.namespace} / </span>}{s.name}</span>}
                    {show("kind") && <span style={{ color: "var(--color-text-tertiary)" }}>{s.kind}</span>}
                    {show("provider") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{s.provider || "-"}</span>}
                    {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind] }}>{role.kind === "fault" ? "▲ " : ""}{role.label}{included(s) && s.reason ? ` · ${s.reason}` : ""}</span>}
                    {show("secrets") && <span title={secretsCov === "unknown" ? "ExternalSecret read unavailable" : secretsCov === "stale" ? "from the last observed ExternalSecret list" : undefined}>{secretsCov === "unknown" ? "?" : `${secretsFor(s).length}${secretsCov === "stale" ? "*" : ""}`}</span>}
                  </div>
                );
              }}
            />
          ) : (
            <VirtualList
              ref={listRef}
              items={visibleSecrets}
              rowHeight={TABLE_ROW_HEIGHT}
              rowKey={(e) => keyOf(e)}
              style={{ flex: 1, minHeight: 0 }}
              render={(e) => {
                const k = keyOf(e);
                const role = roleOf(e);
                const storeCov = kindCov(e.storeKind);
                const st = storeCov !== "unknown" ? resolveStore(e, stores) : undefined;
                return (
                  <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                    aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                    onClick={() => { setCursorKey(k); autoSelect.current = "done"; selectSecret(k === selKey ? null : k); }}
                    onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); selectSecret(k); } }}>
                    {show("lamp") && <Lamp role={role} />}
                    {show("name") && (
                      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                        <span className="lens-cell-name" title={k}><span style={{ color: "var(--color-text-tertiary)" }}>{e.namespace} / </span><span style={{ fontWeight: k === selKey ? 600 : 400 }}>{e.name}</span></span>
                        <CopyButton text={e.name} label={`copy external secret name ${e.name}`} appearOnRowHover />
                      </span>
                    )}
                    {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}>{role.kind === "fault" ? "▲ " : ""}{role.label}{e.state === "Failed" && e.reason ? ` · ${e.reason}` : ""}</span>}
                    {show("refreshed") && <span style={{ color: e.stale ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{refreshedText(e, nowS)}</span>}
                    {show("store") && <span style={{ ...cellStyle, color: storeCov !== "unknown" && (!st || st.rule.included) ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{e.storeName}</span>}
                    {show("target") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{e.targetSecret || "-"}</span>}
                  </div>
                );
              }}
            />
          )}
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{shown} of {total}{partial} {noun}{asOf}{view === "secrets" ? " · broken first, then staleness" : ""}</span>}
      inspector={view === "secrets" && selKey && secretSubject ? (
        <SecretInspector
          cluster={cluster}
          key={`${cluster}/${selKey}`}
          e={secretSubject}
          nowS={nowS}
          removed={secretRemoved}
          unverified={secretUnverified}
          replaced={secretReplaced ? secretPin.adopt : undefined}
          store={kindCov(secretSubject.storeKind) !== "unknown" ? resolveStore(secretSubject, stores) : undefined}
          storeCov={kindCov(secretSubject.storeKind)}
          onClose={() => { autoSelect.current = "done"; selectSecret(null); }}
          onOpenStore={(s) => { setView("stores"); setStatus("all"); setStoreSel(storeKey(s)); setCursorKey(storeKey(s)); }}
        />
      ) : view === "stores" && storeSel && storeSubject ? (
        <StoreInspector
          cluster={cluster}
          key={`${cluster}/${storeSel}`}
          store={storeSubject}
          removed={storeRemoved}
          unverified={storeUnverified}
          replaced={storeReplaced ? storePin.adopt : undefined}
          secrets={secretsFor(storeSubject)}
          secretsCov={secretsCov}
          onClose={() => setStoreSel(null)}
          onOpenSecret={(e) => { setView("secrets"); setStatus("all"); setNamespace(""); setQuery(""); setCursorKey(keyOf(e)); autoSelect.current = "done"; selectSecret(keyOf(e)); }}
        />
      ) : undefined}
      inspectorLabel={view === "stores" ? "Secret store details" : "ExternalSecret details"}
      onCloseInspector={closeSelected}
    />
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

function RemovedNotice({ kind }: { kind: string }) {
  return (
    <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
      <span className="t-status">{kind} removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
    </div>
  );
}

function SecretInspector({ cluster, e, nowS, removed, unverified, replaced, store, storeCov, onClose, onOpenStore }: {
  cluster: string;
  e: ExternalSecretDTO;
  nowS: number;
  removed: boolean;
  unverified: boolean;
  replaced?: () => void;
  store: ExternalSecretStoreDTO | undefined;
  storeCov: JoinCoverage;
  onClose: () => void;
  onOpenStore: (s: ExternalSecretStoreDTO) => void;
}) {
  const ai = useInspectorAI(e.uid ? { kind: "row" as const, task: "eso", part: "externalsecrets", uid: e.uid } : null, `ExternalSecret/${e.namespace}/${e.name}`, !removed && !unverified, cluster);
  // store surfaces derive from the store's rule: only CONFIRMED adverse store
  // health is a diagnosed root cause; Unknown is unverified evidence
  const storeRole = store ? roleOf(store) : null;
  const storeBroken = !!storeRole && (storeRole.kind === "fault" || storeRole.kind === "degraded");
  const cause = causeOf(e, store, storeRole, storeCov, nowS);
  const role = roleOf(e);
  const live = !removed;
  const manifestRef = wellKnownResourceRef("external-secrets.io", "ExternalSecret");
  const more: MenuItem[] = manifestRef ? [{ label: "View manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance(e.namespace, e.name); } }] : [];
  return (
    <div data-testid="eso-secret-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: e.uid || undefined }} replaced={replaced}
        kind="ExternalSecret"
        namespace={e.namespace}
        name={e.name}
        meta={[`store ${e.storeKind}/${e.storeName}`, e.targetSecret ? `→ Secret ${e.targetSecret}` : ""].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind="ExternalSecret" /> : unverified ? <UnverifiedNotice kind="ExternalSecret" /> : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} reason={e.state === "Failed" ? e.reason || undefined : e.stale ? "refresh stopped - last sync ok" : undefined} />
            {e.message && e.state !== "Synced" && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{e.message}</div>}
            {e.stale && <div className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere", marginTop: -4 }}>{staleTitle(e, nowS)}</div>}
          </div>
        )}
        primary={live && e.targetSecret ? { label: "Open target Secret", onSelect: () => { const nav = useFleet.getState(); nav.openResource(SECRET_REF); nav.openInstance(e.namespace, e.targetSecret); } } : undefined}
        more={[...(more), ...ai.menu]}
      >
        {ai.block}
        {storeBroken && (
          <div className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: storeRole!.kind === "fault" ? "var(--color-background-danger)" : "var(--color-background-warning)", boxShadow: `inset 0 0 0 1px ${storeRole!.kind === "fault" ? "var(--color-border-danger)" : "var(--color-border-warning)"}` }}>
            <div style={{ color: ROLE_TEXT[storeRole!.kind], fontWeight: 600 }}>{storeRole!.kind === "fault" ? "▲ Store failed" : "Store not ready"}{storeCov === "stale" ? " (last observed - the store watch is failing)" : ""}: {store!.reason || "unknown"}</div>
            <button onClick={() => onOpenStore(store!)} className="t-identifier" style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", marginTop: 2 }}>{store!.kind}/{store!.name}</button>
            {store!.message && <div style={{ color: "var(--color-text-secondary)", marginTop: 3, overflowWrap: "anywhere" }}>{store!.message}</div>}
          </div>
        )}
        {cause && !storeBroken && (
          <div className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <div style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>Likely cause</div>
            <div style={{ color: "var(--color-text-secondary)", marginTop: 3, overflowWrap: "anywhere" }}>{cause}</div>
          </div>
        )}
        <InspectorBlock title="Store">
          <div className="insp-kv">
            <span>ref</span><span className="t-identifier">{e.storeKind}/{e.storeName}</span>
            <span>health</span>
            <span style={{ color: storeRole ? ROLE_TEXT[storeRole.kind] : "var(--color-text-tertiary)" }}>
              {store
                ? `${storeRole!.label}${storeRole!.kind === "unknown" ? " (unverified)" : ""}${storeCov === "stale" ? " (last observed)" : ""}`
                : storeCov === "unknown" ? `unverified (${e.storeKind} read unavailable)` : storeCov === "stale" ? `not found in the last observed ${e.storeKind} list` : `not found in the watched ${e.storeKind} list`}
            </span>
            {store?.provider && <><span>provider</span><span>{store.provider}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Refresh">
          <div className="insp-kv">
            <span>refresh</span><span>{refreshText(e)}</span>
            <span>last sync</span>
            <span style={{ color: e.stale ? "var(--color-text-warning)" : hasRefreshed(e) ? undefined : "var(--color-text-danger)" }}>
              {hasRefreshed(e) ? `${fmtDuration(ageOf(e, nowS))} ago` : "never synced"}
            </span>
          </div>
        </InspectorBlock>
        {e.message && e.state === "Synced" && (
          <InspectorBlock title="Message" defaultOpen={false}>
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{e.message}</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
}

function StoreInspector({ cluster, store, removed, unverified, replaced, secrets, secretsCov, onClose, onOpenSecret }: { cluster: string; store: ExternalSecretStoreDTO; removed: boolean; unverified: boolean; replaced?: () => void; secrets: ExternalSecretDTO[]; secretsCov: JoinCoverage; onClose: () => void; onOpenSecret: (e: ExternalSecretDTO) => void }) {
  const ai = useInspectorAI(store.uid ? { kind: "row" as const, task: "eso", part: "stores", uid: store.uid } : null, `${store.kind}/${store.namespace ? `${store.namespace}/` : ""}${store.name}`, !removed && !unverified, cluster);
  const role = roleOf(store);
  const ref = wellKnownResourceRef("external-secrets.io", store.kind);
  const view = ref ? () => { const nav = useFleet.getState(); nav.openResource(ref); nav.openInstance(store.kind === "ClusterSecretStore" ? "" : store.namespace, store.name); } : null;
  const blocked = secrets.filter(included);
  return (
    <div data-testid="eso-store-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: store.uid || undefined }} replaced={replaced}
        kind={store.kind}
        namespace={store.namespace || undefined}
        name={store.name}
        meta={[store.provider ? `provider ${store.provider}` : "", store.kind === "ClusterSecretStore" ? "cluster-scoped - any namespace may use it" : `namespaced - serves ExternalSecrets in ${store.namespace}`].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={removed ? <RemovedNotice kind={store.kind} /> : unverified ? <UnverifiedNotice kind={store.kind} /> : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} reason={store.state !== "Synced" ? store.reason || undefined : undefined} />
            {store.message && store.state !== "Synced" && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{store.message}</div>}
          </div>
        )}
        primary={view ? { label: "View manifest in Resources", onSelect: view } : undefined}
        more={ai.menu}
      >
        {ai.block}
        <InspectorBlock title="ExternalSecrets using it" count={secretsCov === "unknown" ? undefined : secrets.length} extra={blocked.length > 0 ? <span className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{blocked.length} not synced or stale</span> : undefined}>
          {secretsCov === "stale" && <span className="t-secondary">From the last observed ExternalSecret list; the watch is currently failing.</span>}
          {secretsCov === "unknown" ? <span className="t-secondary">ExternalSecret read unavailable - usage unverified, not zero.</span>
            : secrets.length === 0 ? <span className="t-secondary">No ExternalSecret references this store.</span> : (
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              {[...blocked, ...secrets.filter((x) => !blocked.includes(x))].map((x) => (
                <button key={keyOf(x)} onClick={() => onOpenSecret(x)} className="t-identifier"
                  style={{ display: "flex", gap: 8, alignItems: "center", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)" }}>
                  <VerdictLamp health={roleOf(x)} size={7} />
                  <span style={{ overflowWrap: "anywhere" }}>{x.namespace}/{x.name}</span>
                </button>
              ))}
            </div>
          )}
        </InspectorBlock>
        {store.message && store.state === "Synced" && (
          <InspectorBlock title="Message" defaultOpen={false}>
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{store.message}</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
}

// causeOf: the "likely cause" text. Only CONFIRMED evidence may be a cause -
// a broken store is the headline card (no duplicate cause); an Unknown store
// is unverified evidence (no store-derived cause); a missing store is a cause
// only with that kind's coverage. The secret's own failure and ESO-native
// staleness still give a cause.
function causeOf(e: ExternalSecretDTO, store: ExternalSecretStoreDTO | undefined, role: { kind: string } | null, cov: JoinCoverage, nowS: number): string {
  if (role && (role.kind === "fault" || role.kind === "degraded")) return "";
  if (!store && cov !== "unknown") return `${e.storeKind}/${e.storeName} was not found in the ${cov === "stale" ? "last observed" : "watched"} ${e.storeKind} list.`;
  if (e.state === "Failed") return `The last sync attempt failed${e.reason ? `: ${e.reason}` : ""}.`;
  if (e.stale) return `The controller has stopped refreshing: last successful sync was ${fmtDuration(ageOf(e, nowS))} ago against a ${fmtDuration(e.refreshIntervalSeconds)} refresh interval.`;
  if (!hasRefreshed(e) && e.state !== "Synced") return "This secret has never synced successfully.";
  return "";
}

