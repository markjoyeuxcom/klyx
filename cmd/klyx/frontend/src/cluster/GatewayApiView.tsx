import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { IconRoute } from "@tabler/icons-react";
import { useNavIntent } from "../chrome/useNavIntent";
import { useFleet } from "../store/fleet";
import type { GatewayRefDTO } from "../store/fleet";
import { useGatewayRows, type GatewayRows } from "../health/useGatewayRows";
import { useGatewayRouteRows } from "../health/useGatewayRouteRows";
import { RouteList } from "./GatewayRoutesView";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, coverageGaps, kindEvidence, lastGoodAt } from "../layout/coverage";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictLamp } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { GatewayTopology } from "./GatewayTopology";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { RANK, REFS, ROLE_TEXT, addressText, gatewayRole, gatewayVerdictDetail, listenerText, roleTone, type Role } from "./gatewayModel";
import { useInspectorAI } from "../ai/AIExplain";
import { staleProps } from "./staleness";
import { useClusterKey } from "./useClusterKey";

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

// GatewayLists - the Gateways task's two parts as two views of one list
// (1c slice 3 §4a): the Gateways, and the HTTPRoutes counted once by UID
// with their worst attachment. Each view's counts are its own part's.
function GatewayLists({ cluster }: { cluster: string }) {
  const [view, setView] = useState<"gateways" | "routes">("gateways");
  const rows = useGatewayRows(cluster);
  const routeRows = useGatewayRouteRows(cluster);
  const gwAttention = rows.partCounts?.included ?? rows.gateways.filter((g) => g.rule.included).length;
  const routeAttention = routeRows.partCounts?.included ?? routeRows.routes.filter((r) => r.rule.included).length;
  // A summary Top link (health 1c slice 4 §5.4): this switches to the
  // part's view and leaves the intent for that view's list to select.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind === "reveal" && intent.task === "gateway") {
      const target = intent.part === "routes" ? "routes" : "gateways";
      if (view !== target) setView(target);
    }
    return "wait";
  }, [view]);
  const toggle = (
    <div role="group" aria-label="Gateway API view" style={{ display: "inline-flex", gap: 4 }}>
      <button className="l-chip" aria-pressed={view === "gateways"} onClick={() => setView("gateways")}>Gateways {gwAttention > 0 && <span style={{ color: "var(--color-text-warning)" }}>{gwAttention}</span>}</button>
      <button className="l-chip" aria-pressed={view === "routes"} onClick={() => setView("routes")}>Routes {routeAttention > 0 && <span style={{ color: "var(--color-text-warning)" }}>{routeAttention}</span>}</button>
    </div>
  );
  return view === "routes"
    ? <RouteList cluster={cluster} rows={routeRows} toggle={toggle} />
    : <GatewayList cluster={cluster} rows={rows} toggle={toggle} />;
}

type Mode = "all" | "attention" | "programmed";
const keyOf = (g: { namespace: string; name: string }) => `${g.namespace}/${g.name}`;
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };

const COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Gateway", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 170, priority: 0 },
  { id: "address", label: "Address", width: 150, priority: 1 },
  { id: "class", label: "Class", width: 140, priority: 2 },
  { id: "listeners", label: "Listeners", width: 200, priority: 3 },
];

function Lamp({ role }: { role: Role }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}

function GatewayList({ cluster, rows, toggle }: { cluster: string; rows: GatewayRows; toggle: React.ReactNode }) {
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
  const mark = ev === "complete" ? "" : ev === "stale" ? "*" : "+";

  const ordered = useMemo(() => [...gateways].sort((a, b) =>
    RANK[gatewayRole(a).kind] - RANK[gatewayRole(b).kind] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [gateways]);
  // global: the count selector's (slice 3 §4a); row-scoped: the breakdown
  const total = rows.partCounts?.total ?? gateways.length;
  const attention = rows.partCounts?.included ?? gateways.filter((g) => g.rule.included).length;
  const notAccepted = gateways.filter((g) => gatewayRole(g).kind === "fault").length;
  const unverified = gateways.filter((g) => ["unknown", "progressing"].includes(gatewayRole(g).kind)).length;
  // "all programmed" is a claim about the present: a complete, current read
  const allOk = gateways.length > 0 && ev === "complete" && !rows.rowsState.updating && !rowsRefreshing && gateways.every((g) => gatewayRole(g).kind === "ok");
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => ordered.filter((g) => {
    const role = gatewayRole(g);
    if (mode === "attention" && RANK[role.kind] > RANK.degraded) return false;
    if (mode === "programmed" && role.kind !== "ok") return false;
    if (!q) return true;
    return [g.namespace, g.name, g.className, role.label, addressText(g.addresses), ...(g.listeners ?? []).map(listenerText)].some((v) => v.toLowerCase().includes(q));
  }), [ordered, mode, q]);

  // A summary Top link for a Gateway: select it past the filters.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "gateway" || intent.part !== "gateways") return "wait";
    const key = `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    if (!gateways.some((g) => keyOf(g) === key)) return ev === "complete" ? "missing" : "wait";
    setMode("all"); setQuery("");
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [gateways, ev]);

  // A selection keeps its last row: removed only on a complete read,
  // otherwise unverified (slice 3 §5a).
  const live = selKey ? gateways.find((g) => keyOf(g) === selKey) : undefined;
  const lastSel = useRef<GatewayRefDTO | null>(null);
  if (live) lastSel.current = live;
  const prior = lastSel.current && keyOf(lastSel.current) === selKey ? lastSel.current : null;
  const selected = live ?? prior ?? undefined;
  const gone: "removed" | "unverified" | null = !live && selected ? (ev === "complete" ? "removed" : "unverified") : null;
  const close = () => setSelKey(null);

  // First fresh read: select the first Gateway needing attention, once.
  const autoDone = useRef(false);
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
    : state === "empty" ? <EmptyState icon={icon} title="No Gateways on this cluster." hint="Gateway API is served, but no Gateway objects exist." />
    : <EmptyState icon={icon} title="No Gateways match the current filters." hint={`${gateways.length} Gateways on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (error && gateways.length > 0) notices.push(<WatchErrorBanner key="read" tool="Gateways" failure={`read failed - showing the last good list${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={error} />);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{rows.rowsState.refreshError ? ` Last re-read failed: ${rows.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);

  return (
    <LensLayout
      label="Gateways"
      title="Gateways"
      summary={state === "loading" || state === "unreadable" || absent ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{total}{mark}</span>
          {attention > 0 && <span className="t-status" data-testid="gw-attention" style={{ color: notAccepted > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{rowsRefreshing ? `${attention} need attention` : [notAccepted ? `▲ ${notAccepted}${mark} not accepted` : "", attention > notAccepted ? `${attention - notAccepted}${mark} need${attention - notAccepted === 1 ? "s" : ""} attention` : ""].filter(Boolean).join(" · ")}</span>}
          {unverified > 0 && <span className="t-secondary">{unverified}{mark} unverified{asOf}</span>}
          {allOk && <span className="t-secondary">all programmed</span>}
        </>
      )}
      controls={(
        <>
          {toggle}
          {gateways.length > 0 && (
            <>
              <div role="group" aria-label="Gateway filter" style={{ display: "inline-flex", gap: 4 }}>
                <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {total}</button>
                <button className="l-chip" aria-pressed={mode === "attention"} onClick={() => setMode("attention")}>Needs attention {attention > 0 && <span style={{ color: notAccepted > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{attention}</span>}</button>
                <button className="l-chip" aria-pressed={mode === "programmed"} onClick={() => setMode("programmed")}>Programmed</button>
              </div>
              <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
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
            {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={visible} rowHeight={TABLE_ROW_HEIGHT} rowKey={keyOf} style={{ flex: 1, minHeight: 0 }}
            render={(g) => {
              const k = keyOf(g);
              const role = gatewayRole(g);
              const address = addressText(g.addresses);
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" {...staleProps(g.stale)} style={gridStyle}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onDoubleClick={() => openGateway(g.namespace, g.name)}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
                  {show("lamp") && <Lamp role={role} />}
                  {show("name") && <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{g.namespace}/</span>{g.name}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}
                    title={g.stale ? `Observed Gen: ${g.observedGeneration ?? "?"} vs Current: ${g.generation ?? "?"}` : undefined}>{role.kind === "fault" ? "▲ " : ""}{role.label}</span>}
                  {show("address") && <span style={{ ...cellStyle, color: address ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{address || "none"}</span>}
                  {show("class") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{g.className || "-"}</span>}
                  {show("listeners") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{(g.listeners ?? []).map(listenerText).join(", ") || "-"}</span>}
                </div>
              );
            }} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · double-click opens topology · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length !== gateways.length ? `${visible.length} of ${gateways.length}${mark} · ` : ""}needs attention first{asOf}</span>}
      inspector={selected ? <GatewayInspector key={selKey} cluster={cluster} g={selected} stale={ev === "stale"} gone={gone} onOpen={() => openGateway(selected.namespace, selected.name)} onClose={close} /> : undefined}
      inspectorLabel="Gateway details"
      onCloseInspector={close}
    />
  );
}

function GatewayInspector({ cluster, g, stale, gone, onOpen, onClose }: { cluster: string; g: GatewayRefDTO; stale: boolean; gone: "removed" | "unverified" | null; onOpen: () => void; onClose: () => void }) {
  const ai = useInspectorAI(g.uid ? { kind: "row" as const, task: "gateway", part: "gateways", uid: g.uid } : null, `Gateway/${g.namespace}/${g.name}`, !gone, cluster);
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
      <InspectorLayout pin={{ cluster: cluster, uid: g.uid || undefined }} kind="Gateway" namespace={g.namespace} name={g.name} meta={g.className ? `class ${g.className}` : "no class"} onClose={onClose}
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
            <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{gatewayVerdictDetail(g)}</div>
            {stale && <div className="t-secondary" style={{ marginTop: 4 }}>From the last good list - the latest read failed.</div>}
          </div>
        )}
        primary={gone === "removed" ? undefined : { label: "Open topology", onSelect: onOpen }}
        more={[...([
          { label: "Open Gateway object", onSelect: () => open(REFS.gateway, g.namespace, g.name) },
          ...(g.className ? [{ label: `Open GatewayClass ${g.className}`, onSelect: () => open(REFS.gatewayClass, "", g.className) }] : []),
        ]), ...ai.menu]}>
        {ai.block}
        <InspectorBlock title="Listeners" count={listeners.length}>
          {listeners.length === 0 ? <div className="t-secondary">No listeners reported.</div> : (
            <div className="insp-kv">
              {listeners.map((l, i) => (
                <Fragment key={i}>
                  <span>{l.name || "unnamed"}</span>
                  <span>{l.protocol}{l.port > 0 ? `:${l.port}` : ""}{l.hostname ? ` · ${l.hostname}` : ""}{(l.certificateRefs?.length ?? 0) > 0 ? ` · ${l.certificateRefs!.length} TLS ref${l.certificateRefs!.length === 1 ? "" : "s"}` : ""}</span>
                </Fragment>
              ))}
            </div>
          )}
        </InspectorBlock>
        <InspectorBlock title="Addresses" count={addresses.length}>
          {addresses.length === 0 ? <div className="t-secondary">None assigned{role.kind === "ok" ? "" : " - typical while the Gateway is not programmed"}.</div> : (
            <div className="insp-kv">
              {addresses.map((a, i) => <Fragment key={i}><span>{a.type || "address"}</span><span>{a.value}</span></Fragment>)}
            </div>
          )}
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}
