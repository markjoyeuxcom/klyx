import { useEffect, useMemo, useRef, useState } from "react";
import { useFleet, ResourceRef, InstanceDTO } from "../store/fleet";
import { loadInstances, listInstancePage } from "../bridge/crd";
import { VirtualList } from "../chrome/VirtualList";
import type { VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { CopyButton } from "../chrome/CopyButton";
import { Age } from "../chrome/Age";
import { EmptyState, SkeletonRows } from "../chrome/primitives";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { IconAlertTriangle, IconStack2 } from "@tabler/icons-react";
import { InstanceDetail } from "./InstanceDetail";
import { riskFor, supportsRiskFilter } from "./resourceRisk";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";

// The generic GVR lens behind every Resources and CRD destination, on the
// workspace template (spec §6 step 3). Same contract as Pods/Nodes/Workloads:
// one table surface with priority-fitted columns, the inspector in the split
// or drawer, one-shot auto-select of the first at-risk row from a fresh read,
// filter exclusion closes, a removed instance keeps its last evidence.


export type ExtraColumn = { key: string; label: string; width: string };
type BrowserScope = "cluster" | "fleet";
type FleetInstanceRow = InstanceDTO & { cluster: string };
// Fleet coverage is per cluster: a cluster can be read fully, capped (more
// pages behind `tokens[cluster]`), or unreadable. Counts are a lower bound
// unless every target is complete.
// A cluster that does not serve the kind is capability evidence (not an
// error, not an empty read). A failed continuation keeps its token so it can
// be retried without rereading the whole fleet.
type FleetInstanceState = {
  rows: FleetInstanceRow[]; loading: boolean; errors: string[]; tokens: Record<string, string>; loadingMore: boolean;
  notServed: string[]; continued: boolean; continuationErrors: string[];
};
const FLEET_IDLE: FleetInstanceState = { rows: [], loading: false, errors: [], tokens: {}, loadingMore: false, notServed: [], continued: false, continuationErrors: [] };

const keyOfRow = (r: { cluster: string; namespace: string; name: string }) => `${r.cluster}/${r.namespace}/${r.name}`;

// floorOf reads a column's minimum from its legacy grid width ("86px" or
// "minmax(130px, 0.9fr)").
function floorOf(width: string): number {
  const m = /(\d+)px/.exec(width);
  return m ? Number(m[1]) : 100;
}

export function InstanceList({ cluster, resource }: { cluster: string; resource: ResourceRef }) {
  const instances = useFleet((s) => s.instances);
  const clusters = useFleet((s) => s.clusters);
  const setFilter = useFleet((s) => s.setInstanceFilter);
  const setRiskOnly = useFleet((s) => s.setInstanceRiskOnly);
  const openInstance = useFleet((s) => s.openInstance);
  const closeInstance = useFleet((s) => s.closeInstance);
  const closeResource = useFleet((s) => s.closeResource);
  const openRef = useFleet((s) => (s.route.name === "cluster" ? s.route.instance ?? null : null));
  const [scope, setScope] = useState<BrowserScope>("cluster");
  const [fleetClusterFilter, setFleetClusterFilter] = useState("");
  const [fleetState, setFleetState] = useState<FleetInstanceState>(FLEET_IDLE);
  const fleetGen = useRef(0);
  const [fleetReload, setFleetReload] = useState(0);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const resKey = `${cluster}|${resource.group}/${resource.version}/${resource.plural}`;

  useEffect(() => {
    void loadInstances(cluster, resource);
    return () => useFleet.getState().clearInstances();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, resource.group, resource.version, resource.plural]);

  const fleetTargetKey = useMemo(() => {
    const connected = clusters.filter((c) => c.state !== "Failed" && c.state !== "Unconnected").map((c) => c.name);
    return (connected.length > 0 ? connected : [cluster]).join("\n");
  }, [clusters, cluster]);
  const fleetTargets = useMemo(() => fleetTargetKey.split("\n").filter(Boolean), [fleetTargetKey]);

  useEffect(() => {
    if (fleetClusterFilter && !fleetTargets.includes(fleetClusterFilter)) setFleetClusterFilter("");
  }, [fleetClusterFilter, fleetTargets]);

  useEffect(() => {
    if (scope !== "fleet") return;
    const gen = ++fleetGen.current;
    setFleetState({ ...FLEET_IDLE, loading: true });
    void Promise.all(fleetTargets.map(async (target) => {
      try {
        const page = await listInstancePage(target, resource);
        return { target, rows: page.items.map((row) => ({ ...row, cluster: target })), token: page.nextToken, error: "", notServed: !!page.notServed };
      } catch {
        return { target, rows: [] as FleetInstanceRow[], token: "", error: target, notServed: false };
      }
    })).then((results) => {
      if (gen !== fleetGen.current) return;
      setFleetState({
        ...FLEET_IDLE,
        rows: results.flatMap((r) => r.rows),
        errors: results.filter((r) => r.error).map((r) => r.error),
        tokens: Object.fromEntries(results.filter((r) => r.token).map((r) => [r.target, r.token])),
        notServed: results.filter((r) => r.notServed).map((r) => r.target),
      });
    });
    // Bumping the generation invalidates this read and any continuation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    return () => { fleetGen.current++; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, fleetTargets, fleetReload, resource.group, resource.version, resource.plural]);

  // Bounded continuation: one more page from every capped cluster.
  const loadMoreFleet = () => {
    const gen = fleetGen.current;
    const pending = Object.entries(fleetState.tokens);
    if (pending.length === 0) return;
    setFleetState((st) => ({ ...st, loadingMore: true }));
    void Promise.all(pending.map(async ([target, token]) => {
      try {
        const page = await listInstancePage(target, resource, token);
        return { target, rows: page.items.map((row) => ({ ...row, cluster: target })), token: page.nextToken, failed: false };
      } catch {
        return { target, rows: [] as FleetInstanceRow[], token, failed: true };
      }
    })).then((results) => {
      if (gen !== fleetGen.current) return;
      setFleetState((st) => {
        const tokens = { ...st.tokens };
        // a failed continuation keeps its token: the same button retries it
        for (const r of results) { if (r.token) tokens[r.target] = r.token; else delete tokens[r.target]; }
        return {
          ...st,
          rows: [...st.rows, ...results.flatMap((r) => r.rows)],
          loadingMore: false,
          continued: st.continued || results.some((r) => !r.failed),
          continuationErrors: results.filter((r) => r.failed).map((r) => r.target),
          tokens,
        };
      });
    });
  };

  const namespaced = resource.scope === "Namespaced";
  const extraColumns = columnsFor(resource);
  const fleetMode = scope === "fleet";
  const riskSupported = supportsRiskFilter(resource);

  // Rows belong to the full recorded identity: cluster + group/version/plural.
  const isCurrent = instances.cluster === cluster && !!instances.ref && instances.ref.group === resource.group &&
    instances.ref.version === resource.version && instances.ref.plural === resource.plural;
  const fleetRows = fleetClusterFilter ? fleetState.rows.filter((row) => row.cluster === fleetClusterFilter) : fleetState.rows;
  const all: FleetInstanceRow[] = useMemo(
    () => (fleetMode ? fleetRows : (isCurrent ? instances.rows.map((row) => ({ ...row, cluster })) : [])),
    [fleetMode, fleetRows, isCurrent, instances.rows, cluster],
  );
  const fleetCounts = fleetState.rows.reduce<Record<string, number>>((acc, row) => { acc[row.cluster] = (acc[row.cluster] ?? 0) + 1; return acc; }, {});
  const q = instances.filter.toLowerCase();
  const rowsWithRisk = useMemo(() => all.map((row) => ({ row, risk: riskFor(resource, row) })), [all, resource]);
  const attentionCount = rowsWithRisk.filter((r) => r.risk.bad).length;
  const rows = useMemo(() => rowsWithRisk
    .filter(({ row, risk }) => {
      if (instances.riskOnly && !risk.bad) return false;
      if (!q) return true;
      return row.name.toLowerCase().includes(q) || row.cluster.toLowerCase().includes(q) || row.namespace.toLowerCase().includes(q) ||
        risk.reason.toLowerCase().includes(q) || Object.values(row.fields ?? {}).some((v) => v.toLowerCase().includes(q));
    })
    .sort((a, b) => {
      if (a.risk.bad !== b.risk.bad) return a.risk.bad ? -1 : 1;
      if (a.row.cluster !== b.row.cluster) return a.row.cluster.localeCompare(b.row.cluster);
      return a.row.namespace === b.row.namespace ? a.row.name.localeCompare(b.row.name) : a.row.namespace.localeCompare(b.row.namespace);
    }), [rowsWithRisk, instances.riskOnly, q]);
  const loading = fleetMode ? fleetState.loading : instances.loading;
  const fleetCapped = Object.keys(fleetState.tokens);
  const fleetAllFailed = fleetMode && !fleetState.loading && fleetTargets.length > 0 && fleetState.errors.length === fleetTargets.length;
  const readError = !fleetMode ? instances.error : fleetAllFailed ? `no cluster could be read (${fleetState.errors.join(", ")})` : null;
  // Lower bound: more pages behind a cluster's cap, or a cluster unreadable.
  const partial = fleetMode ? fleetCapped.length > 0 || fleetState.errors.length > 0 : !!(isCurrent && instances.nextToken);
  // A read is complete only when every page is in and nothing failed: only
  // then can an absent instance be called removed.
  const complete = !fleetMode && isCurrent && !instances.loading && !instances.error && !instances.nextToken;

  function openRow(r: FleetInstanceRow) {
    if (!fleetMode || r.cluster === cluster) { openInstance(r.namespace, r.name); return; }
    const nav = useFleet.getState();
    const section = nav.route.name === "cluster" && nav.route.section === "crds" ? "crds" : "resources";
    nav.openCluster(r.cluster);
    nav.setSection(section);
    nav.openResource(resource);
    nav.openInstance(r.namespace, r.name);
  }

  // Selection lifecycle.
  const openKey = openRef ? keyOfRow({ cluster, namespace: openRef.namespace, name: openRef.name }) : null;
  const openRow_ = openKey ? all.find((r) => keyOfRow(r) === openKey) : undefined;
  const removed = !!openRef && complete && !openRow_;
  // A newly requested selection (row click, cross-link, palette) stays
  // "pending reveal" until its evidence arrives. If it then arrives excluded by
  // a filter that predates the request, the filter is cleared to reveal it;
  // a filter the operator edits after the request wins, and an operator
  // filter excluding an already-revealed selection closes it.
  const reveal = useRef<{ key: string; filter: string; riskOnly: boolean } | null>(null);
  const lastOpenKey = useRef<string | null>(null);
  if (openKey !== lastOpenKey.current) {
    lastOpenKey.current = openKey;
    reveal.current = openKey ? { key: openKey, filter: instances.filter, riskOnly: instances.riskOnly } : null;
  }
  useEffect(() => {
    if (!openKey || !openRow_) return;
    const visible = rows.some(({ row }) => row === openRow_);
    const r = reveal.current;
    if (r && r.key === openKey) {
      const operatorEdited = instances.filter !== r.filter || instances.riskOnly !== r.riskOnly;
      reveal.current = null;
      if (!visible && !operatorEdited) { setFilter(""); setRiskOnly(false); return; }
    }
    if (!visible) closeInstance();
  }, [openKey, openRow_, rows, instances.filter, instances.riskOnly, setFilter, setRiskOnly, closeInstance]);

  // One-shot auto-select of the first at-risk row, only from a fresh read
  // received after route entry, only in cluster scope.
  const autoSelect = useRef<{ key: string; done: boolean; entry: InstanceDTO[] }>({ key: resKey, done: false, entry: instances.rows });
  if (autoSelect.current.key !== resKey) autoSelect.current = { key: resKey, done: false, entry: instances.rows };
  useEffect(() => {
    const a = autoSelect.current;
    if (a.done || fleetMode || !isCurrent || instances.loading || instances.error || instances.rows === a.entry) return;
    a.done = true;
    if (!riskSupported || useFleet.getState().route.name !== "cluster" || openRef) return;
    const first = rows.find((r) => r.risk.bad);
    if (first) openInstance(first.row.namespace, first.row.name);
  }, [fleetMode, isCurrent, instances.loading, instances.error, instances.rows, riskSupported, rows, openRef, openInstance]);

  // Keyboard: j/k move a key-based cursor, Enter opens, / focuses search.
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const cursorIdx = cursorKey === null ? -1 : rows.findIndex(({ row }) => keyOfRow(row) === cursorKey);
  useListKeys({
    count: rows.length,
    selected: cursorIdx,
    onSelect: (idx) => { const r = rows[idx]; if (r) { setCursorKey(keyOfRow(r.row)); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const r = rows[idx]; if (r) openRow(r.row); },
    onEscape: () => { if (openRef) closeInstance(); },
    searchRef,
  });

  // Columns: identity keeps its floor; scope and kind-specific columns join by
  // priority; age last.
  const columns: (ColumnDef & { label: string })[] = useMemo(() => [
    ...(riskSupported ? [{ id: "lamp", label: "", width: 16, priority: 0 }] : []),
    ...(fleetMode ? [{ id: "cluster", label: "Cluster", width: 120, priority: 1 }] : []),
    ...(namespaced ? [{ id: "namespace", label: "Namespace", width: 140, priority: 1 }] : []),
    { id: "name", label: "Name", width: 240, priority: 0, flex: true },
    ...extraColumns.map((c, i) => ({ id: `f:${c.key}`, label: c.label, width: floorOf(c.width), priority: 2 + i })),
    { id: "age", label: "Age", width: 56, priority: 2 + extraColumns.length },
  ], [riskSupported, fleetMode, namespaced, extraColumns]);
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const clearFilters = () => { setFilter(""); setRiskOnly(false); setFleetClusterFilter(""); };
  let state: DatasetState = "ready";
  if (loading && all.length === 0) state = "loading";
  else if (readError && all.length === 0) state = "unreadable";
  else if (all.length === 0) state = "empty";
  else if (rows.length === 0) state = "no-match";
  const where = fleetMode ? "across the fleet" : `in ${cluster}`;
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={8} label={`loading ${resource.kind} instances`} /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title={`${resource.kind} instances ${where} could not be read.`} hint={readError ?? undefined} action={fleetMode ? { label: "Retry", onClick: () => setFleetReload((n) => n + 1) } : { label: "Retry", onClick: () => void loadInstances(cluster, resource) }} />
    : state === "empty" ? <EmptyState icon={<IconStack2 size={28} stroke={1.2} />} title={`No ${resource.kind} instances ${where}.`} />
    : <EmptyState icon={<IconStack2 size={28} stroke={1.2} />} title={`No ${resource.kind} instances match the current filters.`} hint={`${all.length} loaded ${where}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  const more = partial ? "+" : "";
  const filterBits = [instances.riskOnly ? "attention" : "", instances.filter ? "search" : "", fleetClusterFilter ? fleetClusterFilter : ""].filter(Boolean);

  return (
    <LensLayout
      label={`${resource.kind} resources`}
      title={
        <span style={{ display: "inline-flex", alignItems: "baseline", gap: 6, minWidth: 0 }}>
          <button onClick={closeResource} aria-label="back to resources" className="t-control" style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)" }}>
            {resource.scope === "Cluster" ? "Cluster resources" : "Resources"}
          </button>
          <span style={{ color: "var(--color-text-tertiary)" }}>/</span>
          <span>{resource.kind}</span>
        </span>
      }
      summary={state === "loading" || state === "unreadable" ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{all.length}{more}</span>
          {riskSupported && (attentionCount > 0
            ? <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{attentionCount}{more} need attention</span>
            : <span className="t-secondary">{partial ? "no known attention in what was read" : "none need attention"}</span>)}
          <span className="t-secondary">{resource.scope === "Cluster" ? "cluster-scoped" : "namespaced"} · {resource.group || "core"}/{resource.version} · raw resource view</span>
        </>
      )}
      controls={
        <>
          <div role="group" aria-label="resource browsing scope" style={{ display: "inline-flex", gap: 4 }}>
            <button className="l-chip" aria-pressed={scope === "cluster"} onClick={() => setScope("cluster")}>This cluster</button>
            <button className="l-chip" aria-pressed={scope === "fleet"} onClick={() => setScope("fleet")}>Fleet</button>
          </div>
          {fleetMode && fleetTargets.length > 1 && (
            <label style={{ display: "flex" }}>
              <span className="sr-only">Fleet cluster</span>
              <select className="l-input" value={fleetClusterFilter} onChange={(e) => setFleetClusterFilter(e.target.value)}>
                <option value="">All clusters ({fleetState.rows.length})</option>
                {fleetTargets.map((t) => <option key={t} value={t}>{t} ({fleetCounts[t] ?? 0})</option>)}
              </select>
            </label>
          )}
          {riskSupported && (
            <button className="l-chip" aria-pressed={instances.riskOnly} onClick={() => setRiskOnly(!instances.riskOnly)}>
              Attention {attentionCount > 0 && <span style={{ color: "var(--color-text-warning)" }}>{attentionCount}</span>}
            </button>
          )}
          <label style={{ display: "flex", flex: "0 1 280px", minWidth: 160 }}>
            <span className="sr-only">Search {resource.kind}</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} value={instances.filter} onChange={(e) => setFilter(e.target.value)} placeholder={fleetMode ? "Search cluster, name, namespace" : "Search name, namespace, fields"} />
          </label>
        </>
      }
      notice={(readError && all.length > 0) || (fleetMode && !fleetAllFailed && (fleetState.errors.length > 0 || fleetCapped.length > 0 || fleetState.notServed.length > 0)) ? (
        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          {readError && all.length > 0 && <WatchErrorBanner tool={resource.kind} failure="read failed - showing the last good read" error={readError} />}
          {fleetMode && !fleetAllFailed && fleetCapped.length > 0 && (
            <div role="status" className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 8, padding: "0 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
              <span style={{ flex: 1, minWidth: 0 }}>
                {fleetState.continued ? "More pages available" : "First page only"} for {fleetCapped.join(", ")}: counts are a lower bound.
                {fleetState.continuationErrors.length > 0 && <span style={{ color: "var(--color-text-warning)" }}> The next page failed for {fleetState.continuationErrors.join(", ")}.</span>}
              </span>
              <button className="l-btn" disabled={fleetState.loadingMore} onClick={loadMoreFleet}>
                {fleetState.loadingMore ? "Loading…" : fleetState.continuationErrors.length > 0 ? "Retry next page" : "Load next page"}
              </button>
            </div>
          )}
          {fleetMode && !fleetAllFailed && fleetState.notServed.length > 0 && (
            <div role="status" className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 8, padding: "0 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
              <span>{resource.kind} is not served on {fleetState.notServed.join(", ")} (not installed there) - those clusters contribute no rows.</span>
            </div>
          )}
          {fleetMode && !fleetAllFailed && fleetState.errors.length > 0 && (
            <div role="status" className="t-control" style={{ minHeight: 32, display: "flex", alignItems: "center", gap: 8, padding: "0 10px", borderRadius: 4, background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)", color: "var(--color-text-warning)" }}>
              <IconAlertTriangle size={14} stroke={1.6} aria-hidden="true" />
              <span>{fleetState.errors.length} cluster{fleetState.errors.length === 1 ? "" : "s"} unreadable: {fleetState.errors.join(", ")}. Fleet counts exclude them.</span>
            </div>
          )}
        </div>
      ) : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" data-testid="instance-list-header" style={gridStyle}>
            {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList
            ref={listRef}
            items={rows}
            rowHeight={TABLE_ROW_HEIGHT}
            rowKey={({ row }) => keyOfRow(row)}
            style={{ flex: 1, minHeight: 0 }}
            render={({ row: r, risk }) => {
              const k = keyOfRow(r);
              return (
                <div
                  key={k}
                  role="button"
                  tabIndex={0}
                  data-rowhost="true"
                  className="lens-row"
                  aria-selected={k === cursorKey}
                  data-cursor={k === cursorKey ? "true" : undefined}
                  data-selected={k === openKey ? "true" : undefined}
                  style={gridStyle}
                  onClick={() => { setCursorKey(k); openRow(r); }}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openRow(r); } }}
                >
                  {show("lamp") && (risk.bad
                    ? <span role="img" aria-label={`needs attention: ${risk.reason}`} title={risk.reason} style={{ width: 8, height: 8, borderRadius: "50%", background: "var(--color-text-warning)", justifySelf: "center" }} />
                    : <span />)}
                  {show("cluster") && <span className="t-identifier" style={{ color: "var(--color-text-info)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{r.cluster}</span>}
                  {show("namespace") && <span style={{ color: "var(--color-text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{r.namespace}</span>}
                  {show("name") && (
                    <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                      <span className="lens-cell-name t-identifier" title={r.name} style={{ fontWeight: k === openKey ? 600 : 400 }}>{r.name}</span>
                      <CopyButton text={r.name} label={`copy name ${r.name}`} size={11} appearOnRowHover />
                    </span>
                  )}
                  {extraColumns.map((c) => show(`f:${c.key}`) && (
                    <span key={c.key} className="t-identifier" style={{ color: "var(--color-text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={r.fields?.[c.key] || "-"}>{r.fields?.[c.key] || "-"}</span>
                  ))}
                  {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}><Age iso={r.created} fallback="" /></span>}
                </div>
              );
            }}
          />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={
        <>
          <span className="t-secondary">{rows.length} of {all.length}{more} {resource.plural}{filterBits.length ? ` · filter: ${filterBits.join(", ")}` : ""}</span>
          {filterBits.length > 0 && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all</button>}
          {!fleetMode && isCurrent && instances.nextToken && (
            <button className="l-btn" style={{ height: 22 }} onClick={() => void loadInstances(cluster, resource, instances.nextToken)}>Load more</button>
          )}
        </>
      }
      inspector={openRef && (openRow_ || removed || !complete) ? (
        <InstanceDetail key={`${cluster}/${resource.group}/${resource.version}/${resource.plural}/${openRef.namespace}/${openRef.name}`} cluster={cluster} resource={resource} instance={openRef} removed={removed} />
      ) : undefined}
      inspectorLabel={`${resource.kind} details`}
      onCloseInspector={closeInstance}
    />
  );
}

export function columnsFor(resource: ResourceRef): ExtraColumn[] {
  if (resource.group === "" && resource.version === "v1" && resource.plural === "services") {
    return [
      { key: "type", label: "type", width: "86px" },
      { key: "clusterIP", label: "cluster ip", width: "120px" },
      { key: "externalIP", label: "external ip", width: "minmax(130px, 0.9fr)" },
      { key: "ports", label: "ports", width: "minmax(180px, 1.2fr)" },
    ];
  }
  if (resource.group === "discovery.k8s.io" && resource.version === "v1" && resource.plural === "endpointslices") {
    return [
      { key: "service", label: "service", width: "minmax(130px, 0.8fr)" },
      { key: "addressType", label: "addr", width: "74px" },
      { key: "endpoints", label: "ready", width: "70px" },
      { key: "addresses", label: "addresses", width: "minmax(170px, 1fr)" },
      { key: "ports", label: "ports", width: "minmax(130px, 0.8fr)" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "persistentvolumeclaims") {
    return [
      { key: "status", label: "status", width: "86px" },
      { key: "class", label: "class", width: "minmax(110px, 0.8fr)" },
      { key: "size", label: "size", width: "82px" },
      { key: "modes", label: "modes", width: "86px" },
      { key: "volume", label: "volume", width: "minmax(150px, 1fr)" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "persistentvolumes") {
    return [
      { key: "status", label: "status", width: "86px" },
      { key: "class", label: "class", width: "minmax(110px, 0.8fr)" },
      { key: "size", label: "size", width: "82px" },
      { key: "modes", label: "modes", width: "86px" },
      { key: "claim", label: "claim", width: "minmax(150px, 1fr)" },
      { key: "reclaim", label: "reclaim", width: "94px" },
    ];
  }
  if (resource.group === "networking.k8s.io" && resource.version === "v1" && resource.plural === "ingresses") {
    return [
      { key: "class", label: "class", width: "minmax(110px, 0.7fr)" },
      { key: "hosts", label: "hosts", width: "minmax(170px, 1fr)" },
      { key: "address", label: "address", width: "minmax(130px, 0.8fr)" },
      { key: "tls", label: "tls", width: "minmax(100px, 0.6fr)" },
      { key: "backends", label: "backends", width: "minmax(160px, 1fr)" },
    ];
  }
  if (resource.group === "autoscaling" && resource.version === "v2" && resource.plural === "horizontalpodautoscalers") {
    return [
      { key: "target", label: "target", width: "minmax(150px, 1fr)" },
      { key: "replicas", label: "min/current/desired/max", width: "160px" },
      { key: "metrics", label: "metrics", width: "minmax(190px, 1.2fr)" },
    ];
  }
  if (resource.group === "policy" && resource.version === "v1" && resource.plural === "poddisruptionbudgets") {
    return [
      { key: "allowed", label: "allowed", width: "80px" },
      { key: "healthy", label: "healthy", width: "90px" },
      { key: "expected", label: "expected", width: "90px" },
    ];
  }
  if (resource.group === "batch" && resource.version === "v1" && resource.plural === "jobs") {
    return [
      { key: "active", label: "active", width: "72px" },
      { key: "succeeded", label: "succeeded", width: "86px" },
      { key: "failed", label: "failed", width: "72px" },
      { key: "completions", label: "complete", width: "90px" },
    ];
  }
  if (resource.group === "batch" && resource.version === "v1" && resource.plural === "cronjobs") {
    return [
      { key: "schedule", label: "schedule", width: "minmax(130px, 0.8fr)" },
      { key: "suspended", label: "suspend", width: "74px" },
      { key: "active", label: "active", width: "70px" },
      { key: "lastSchedule", label: "last", width: "128px" },
      { key: "lastSucceeded", label: "success", width: "128px" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "secrets") {
    return [
      { key: "type", label: "type", width: "minmax(170px, 1fr)" },
      { key: "keys", label: "keys", width: "70px" },
      { key: "immutable", label: "immutable", width: "90px" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "configmaps") {
    return [
      { key: "keys", label: "keys", width: "70px" },
      { key: "immutable", label: "immutable", width: "90px" },
    ];
  }
  if (resource.group === "cert-manager.io" && resource.plural === "certificates") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "issuer", label: "issuer", width: "minmax(140px, 0.9fr)" },
      { key: "expires", label: "expires", width: "100px" },
      { key: "renew", label: "renew", width: "100px" },
      { key: "dns", label: "dns", width: "minmax(180px, 1.2fr)" },
    ];
  }
  if (resource.group === "cert-manager.io" && resource.plural === "certificaterequests") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "issuer", label: "issuer", width: "minmax(140px, 0.9fr)" },
      { key: "approved", label: "approved", width: "90px" },
      { key: "denied", label: "denied", width: "80px" },
      { key: "duration", label: "duration", width: "90px" },
    ];
  }
  if (resource.group === "cert-manager.io" && (resource.plural === "issuers" || resource.plural === "clusterissuers")) {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "type", label: "type", width: "100px" },
      { key: "server", label: "server", width: "minmax(200px, 1.2fr)" },
    ];
  }
  if (resource.group === "external-secrets.io" && resource.plural === "externalsecrets") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "store", label: "store", width: "minmax(140px, 0.9fr)" },
      { key: "target", label: "target secret", width: "minmax(140px, 0.9fr)" },
      { key: "refresh", label: "refresh", width: "90px" },
      { key: "synced", label: "synced", width: "108px" },
    ];
  }
  if (resource.group === "external-secrets.io" && (resource.plural === "secretstores" || resource.plural === "clustersecretstores")) {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "provider", label: "provider", width: "120px" },
      { key: "controller", label: "controller", width: "minmax(140px, 0.9fr)" },
    ];
  }
  if (resource.group === "helm.toolkit.fluxcd.io" && resource.plural === "helmreleases") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "suspended", label: "suspend", width: "78px" },
      { key: "chart", label: "chart", width: "minmax(150px, 1fr)" },
      { key: "source", label: "source", width: "minmax(150px, 1fr)" },
      { key: "revision", label: "revision", width: "minmax(160px, 1fr)" },
    ];
  }
  if (resource.group === "kustomize.toolkit.fluxcd.io" && resource.plural === "kustomizations") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "suspended", label: "suspend", width: "78px" },
      { key: "source", label: "source", width: "minmax(150px, 1fr)" },
      { key: "path", label: "path", width: "minmax(140px, 0.9fr)" },
      { key: "revision", label: "revision", width: "minmax(160px, 1fr)" },
    ];
  }
  if (resource.group === "networking.k8s.io" && resource.version === "v1" && resource.plural === "networkpolicies") {
    return [
      { key: "selector", label: "selector", width: "minmax(170px, 1fr)" },
      { key: "policyTypes", label: "types", width: "110px" },
      { key: "ingress", label: "ingress", width: "76px" },
      { key: "egress", label: "egress", width: "76px" },
    ];
  }
  if (resource.group === "cilium.io" && (resource.plural === "ciliumnetworkpolicies" || resource.plural === "ciliumclusterwidenetworkpolicies")) {
    return [
      { key: "selector", label: "selector", width: "minmax(170px, 1fr)" },
      { key: "ingress", label: "ingress", width: "90px" },
      { key: "egress", label: "egress", width: "90px" },
      { key: "scope", label: "scope", width: "90px" },
    ];
  }
  return [];
}

