import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { useDescribe } from "../chrome/describe/useDescribe";
import { IconRoute } from "@tabler/icons-react";
import { useNavIntent } from "../chrome/useNavIntent";
import { useFleet } from "../store/fleet";
import type { GatewayRefDTO, ListenerSetRowDTO } from "../store/fleet";
import { useGatewayRows, type GatewayRows } from "../health/useGatewayRows";
import { useGatewayRouteRows } from "../health/useGatewayRouteRows";
import { useListenerSetRows, type ListenerSetRows } from "../health/useListenerSetRows";
import { RouteList } from "./GatewayRoutesView";
import { ListenerSetList } from "./ListenerSetsView";
import { ListenerList, allowedListenersText, ordinal, parentText } from "./gatewayListeners";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, coverageGaps, kindEvidence, lastGoodAt } from "../layout/coverage";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { CountSentence } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { GatewayTopology } from "./GatewayTopology";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { RANK, REFS, ROLE_TEXT, addressText, gatewayRole, gatewayVerdictDetail, listenerText, roleTone } from "./gatewayModel";
import { useInspectorAI } from "../ai/AIExplain";
import { staleProps } from "./staleness";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

// GatewayApiView - the Gateway API lens on the workspace template (spec §6
// step 6). Without a selected Gateway it is the Gateways table (not accepted
// first) with an inspector whose primary action opens the topology canvas;
// with one (route.gateway) it is that Gateway's topology. Hubble flows are a
// Network destination of their own (HubbleFlowsView).

export function GatewayApiView({ cluster }: { cluster: string }) {
  const route = useFleet((s) => s.route);
  const gateway = route.name === "cluster" ? route.gateway : undefined;
  // Keyed: filters, selection and one-shot auto-selection never carry over
  // between clusters or Gateways.
  if (gateway) return <GatewayTopology key={`${cluster}|${gateway.namespace}/${gateway.name}`} cluster={cluster} gateway={gateway} />;
  return <GatewayLists key={cluster} cluster={cluster} />;
}

// GatewayLists - the Gateways task's parts as views of one list (1c slice 3
// §4a, coverage S7a §9): the Gateways, the ListenerSets (offered unless the
// kind is confirmed not served), and the HTTPRoutes counted once by UID with
// their worst attachment. Each view's counts are its own part's.
type View = "gateways" | "listenersets" | "routes";
function GatewayLists({ cluster }: { cluster: string }) {
  const [view, setView] = useState<View>("gateways");
  const rows = useGatewayRows(cluster);
  const lsRows = useListenerSetRows(cluster);
  const routeRows = useGatewayRouteRows(cluster);
  const openGateway = useFleet((s) => s.openGateway);
  const gwAttention = rows.partCounts?.included ?? rows.gateways.filter((g) => g.rule.included).length;
  const lsAttention = lsRows.partCounts?.included ?? lsRows.sets.filter((s) => s.rule.included).length;
  const routeAttention = routeRows.partCounts?.included ?? routeRows.routes.filter((r) => r.rule.included).length;
  // capability over assumption: a confirmed absence hides the view
  const lsServed = lsRows.coverage.find((k) => k.kind === "ListenerSet")?.coverage !== "absent";
  // A summary Top link (health 1c slice 4 §5.4): this switches to the
  // part's view and leaves the intent for that view's list to select.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind === "reveal" && intent.task === "gateway") {
      const target: View = intent.part === "routes" ? "routes" : intent.part === "listenersets" ? "listenersets" : "gateways";
      if (view !== target) setView(target);
    }
    return "wait";
  }, [view]);
  const shown: View = view === "listenersets" && !lsServed ? "gateways" : view;
  const chip = (v: View, label: string, n: number) => (
    <button className="l-chip" data-view={v} aria-pressed={shown === v} onClick={() => setView(v)}>{label} {n > 0 && <span style={{ color: "var(--color-text-warning)" }}>{n}</span>}</button>
  );
  const toggle = (
    <div role="group" aria-label="Gateway API view" className="l-seg" data-testid="gateway-views">
      {chip("gateways", "Gateways", gwAttention)}
      {lsServed && chip("listenersets", "ListenerSets", lsAttention)}
      {chip("routes", "Routes", routeAttention)}
    </div>
  );
  return shown === "routes"
    ? <RouteList cluster={cluster} rows={routeRows} toggle={toggle} />
    : shown === "listenersets"
      ? <ListenerSetList cluster={cluster} rows={lsRows} toggle={toggle} onOpenGateway={openGateway} />
      : <GatewayList cluster={cluster} rows={rows} sets={lsRows} toggle={toggle} />;
}

// accepted: the controller accepted the ListenerSet now, so its listeners
// merge into the parent (the list column's count).
const acceptedNow = (s: ListenerSetRowDTO) => s.acceptedCond.status === "True" && !(s.generation && s.acceptedCond.observedGeneration < s.generation);
const setsOf = (sets: ListenerSetRowDTO[], g: { namespace: string; name: string }) =>
  sets.filter((s) => s.parent.kind === "Gateway" && s.parent.group === "gateway.networking.k8s.io" && s.parent.namespace === g.namespace && s.parent.name === g.name)
    .sort((a, b) => a.precedence - b.precedence || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));

type Mode = "all" | "attention" | "programmed";
const keyOf = (g: { namespace: string; name: string }) => `${g.namespace}/${g.name}`;
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };

const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "name", label: "Gateway", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 170, priority: 0 },
  { id: "address", label: "Address", width: 150, priority: 1 },
  { id: "class", label: "Class", width: 140, priority: 2 },
  { id: "listeners", label: "Listeners", width: 200, priority: 3 },
];


function GatewayList({ cluster, rows, sets, toggle }: { cluster: string; rows: GatewayRows; sets: ListenerSetRows; toggle: React.ReactNode }) {
  // The Gateways are the Observer's shared rows (1c slice 3 §5.6) - live, not
  // a snapshot; the topology stays a fetch.
  const openGateway = useFleet((s) => s.openGateway);
  const [mode, setMode] = useState<Mode>("all");
  const [query, setQuery] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  // Evidence is the Gateway kind's own coverage: only a confirmed absence is
  // "not served"; a stale read keeps its last good rows, qualified.
  const ev = kindEvidence(!rows.loading, rows.coverage, "Gateway", rows.watchError);
  const absent = rows.coverage.find((k) => k.kind === "Gateway")?.coverage === "absent";
  const error = rows.watchError;
  const gateways = rows.gateways;
  const rowsRefreshing = rows.rowsState.refreshing;
  const rowsAsOf = rows.rowsState.evaluatedAt ? clock(rows.rowsState.evaluatedAt) : "the last read";
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = lastGoodAt(rows.coverage, ["Gateway"]);
  const discoveryGap = coverageGaps(rows.coverage);
  // Gateways are budgeted (coverage S7a): a truncated read is current but not
  // complete - counts are lower bounds and a Gateway left out is unverified
  const capped = rows.coverage.find((k) => k.kind === "Gateway" && k.truncated);
  const complete = ev === "complete" && !capped;
  const mark = complete ? "" : ev === "stale" ? "*" : "+";
  // the ListenerSets read the joins come from: a current read is needed for
  // any merged count, and a truncated one gives a lower bound
  const lsEv = kindEvidence(!sets.loading, sets.coverage, "ListenerSet", sets.watchError);
  const lsCapped = sets.coverage.some((k) => k.kind === "ListenerSet" && k.truncated);

  const ordered = useMemo(() => [...gateways].sort((a, b) =>
    RANK[gatewayRole(a).kind] - RANK[gatewayRole(b).kind] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [gateways]);
  // global: the count selector's (slice 3 §4a); row-scoped: the breakdown
  const total = rows.partCounts?.total ?? gateways.length;
  const attention = rows.partCounts?.included ?? gateways.filter((g) => g.rule.included).length;
  const notAccepted = gateways.filter((g) => gatewayRole(g).kind === "fault").length;
  // the header's mark from the reading that gave its count (slice 11b review)
  const gwFault = rows.partCounts ? rows.partCounts.fault : gateways.filter((g) => g.rule.included && gatewayRole(g).kind === "fault").length;
  const unverified = gateways.filter((g) => ["unknown", "progressing"].includes(gatewayRole(g).kind)).length;
  // "all programmed" is a claim about the present: a complete, current read
  const allOk = gateways.length > 0 && complete && !rows.rowsState.updating && !rowsRefreshing && gateways.every((g) => gatewayRole(g).kind === "ok");
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => ordered.filter((g) => {
    const role = gatewayRole(g);
    if (mode === "attention" && RANK[role.kind] > RANK.degraded) return false;
    if (mode === "programmed" && role.kind !== "ok") return false;
    if (!q) return true;
    return [g.namespace, g.name, g.className, role.label, addressText(g.addresses), ...(g.listeners ?? []).map(listenerText)].some((v) => v.toLowerCase().includes(q));
  }), [ordered, mode, q]);

  // the one-shot first-broken selection; a consumed reveal is that selection
  // (a view mounting with a pending reveal runs both in one commit)
  const autoDone = useRef(false);
  // A summary Top link for a Gateway: select it past the filters.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "gateway" || intent.part !== "gateways") return "wait";
    const key = `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const row = gateways.find((g) => keyOf(g) === key);
    // An opening by UID (slice 9 note §4.2): pinned to that UID at once; a
    // different or missing row is said, never substituted.
    if (intent.ref.uid) expectPin(cluster, "Gateway", key, intent.ref.uid);
    else if (!row) return complete ? "missing" : "wait";
    setMode("all"); setQuery("");
    setCursorKey(key);
    setSelKey(key);
    autoDone.current = true;
    return "consumed";
  }, [gateways, complete]);

  // A selection keeps its last row - removed only on a complete read,
  // otherwise unverified (slice 3 §5a) - and is pinned to its Gateway's UID:
  // a same-name Gateway with a new UID on a complete read is "replaced"
  // (The Line slice 4 §6.3), never silently followed.
  const found = selKey ? gateways.find((g) => keyOf(g) === selKey) : undefined;
  const pin = usePinnedSubject(cluster, selKey, found, complete, "Gateway");
  const selected = pin.subject ?? undefined;
  const gone: "removed" | "unverified" | null = pin.gone === "replaced" ? "removed" : pin.gone;
  const close = () => setSelKey(null);

  // First fresh read: select the first Gateway needing attention, once.
  useEffect(() => {
    if (autoDone.current || ev !== "complete") return;
    autoDone.current = true;
    const first = visible[0];
    if (first && first.rule.included && selKey === null) { setSelKey(keyOf(first)); setCursorKey(keyOf(first)); }
  }, [ev, visible, selKey, setSelKey]);
  // Later operator filter edits win: one that hides the selection closes it.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !visible.includes(selected)) setSelKey(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, q]);

  const cursorIdx = cursorKey === null ? -1 : visible.findIndex((g) => keyOf(g) === cursorKey);
  useListKeys({
    count: visible.length,
    selected: cursorIdx,
    onSelect: (i) => { const g = visible[i]; if (g) { setCursorKey(keyOf(g)); listRef.current?.scrollToIndex(i); } },
    onActivate: (i) => { const g = visible[i]; if (g) { setCursorKey(keyOf(g)); setSelKey(keyOf(g)); } },
    onEscape: close,
    searchRef,
  });

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  // a confirmed absence is complete evidence of none: the empty state names it
  let state: DatasetState = "ready";
  if (ev === "failed" && gateways.length === 0) state = "unreadable";
  else if (ev === "pending" && gateways.length === 0) state = "loading";
  else if (gateways.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";

  const icon = <IconRoute size={28} stroke={1.2} />;
  const clearFilters = () => { setMode("all"); setQuery(""); };
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading Gateways" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not list Gateways." hint={error ?? undefined} />
    : state === "empty" && absent ? <EmptyState icon={icon} title="Gateway API is not installed on this cluster." hint="Klyx observes no gateway.networking.k8s.io Gateways API here." />
    : state === "empty" && rowsRefreshing && total > 0 ? <EmptyState icon={icon} title={`No Gateways as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${total}.`} />
    : state === "empty" && ev === "stale" ? <EmptyState icon={icon} title="No Gateways in the last good read." hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${error ?? "unverified"}.`} />
    : state === "empty" && capped ? <EmptyState icon={icon} title="No Gateways kept within Klyx's budget." hint={`${capped.notRetained ?? "Some"} Gateway${capped.notRetained === 1 ? "" : "s"} left out by the budget: they exist, but Klyx keeps none of them.`} />
    : state === "empty" ? <EmptyState icon={icon} title="No Gateways on this cluster." hint="Gateway API is served, but no Gateway objects exist." />
    : <EmptyState icon={icon} title="No Gateways match the current filters." hint={`${gateways.length} Gateways on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (error && gateways.length > 0) notices.push(<WatchErrorBanner key="read" tool="Gateways" failure={`read failed - showing the last good list${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={error} />);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{rows.rowsState.refreshError ? ` Last re-read failed: ${rows.rowsState.refreshError}` : ""}</div>);
  if (capped) notices.push(<div key="capped" role="status" data-testid="gateways-capped" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)" }}>
    Capped: {capped.notRetained ?? "some"} Gateway{capped.notRetained === 1 ? "" : "s"} not retained - Klyx keeps a bounded amount per cluster, so counts are lower bounds and a Gateway left out is unverified, never removed.</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);

  return (
    <LensLayout
      label="Gateways"
      title="Gateways and routes"
      summary={state === "loading" || state === "unreadable" || absent ? null : (
        <>
          <CountSentence total={total} marker={mark} noun={["gateway", "gateways"]}
            attention={{ count: attention, phrase: "", fault: gwFault > 0, testid: "gw-attention",
              text: rowsRefreshing ? `${attention} need attention` : [notAccepted ? `${notAccepted}${mark} not accepted` : "", attention > notAccepted ? `${attention - notAccepted}${mark} need${attention - notAccepted === 1 ? "s" : ""} attention` : ""].filter(Boolean).join(" · ") }} />
          {unverified > 0 && <span className="t-secondary">{unverified}{mark} unverified{asOf}</span>}
          {allOk && <span className="t-secondary">all programmed</span>}
        </>
      )}
      controls={(
        <>
          {toggle}
          {gateways.length > 0 && (
            <>
              <div role="group" aria-label="Gateway filter" className="l-seg">
                <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {total}</button>
                <button className="l-chip" aria-pressed={mode === "attention"} onClick={() => setMode("attention")}>Needs attention {attention > 0 && <span style={{ color: notAccepted > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{attention}</span>}</button>
                <button className="l-chip" aria-pressed={mode === "programmed"} onClick={() => setMode("programmed")}>Programmed</button>
              </div>
              <label className="lens-search">
                <span className="sr-only">Search Gateways</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
                  value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search name, class, address, listener" />
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
            render={(g) => {
              const k = keyOf(g);
              const role = gatewayRole(g);
              const address = addressText(g.addresses);
              // merged listeners from a current ListenerSets read only; a
              // truncated read is a lower bound (Codex's S7a review, finding 2)
              const merged = lsEv === "complete" ? setsOf(sets.sets, g).filter(acceptedNow) : [];
              const extra = merged.reduce((n, s) => n + s.listeners.length, 0);
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" {...staleProps(g.stale)} style={gridStyle}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onDoubleClick={() => openGateway(g.namespace, g.name)}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
                  {show("mark") && <VerdictMark health={role} stale={g.stale} />}
                  {show("name") && <span className="lens-cell-name" title={`${g.namespace}/${g.name}`}><span style={{ color: "var(--color-text-tertiary)" }}>{g.namespace} / </span>{g.name}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }}
                    title={g.stale ? `Observed Gen: ${g.observedGeneration ?? "?"} vs Current: ${g.generation ?? "?"}` : undefined}>{role.label}</span>}
                  {show("address") && <span style={{ ...cellStyle, color: address ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{address || "none"}</span>}
                  {show("class") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{g.className || "-"}</span>}
                  {show("listeners") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}
                    title={extra > 0 ? `${extra} more from ${merged.length} accepted ListenerSet${merged.length === 1 ? "" : "s"}` : undefined}>
                    {(g.listeners ?? []).map(listenerText).join(", ") || "-"}{extra > 0 ? ` +${extra}${lsCapped ? " or more" : ""} from ListenerSets` : ""}</span>}
                </div>
              );
            }} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · double-click opens topology · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length !== gateways.length ? `${visible.length} of ${gateways.length}${mark} · ` : ""}needs attention first{asOf}</span>}
      inspector={selected ? <GatewayInspector key={selKey} cluster={cluster} g={selected} sets={sets} stale={ev === "stale"} gone={gone} replaced={pin.gone === "replaced" ? pin.adopt : undefined} onOpen={() => openGateway(selected.namespace, selected.name)} onClose={close} />
        : selKey && pin.expected && pin.gone ? <PinnedGone cluster={cluster} kind="Gateway" namespace={selKey.split("/")[0]} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={close} />
        : undefined}
      inspectorLabel="Gateway details"
      onCloseInspector={close}
    />
  );
}

function GatewayInspector({ cluster, g, sets, stale, gone, replaced, onOpen, onClose }: { cluster: string; g: GatewayRefDTO; sets: ListenerSetRows; stale: boolean; gone: "removed" | "unverified" | null; replaced?: () => void; onOpen: () => void; onClose: () => void }) {
  const ai = useInspectorAI(g.uid ? { kind: "row" as const, task: "gateway", part: "gateways", uid: g.uid } : null, `Gateway/${g.namespace}/${g.name}`, !gone, cluster);
  // Describe (slice 14c): kubectl's report for this object, read-only; never
  // for a removed or unverified selection.
  const describe = useDescribe(cluster, !gone ? { kind: "Gateway", namespace: g.namespace, name: g.name, uid: g.uid } : null);
  const role = gatewayRole(g);
  const open = (ref: typeof REFS.gateway | typeof REFS.gatewayClass, ns: string, name: string) => {
    const nav = useFleet.getState();
    nav.openResource(ref);
    nav.openInstance(ns, name);
  };
  const listeners = g.listeners ?? [];
  const addresses = g.addresses ?? [];
  return (
    <div data-testid="gateway-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: g.uid || undefined }} replaced={replaced} kind="Gateway" namespace={g.namespace} name={g.name} meta={g.className ? `class ${g.className}` : "no class"} onClose={onClose}
        verdict={gone === "removed" ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">Gateway removed.</span> It is no longer in the current read. The details below are its last evidence.
          </div>
        ) : gone === "unverified" ? (
          <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <span className="t-status">Not in the current Gateway read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
          </div>
        ) : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} />
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>{gatewayVerdictDetail(g)}</div>
            {stale && <div className="t-secondary" style={{ marginTop: 4 }}>From the last good list - the latest read failed.</div>}
          </div>
        )}
        primary={gone === "removed" ? undefined : { label: "Open topology", onSelect: onOpen }}
        more={[...([
          { label: "Open Gateway object", onSelect: () => open(REFS.gateway, g.namespace, g.name) },
          ...(g.className ? [{ label: `Open GatewayClass ${g.className}`, onSelect: () => open(REFS.gatewayClass, "", g.className) }] : []),
        ]), ...describe.menu, ...ai.menu]}>
        {ai.block}
        <InspectorBlock title="Listeners" count={listeners.length}>
          {listeners.length === 0 ? <div className="t-secondary">No listeners reported.</div> : <ListenerList listeners={listeners} testid="gateway-listeners" />}
        </InspectorBlock>
        <GatewayListenerSets g={g} sets={sets} gone={gone} gatewayStale={stale} />
        <InspectorBlock title="Addresses" count={addresses.length}>
          {addresses.length === 0 ? <div className="t-secondary">None assigned{role.kind === "ok" ? "" : " - typical while the Gateway is not programmed"}.</div> : (
            <div className="insp-kv">
              {addresses.map((a, i) => <Fragment key={i}><span>{a.type || "address"}</span><span>{a.value}</span></Fragment>)}
            </div>
          )}
        </InspectorBlock>
      </InspectorLayout>
      {/* the sheet sits beside the layout, so it shows on the Manifest tab too */}
      {describe.sheet}
    </div>
  );
}

// GatewayListenerSets - the ListenerSets naming this Gateway, in the parent's
// merge order after its own listeners (coverage S7a §4, §9), each with its
// controller's verdict and listeners, beside the Gateway's allowedListeners
// and its own attachedListenerSets count. A difference is said, never
// reconciled; a conflict is the controller's to decide.
function GatewayListenerSets({ g, sets, gone, gatewayStale }: { g: GatewayRefDTO; sets: ListenerSetRows; gone: "removed" | "unverified" | null; gatewayStale: boolean }) {
  const served = sets.coverage.find((k) => k.kind === "ListenerSet")?.coverage;
  if (served === "absent") return null;
  // the live join names a parent by namespace/name, not UID: a Gateway that
  // is no longer the current object of its name takes none of it (Codex's
  // S7a implementation review, finding 4)
  if (gone) return (
    <InspectorBlock title="ListenerSets">
      <div className="t-secondary" data-testid="gateway-listenersets-withheld">Not joined: this Gateway is not the current one of its name, so the ListenerSets naming {g.namespace}/{g.name} now are not its evidence.</div>
    </InspectorBlock>
  );
  const mine = setsOf(sets.sets, g);
  const ev = sets.loading ? "pending" : kindEvidence(true, sets.coverage, "ListenerSet", sets.watchError);
  const capped = sets.coverage.some((k) => k.kind === "ListenerSet" && k.truncated);
  const complete = ev === "complete" && !capped;
  // nothing to say for a Gateway that allows none, has none and reports none
  if (!g.allowedListeners?.set && mine.length === 0 && g.attachedListenerSets == null && complete) return null;
  const accepted = mine.filter(acceptedNow).length;
  const reported = g.attachedListenerSets;
  const lastGood = lastGoodAt(sets.coverage, ["ListenerSet"]);
  const asOfRead = ev === "stale" ? ` (the last good read${lastGood ? `, ${clock(lastGood)}` : ""})` : "";
  const count = sets.loading ? "reading ListenerSets" : `${mine.length}${complete ? "" : "+"} found naming this Gateway, ${accepted} accepted${asOfRead}`;
  // a disagreement only between two current readings: the ListenerSets read
  // complete, and the Gateway's own count from a current read of it - a
  // retained current Gateway's fields stand even if others were left out,
  // but a stale one's count is historical (Codex's S7a re-review, finding 1)
  const differs = reported != null && complete && !gatewayStale && reported !== accepted;
  const reportedText = reported == null ? " · the controller reports no attached count"
    : gatewayStale ? ` · the controller reported ${reported} attached when the Gateways were last read` : ` · the controller reports ${reported} attached`;
  return (
    <InspectorBlock title="ListenerSets" count={mine.length}>
      <div data-testid="gateway-listenersets" style={{ display: "grid", gap: 8 }}>
        <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>{allowedListenersText(g.allowedListeners)}</div>
        <div className="t-secondary" data-testid="gateway-listenersets-count" style={{ color: differs ? "var(--color-text-warning)" : undefined }}>
          {count}{reportedText}{differs ? " - they differ" : ""}
        </div>
        {mine.map((s) => {
          const role = { kind: s.rule.role as "fault" | "degraded" | "progressing" | "ok" | "unknown", label: s.rule.label };
          return (
            <div key={`${s.namespace}/${s.name}`} data-testid="gateway-listenerset" style={{ display: "grid", gap: 4, paddingTop: 6, borderTop: "1px solid var(--color-border-tertiary)" }}>
              <div className="t-body" style={{ overflowWrap: "anywhere" }}>
                <span style={{ color: "var(--color-text-tertiary)" }}>{ordinal(s.precedence)} · </span>
                <span style={{ color: "var(--color-text-primary)", fontWeight: 500 }}>{s.namespace}/{s.name}</span>
                <span style={{ color: ROLE_TEXT[role.kind] }}> {role.label}</span>
              </div>
              {s.allowance.disagrees && <div className="t-secondary" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>{s.allowance.text}</div>}
              {parentText(s) && <div className="t-secondary">{parentText(s)}</div>}
              <ListenerList listeners={s.listeners} testid={`gateway-listenerset-${s.namespace}-${s.name}`} />
            </div>
          );
        })}
        {!complete && !sets.loading && <div className="t-secondary">{capped ? "Klyx's budget left ListenerSets out" : ev === "stale" ? "The ListenerSets read is failing" : "The ListenerSets read is incomplete"}: one that ranks earlier may be missing, and no difference from the controller's count is claimed.</div>}
      </div>
    </InspectorBlock>
  );
}
