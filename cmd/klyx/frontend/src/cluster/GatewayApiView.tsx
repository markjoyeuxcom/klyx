import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { IconRoute } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { GatewayRefDTO } from "../store/fleet";
import { listGateways } from "../bridge/gateway";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, useReceivedAt } from "../chrome/Age";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictLamp } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { GatewayTopology } from "./GatewayTopology";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { RANK, REFS, ROLE_TEXT, addressText, gatewayHealth, gatewayVerdictDetail, listenerText, roleTone, type Role } from "./gatewayModel";

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
  return <GatewayList key={cluster} cluster={cluster} />;
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

function GatewayList({ cluster }: { cluster: string }) {
  const net = useFleet((s) => s.network);
  const openGateway = useFleet((s) => s.openGateway);
  const [mode, setMode] = useState<Mode>("all");
  const [query, setQuery] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useState<string | null>(null);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  const refresh = () => void listGateways(cluster).catch(() => { /* rendered from the store */ });
  useEffect(refresh, [cluster]);

  const mine = net.cluster === cluster;
  const loading = !mine || net.listLoading;
  const error = mine ? net.listError ?? null : null;
  const served = mine && net.served;
  const gateways = useMemo(() => (mine ? net.gateways : []), [mine, net.gateways]);
  const receivedAt = useReceivedAt(mine && !net.listLoading && !error ? net.gateways : null);

  const ordered = useMemo(() => [...gateways].sort((a, b) =>
    RANK[gatewayHealth(a).kind] - RANK[gatewayHealth(b).kind] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [gateways]);
  const attention = gateways.filter((g) => RANK[gatewayHealth(g).kind] <= RANK.degraded).length;
  const notAccepted = gateways.filter((g) => gatewayHealth(g).kind === "fault").length;
  const unverified = gateways.filter((g) => ["unknown", "progressing"].includes(gatewayHealth(g).kind)).length;
  const allOk = gateways.length > 0 && gateways.every((g) => gatewayHealth(g).kind === "ok");
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => ordered.filter((g) => {
    const role = gatewayHealth(g);
    if (mode === "attention" && RANK[role.kind] > RANK.degraded) return false;
    if (mode === "programmed" && role.kind !== "ok") return false;
    if (!q) return true;
    return [g.namespace, g.name, g.className, role.label, addressText(g.addresses), ...(g.listeners ?? []).map(listenerText)].some((v) => v.toLowerCase().includes(q));
  }), [ordered, mode, q]);

  const selected = selKey ? gateways.find((g) => keyOf(g) === selKey) : undefined;
  const close = () => setSelKey(null);

  // First fresh read: select the first Gateway needing attention, once.
  const autoDone = useRef(false);
  useEffect(() => {
    if (autoDone.current || loading || error || !served) return;
    autoDone.current = true;
    const first = visible[0];
    if (first && RANK[gatewayHealth(first).kind] <= RANK.degraded && selKey === null) { setSelKey(keyOf(first)); setCursorKey(keyOf(first)); }
  }, [loading, error, served, visible, selKey]);
  // Later operator filter edits win: one that hides the selection closes it.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !visible.includes(selected)) setSelKey(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, q]);
  // A Gateway gone from a complete list read closes; a failed read keeps it.
  useEffect(() => { if (selKey && !selected && mine && !net.listLoading && !error) setSelKey(null); }, [selKey, selected, mine, net.listLoading, error]);

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

  let state: DatasetState = "ready";
  if (error && gateways.length === 0) state = "unreadable";
  else if (loading && gateways.length === 0) state = "loading";
  else if (gateways.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";

  const icon = <IconRoute size={28} stroke={1.2} />;
  const clearFilters = () => { setMode("all"); setQuery(""); };
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading Gateways" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not list Gateways." hint={error ?? undefined} action={{ label: "Retry", onClick: refresh }} />
    : state === "empty" && !served ? <EmptyState icon={icon} title="Gateway API is not installed on this cluster." hint="Klyx found no gateway.networking.k8s.io API here." />
    : state === "empty" ? <EmptyState icon={icon} title="No Gateways on this cluster." hint="Gateway API is served, but no Gateway objects exist." />
    : <EmptyState icon={icon} title="No Gateways match the current filters." hint={`${gateways.length} Gateways on this cluster.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  return (
    <LensLayout
      label="Gateways"
      title="Gateways"
      summary={state === "loading" || state === "unreadable" || !served ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{gateways.length}{error ? "*" : ""}</span>
          <span className="t-secondary">Gateway{gateways.length === 1 ? "" : "s"}</span>
          {attention > 0 && <span className="t-status" style={{ color: notAccepted > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{[notAccepted ? `▲ ${notAccepted}${error ? "*" : ""} not accepted` : "", attention > notAccepted ? `${attention - notAccepted}${error ? "*" : ""} need${attention - notAccepted === 1 ? "s" : ""} attention` : ""].filter(Boolean).join(" · ")}</span>}
          {unverified > 0 && <span className="t-secondary">{unverified}{error ? "*" : ""} unverified</span>}
          {allOk && <span className="t-secondary">all programmed{error ? " at the last read" : ""}</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="Read on demand - not a live watch.">snapshot</span>
          <UpdatedAt at={receivedAt} />
          <button className="l-btn" onClick={refresh} disabled={mine && net.listLoading}>{mine && net.listLoading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={gateways.length > 0 ? (
        <>
          {(
            <>
              <div role="group" aria-label="Gateway filter" style={{ display: "inline-flex", gap: 4 }}>
                <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {gateways.length}</button>
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
      ) : undefined}
      notice={error && gateways.length > 0 ? (
        <div role="status" className="t-body" style={{ color: "var(--color-text-warning)", padding: "4px 0", overflowWrap: "anywhere" }}>
          <span className="t-status">Read failed - showing the last good list.</span> {error}
        </div>
      ) : undefined}
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
              const role = gatewayHealth(g);
              const address = addressText(g.addresses);
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" style={{ ...gridStyle, opacity: g.stale ? 0.6 : undefined }}
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
      footerEnd={<span className="t-secondary">{visible.length !== gateways.length ? `${visible.length} of ${gateways.length} · ` : ""}needs attention first</span>}
      inspector={selected ? <GatewayInspector key={selKey} g={selected} stale={!!error} onOpen={() => openGateway(selected.namespace, selected.name)} onClose={close} /> : undefined}
      inspectorLabel="Gateway details"
      onCloseInspector={close}
    />
  );
}

function GatewayInspector({ g, stale, onOpen, onClose }: { g: GatewayRefDTO; stale: boolean; onOpen: () => void; onClose: () => void }) {
  const role = gatewayHealth(g);
  const open = (ref: typeof REFS.gateway | typeof REFS.gatewayClass, ns: string, name: string) => {
    const nav = useFleet.getState();
    nav.openResource(ref);
    nav.openInstance(ns, name);
  };
  const listeners = g.listeners ?? [];
  const addresses = g.addresses ?? [];
  return (
    <div data-testid="gateway-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout kind="Gateway" namespace={g.namespace} name={g.name} meta={g.className ? `class ${g.className}` : "no class"} onClose={onClose}
        verdict={
          <div>
            <InspectorStatus tone={roleTone(role.kind)} label={role.label} />
            <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{gatewayVerdictDetail(g)}</div>
            {stale && <div className="t-secondary" style={{ marginTop: 4 }}>From the last good list - the latest read failed.</div>}
          </div>
        }
        primary={{ label: "Open topology", onSelect: onOpen }}
        more={[
          { label: "Open Gateway object", onSelect: () => open(REFS.gateway, g.namespace, g.name) },
          ...(g.className ? [{ label: `Open GatewayClass ${g.className}`, onSelect: () => open(REFS.gatewayClass, "", g.className) }] : []),
        ]}>
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
