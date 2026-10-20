import { useMemo, useRef, useState } from "react";
import { IconCpu } from "@tabler/icons-react";
import type { RuleDTO } from "../store/fleet";
import type { DRAClaimDTO, DRAClassDTO, DRAPoolDTO, DRAViewDTO } from "../bridge/dra";
import { allocatedIn, claimsOfClass, useDRAClaims, useDRAClasses, useDRAPools, type DRAPartRows } from "../health/useDRARows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, kindEvidence, lastGoodAt } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { CountSentence, MarkedPhrase } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { LensParts } from "../chrome/LensParts";
import { useNavIntent } from "../chrome/useNavIntent";

// DRAView - the DRA lens (coverage S11b §5): ResourceClaims broken first
// with the scheduler's evidence, device pools read per node under every
// scheduler the cluster may run, and DeviceClasses as facts. Read-only.
// "Available" and "registered" are never said: the figure is "not
// allocated per the API", and a node without slices is only that.

type Tab = "claims" | "pools" | "classes";
type RoleKind = "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused";
const RANK: Record<string, number> = { fault: 0, degraded: 1, unknown: 2, progressing: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown;
const roleKind = (role: string): RoleKind => (role in RANK ? role : "unknown") as RoleKind;
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const toneOf = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const muted = (s: React.ReactNode) => <span style={{ color: "var(--color-text-secondary)" }}>{s}</span>;
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

const COLUMNS: Record<Tab, (ColumnDef & { label: string })[]> = {
  claims: [
    ROW_MARK,
    { id: "name", label: "Claim", width: 240, priority: 0, flex: true },
    { id: "status", label: "Status", width: 280, priority: 0 },
    { id: "devices", label: "Devices", width: 120, priority: 1 },
    { id: "users", label: "In use by", width: 110, priority: 1 },
    { id: "class", label: "Class", width: 170, priority: 2 },
  ],
  pools: [
    ROW_MARK,
    { id: "name", label: "Pool", width: 260, priority: 0, flex: true },
    { id: "status", label: "Status", width: 260, priority: 0 },
    { id: "slices", label: "Slices", width: 90, priority: 1 },
    { id: "allocated", label: "Allocated", width: 120, priority: 1 },
    { id: "views", label: "Nodes", width: 120, priority: 2 },
  ],
  classes: [
    { id: "name", label: "DeviceClass", width: 240, priority: 0, flex: true },
    { id: "selectors", label: "Selectors", width: 100, priority: 1 },
    { id: "drivers", label: "Config drivers", width: 200, priority: 2 },
    { id: "extended", label: "Extended resource", width: 200, priority: 1 },
    { id: "claims", label: "Claims", width: 90, priority: 1 },
  ],
};
const NOUN: Record<Tab, [string, string]> = { claims: ["claim", "claims"], pools: ["pool", "pools"], classes: ["device class", "device classes"] };
const KIND: Record<Tab, string> = { claims: "ResourceClaim", pools: "ResourceSlice", classes: "DeviceClass" };

type Item = { key: string; uid: string; rule?: RuleDTO };

// joinMark qualifies a figure joined from another part's rows: "+" when that
// read is not current and whole, so the figure is a lower bound.
const joinMark = (p: DRAPartRows<unknown>) => (p.complete ? "" : p.loading ? "?" : "+");

export function DRAView({ cluster }: { cluster: string }) {
  const claims = useDRAClaims(cluster);
  const pools = useDRAPools(cluster);
  const classes = useDRAClasses(cluster);
  const [tab, setTab] = useState<Tab>("claims");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const listRef = useRef<VirtualListHandle>(null);

  const claimItems = useMemo(() => claims.rows.map((c) => ({ ...c, key: `claim/${c.namespace}/${c.name}` }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [claims.rows]);
  const poolItems = useMemo(() => pools.rows.map((p) => ({ ...p, uid: p.key, key: `pool/${p.key}` }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || a.key.localeCompare(b.key)), [pools.rows]);
  const classItems = useMemo(() => classes.rows.map((c) => ({ ...c, key: `class/${c.name}` })).sort((a, b) => a.name.localeCompare(b.name)), [classes.rows]);
  const part: Record<Tab, DRAPartRows<unknown>> = { claims, pools, classes };
  const items: Record<Tab, Item[]> = { claims: claimItems, pools: poolItems, classes: classItems };
  const cur = part[tab];
  const list = items[tab];
  const ev = kindEvidence(!cur.loading, cur.coverage, KIND[tab], cur.watchError);
  // the attention count: a native part's included rows (fault and degraded)
  const faults = (t: Tab) => (t === "classes" ? 0 : part[t].counts?.included ?? 0);

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
    if (intent.kind !== "reveal" || intent.task !== "dra") return "wait";
    const t: Tab = intent.part === "pools" ? "pools" : intent.part === "classes" ? "classes" : "claims";
    const key = t === "claims" ? `claim/${intent.ref.namespace ?? ""}/${intent.ref.name}` : t === "pools" ? `pool/${intent.ref.name}` : `class/${intent.ref.name}`;
    if (!items[t].some((r) => r.key === key)) return part[t].complete ? "missing" : "wait";
    setTab(t);
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [claimItems, poolItems, classItems]);

  const fitted = useMemo(() => fitColumns(COLUMNS[tab], bodyWidth), [tab, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  let state: DatasetState = "ready";
  if (ev === "failed" && list.length === 0) state = "unreadable";
  else if (ev === "pending" && list.length === 0) state = "loading";
  else if (list.length === 0) state = "empty";
  const icon = <IconCpu size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={4} label={`reading ${NOUN[tab][1]}`} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${NOUN[tab][1]}.`} hint={cur.watchError ?? undefined} />
    : <EmptyState icon={icon} title={`No ${NOUN[tab][1]} on this cluster.`} />;

  const notices: React.ReactNode[] = [];
  if (cur.watchError && list.length > 0) {
    const lg = lastGoodAt(cur.coverage, [KIND[tab]]);
    notices.push(<WatchErrorBanner key="watch" tool="DRA" failure={`read failed - showing the last good state${lg ? ` from ${clock(lg)}` : ""}`} error={cur.watchError} />);
  }
  if (tab === "pools") notices.push(<div key="avail" className="t-secondary">Allocated is per the API: the scheduler may hold allocations it has not written yet, so free devices are never shown.</div>);
  if (tab === "classes") notices.push(<div key="facts" className="t-secondary">Device classes are facts: their selectors are counted, never shown.</div>);

  const rowFor = (it: Item, cells: Record<string, React.ReactNode>) => (
    <div key={it.key} role="button" tabIndex={0} className="lens-row" style={gridStyle} data-testid={`dra-row-${it.key}`}
      aria-selected={it.key === cursorKey} data-cursor={it.key === cursorKey ? "true" : undefined} data-selected={it.key === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(it.key); setSelKey(it.key === selKey ? null : it.key); }}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(it.key); } }}>
      {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id} style={c.id === "mark" ? undefined : cell}>{cells[c.id]}</span>)}
    </div>
  );
  const status = (r: RuleDTO, message?: string) => {
    const k = roleKind(r.role);
    return <span style={{ ...cell, color: ROLE_TEXT[k] }}>{r.label}{message && (k === "fault" || k === "degraded" || k === "unknown") ? ` · ${message}` : ""}</span>;
  };
  const cm = joinMark(claims);
  const render = (it: Item) => {
    if (tab === "claims") {
      const c = it as DRAClaimDTO & Item;
      const devs = c.allocated ? plural(c.results.length, "device", "devices") : `${c.requests.reduce((n, r) => n + (r.count ?? 1), 0)} requested`;
      return rowFor(it, { mark: <VerdictMark health={{ kind: roleKind(c.rule.role), label: c.rule.label }} />,
        name: <span className="lens-cell-name">{c.namespace} / {c.name}</span>, status: status(c.rule, c.reading.message),
        devices: muted(devs), users: muted(c.allocated ? String(c.reservedCount) : c.consumers.length ? `${c.consumers.length} waiting` : "-"),
        class: muted(c.requests.map((r) => r.class).filter(Boolean).join(", ") || "-") });
    }
    if (tab === "pools") {
      const p = it as DRAPoolDTO & Item;
      const a = allocatedIn(claims.rows, p.driver, p.pool);
      const named = p.nodes ?? p.views.filter((v) => v.node).length;
      return rowFor(it, { mark: <VerdictMark health={{ kind: roleKind(p.rule.role), label: p.rule.label }} />,
        name: <span className="lens-cell-name">{p.driver} / {p.pool}</span>, status: status(p.rule, p.reading.message),
        slices: muted(String(p.slices)), allocated: muted(`${a.devices}${cm}${a.shares ? ` (${a.shares} shared)` : ""}`),
        views: muted(named ? plural(named, "node", "nodes") : "all nodes") });
    }
    const c = it as DRAClassDTO & Item;
    return rowFor(it, { name: <span className="lens-cell-name">{c.name}</span>, selectors: muted(String(c.selectors)),
      drivers: muted(c.configDrivers.join(", ") || "-"), extended: muted(c.extendedResourceName || "-"), claims: muted(`${claimsOfClass(claims.rows, c.name)}${cm}`) });
  };

  const tabChip = (t: Tab, label: string) => {
    const n = faults(t);
    return <button className="l-chip" aria-pressed={tab === t} onClick={() => { setTab(t); setSelKey(null); setCursorKey(null); }}>{label}{n ? <span style={{ color: "var(--color-text-warning)" }}> {n}</span> : null}</button>;
  };
  const selTab: Tab | null = selKey?.startsWith("claim/") ? "claims" : selKey?.startsWith("pool/") ? "pools" : selKey?.startsWith("class/") ? "classes" : null;
  const selected = selTab ? items[selTab].find((r) => r.key === selKey) : undefined;
  const openPool = (key: string) => { setTab("pools"); setCursorKey(`pool/${key}`); setSelKey(`pool/${key}`); };
  const inspector = selTab && selected ? (
    selTab === "claims" ? <ClaimInspector cluster={cluster} c={selected as unknown as DRAClaimDTO} pools={pools.rows} onOpenPool={openPool} onClose={() => setSelKey(null)} />
      : selTab === "pools" ? <PoolInspector cluster={cluster} p={selected as unknown as DRAPoolDTO} claims={claims} onClose={() => setSelKey(null)} />
      : <ClassInspector cluster={cluster} c={selected as unknown as DRAClassDTO} claims={claims} onClose={() => setSelKey(null)} />
  ) : undefined;

  const total = tab === "classes" ? list.length : cur.counts ? cur.counts.total : list.length;
  return (
    <LensLayout
      label="DRA"
      title="Devices (DRA)"
      part="dra"
      summary={(
        <>
          <CountSentence total={total} marker={ev === "complete" ? "" : "+"} noun={NOUN[tab]} />
          {tab === "classes" ? <span className="t-secondary">facts, no verdict</span>
            : faults(tab) > 0 ? <MarkedPhrase fault={false}>{`${faults(tab)} ${tab === "claims" ? "cannot be allocated or appear stuck" : "incomplete or invalid"}`}</MarkedPhrase> : <span className="t-secondary">none failing</span>}
        </>
      )}
      controls={(
        <>
          <LensParts cluster={cluster} groupId="dra" />
          <div role="group" aria-label="DRA objects" className="l-seg">
            {tabChip("claims", "Claims")}
            {tabChip("pools", "Pools")}
            {tabChip("classes", "Device classes")}
          </div>
        </>
      )}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={list} rowHeight={TABLE_ROW_HEIGHT} rowKey={(r) => r.key} style={{ flex: 1, minHeight: 0 }} render={render} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
      footerEnd={<span className="t-secondary">{tab === "classes" ? "By name" : "Broken first"} · read-only</span>}
      inspector={inspector}
      inspectorLabel="DRA object details"
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

// outcomeWords says one view's outcome.
export function outcomeWords(v: DRAViewDTO): string {
  if (!v.agreed) return "not established: it depends on the scheduler's version or feature gate";
  const first = v.variants[0];
  switch (v.kind) {
    case "updating": return `being updated (${first?.have ?? 0} of ${first?.want ?? 0} slices)`;
    case "invalid": return `invalid${first?.rule ? `: ${first.rule}` : ""}`;
    case "complete": return devicesWords(v);
    case "none": return "serves no devices here";
    case "obsolete": return "obsolete here: a newer generation exists elsewhere";
    case "ok": return `reads OK under every allowed scheduler: ${devicesWords(v)}`;
    case "unestablished": return `not established${first?.rule ? `: ${first.rule}` : ""}`;
  }
  return v.kind ?? "";
}

// devicesWords says the devices a view can allocate, qualified when the
// variants disagree on the count (S11b §3.2: variant-dependent figures).
function devicesWords(v: DRAViewDTO): string {
  const counts = [...new Set(v.variants.map((x) => x.devices))].sort((a, b) => a - b);
  if (counts.length === 1) return plural(counts[0], "device", "devices");
  return `${counts[0]} to ${counts[counts.length - 1]} devices, depending on the scheduler`;
}

export const viewName = (n: string) => (n ? n : "a node no slice names (if there is one)");

function ClaimInspector({ cluster, c, pools, onOpenPool, onClose }: { cluster: string; c: DRAClaimDTO; pools: DRAPoolDTO[]; onOpenPool: (key: string) => void; onClose: () => void }) {
  const ro = roleKind(c.rule.role);
  const describe = useDescribe(cluster, { kind: "ResourceClaim", namespace: c.namespace, name: c.name, uid: c.uid });
  const ai = useInspectorAI(c.uid ? { kind: "row" as const, task: "dra", part: "claims", uid: c.uid } : null, `ResourceClaim/${c.namespace}/${c.name}`, true, cluster);
  const trouble = pools.filter((p) => p.reading.kind !== "ok");
  const blocked = c.consumers.some((x) => x.row === "b" || x.row === "c");
  return (
    <div data-testid="dra-claim-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: c.uid || undefined }} kind="ResourceClaim" name={c.name} meta={c.namespace} onClose={onClose}
        verdict={<InspectorStatus tone={toneOf(ro)} kind={ro} label={c.reading.label} reason={c.reading.message || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Claim">
          <div className="insp-kv">
            <span>state</span><span>{c.reading.native || "-"}</span>
            {c.ownerKind && <><span>owner</span><span>{c.ownerKind} {c.ownerName}{c.podClaimName ? ` (its claim "${c.podClaimName}")` : ""}</span></>}
            {c.extended && <><span>extended</span><span>the scheduler's claim for the pod's DRA-backed extended resources</span></>}
            {c.deleting && <><span>finalizers</span><span>{(c.finalizers ?? []).join(", ") || "none"}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title={`Requests · ${c.requests.length}`}>
          {c.requests.map((r) => <div key={r.name} className="t-body" data-testid="dra-claim-request">{r.name} <span className="t-secondary">· {r.class || (r.subclasses ?? []).join(" or ") || "-"} · {r.mode === "All" ? "all matching devices" : `${r.count ?? 1}`}{r.adminAccess ? " · admin access" : ""} (selectors not shown)</span></div>)}
        </InspectorBlock>
        {c.allocated && <InspectorBlock title={`Allocated · ${c.results.length}`}>
          {c.results.map((r) => <div key={`${r.driver}/${r.pool}/${r.device}/${r.shareId ?? ""}`} className="t-body" style={{ overflowWrap: "anywhere" }}>
            {r.driver}/{r.pool}/{r.device} <span className="t-secondary">· request {r.request}{r.shareId ? " · a share" : ""}{r.adminAccess ? " · admin access" : ""}</span></div>)}
          <div className="t-secondary" style={{ marginTop: 4 }}>{c.reservedCount ? `In use by ${c.reserved.map((x) => (x.resource === "pods" ? `pod ${x.name}` : `${x.resource} ${x.name} (not a pod)`)).join(", ")}${c.reservedCount > c.reserved.length ? ` and ${c.reservedCount - c.reserved.length} more` : ""}.` : "Not in use: the controller releases an allocation no consumer holds."}</div>
        </InspectorBlock>}
        {c.devices.length > 0 && <InspectorBlock title="Device status (the driver's)">
          {c.devices.map((d) => <div key={`${d.driver}/${d.pool}/${d.device}`} className="t-body" style={{ overflowWrap: "anywhere" }} data-testid="dra-device-status">
            {d.driver}/{d.pool}/{d.device} <span className="t-secondary">· {d.conditions.map((x) => `${x.type} ${x.status}${x.reason ? ` (${x.reason})` : ""}${x.message ? `: ${x.message}` : ""}`).join(" · ") || "no conditions"}</span></div>)}
        </InspectorBlock>}
        {c.consumers.length > 0 && <InspectorBlock title={`Waiting consumers · ${c.consumers.length + (c.consumersOmitted ?? 0)}`}>
          {c.consumers.map((x) => <div key={x.pod} className="t-body" data-testid="dra-claim-consumer" style={{ overflowWrap: "anywhere", padding: "2px 0" }}>
            <span style={{ fontFamily: "var(--font-mono)" }}>{x.pod}</span> <span style={{ color: ROLE_TEXT[roleKind(x.kind)] }}>· {x.label}</span>
            {x.meaning && <div className="t-secondary">{x.meaning}</div>}
            {x.message && <div className="t-secondary">The scheduler: "{x.message}"</div>}
          </div>)}
          {(c.consumersOmitted ?? 0) > 0 && <div className="t-secondary">{c.consumersOmitted} more consumers not shown</div>}
        </InspectorBlock>}
        {blocked && trouble.length > 0 && <InspectorBlock title="Check also">
          <div className="t-secondary">Pools on this cluster with a problem. Which pools this claim's class can select is decided by its selectors, which Klyx does not evaluate.</div>
          {trouble.slice(0, 5).map((p) => <button key={p.key} onClick={() => onOpenPool(p.key)} className="t-body"
            style={{ display: "block", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)" }}>{p.key} · {p.reading.label}</button>)}
        </InspectorBlock>}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function PoolInspector({ cluster, p, claims, onClose }: { cluster: string; p: DRAPoolDTO; claims: DRAPartRows<DRAClaimDTO>; onClose: () => void }) {
  const ro = roleKind(p.rule.role);
  // a pool has no object UID (its identity is driver/pool), so Explain is
  // not offered here
  const a = allocatedIn(claims.rows, p.driver, p.pool);
  const mark = joinMark(claims as DRAPartRows<unknown>);
  return (
    <div data-testid="dra-pool-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster }} kind="Device pool" name={`${p.driver} / ${p.pool}`} meta={`generation ${p.generation} · ${plural(p.slices, "slice", "slices")}`} onClose={onClose}
        verdict={<InspectorStatus tone={toneOf(ro)} kind={ro} label={p.reading.label} reason={p.reading.message || undefined} />}
      >
        <InspectorBlock title="Read as the scheduler reads it">
          <div className="t-secondary" data-testid="dra-pool-assumed">{p.assumed}. The scheduler judges a pool per node, so each node the slices name is read on its own.</div>
          {p.unread && <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>Not every slice was read (the slices budget): no pool is established.</div>}
        </InspectorBlock>
        <InspectorBlock title={`Nodes · ${p.views.length + (p.viewsOmitted ?? 0)}`}>
          {p.views.map((v) => <div key={v.node || "*"} className="t-body" data-testid="dra-pool-view" style={{ overflowWrap: "anywhere", padding: "2px 0" }}>
            <span style={{ fontFamily: "var(--font-mono)" }}>{viewName(v.node)}</span> <span className="t-secondary">· {outcomeWords(v)}</span>
            {!v.agreed && <div className="t-secondary">{v.variants.map((x) => `1.${x.minor}${x.partitionable ? " (partitionable devices on)" : ""}: ${x.outcome}`).join(" · ")}</div>}
          </div>)}
          {(p.viewsOmitted ?? 0) > 0 && <div className="t-secondary" data-testid="dra-pool-views-omitted">{plural(p.viewsOmitted ?? 0, "more node", "more nodes")} not listed: the {p.views.length} worst are shown, and the reading above is from every node.</div>}
          {(p.obsolete ?? []).length > 0 && <div className="t-secondary">Obsolete on {(p.obsolete ?? []).join(", ")}{p.obsoleteOmitted ? ` and ${p.obsoleteOmitted} more` : ""}: a driver updating node by node passes through this.</div>}
        </InspectorBlock>
        <InspectorBlock title="Allocated">
          <div className="t-body" data-testid="dra-pool-allocated">{a.devices}{mark} {a.devices === 1 ? "device" : "devices"} allocated per the API{a.shares ? `, ${a.shares} as shares` : ""}</div>
          <div className="t-secondary">Admin-access allocations are not counted. {mark === "+" ? "The claims read is not current and whole: this is a lower bound." : ""}</div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}

function ClassInspector({ cluster, c, claims, onClose }: { cluster: string; c: DRAClassDTO; claims: DRAPartRows<DRAClaimDTO>; onClose: () => void }) {
  const describe = useDescribe(cluster, { kind: "DeviceClass", namespace: "", name: c.name, uid: c.uid });
  return (
    <div data-testid="dra-class-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: c.uid || undefined }} kind="DeviceClass" name={c.name} meta="cluster-scoped" onClose={onClose}
        verdict={<div className="t-secondary">A device class is a fact in Klyx.</div>}
        more={[...describe.menu]}
      >
        <InspectorBlock title="Class">
          <div className="insp-kv">
            <span>selectors</span><span>{c.selectors} (CEL not shown)</span>
            <span>config drivers</span><span>{c.configDrivers.join(", ") || "none"}</span>
            {c.extendedResourceName && <><span>extended resource</span><span>{c.extendedResourceName}</span></>}
            <span>claims</span><span>{claimsOfClass(claims.rows, c.name)}{joinMark(claims as DRAPartRows<unknown>)} request it</span>
          </div>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
