import { useEffect, useMemo, useRef, useState } from "react";
import { IconRoute } from "@tabler/icons-react";
import { useNavIntent } from "../chrome/useNavIntent";
import { useFleet } from "../store/fleet";
import type { GatewayRouteRowDTO } from "../store/fleet";
import type { GatewayRouteRows } from "../health/useGatewayRouteRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictLamp } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { clock, combineEvidence, coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
import { RANK, ROLE_TEXT, roleTone, routeRole, type Role } from "./gatewayModel";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone, expectsOther } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

// RouteList - the Gateways task's routes part (1c slice 3 §4a): every
// HTTPRoute attached to a Gateway, counted once by UID with the role, label
// and parent of its WORST attachment (the captured evaluation), its header
// and attention count the part's own. The verdicts were evaluated against the
// parent Gateways, Services and EndpointSlices: those are dependencies - they
// qualify the verdicts, never the list's completeness. The data path stays
// the topology (Open topology, on the worst parent).

type Mode = "all" | "attention";
const keyOf = (r: { namespace: string; name: string }) => `${r.namespace}/${r.name}`;
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "HTTPRoute", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 190, priority: 0 },
  { id: "parent", label: "Worst parent", width: 180, priority: 1 },
  { id: "parents", label: "Parents", width: 64, priority: 2 },
];

function Lamp({ role }: { role: Role }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}

// depQualifier: what the verdicts can claim, from the dependencies they were
// evaluated against: "" current, "last observed" when one is stale,
// "unverified" when one has no read.
function depQualifier(deps: { kind: string; coverage?: string }[]): { word: "" | "last observed" | "unverified"; kinds: string[] } {
  const unknown = deps.filter((d) => d.coverage === "checking" || d.coverage === "unreadable").map((d) => d.kind);
  if (unknown.length) return { word: "unverified", kinds: unknown };
  const stale = deps.filter((d) => d.coverage === "stale").map((d) => d.kind);
  return stale.length ? { word: "last observed", kinds: stale } : { word: "", kinds: [] };
}

export function RouteList({ cluster, rows, toggle }: { cluster: string; rows: GatewayRouteRows; toggle: React.ReactNode }) {
  const openGateway = useFleet((s) => s.openGateway);
  const selectRoute = useFleet((s) => s.selectRoute);
  const [mode, setMode] = useState<Mode>("all");
  const [query, setQuery] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  const ev: KindEvidence = kindEvidence(!rows.loading, rows.coverage, "HTTPRoute", rows.watchError);
  const absent = rows.coverage.find((k) => k.kind === "HTTPRoute")?.coverage === "absent";
  // These are ATTACHED routes (Codex review of b2eff54 #1): a route counts
  // only through a parent Gateway in the same capture, so membership needs
  // the HTTPRoute read AND the parent Gateways read. Services and
  // EndpointSlices qualify verdicts only, never membership.
  const gatewaysDep = rows.dependencies.find((k) => k.kind === "Gateway");
  const parentsEv: KindEvidence = gatewaysDep ? kindEvidence(true, [gatewaysDep], "Gateway", null) : rows.loading ? "pending" : "complete";
  const memberEv = combineEvidence([ev, parentsEv]);
  const dep = depQualifier(rows.dependencies);
  const routes = rows.routes;
  const rowsRefreshing = rows.rowsState.refreshing;
  const rowsAsOf = rows.rowsState.evaluatedAt ? clock(rows.rowsState.evaluatedAt) : "the last read";
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = lastGoodAt(rows.coverage, ["HTTPRoute"]);
  // a discovery gap of the route kind or of anything it was joined with
  const discoveryGap = coverageGaps([...rows.coverage, ...rows.dependencies]);
  const mark = memberEv === "complete" ? "" : memberEv === "stale" ? "*" : "+";
  // global: the count selector's (slice 3 §4a) - the header and the filter
  // count equal the part's Included
  const total = rows.partCounts?.total ?? routes.length;
  const attention = rows.partCounts?.included ?? routes.filter((r) => r.rule.included).length;

  const ordered = useMemo(() => [...routes].sort((a, b) =>
    RANK[routeRole(a).kind] - RANK[routeRole(b).kind] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [routes]);
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => ordered.filter((r) => {
    if (mode === "attention" && !r.rule.included) return false;
    return !q || [r.namespace, r.name, r.parent, r.rule.label].some((v) => v.toLowerCase().includes(q));
  }), [ordered, mode, q]);

  // A summary Top link for an attached route (its worst parent named on
  // the ref): select it past the filters.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "gateway" || intent.part !== "routes") return "wait";
    const key = `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const row = routes.find((r) => keyOf(r) === key);
    // An opening by UID (slice 9 note §4.2): pinned to that UID at once; a
    // different or missing route is said, never substituted.
    if (expectsOther(row, intent.ref.uid)) {
      expectPin(cluster, key, intent.ref.uid!);
      setMode("all"); setQuery("");
      setCursorKey(key);
      setSelKey(key);
      return "consumed";
    }
    if (!row) {
      if (ev !== "complete" || parentsEv !== "complete") return "wait";
      // absent from a complete membership read: detached, not proven deleted
      useFleet.getState().setActionStatus({ kind: "error", message: `route ${key} is no longer attached to a Gateway - it may still exist` });
      return "consumed";
    }
    setMode("all"); setQuery("");
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [routes, ev, parentsEv]);

  // A selection keeps its last row. Missing from a membership read that is
  // complete (routes AND parent Gateways current) it is no longer attached -
  // not necessarily deleted; otherwise unverified (slice 3 §5a).
  // Pinned to the route's UID (The Line slice 4 §6.3): a same-name route
  // with a new UID on a complete read is "replaced", never followed.
  const found = selKey ? routes.find((r) => keyOf(r) === selKey) : undefined;
  const pin = usePinnedSubject(cluster, selKey, found, memberEv === "complete");
  const selected = pin.subject ?? undefined;
  const gone: "detached" | "unverified" | null = pin.gone === "replaced" || pin.gone === "removed" ? "detached" : pin.gone;
  const close = () => setSelKey(null);
  // Operator filter edits win: one that hides the selection closes it; a
  // read change never does (Codex review of b2eff54 #2).
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !visible.some((r) => keyOf(r) === keyOf(selected))) setSelKey(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, q]);
  const openTopology = (r: GatewayRouteRowDTO) => {
    const slash = r.parent.indexOf("/");
    openGateway(r.parent.slice(0, slash), r.parent.slice(slash + 1));
    selectRoute(keyOf(r));
  };

  const cursorIdx = cursorKey === null ? -1 : visible.findIndex((r) => keyOf(r) === cursorKey);
  useListKeys({
    count: visible.length,
    selected: cursorIdx,
    onSelect: (i) => { const r = visible[i]; if (r) { setCursorKey(keyOf(r)); listRef.current?.scrollToIndex(i); } },
    onActivate: (i) => { const r = visible[i]; if (r) { setCursorKey(keyOf(r)); setSelKey(keyOf(r)); } },
    onEscape: close,
    searchRef,
  });
  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  let state: DatasetState = "ready";
  if (memberEv === "failed" && routes.length === 0) state = "unreadable";
  else if (memberEv === "pending" && routes.length === 0) state = "loading";
  else if (routes.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";
  const icon = <IconRoute size={28} stroke={1.2} />;
  const clearFilters = () => { setMode("all"); setQuery(""); };
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading HTTPRoutes" />
    : state === "unreadable" ? (ev === "failed"
      ? <EmptyState icon={icon} title="Could not list HTTPRoutes." hint={rows.watchError ?? undefined} />
      : <EmptyState icon={icon} title="Attached HTTPRoutes are unverified." hint={`The parent Gateways could not be read${gatewaysDep?.error ? `: ${gatewaysDep.error}` : ""} - which routes are attached is unknown, not none.`} />)
    : state === "empty" && absent ? <EmptyState icon={icon} title="HTTPRoutes are not served on this cluster." hint="Klyx observes no gateway.networking.k8s.io HTTPRoute API here." />
    : state === "empty" && rowsRefreshing && total > 0 ? <EmptyState icon={icon} title={`No HTTPRoutes as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${total}.`} />
    : state === "empty" && memberEv === "stale" ? <EmptyState icon={icon} title="No attached HTTPRoutes in the last good read." hint={ev === "stale" ? `The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${rows.watchError ?? "unverified"}.` : "The parent Gateways read is failing: the attachments are last observed."} />
    : state === "empty" ? <EmptyState icon={icon} title="No HTTPRoutes attached to a Gateway." hint="A route whose only parent Gateway is gone is not counted." />
    : <EmptyState icon={icon} title="No HTTPRoutes match the current filters." hint={`${routes.length} routes on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (rows.watchError && routes.length > 0) notices.push(<WatchErrorBanner key="read" tool="HTTPRoutes" failure={`read failed - showing the last good list${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={rows.watchError} />);
  if (dep.word) notices.push(<div key="deps" role="status" data-testid="route-deps" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Route verdicts are {dep.word}: they were evaluated against {dep.kinds.join(", ")} {dep.word === "unverified" ? "without a current read" : "from a failing read"}.</div>);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{rows.rowsState.refreshError ? ` Last re-read failed: ${rows.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  const verdictMark = dep.word ? " *" : "";

  // Explain anchors on the route's captured attachment (its worst parent),
  // fenced by UID; a detached or unverified selection does not ask.
  const ai = useInspectorAI(selected?.uid ? { kind: "row" as const, task: "gateway", part: "routes", uid: selected.uid } : null, selected ? `HTTPRoute/${selected.namespace}/${selected.name}` : "", !!selected && !gone, cluster);
  return (
    <LensLayout
      label="Gateways"
      title="HTTPRoutes"
      summary={state === "loading" || state === "unreadable" || absent ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{total}{mark}</span>
          <span className="t-secondary">HTTPRoute{total === 1 ? "" : "s"}</span>
          {attention > 0 && <span className="t-status" data-testid="route-attention" style={{ color: "var(--color-text-warning)" }}>{attention} with a failing attachment{dep.word ? ` (${dep.word})` : ""}</span>}
        </>
      )}
      controls={(
        <>
          {toggle}
          {routes.length > 0 && (
            <>
              <div role="group" aria-label="Route filter" style={{ display: "inline-flex", gap: 4 }}>
                <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {total}</button>
                <button className="l-chip" aria-pressed={mode === "attention"} onClick={() => setMode("attention")}>Needs attention {attention > 0 && <span style={{ color: "var(--color-text-warning)" }}>{attention}</span>}</button>
              </div>
              <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
                <span className="sr-only">Search HTTPRoutes</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
                  value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search name, parent, status" />
              </label>
            </>
          )}
        </>
      )}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={visible} rowHeight={TABLE_ROW_HEIGHT} rowKey={keyOf} style={{ flex: 1, minHeight: 0 }}
            render={(r) => {
              const k = keyOf(r);
              const role = routeRole(r);
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(k); } }}>
                  {show("lamp") && <Lamp role={role} />}
                  {show("name") && <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{r.namespace}/</span>{r.name}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}>{role.kind === "fault" ? "▲ " : ""}{role.label}{verdictMark}</span>}
                  {show("parent") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{r.parent}</span>}
                  {show("parents") && <span style={{ color: "var(--color-text-tertiary)" }}>{r.attachments.length}</span>}
                </div>
              );
            }} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length !== routes.length ? `${visible.length} of ${routes.length}${mark} · ` : ""}counted once per route, by its worst parent{asOf}</span>}
      inspector={selected ? (
        <div data-testid="route-row-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
          <InspectorLayout pin={{ cluster: cluster, uid: selected.uid || undefined }} replaced={pin.gone === "replaced" ? pin.adopt : undefined} kind="HTTPRoute" namespace={selected.namespace} name={selected.name} meta={`worst parent ${selected.parent}`} onClose={close}
            verdict={gone === "detached" ? (
              <div role="status" data-testid="route-detached" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
                <span className="t-status">No longer attached to a Gateway.</span> The route and its parent Gateways were both read in full and it is not in the attached set - it may still exist. The details below are its last evidence.
              </div>
            ) : gone === "unverified" ? (
              <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
                <span className="t-status">Not in the current attached-route read.</span> The HTTPRoute or parent Gateway read is incomplete, so this is unverified - it may still be attached. The details below are its last observed row.
              </div>
            ) : (
              <div>
                <InspectorStatus tone={roleTone(routeRole(selected).kind)} kind={routeRole(selected).kind} label={routeRole(selected).label} reason={`worst parent ${selected.parent}`} />
                {dep.word && <div className="t-secondary" style={{ marginTop: -4 }}>The verdict is {dep.word} - evaluated against {dep.kinds.join(", ")}.</div>}
              </div>
            )}
            primary={gone === "detached" ? undefined : { label: `Open topology (${selected.parent})`, onSelect: () => openTopology(selected) }}
            more={ai.menu}>
            {ai.block}
            <InspectorBlock title="Parents" count={selected.attachments.length}>
              {selected.attachments.map((a) => {
                const role = routeRole(a);
                return (
                  <div key={a.parent} data-testid="route-attachment" style={{ display: "flex", gap: 8, alignItems: "center", fontSize: 12 }}>
                    <Lamp role={role} />
                    <span className="t-identifier">{a.parent}</span>
                    <span style={{ color: ROLE_TEXT[role.kind] }}>{role.label}</span>
                  </div>
                );
              })}
            </InspectorBlock>
          </InspectorLayout>
        </div>
      ) : selKey && pin.expected && pin.gone ? (
        <PinnedGone cluster={cluster} kind="HTTPRoute" namespace={selKey.split("/")[0]} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone}
          removedWords={<><span className="t-status">No longer attached to a Gateway.</span> The HTTPRoute you opened is not in a complete read of the attached routes - it may still exist.</>}
          onAdopt={pin.next ? pin.adopt : undefined} onClose={close} />
      ) : undefined}
      inspectorLabel="HTTPRoute details"
      onCloseInspector={close}
    />
  );
}
