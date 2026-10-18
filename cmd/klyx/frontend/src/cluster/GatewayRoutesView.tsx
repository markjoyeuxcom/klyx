import { useEffect, useMemo, useRef, useState } from "react";
import { useDescribe } from "../chrome/describe/useDescribe";
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
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { CountSentence } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { clock, combineEvidence, coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
import { RANK, ROLE_TEXT, ROUTE_KINDS, roleTone, routeId, routeKindOf, routeKindOfPlural, routeRole } from "./gatewayModel";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone, expectsOther } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

// RouteList - the Gateways task's routes part (1c slice 3 §4a; coverage S7b
// §9): every route of the five kinds attached to a Gateway, counted once by
// UID with the role, label and parent of its WORST attachment (the captured
// evaluation), its header and attention count the part's own. The verdicts
// were evaluated against the parent Gateways and ListenerSets, their
// classes, Services, EndpointSlices, ReferenceGrants and Namespaces: those
// are dependencies - they qualify the verdicts, never the list's
// completeness. The data path stays the topology (Open topology, on the
// worst attachment's Gateway). Identity is kind/namespace/name.

type Mode = "all" | "attention";
const keyOf = routeId;
// a selection key is kind/namespace/name; namespaces and names hold no "/"
const splitKey = (key: string) => { const [kind, namespace, ...rest] = key.split("/"); return { kind, namespace, name: rest.join("/") }; };
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "name", label: "Route", width: 240, priority: 0, flex: true },
  { id: "kind", label: "Kind", width: 96, priority: 1 },
  { id: "status", label: "Status", width: 190, priority: 0 },
  { id: "parent", label: "Worst parent", width: 280, priority: 1 },
  { id: "parents", label: "Parents", width: 64, priority: 2 },
];
const ROUTE_KIND_SET: ReadonlySet<string> = new Set(ROUTE_KINDS);


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
  const [kindFilter, setKindFilter] = useState("");
  const [query, setQuery] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  // every route kind is this part's own evidence: the list is as complete as
  // its weakest served kind, and absent only when no kind is served
  const kindCoverage = rows.coverage.filter((k) => ROUTE_KIND_SET.has(k.kind));
  const ev: KindEvidence = kindCoverage.length > 0
    ? combineEvidence(kindCoverage.map((k) => kindEvidence(!rows.loading, rows.coverage, k.kind, rows.watchError)))
    : kindEvidence(!rows.loading, rows.coverage, "HTTPRoute", rows.watchError);
  const absent = kindCoverage.length > 0 && kindCoverage.every((k) => k.coverage === "absent");
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
  const lastGood = lastGoodAt(rows.coverage, [...ROUTE_KINDS]);
  // a discovery gap of the route kind or of anything it was joined with
  const discoveryGap = coverageGaps([...rows.coverage, ...rows.dependencies]);
  const mark = memberEv === "complete" ? "" : memberEv === "stale" ? "*" : "+";
  // global: the count selector's (slice 3 §4a) - the header and the filter
  // count equal the part's Included
  const total = rows.partCounts?.total ?? routes.length;
  const attention = rows.partCounts?.included ?? routes.filter((r) => r.rule.included).length;
  // the header's mark from the reading that gave its count (slice 11b review)
  const routeFault = rows.partCounts ? rows.partCounts.fault : routes.filter((r) => r.rule.included && routeRole(r).kind === "fault").length;

  const ordered = useMemo(() => [...routes].sort((a, b) =>
    RANK[routeRole(a).kind] - RANK[routeRole(b).kind] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name) || routeKindOf(a).localeCompare(routeKindOf(b))), [routes]);
  const kindCounts = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of routes) m.set(routeKindOf(r), (m.get(routeKindOf(r)) ?? 0) + 1);
    return m;
  }, [routes]);
  const kinds = [...kindCounts.keys()].sort();
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => ordered.filter((r) => {
    if (mode === "attention" && !r.rule.included) return false;
    if (kindFilter && routeKindOf(r) !== kindFilter) return false;
    return !q || [r.namespace, r.name, routeKindOf(r), r.parentLabel ?? r.parent, r.rule.label].some((v) => v.toLowerCase().includes(q));
  }), [ordered, mode, kindFilter, q]);

  // A summary Top link for an attached route (its worst parent named on
  // the ref): select it past the filters.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "gateway" || intent.part !== "routes") return "wait";
    // the ref's resource names the kind; a ref without one is an HTTPRoute's
    const kind = routeKindOfPlural(intent.ref.gvr.split("/").pop() ?? "") || "HTTPRoute";
    const key = `${kind}/${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const row = routes.find((r) => keyOf(r) === key);
    // An opening by UID (slice 9 note §4.2): pinned to that UID at once; a
    // different or missing route is said, never substituted.
    if (intent.ref.uid) expectPin(cluster, kind, key, intent.ref.uid);
    if (expectsOther(row, intent.ref.uid)) {
      setMode("all"); setKindFilter(""); setQuery("");
      setCursorKey(key);
      setSelKey(key);
      return "consumed";
    }
    if (!row) {
      if (ev !== "complete" || parentsEv !== "complete") return "wait";
      // absent from a complete membership read: detached, not proven deleted
      useFleet.getState().setActionStatus({ kind: "error", message: `${kind} ${key.slice(kind.length + 1)} is no longer attached to a Gateway - it may still exist` });
      return "consumed";
    }
    setMode("all"); setKindFilter(""); setQuery("");
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
  const selKind = selKey ? splitKey(selKey).kind : "HTTPRoute";
  const pin = usePinnedSubject(cluster, selKey, found, memberEv === "complete", selKind);
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
  }, [mode, kindFilter, q]);
  // The worst attachment's Gateway, with the lane of that attachment; a
  // payload from before S7b names the Gateway as its parent.
  const gatewayOf = (r: GatewayRouteRowDTO) => r.gateway ?? (r.kind ? "" : r.parent);
  const openTopology = (r: GatewayRouteRowDTO) => {
    const g = gatewayOf(r);
    const slash = g.indexOf("/");
    openGateway(g.slice(0, slash), g.slice(slash + 1));
    selectRoute(r.kind ? `${keyOf(r)}@${r.parent}` : keyOf(r));
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
  const clearFilters = () => { setMode("all"); setKindFilter(""); setQuery(""); };
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading routes" />
    : state === "unreadable" ? (ev === "failed"
      ? <EmptyState icon={icon} title="Could not list the routes." hint={rows.watchError ?? undefined} />
      : <EmptyState icon={icon} title="Attached routes are unverified." hint={`The parent Gateways could not be read${gatewaysDep?.error ? `: ${gatewaysDep.error}` : ""} - which routes are attached is unknown, not none.`} />)
    : state === "empty" && absent ? <EmptyState icon={icon} title="Routes are not served on this cluster." hint="Klyx observes no gateway.networking.k8s.io route API here (HTTPRoute, GRPCRoute, TLSRoute, TCPRoute or UDPRoute)." />
    : state === "empty" && rowsRefreshing && total > 0 ? <EmptyState icon={icon} title={`No routes as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${total}.`} />
    : state === "empty" && memberEv === "stale" ? <EmptyState icon={icon} title="No attached routes in the last good read." hint={ev === "stale" ? `The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${rows.watchError ?? "unverified"}.` : "The parent Gateways read is failing: the attachments are last observed."} />
    : state === "empty" ? <EmptyState icon={icon} title="No routes attached to a Gateway." hint="A route whose only parent is gone is not counted." />
    : <EmptyState icon={icon} title="No routes match the current filters." hint={`${routes.length} routes on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (rows.watchError && routes.length > 0) notices.push(<WatchErrorBanner key="read" tool="routes" failure={`read failed - showing the last good list${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={rows.watchError} />);
  if (dep.word) notices.push(<div key="deps" role="status" data-testid="route-deps" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Route verdicts are {dep.word}: they were evaluated against {dep.kinds.join(", ")} {dep.word === "unverified" ? "without a current read" : "from a failing read"}.</div>);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{rows.rowsState.refreshError ? ` Last re-read failed: ${rows.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  const verdictMark = dep.word ? " *" : "";

  // Explain anchors on the route's captured attachment (its worst parent),
  // fenced by UID; a detached or unverified selection does not ask.
  const ai = useInspectorAI(selected?.uid ? { kind: "row" as const, task: "gateway", part: "routes", uid: selected.uid } : null, selected ? keyOf(selected) : "", !!selected && !gone, cluster);
  // Describe (slice 14c): kubectl's report for this object, read-only; never
  // for a removed or unverified selection.
  const describe = useDescribe(cluster, selected && !gone ? { kind: routeKindOf(selected), namespace: selected.namespace, name: selected.name, uid: selected.uid } : null);
  const selectedGateway = selected ? gatewayOf(selected) : "";
  return (
    <LensLayout
      label="Gateways"
      title="Gateways and routes"
      summary={state === "loading" || state === "unreadable" || absent ? null : (
        <>
          <CountSentence total={total} marker={mark} noun={["route", "routes"]}
            attention={{ count: attention, phrase: "with a failing attachment", fault: routeFault > 0, detail: dep.word || undefined, testid: "route-attention" }} />
        </>
      )}
      controls={(
        <>
          {toggle}
          {routes.length > 0 && (
            <>
              <div role="group" aria-label="Route filter" className="l-seg">
                <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {total}</button>
                <button className="l-chip" aria-pressed={mode === "attention"} onClick={() => setMode("attention")}>Needs attention {attention > 0 && <span style={{ color: "var(--color-text-warning)" }}>{attention}</span>}</button>
              </div>
              {kinds.length > 1 && (
                <label style={{ display: "flex" }}>
                  <span className="sr-only">Route kind</span>
                  <select className="l-input" data-testid="route-kind-filter" value={kindFilter} onChange={(e) => setKindFilter(e.target.value)}>
                    <option value="">All kinds</option>
                    {kinds.map((k) => <option key={k} value={k}>{k} ({kindCounts.get(k)})</option>)}
                  </select>
                </label>
              )}
              <label className="lens-search">
                <span className="sr-only">Search routes</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
                  value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search name, kind, parent, status" />
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
            {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id} data-col={c.id}>{c.label}</span>)}
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
                  {show("mark") && <VerdictMark health={role} />}
                  {show("name") && <span className="lens-cell-name" title={`${routeKindOf(r)} ${r.namespace}/${r.name}`}><span style={{ color: "var(--color-text-tertiary)" }}>{r.namespace} / </span>{r.name}</span>}
                  {show("kind") && <span data-col="kind" style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{routeKindOf(r)}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }}>{role.label}{verdictMark}</span>}
                  {show("parent") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }} title={r.parent}>{r.parentLabel || r.parent}</span>}
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
          <InspectorLayout pin={{ cluster: cluster, uid: selected.uid || undefined }} replaced={pin.gone === "replaced" ? pin.adopt : undefined} kind={routeKindOf(selected)} namespace={selected.namespace} name={selected.name} meta={`worst parent ${selected.parentLabel || selected.parent}`} onClose={close}
            verdict={gone === "detached" ? (
              <div role="status" data-testid="route-detached" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
                <span className="t-status">No longer attached to a Gateway.</span> The route and its parent Gateways were both read in full and it is not in the attached set - it may still exist. The details below are its last evidence.
              </div>
            ) : gone === "unverified" ? (
              <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
                <span className="t-status">Not in the current attached-route read.</span> The route or parent Gateway read is incomplete, so this is unverified - it may still be attached. The details below are its last observed row.
              </div>
            ) : (
              <div>
                <InspectorStatus tone={roleTone(routeRole(selected).kind)} kind={routeRole(selected).kind} label={routeRole(selected).label} reason={`worst parent ${selected.parentLabel || selected.parent}`} />
                {dep.word && <div className="t-secondary" style={{ marginTop: -4 }}>The verdict is {dep.word} - evaluated against {dep.kinds.join(", ")}.</div>}
              </div>
            )}
            primary={gone === "detached" || !selectedGateway ? undefined : { label: `Open topology (${selectedGateway})`, onSelect: () => openTopology(selected) }}
            more={[...describe.menu, ...ai.menu]}>
            {ai.block}
            <InspectorBlock title="Parents" count={selected.attachments.length}>
              {selected.attachments.map((a) => {
                const role = routeRole(a);
                const ev = a.evidence;
                return (
                  <div key={a.parent} data-testid="route-attachment" style={{ display: "grid", gap: 2, fontSize: 12 }}>
                    <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                      <VerdictMark health={role} size={8} />
                      <span className="t-identifier" title={a.parent}>{a.parentLabel || a.parent}</span>
                      <span style={{ color: ROLE_TEXT[role.kind] }}>{role.label}</span>
                    </div>
                    {ev && (
                      <div className="t-secondary" style={{ paddingLeft: 16, display: "grid", gap: 1, overflowWrap: "anywhere" }}>
                        <span>{ev.controller ? <>judged by <span className="t-identifier">{ev.controller}</span></> : <span style={{ color: "var(--color-text-warning)" }}>controller not established{ev.controllerWhy ? ` - ${ev.controllerWhy}` : ""}</span>}</span>
                        {(ev.others ?? []).map((o, i) => <span key={i}>an entry from another controller ({o.controller || "unnamed"}), not judged</span>)}
                        {ev.allowance.text && <span style={{ color: ev.allowance.disagrees ? "var(--color-text-warning)" : undefined }}>{ev.allowance.text}</span>}
                        {(ev.grants ?? []).map((g, i) => <span key={`g${i}`}>rule {g.rule + 1} {g.backend}: {g.text}</span>)}
                        {(ev.features ?? []).map((f, i) => <span key={`f${i}`}>{f}</span>)}
                        {(ev.disagreements ?? []).map((d, i) => <span key={`d${i}`} style={{ color: "var(--color-text-warning)" }}>{d}</span>)}
                      </div>
                    )}
                  </div>
                );
              })}
            </InspectorBlock>
          </InspectorLayout>
          {/* the sheet sits beside the layout, so it shows on the Manifest tab too */}
          {describe.sheet}
        </div>
      ) : selKey && pin.expected && pin.gone ? (
        <PinnedGone cluster={cluster} kind={splitKey(selKey).kind} namespace={splitKey(selKey).namespace} name={splitKey(selKey).name} uid={pin.expected} gone={pin.gone}
          removedWords={<><span className="t-status">No longer attached to a Gateway.</span> The {splitKey(selKey).kind} you opened is not in a complete read of the attached routes - it may still exist.</>}
          onAdopt={pin.next ? pin.adopt : undefined} onClose={close} />
      ) : undefined}
      inspectorLabel="Route details"
      onCloseInspector={close}
    />
  );
}
