import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { IconChartDots3, IconRoute } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { GatewayRefDTO, HubbleStatsDTO } from "../store/fleet";
import { getHubbleFlowStats, listGateways } from "../bridge/gateway";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, useReceivedAt } from "../chrome/Age";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictLamp } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { useHasHubble } from "./HubbleFlowsPanel";
import { GatewayTopology } from "./GatewayTopology";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { RANK, REFS, ROLE_TEXT, addressText, gatewayHealth, gatewayVerdictDetail, listenerText, roleTone, type Role } from "./gatewayModel";

// GatewayApiView - the Gateway API lens on the workspace template (spec §6
// step 6). Without a selected Gateway it is the Gateways table (not accepted
// first) with an inspector whose primary action opens the topology canvas;
// with one (route.gateway) it is that Gateway's topology. Hubble flow triage,
// when Cilium Hubble is detected, is a second view of the same lens.

export function GatewayApiView({ cluster }: { cluster: string }) {
  const route = useFleet((s) => s.route);
  const gateway = route.name === "cluster" ? route.gateway : undefined;
  // Keyed: filters, selection and one-shot auto-selection never carry over
  // between clusters or Gateways.
  if (gateway) return <GatewayTopology key={`${cluster}|${gateway.namespace}/${gateway.name}`} cluster={cluster} gateway={gateway} />;
  return <GatewayList key={cluster} cluster={cluster} />;
}

type Mode = "all" | "attention" | "programmed";
type View = "gateways" | "flows";
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
  const hasHubble = useHasHubble(cluster);
  const [view, setView] = useState<View>("gateways");
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
    count: view === "gateways" ? visible.length : 0,
    selected: cursorIdx,
    onSelect: (i) => { const g = visible[i]; if (g) { setCursorKey(keyOf(g)); listRef.current?.scrollToIndex(i); } },
    onActivate: (i) => { const g = visible[i]; if (g) { setCursorKey(keyOf(g)); setSelKey(keyOf(g)); } },
    onEscape: close,
    searchRef,
  });

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const switchView = (v: View) => { setView(v); setSelKey(null); setCursorKey(null); };
  const flows = useHubble(cluster, view === "flows");

  if (view === "flows" && hasHubble) {
    return <FlowsLayout flows={flows} viewSwitch={<ViewSwitch view={view} onView={switchView} attention={attention} notAccepted={notAccepted} />} />;
  }

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
          {attention > 0
            ? <span className="t-status" style={{ color: notAccepted > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{[notAccepted ? `▲ ${notAccepted}${error ? "*" : ""} not accepted` : "", attention > notAccepted ? `${attention - notAccepted}${error ? "*" : ""} not programmed` : ""].filter(Boolean).join(" · ")}</span>
            : gateways.length > 0 && <span className="t-secondary">all programmed{error ? " at the last read" : ""}</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="Read on demand - not a live watch.">snapshot</span>
          <UpdatedAt at={receivedAt} />
          <button className="l-btn" onClick={refresh} disabled={mine && net.listLoading}>{mine && net.listLoading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={(gateways.length > 0 || hasHubble) ? (
        <>
          {hasHubble && <ViewSwitch view={view} onView={switchView} attention={attention} notAccepted={notAccepted} />}
          {gateways.length > 0 && (
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
      footerEnd={<span className="t-secondary">{visible.length !== gateways.length ? `${visible.length} of ${gateways.length} · ` : ""}not accepted first</span>}
      inspector={selected ? <GatewayInspector key={selKey} g={selected} stale={!!error} onOpen={() => openGateway(selected.namespace, selected.name)} onClose={close} /> : undefined}
      inspectorLabel="Gateway details"
      onCloseInspector={close}
    />
  );
}

function ViewSwitch({ view, onView, attention, notAccepted }: { view: View; onView: (v: View) => void; attention: number; notAccepted: number }) {
  return (
    <div role="group" aria-label="Gateway API view" style={{ display: "inline-flex", gap: 4 }}>
      <button className="l-chip" aria-pressed={view === "gateways"} onClick={() => onView("gateways")}>Gateways {attention > 0 && <span style={{ color: notAccepted > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{attention}</span>}</button>
      <button className="l-chip" aria-pressed={view === "flows"} onClick={() => onView("flows")}>Flows (Hubble)</button>
    </div>
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

// ---- Hubble flows --------------------------------------------------------

type Flows = { stats: HubbleStatsDTO | null; failed: boolean; loading: boolean; at: number | null; refresh: () => void };

// useHubble reads Hubble's Prometheus series on demand while the Flows view
// is open; a late answer from an earlier request (or cluster) is dropped.
function useHubble(cluster: string, active: boolean): Flows {
  const [pinned, setPinned] = useState<{ cluster: string; stats: HubbleStatsDTO | null; at: number } | null>(null);
  const [loading, setLoading] = useState(false);
  const gen = useRef(0);
  const refresh = () => {
    const g = ++gen.current;
    const origin = cluster;
    setLoading(true);
    getHubbleFlowStats(origin).then((s) => s, () => null).then((s) => {
      if (g !== gen.current) return;
      setLoading(false);
      setPinned({ cluster: origin, stats: s, at: Date.now() });
    });
  };
  useEffect(() => { if (active) refresh(); }, [active, cluster]); // eslint-disable-line react-hooks/exhaustive-deps
  const mine = pinned && pinned.cluster === cluster ? pinned : null;
  return { stats: mine?.stats ?? null, failed: !!mine && mine.stats === null, loading, at: mine && mine.stats ? mine.at : null, refresh };
}

const fmtRate = (v: number | null | undefined) => (v == null ? "-" : v >= 10 ? `${Math.round(v)}` : v.toFixed(2));

function FlowsLayout({ flows, viewSwitch }: { flows: Flows; viewSwitch: React.ReactNode }) {
  const s = flows.stats;
  const hasAny = !!s && (s.forwardRate !== null || s.dropRate !== null || s.dropReasons.length > 0 || s.dnsTotalRate !== null || s.httpTotalRate !== null);
  let state: DatasetState = "ready";
  if (flows.failed) state = "unreadable";
  else if (!s) state = "loading";
  else if (!s.status.available || !hasAny) state = "empty";
  const icon = <IconChartDots3 size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={4} label="reading Hubble metrics" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read Hubble metrics." hint="The metrics call failed." action={{ label: "Retry", onClick: flows.refresh }} />
    : s && !s.status.available ? <EmptyState icon={icon} title="Hubble metrics are unavailable." hint={s.status.message} />
    : <EmptyState icon={icon} title="No Hubble flow series yet." hint={s?.status.message} />;
  const drops = s?.dropRate ?? null;
  return (
    <LensLayout
      label="Hubble flows"
      title="Flows (Hubble)"
      summary={state !== "ready" || !s ? null : (
        <>
          {drops !== null && drops > 0
            ? <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {fmtRate(drops)} dropped/s</span>
            : drops !== null ? <span className="t-secondary">no drops</span> : null}
          {s.forwardRate !== null && <span className="t-secondary">{fmtRate(s.forwardRate)} forwarded/s</span>}
          <span className="t-secondary">5m rates</span>
        </>
      )}
      headerEnd={
        <>
          <UpdatedAt at={flows.at} />
          <button className="l-btn" onClick={flows.refresh} disabled={flows.loading}>{flows.loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={viewSwitch}
      state={state}
      stateView={stateView}
      body={s ? (
        <div className="lens-canvas" data-testid="hubble-flows">
          <div className="gw-node">
            <span className="t-legend">rates</span>
            <div className="gw-facts">
              <span>dropped</span><span style={{ color: (drops ?? 0) > 0 ? "var(--color-text-danger)" : undefined }}>{drops === null ? "not collected" : `${fmtRate(drops)}/s`}</span>
              <span>forwarded</span><span>{s.forwardRate === null ? "not collected" : `${fmtRate(s.forwardRate)}/s`}</span>
              <span>DNS errors</span><span style={{ color: (s.dnsErrorRate ?? 0) > 0 ? "var(--color-text-warning)" : undefined }}>{s.dnsErrorRate === null ? "not collected" : `${fmtRate(s.dnsErrorRate)}/s of ${fmtRate(s.dnsTotalRate)}/s`}</span>
              <span>HTTP 5xx</span><span style={{ color: (s.httpErrorRate ?? 0) > 0 ? "var(--color-text-danger)" : undefined }}>{s.httpErrorRate === null ? "not collected" : `${fmtRate(s.httpErrorRate)}/s of ${fmtRate(s.httpTotalRate)}/s`}</span>
            </div>
          </div>
          <div className="gw-node">
            <span className="t-legend">drop reasons · highest rate first</span>
            {s.dropReasons.length === 0 ? <span className="t-secondary">{drops === null ? "Drop metrics are not collected." : "No drops in the window."}</span> : (
              <div style={{ display: "grid", gridTemplateColumns: "88px minmax(0, 1fr) auto", gap: "3px 12px", font: "400 12px/16px var(--font-mono)" }}>
                {s.dropReasons.map((r) => (
                  <Fragment key={`${r.reason}/${r.protocol}`}>
                    <span style={{ color: "var(--color-text-danger)", textAlign: "right" }}>{fmtRate(r.rate)}/s</span>
                    <span style={{ color: "var(--color-text-primary)", overflowWrap: "anywhere" }}>{r.reason}</span>
                    <span style={{ color: "var(--color-text-tertiary)" }}>{r.protocol}</span>
                  </Fragment>
                ))}
              </div>
            )}
          </div>
        </div>
      ) : null}
      footerEnd={<span className="t-secondary">Hubble's Prometheus metrics through the cluster's metrics source · read on demand</span>}
    />
  );
}
