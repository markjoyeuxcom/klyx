import { useMemo, useRef, useState } from "react";
import { IconNetwork } from "@tabler/icons-react";
import type { RuleDTO } from "../store/fleet";
import type { LBCondDTO, LBPoolDTO, LBServiceDTO, LBStateDTO } from "../bridge/loadbalancers";
import { useLBPools, useLBServices, useLBStates, type LBPartRows } from "../health/useLBRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, kindEvidence, lastGoodAt } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { Aspect } from "../chrome/primitives/Aspect";
import { CountSentence, MarkedPhrase } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { useNavIntent } from "../chrome/useNavIntent";
import { expectPin } from "../nav/inspectorPin";
import { formatAge, useNow } from "../chrome/Age";

// LoadBalancersView - the Load balancers lens (coverage S4 §6): LoadBalancer
// Services judged by their address, with the class -> controller -> pool ->
// address chain and each piece of evidence's source, the MetalLB and Cilium
// pools, and MetalLB's configuration states. Read-only: pools and classes
// are desired state, in Git. An assigned address proves allocation, never
// that traffic reaches the Service.

type Tab = "services" | "pools" | "config";
type RoleKind = "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused";
// pending first (S4 §6): past the grace, degraded, then within the grace
const RANK: Record<string, number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown;
const roleKind = (role: string): RoleKind => (role in RANK ? role : "unknown") as RoleKind;
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const toneOf = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const why = (reason: string | undefined, label: string) => (reason ? (reason.startsWith(label + ": ") ? reason.slice(label.length + 2) : reason) : "");
const muted = (s: React.ReactNode) => <span style={{ ...cell, color: "var(--color-text-secondary)" }}>{s}</span>;

const COLUMNS: Record<Tab, (ColumnDef & { label: string })[]> = {
  services: [
    ROW_MARK,
    { id: "name", label: "Name", width: 240, priority: 0, flex: true },
    { id: "status", label: "Status", width: 220, priority: 0 },
    { id: "class", label: "Class", width: 150, priority: 2 },
    { id: "address", label: "Address", width: 150, priority: 1 },
    { id: "controller", label: "Expected controller", width: 180, priority: 1 },
    { id: "pending", label: "Pending", width: 90, priority: 1 },
  ],
  pools: [
    ROW_MARK,
    { id: "name", label: "Name", width: 220, priority: 0, flex: true },
    { id: "status", label: "Status", width: 240, priority: 0 },
    { id: "kind", label: "Kind", width: 90, priority: 1 },
    { id: "addresses", label: "Addresses", width: 170, priority: 2 },
    { id: "advertised", label: "Advertised by", width: 170, priority: 2 },
  ],
  config: [
    ROW_MARK,
    { id: "name", label: "Name", width: 220, priority: 0, flex: true },
    { id: "status", label: "Status", width: 280, priority: 0 },
    { id: "role", label: "Writer", width: 160, priority: 1 },
    { id: "ns", label: "Namespace", width: 150, priority: 2 },
  ],
};

const PARTS: Record<Tab, { noun: [string, string]; lens: string; kind: string }> = {
  services: { noun: ["LoadBalancer Service", "LoadBalancer Services"], lens: "LBServices", kind: "Service" },
  pools: { noun: ["address pool", "address pools"], lens: "LBPools", kind: "IPAddressPool" },
  config: { noun: ["configuration state", "configuration states"], lens: "LBStates", kind: "ConfigurationState" },
};
const KEY_KIND: Record<Tab, string> = { services: "Service", pools: "Pool", config: "State" };
const TAB_OF_KEY: Record<string, Tab> = { Service: "services", Pool: "pools", State: "config" };
const keyOf = (tab: Tab, ...parts: string[]) => `${KEY_KIND[tab]}/${parts.join("/")}`;
const poolKey = (p: { kind: string; namespace?: string; name: string }) => keyOf("pools", p.kind, p.namespace ?? "", p.name);
const kindOfPool = (p: { kind: string }) => (p.kind === "cilium" ? "CiliumLoadBalancerIPPool" : "IPAddressPool");

// The lens's own limit, said every time (S4 §3).
export const ALLOCATION_LIMIT = "An assigned address proves allocation, not that traffic reaches the Service: announcement, BGP sessions and the data path are not read.";

type Item = { key: string; uid: string; name: string };

const pendingText = (s: LBServiceDTO, now: number) => {
  if (!s.pendingSince) return "";
  const age = formatAge((now - Date.parse(s.pendingSince)) / 1000);
  return s.pendingObserved ? age : `≥${age}`;
};

const controllerText = (s: LBServiceDTO) => {
  const ex = s.expected;
  if (ex.candidates.length > 0) return ex.candidates.map((c) => c.controller).join(", ");
  return ex.note ? "none detected" : "-";
};

export function LoadBalancersView({ cluster }: { cluster: string }) {
  const svcs = useLBServices(cluster);
  const pools = useLBPools(cluster);
  const states = useLBStates(cluster);
  const now = useNow();
  const [tab, setTab] = useState<Tab>("services");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const listRef = useRef<VirtualListHandle>(null);

  const services = useMemo(() => svcs.rows.map((s) => ({ ...s, key: keyOf("services", s.namespace, s.name) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule)
      || (a.pendingSince && b.pendingSince ? Date.parse(a.pendingSince) - Date.parse(b.pendingSince) : 0)
      || `${a.namespace}/${a.name}`.localeCompare(`${b.namespace}/${b.name}`)), [svcs.rows]);
  const poolItems = useMemo(() => pools.rows.map((p) => ({ ...p, key: poolKey(p) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)), [pools.rows]);
  const stateItems = useMemo(() => states.rows.map((s) => ({ ...s, key: keyOf("config", s.namespace, s.name) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || `${a.namespace}/${a.name}`.localeCompare(`${b.namespace}/${b.name}`)), [states.rows]);
  const part: Record<Tab, LBPartRows<unknown>> = { services: svcs, pools, config: states };
  const items: Record<Tab, Item[]> = { services, pools: poolItems, config: stateItems };
  const cur = part[tab];
  const list = items[tab];
  const ev = kindEvidence(!cur.loading, cur.coverage, PARTS[tab].kind, cur.watchError);
  const attention = (t: Tab) => (part[t].counts ? part[t].counts!.included : 0); // the rule's inclusion: faults and degraded
  const faults = (t: Tab) => (part[t].counts ? part[t].counts!.fault : 0);

  const keys = list.map((r) => r.key);
  const cursorIdx = cursorKey === null ? -1 : keys.indexOf(cursorKey);
  useListKeys({
    count: keys.length,
    selected: cursorIdx,
    onSelect: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); setSelKey(k); } },
    onEscape: () => setSelKey(null),
  });
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "lb") return "wait";
    const t: Tab = intent.part === "pools" ? "pools" : intent.part === "metallb" ? "config" : "services";
    const key = t === "pools" ? poolKey({ kind: intent.ref.gvr.startsWith("cilium.io") ? "cilium" : "metallb", namespace: intent.ref.namespace, name: intent.ref.name })
      : keyOf(t, intent.ref.namespace ?? "", intent.ref.name);
    const found = items[t].some((r) => r.key === key);
    const tev = kindEvidence(!part[t].loading, part[t].coverage, PARTS[t].kind, part[t].watchError);
    if (intent.ref.uid) expectPin(cluster, PARTS[t].lens, key, intent.ref.uid);
    else if (!found) return tev === "complete" ? "missing" : "wait";
    setTab(t);
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [services, poolItems, stateItems]);

  const fitted = useMemo(() => fitColumns(COLUMNS[tab], bodyWidth), [tab, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const selTab: Tab | null = TAB_OF_KEY[selKey?.split("/")[0] ?? ""] ?? null;
  const live = selTab ? items[selTab].find((r) => r.key === selKey) : undefined;
  const selEv = selTab ? kindEvidence(!part[selTab].loading, part[selTab].coverage, PARTS[selTab].kind, part[selTab].watchError) : "pending";
  const pin = usePinnedSubject(cluster, selTab ? selKey : null, live, selEv === "complete", selTab ? PARTS[selTab].lens : undefined);

  let state: DatasetState = "ready";
  if (ev === "failed" && list.length === 0) state = "unreadable";
  else if (ev === "pending" && list.length === 0) state = "loading";
  else if (list.length === 0) state = "empty";
  const icon = <IconNetwork size={28} stroke={1.2} />;
  const emptyHint: Record<Tab, string> = {
    services: "A Service of type LoadBalancer appears here once one exists.",
    pools: "Neither MetalLB IPAddressPools nor Cilium LB IPAM pools exist (or their APIs are not installed).",
    config: "MetalLB writes a ConfigurationState for its controller and each speaker (v0.15 and later).",
  };
  const stateView = state === "loading" ? <SkeletonRows rows={4} label={`reading ${PARTS[tab].noun[1]}`} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${PARTS[tab].noun[1]}.`} hint={cur.watchError ?? undefined} />
    : <EmptyState icon={icon} title={`No ${PARTS[tab].noun[1]} on this cluster.`} hint={emptyHint[tab]} />;

  const notices: React.ReactNode[] = [];
  if (cur.watchError && list.length > 0) {
    const lg = lastGoodAt(cur.coverage, [PARTS[tab].kind]);
    notices.push(<WatchErrorBanner key="watch" tool="Load balancers" failure={`read failed - showing the last good state${lg ? ` from ${clock(lg)}` : ""}`} error={cur.watchError} />);
  }

  const rowFor = (it: Item, cells: Record<string, React.ReactNode>) => (
    <div key={it.key} role="button" tabIndex={0} className="lens-row" style={gridStyle} data-testid={`lb-row-${it.name}`}
      aria-selected={it.key === cursorKey} data-cursor={it.key === cursorKey ? "true" : undefined} data-selected={it.key === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(it.key); setSelKey(it.key === selKey ? null : it.key); }}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(it.key); } }}>
      {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id} style={c.id === "mark" ? undefined : cell}>{cells[c.id]}</span>)}
    </div>
  );
  const status = (r: RuleDTO, reason?: string) => {
    const k = roleKind(r.role);
    const w = k === "fault" || k === "degraded" ? why(reason, r.label) : "";
    return <span style={{ ...cell, color: ROLE_TEXT[k] }}>{r.label}{w ? ` · ${w}` : ""}</span>;
  };
  const mark = (r: RuleDTO) => <VerdictMark health={{ kind: roleKind(r.role), label: r.label }} />;
  const render = (it: Item) => {
    if (tab === "services") {
      const s = it as LBServiceDTO & Item;
      const addr = s.allocated ? `${s.ips[0] ?? s.hostnames[0] ?? ""}${s.addressCount > 1 ? ` +${s.addressCount - 1}` : ""}` : "none";
      return rowFor(it, { mark: mark(s.rule), name: <span className="lens-cell-name">{s.namespace}/{s.name}</span>, status: status(s.rule, s.reason),
        class: muted(s.class || "default"), address: muted(addr), controller: muted(controllerText(s)), pending: muted(pendingText(s, now) || "-") });
    }
    if (tab === "pools") {
      const p = it as LBPoolDTO & Item;
      return rowFor(it, { mark: mark(p.rule), name: <span className="lens-cell-name">{p.namespace ? `${p.namespace}/` : ""}{p.name}</span>, status: status(p.rule, p.reason),
        kind: muted(p.kind === "cilium" ? "Cilium" : "MetalLB"),
        addresses: muted(`${p.blocks[0] ?? "-"}${p.blockCount > 1 ? ` +${p.blockCount - 1}` : ""}`),
        advertised: muted(p.kind === "cilium" ? "-" : p.selectedBy.length ? p.selectedBy.join(", ") : p.unresolved.length ? "unresolved" : "none") });
    }
    const s = it as LBStateDTO & Item;
    return rowFor(it, { mark: mark(s.rule), name: <span className="lens-cell-name">{s.name}</span>, status: status(s.rule, s.reason),
      role: muted(s.role === "speaker" ? `speaker on ${s.node}` : s.role ?? "-"), ns: muted(s.namespace) });
  };

  const tabChip = (t: Tab, label: string) => {
    const n = attention(t);
    const f = faults(t);
    return <button className="l-chip" aria-pressed={tab === t} onClick={() => { setTab(t); setSelKey(null); setCursorKey(null); }}>
      {label}{n ? <span style={{ color: f ? "var(--color-text-danger)" : "var(--color-text-warning)" }}> {n}</span> : null}</button>;
  };
  const open = (k: string) => { const t = TAB_OF_KEY[k.split("/")[0]] ?? "services"; setTab(t); setCursorKey(k); setSelKey(k); };
  const subject = pin.subject as (Item & (LBServiceDTO | LBPoolDTO | LBStateDTO)) | null;
  const gone = pin.gone === "replaced" ? "removed" : pin.gone;
  const inspector = selTab && subject ? (
    selTab === "services" ? <ServiceInspector cluster={cluster} s={subject as LBServiceDTO} now={now} gone={gone} pools={poolItems} onOpen={open} onClose={() => setSelKey(null)} />
      : selTab === "pools" ? <PoolInspector cluster={cluster} p={subject as LBPoolDTO} gone={gone} onClose={() => setSelKey(null)} />
      : <StateInspector cluster={cluster} s={subject as LBStateDTO} gone={gone} onClose={() => setSelKey(null)} />
  ) : selTab && selKey && pin.expected && pin.gone
    ? <PinnedGone cluster={cluster} kind={PARTS[selTab].kind} name={selKey.split("/").slice(-1)[0]} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
    : undefined;

  const total = cur.counts ? cur.counts.total : list.length;
  const word: Record<Tab, string> = { services: "without an address or degraded", pools: "in conflict, exhausted or not advertised", config: "invalid" };
  const showConfig = states.rows.length > 0 || pools.rows.some((p) => p.kind === "metallb");
  return (
    <LensLayout
      label="Load balancers"
      title="Load balancers"
      part="loadbalancers"
      summary={(
        <>
          <CountSentence total={total} marker={ev === "complete" ? "" : "+"} noun={PARTS[tab].noun} />
          {attention(tab) > 0 ? <MarkedPhrase fault={faults(tab) > 0}>{`${attention(tab)} ${word[tab]}`}</MarkedPhrase> : <span className="t-secondary">none failing</span>}
        </>
      )}
      controls={(
        <div role="group" aria-label="Load balancer objects" className="l-seg">
          {tabChip("services", "Services")}
          {tabChip("pools", "Pools")}
          {(showConfig || tab === "config") && tabChip("config", "MetalLB configuration")}
        </div>
      )}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={list} rowHeight={TABLE_ROW_HEIGHT} rowKey={(r) => r.uid || r.key} style={{ flex: 1, minHeight: 0 }} render={render} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
      footerEnd={<span className="t-secondary">{tab === "services" ? "Pending first" : "Broken first"} · read-only · allocation, not traffic</span>}
      inspector={inspector}
      inspectorLabel="Load balancer details"
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

type Gone = "removed" | "unverified" | null;

function goneVerdict(kind: string, gone: Gone) {
  return <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${kind} removed.` : `Not in the current ${kind} read.`}</span> The details below are its last evidence.</div>;
}

type Station = { label: string; detail: string; aspect: "none" | "fault" | "attention" | "checking" | "syncing" };

// Chain is a Service's allocation drawing (S4 §6): class -> controller ->
// pool -> address, each station with its state.
export function Chain({ name, stations }: { name: string; stations: Station[] }) {
  return (
    <div data-testid="lb-chain" role="list" aria-label={`Allocation of ${name}`} style={{ display: "flex", alignItems: "flex-start", padding: "4px 0 2px" }}>
      {stations.map((s, i) => (
        <div key={s.label} role="listitem" title={`${s.label}: ${s.detail}`} style={{ display: "grid", justifyItems: "center", gap: 2, flex: "1 1 0", minWidth: 0, padding: "0 2px" }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "center", height: 14, width: "100%",
            backgroundImage: "linear-gradient(var(--color-border-secondary), var(--color-border-secondary))", backgroundSize: i === 0 || i === stations.length - 1 ? "50% 1px" : "100% 1px",
            backgroundRepeat: "no-repeat", backgroundPosition: i === 0 ? "right center" : i === stations.length - 1 ? "left center" : "center" }}>
            {s.aspect === "none" ? <span role="img" aria-label={`${s.label}: ok`} style={{ width: 2, height: 10, background: "var(--color-text-secondary)" }} />
              : <Aspect kind={s.aspect as never} size={10} label={`${s.label}: ${s.aspect}`} />}
          </div>
          <div className="t-legend" style={{ color: "var(--color-text-tertiary)" }}>{s.label}</div>
          <div className="t-secondary" style={{ maxWidth: "100%", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontSize: 11 }}>{s.detail}</div>
        </div>
      ))}
    </div>
  );
}

// stationsOf derives the chain from the row; it never judges beyond it.
export function stationsOf(s: LBServiceDTO): Station[] {
  const k = roleKind(s.rule.role);
  const pendingAspect: Station["aspect"] = k === "fault" ? "fault" : k === "progressing" ? "syncing" : "checking";
  const ex = s.expected;
  const ctl = ex.candidates.length > 0 ? ex.candidates.map((c) => c.controller).join(", ") : "none detected";
  const pool = s.diagnosis?.pool;
  const stations: Station[] = [
    { label: "class", detail: s.class || "default", aspect: ex.known ? "none" : "attention" },
    { label: "controller", detail: ctl, aspect: ex.candidates.length > 0 ? "none" : s.allocated ? "checking" : pendingAspect },
    { label: "pool", detail: pool ? `${pool.name} · ${pool.state}` : s.allocated ? "-" : "not named",
      aspect: !pool ? "none" : pool.state === "found" ? "none" : pool.state === "missing" ? (k === "fault" ? "fault" : "attention") : "checking" },
    { label: "address", detail: s.allocated ? `${s.ips[0] ?? s.hostnames[0] ?? ""}${s.addressCount > 1 ? ` +${s.addressCount - 1}` : ""}` : "none",
      aspect: s.allocated ? (k === "degraded" ? "attention" : "none") : pendingAspect },
  ];
  return stations;
}

const condText = (c: LBCondDTO) => `${c.type}=${c.status}${c.reason ? ` (${c.reason})` : ""}${c.message ? `: ${c.message}` : ""}`;

function ServiceInspector({ cluster, s, now, gone, pools, onOpen, onClose }: {
  cluster: string; s: LBServiceDTO; now: number; gone: Gone; pools: (LBPoolDTO & Item)[]; onOpen: (k: string) => void; onClose: () => void;
}) {
  const ro = roleKind(s.rule.role);
  const describe = useDescribe(cluster, !gone ? { kind: "Service", namespace: s.namespace, name: s.name, uid: s.uid } : null);
  const ai = useInspectorAI(s.uid ? { kind: "row" as const, task: "lb", part: "services", uid: s.uid } : null, `Service/${s.namespace}/${s.name}`, !gone, cluster);
  const d = s.diagnosis;
  const requested = d?.pool?.state === "found" ? pools.find((p) => p.kind === "metallb" && p.name === d.pool!.name && p.namespace === d.pool!.namespace) : undefined;
  const pending = s.pendingSince ? formatAge((now - Date.parse(s.pendingSince)) / 1000) : "";
  return (
    <div data-testid="lb-service-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: s.uid || undefined }} kind="Service" name={s.name} meta={s.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("Service", gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={s.rule.label} reason={why(s.reason, s.rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Class to address">
          <Chain name={s.name} stations={stationsOf(s)} />
          {s.pendingSince && (
            <div className="t-secondary" data-testid="lb-pending" style={{ marginTop: 4 }}>
              {s.pendingObserved ? `Pending for ${pending}: Klyx saw it lose its address or become a LoadBalancer.`
                : `Pending at least ${pending}: its start was not observed, so the time counts from Klyx's first observation.`}
              {ro === "progressing" ? " It is a fault after 10 minutes without an address." : ""}
            </div>
          )}
        </InspectorBlock>
        <InspectorBlock title="Address">
          <div className="insp-kv">
            <span>assigned</span><span style={{ overflowWrap: "anywhere" }}>{s.allocated ? [...s.ips, ...s.hostnames].join(", ") + (s.addressCount > s.ips.length + s.hostnames.length ? ` +${s.addressCount - s.ips.length - s.hostnames.length}` : "") : "none"}</span>
            {s.entries > s.addressCount && <><span>empty entries</span><span>{s.entries - s.addressCount} ingress entries carry neither an IP nor a hostname</span></>}
            {s.allocated && <><span>families</span><span>{s.families.join(", ") || "unknown (a hostname)"}</span></>}
            {(s.ipFamilies.length > 0 || s.ipFamilyPolicy) && <><span>requested</span><span>{[s.ipFamilies.join(", "), s.ipFamilyPolicy].filter(Boolean).join(" · ")}</span></>}
            {s.missingFamily && <><span>missing</span><span style={{ color: "var(--color-text-warning)" }}>{s.missingFamily} is not assigned, and the Service requires both</span></>}
            {s.oneFamilyPreferred && <><span>one family</span><span>PreferDualStack with one family assigned: allowed, said only</span></>}
            {s.externalTrafficPolicy && <><span>traffic policy</span><span>{s.externalTrafficPolicy}</span></>}
            {(s.portError || s.portsErrorCond) && <><span>ports</span><span style={{ color: "var(--color-text-warning)" }}>{s.portError ? "an ingress port reports an error (text not shown)" : "LoadBalancerPortsError is True"}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Expected controller">
          {s.expected.candidates.length === 0 && <div className="t-secondary" style={{ color: s.allocated ? undefined : "var(--color-text-warning)" }}>{s.expected.note ?? "none"}</div>}
          {s.expected.candidates.map((c) => (
            <div key={c.controller} className="t-body" data-testid={`lb-candidate-${c.controller}`}>{c.controller}
              {c.limit ? <span className="t-secondary"> · {c.limit}</span> : null}
            </div>
          ))}
          {s.expected.candidates.length > 0 && s.expected.note && <div className="t-secondary">{s.expected.note}</div>}
          <div className="t-secondary" style={{ marginTop: 4 }}>{s.class ? "A requested class does not prove its controller runs." : "Candidates are the controllers Klyx can see that serve Services without a class; none is proven the owner."}</div>
        </InspectorBlock>
        {d && (
          <InspectorBlock title="Evidence">
            <div data-testid="lb-evidence" style={{ display: "grid", gap: 6 }}>
              <div>
                <div className="t-legend">Warning events (by the Service's UID)</div>
                {d.events.length === 0
                  ? <div className="t-secondary">{d.eventsRead === "unverified" ? "Events were not read: none can be said absent." : "None names this Service."}</div>
                  : d.events.map((e, i) => <div key={i} className="t-body"><span style={{ color: "var(--color-text-warning)" }}>{e.reason}</span>{e.message ? <span className="t-secondary">: {e.message}</span> : null}{e.count && e.count > 1 ? <span className="t-secondary"> ×{e.count}</span> : null}</div>)}
                {d.eventsRead === "partial" && <div className="t-secondary">The events read is capped or stale: an absent event proves nothing.</div>}
              </div>
              {d.cilium && <div>
                <div className="t-legend">Cilium LB IPAM condition</div>
                <div className="t-body" style={{ color: d.cilium.current ? undefined : "var(--color-text-tertiary)" }}>{condText(d.cilium)}{d.cilium.current ? "" : " · historical: for an earlier generation"}</div>
              </div>}
              {(d.svclb || s.expected.candidates.some((c) => c.controller === "k3s ServiceLB")) && <div>
                <div className="t-legend">k3s ServiceLB</div>
                {d.svclb?.daemonSet
                  ? <div className="t-body">{d.svclb.daemonSet}: {d.svclb.ready} of {d.svclb.pods} pods ready{d.svclb.pending ? `, ${d.svclb.pending} pending${d.svclb.pendingReason ? ` (${d.svclb.pendingReason})` : ""}` : ""}
                    {d.svclb.hostPortConflict ? <span style={{ color: "var(--color-text-warning)" }}> · no node has the Service's ports free</span> : null}</div>
                  : <div className="t-secondary">No svclb DaemonSet for this Service (by its UID).</div>}
                {d.svclb?.candidates ? <div className="t-secondary">{d.svclb.candidates} svclb pods carry its name but belong to another DaemonSet: unproven, a replaced Service's.</div> : null}
                {!d.svclbRead && <div className="t-secondary">DaemonSets or pods were not read completely.</div>}
              </div>}
              {d.request.length > 0 && <div>
                <div className="t-legend">Address request</div>
                {d.request.map((r) => <div key={r} className="t-body" style={{ overflowWrap: "anywhere" }}>{r}</div>)}
              </div>}
              {d.pool && <div>
                <div className="t-legend">Requested pool</div>
                <div className="t-body">
                  {requested ? <button onClick={() => onOpen(requested.key)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)" }}>{d.pool.name}</button> : d.pool.name}
                  <span className="t-secondary"> · {d.pool.state === "found" ? `in ${d.pool.namespace}` : d.pool.state === "missing" ? `does not exist in ${d.pool.namespace}` : "not verified (MetalLB's namespace is not known, or the pools read is incomplete)"}</span>
                </div>
              </div>}
              {d.silent && <div>
                <div className="t-legend">Nothing speaks</div>
                <div className="t-body">{d.silent}</div>
              </div>}
              <div className="t-secondary">Other pools in the cluster are context, never the cause: their selectors are not evaluated.</div>
            </div>
          </InspectorBlock>
        )}
        {s.conditions.length > 0 && <InspectorBlock title={`Conditions · ${s.conditionCount}`}>
          {s.conditions.map((c) => <div key={c.type} className="t-body" style={{ color: c.current ? undefined : "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>{condText(c)}{c.current ? "" : " · historical"}</div>)}
          {s.conditionCount > s.conditions.length && <div className="t-secondary">+{s.conditionCount - s.conditions.length} more</div>}
        </InspectorBlock>}
        <InspectorBlock title="What Klyx cannot see">
          <ul className="t-secondary" data-testid="lb-limits" style={{ margin: 0, paddingLeft: 16 }}>
            {[ALLOCATION_LIMIT, ...s.limits.filter((l) => !l.startsWith("An assigned address"))].map((l) => <li key={l}>{l}</li>)}
          </ul>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

const countOr = (v: number | undefined) => (v === undefined ? "not reported" : String(v));
const exact = (v: string | undefined, st: string | undefined) => v ?? (st === "stale" ? "for an earlier change" : st === "malformed" ? "unreadable" : "not reported");

function PoolInspector({ cluster, p, gone, onClose }: { cluster: string; p: LBPoolDTO; gone: Gone; onClose: () => void }) {
  const kind = kindOfPool(p);
  const ro = roleKind(p.rule.role);
  const describe = useDescribe(cluster, !gone ? { kind, namespace: p.namespace ?? "", name: p.name, uid: p.uid } : null);
  const ai = useInspectorAI(p.uid ? { kind: "row" as const, task: "lb", part: "pools", uid: p.uid } : null, `${kind}/${p.name}`, !gone, cluster);
  return (
    <div data-testid="lb-pool-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: p.uid || undefined }} kind={kind} name={p.name} meta={p.namespace || "cluster-scoped"} onClose={onClose}
        verdict={gone ? goneVerdict(kind, gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={p.rule.label} reason={why(p.reason, p.rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Pool">
          <div className="insp-kv">
            <span>addresses</span><span style={{ overflowWrap: "anywhere" }}>{p.blocks.join(", ") || "-"}{p.blockCount > p.blocks.length ? ` +${p.blockCount - p.blocks.length}` : ""}</span>
            {p.kind === "cilium" ? <>
              <span>available</span><span data-testid="lb-counts">{exact(p.counts?.available, p.counts?.availableState)} of {exact(p.counts?.total, p.counts?.totalState)} · used {exact(p.counts?.used, p.counts?.usedState)}</span>
              {p.disabled && <><span>disabled</span><span>spec.disabled is set: Cilium allocates nothing from it</span></>}
              <span>services</span><span>{p.serviceSelector ? "a serviceSelector narrows which Services it serves (not evaluated)" : "any Service"}</span>
            </> : <>
              <span>IPv4</span><span data-testid="lb-counts">{countOr(p.status?.availableIPv4)} available · {countOr(p.status?.assignedIPv4)} assigned</span>
              <span>IPv6</span><span>{countOr(p.status?.availableIPv6)} available · {countOr(p.status?.assignedIPv6)} assigned</span>
              <span>auto-assign</span><span>{p.autoAssign === false ? "off: only Services that request it get addresses" : "on"}</span>
              {p.serviceAllocation && <><span>allocation</span><span>serviceAllocation narrows which Services it serves (not evaluated)</span></>}
            </>}
          </div>
        </InspectorBlock>
        {p.kind === "cilium" && p.conflict && <InspectorBlock title="Conflict">
          <div className="t-body" style={{ color: p.conflict.current ? (p.conflict.status === "True" ? "var(--color-text-danger)" : undefined) : "var(--color-text-tertiary)" }}>
            {condText(p.conflict)}{p.conflict.current ? "" : " · historical: for an earlier generation"}
          </div>
        </InspectorBlock>}
        {p.kind === "metallb" && <InspectorBlock title="Advertisements">
          <div className="insp-kv">
            <span>selected by</span><span>{p.selectedBy.join(", ") || "none"}</span>
            {p.unresolved.length > 0 && <><span>unresolved</span><span>{p.unresolved.join(", ")} (a cut list or selector: its selection could not be evaluated)</span></>}
            {p.adsRead !== "complete" && <><span>read</span><span>the advertisements read is {p.adsRead}: selection is not judged</span></>}
            <span>namespace</span><span>{p.instance === "state" ? "MetalLB's (its controller ConfigurationState is here)" : p.instance === "workload" ? "MetalLB's, by its controller Deployment" : "no MetalLB instance found here"}</span>
            {p.invalid.length > 0 && <><span>configuration</span><span style={{ color: "var(--color-text-warning)" }}>{p.invalid.join(", ")} report Invalid: MetalLB may still run an earlier valid configuration</span></>}
            {p.labelsCut && <><span>labels</span><span>more labels than Klyx keeps: selection by labels may be unresolved</span></>}
          </div>
        </InspectorBlock>}
        {p.conditions.length > 0 && p.kind === "cilium" && <InspectorBlock title="Conditions">
          {p.conditions.map((c) => <div key={c.type} className="t-body" style={{ color: c.current ? undefined : "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>{condText(c)}{c.current ? "" : " · historical"}</div>)}
        </InspectorBlock>}
        <InspectorBlock title="What Klyx cannot see">
          <ul className="t-secondary" data-testid="lb-pool-limits" style={{ margin: 0, paddingLeft: 16 }}>
            {[...p.limits, "Which Services this pool may serve: its selectors are not evaluated, so it is context for a Service, never its cause."].map((l) => <li key={l}>{l}</li>)}
          </ul>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function StateInspector({ cluster, s, gone, onClose }: { cluster: string; s: LBStateDTO; gone: Gone; onClose: () => void }) {
  const ro = roleKind(s.rule.role);
  const describe = useDescribe(cluster, !gone ? { kind: "ConfigurationState", namespace: s.namespace, name: s.name, uid: s.uid } : null);
  const ai = useInspectorAI(s.uid ? { kind: "row" as const, task: "lb", part: "metallb", uid: s.uid } : null, `ConfigurationState/${s.name}`, !gone, cluster);
  return (
    <div data-testid="lb-state-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: s.uid || undefined }} kind="ConfigurationState" name={s.name} meta={s.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("ConfigurationState", gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={s.rule.label} reason={why(s.reason, s.rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Configuration">
          <div className="insp-kv">
            <span>writer</span><span>{s.role === "speaker" ? `the speaker on ${s.node}` : s.role === "controller" ? "the controller" : "-"}</span>
            <span>result</span><span>{s.result || "not reported"}</span>
            {s.error && <><span>error</span><span style={{ overflowWrap: "anywhere", color: "var(--color-text-warning)" }}>{s.error}</span></>}
          </div>
          <div className="t-secondary" style={{ marginTop: 4 }}>An invalid configuration leaves MetalLB on its last valid one: the objects in the cluster may not be what it runs.</div>
        </InspectorBlock>
        {s.conditions.length > 0 && <InspectorBlock title={`Conditions · ${s.conditionCount}`}>
          {s.conditions.map((c) => <div key={c.type + c.reason} className="t-body" style={{ overflowWrap: "anywhere" }}>{condText(c)}</div>)}
        </InspectorBlock>}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
