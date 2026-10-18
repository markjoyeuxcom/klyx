import { useEffect, useMemo, useRef, useState } from "react";
import { IconRoute } from "@tabler/icons-react";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useNavIntent } from "../chrome/useNavIntent";
import { useFleet } from "../store/fleet";
import type { ListenerSetRowDTO } from "../store/fleet";
import type { ListenerSetRows } from "../health/useListenerSetRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, kindEvidence, lastGoodAt } from "../layout/coverage";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { CountSentence } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { RANK, REFS, ROLE_TEXT, condState, listenerText, roleTone, type Role } from "./gatewayModel";
import { ListenerList, PARENT_TEXT, ordinal, parentKey } from "./gatewayListeners";
import { useInspectorAI } from "../ai/AIExplain";
import { staleProps } from "./staleness";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

// ListenerSetList - the Gateways task's ListenerSets part as a view beside
// Gateways and Routes (coverage S7a §9): broken first. A ListenerSet's
// verdict is its controller's conditions, then its parent as Klyx could
// establish it; Klyx's own reading of the parent's allowedListeners is
// evidence shown beside it, never the verdict.

type Mode = "all" | "attention";
const keyOf = (s: { namespace: string; name: string }) => `${s.namespace}/${s.name}`;
const roleOf = (s: { rule: ListenerSetRowDTO["rule"] }): Role => ({ kind: s.rule.role as Role["kind"], label: s.rule.label });
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
// the version read (S7 §2), named in the inspector
const readAs = (gvr?: string) => { const v = gvr?.split("/")[1]; return v ? `read as listenersets.${v}` : ""; };

const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "name", label: "ListenerSet", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 200, priority: 0 },
  { id: "parent", label: "Parent", width: 180, priority: 1 },
  { id: "allowance", label: "Allowed by parent", width: 150, priority: 2 },
  { id: "listeners", label: "Listeners", width: 200, priority: 3 },
];

export function ListenerSetList({ cluster, rows, toggle, onOpenGateway }: { cluster: string; rows: ListenerSetRows; toggle: React.ReactNode; onOpenGateway: (namespace: string, name: string) => void }) {
  const [mode, setMode] = useState<Mode>("all");
  const [query, setQuery] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  const ev = kindEvidence(!rows.loading, rows.coverage, "ListenerSet", rows.watchError);
  const absent = rows.coverage.find((k) => k.kind === "ListenerSet")?.coverage === "absent";
  const error = rows.watchError;
  const sets = rows.sets;
  const refreshing = rows.rowsState.refreshing;
  const rowsAsOf = rows.rowsState.evaluatedAt ? clock(rows.rowsState.evaluatedAt) : "the last read";
  const asOf = refreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = lastGoodAt(rows.coverage, ["ListenerSet"]);
  const mark = ev === "complete" ? "" : ev === "stale" ? "*" : "+";

  const ordered = useMemo(() => [...sets].sort((a, b) =>
    RANK[roleOf(a).kind] - RANK[roleOf(b).kind] || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [sets]);
  const total = rows.partCounts?.total ?? sets.length;
  const attention = rows.partCounts?.included ?? sets.filter((s) => s.rule.included).length;
  const fault = rows.partCounts ? rows.partCounts.fault : sets.filter((s) => s.rule.included && roleOf(s).kind === "fault").length;
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => ordered.filter((s) => {
    if (mode === "attention" && RANK[roleOf(s).kind] > RANK.degraded) return false;
    if (!q) return true;
    return [s.namespace, s.name, parentKey(s), roleOf(s).label, s.allowance.from, ...s.listeners.map(listenerText)].some((v) => v.toLowerCase().includes(q));
  }), [ordered, mode, q]);

  // the one-shot first-broken selection; a consumed reveal is that selection
  // (a view mounting with a pending reveal runs both in one commit)
  const autoDone = useRef(false);
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "gateway" || intent.part !== "listenersets") return "wait";
    const key = `${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const row = sets.find((s) => keyOf(s) === key);
    if (intent.ref.uid) expectPin(cluster, "ListenerSet", key, intent.ref.uid);
    else if (!row) return ev === "complete" ? "missing" : "wait";
    setMode("all"); setQuery("");
    setCursorKey(key);
    setSelKey(key);
    autoDone.current = true;
    return "consumed";
  }, [sets, ev]);

  const found = selKey ? sets.find((s) => keyOf(s) === selKey) : undefined;
  const pin = usePinnedSubject(cluster, selKey, found, ev === "complete", "ListenerSet");
  const selected = pin.subject ?? undefined;
  const gone: "removed" | "unverified" | null = pin.gone === "replaced" ? "removed" : pin.gone;
  const close = () => setSelKey(null);

  useEffect(() => {
    if (autoDone.current || ev !== "complete") return;
    autoDone.current = true;
    const first = visible[0];
    if (first && first.rule.included && selKey === null) { setSelKey(keyOf(first)); setCursorKey(keyOf(first)); }
  }, [ev, visible, selKey, setSelKey]);
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !visible.includes(selected)) setSelKey(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, q]);

  const cursorIdx = cursorKey === null ? -1 : visible.findIndex((s) => keyOf(s) === cursorKey);
  useListKeys({
    count: visible.length,
    selected: cursorIdx,
    onSelect: (i) => { const s = visible[i]; if (s) { setCursorKey(keyOf(s)); listRef.current?.scrollToIndex(i); } },
    onActivate: (i) => { const s = visible[i]; if (s) { setCursorKey(keyOf(s)); setSelKey(keyOf(s)); } },
    onEscape: close,
    searchRef,
  });

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  let state: DatasetState = "ready";
  if (ev === "failed" && sets.length === 0) state = "unreadable";
  else if (ev === "pending" && sets.length === 0) state = "loading";
  else if (sets.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";

  const icon = <IconRoute size={28} stroke={1.2} />;
  const clearFilters = () => { setMode("all"); setQuery(""); };
  const stateView = state === "loading" ? <SkeletonRows rows={4} label="reading ListenerSets" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not list ListenerSets." hint={error ?? undefined} />
    : state === "empty" && absent ? <EmptyState icon={icon} title="ListenerSets are not served on this cluster." hint="Gateway API 1.5 and later serve gateway.networking.k8s.io/v1 ListenerSets; this cluster does not." />
    : state === "empty" && ev === "stale" ? <EmptyState icon={icon} title="No ListenerSets in the last good read." hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${error ?? "unverified"}.`} />
    : state === "empty" ? <EmptyState icon={icon} title="No ListenerSets on this cluster." hint="ListenerSets are served, but none exist: every listener is a Gateway's own." />
    : <EmptyState icon={icon} title="No ListenerSets match the current filters." hint={`${sets.length} ListenerSets on this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  const notices: React.ReactNode[] = [];
  if (error && sets.length > 0) notices.push(<WatchErrorBanner key="read" tool="ListenerSets" failure={`read failed - showing the last good list${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={error} />);
  if (refreshing) notices.push(<div key="r" role="status" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.</div>);

  return (
    <LensLayout
      label="ListenerSets"
      title="Gateways and routes"
      summary={state === "loading" || state === "unreadable" || absent ? null : (
        <CountSentence total={total} marker={mark} noun={["ListenerSet", "ListenerSets"]}
          attention={{ count: attention, phrase: "need attention", fault: fault > 0, testid: "ls-attention" }} />
      )}
      controls={(
        <>
          {toggle}
          {sets.length > 0 && (
            <>
              <div role="group" aria-label="ListenerSet filter" className="l-seg">
                <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {total}</button>
                <button className="l-chip" aria-pressed={mode === "attention"} onClick={() => setMode("attention")}>Needs attention {attention > 0 && <span style={{ color: fault > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{attention}</span>}</button>
              </div>
              <label className="lens-search">
                <span className="sr-only">Search ListenerSets</span>
                <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
                  value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search name, parent, listener" />
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
            render={(s) => {
              const k = keyOf(s);
              const role = roleOf(s);
              const parentNote = PARENT_TEXT[s.parentState] ? ` · ${s.parentState}` : "";
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" data-testid="listenerset-row" {...staleProps(s.stale)} style={gridStyle}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(k); } }}>
                  {show("mark") && <VerdictMark health={role} stale={s.stale} />}
                  {show("name") && <span className="lens-cell-name" title={k}><span style={{ color: "var(--color-text-tertiary)" }}>{s.namespace} / </span>{s.name}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }}
                    title={s.stale ? `Observed Gen: ${s.observedGeneration} vs Current: ${s.generation ?? "?"}` : undefined}>{role.label}</span>}
                  {show("parent") && <span style={{ ...cellStyle, color: s.parentState === "found" ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{parentKey(s)}{parentNote}</span>}
                  {show("allowance") && <span style={{ ...cellStyle, color: s.allowance.disagrees ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}
                    title={s.allowance.text}>{s.allowance.from ? `${s.allowance.from}${s.allowance.allowed ? ` · ${s.allowance.allowed}` : ""}` : "-"}{s.allowance.disagrees ? " · disagrees" : ""}</span>}
                  {show("listeners") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{s.listeners.map(listenerText).join(", ") || "-"}</span>}
                </div>
              );
            }} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length !== sets.length ? `${visible.length} of ${sets.length}${mark} · ` : ""}needs attention first{asOf}</span>}
      inspector={selected ? <ListenerSetInspector key={selKey} cluster={cluster} s={selected} stale={ev === "stale"} gone={gone} replaced={pin.gone === "replaced" ? pin.adopt : undefined} onOpenGateway={onOpenGateway} onClose={close} />
        : selKey && pin.expected && pin.gone ? <PinnedGone cluster={cluster} kind="ListenerSet" namespace={selKey.split("/")[0]} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={close} />
        : undefined}
      inspectorLabel="ListenerSet details"
      onCloseInspector={close}
    />
  );
}

// listenerSetVerdictDetail explains the verdict from the evidence it was read from.
function listenerSetVerdictDetail(s: ListenerSetRowDTO): string {
  const a = condState(s.acceptedCond, s.generation);
  const p = condState(s.programmedCond, s.generation);
  const why = (c: { reason: string; message: string }) => [c.reason, c.message].filter(Boolean).join(": ");
  if (a.status === "False" && !a.stale) return `Its controller did not accept it${why(a) ? ` - ${why(a)}` : ""}. Its listeners are not merged into the parent.`;
  if (a.status === "True" && p.status === "False" && !p.stale) return `Accepted, but not programmed${why(p) ? ` - ${why(p)}` : ""}.`;
  if (s.parentState === "not found") return "Its parent Gateway is absent from a complete read of the Gateways: its listeners join nothing.";
  if (s.parentState === "not established") return "Its parent Gateway is not in the Gateways read, and that read is incomplete - whether it exists is unverified.";
  if (s.parentState === "unsupported") return `Its parent is a ${s.parent.kind}; Klyx reads Gateway parents only.`;
  if (a.status === "absent") return "No controller has written an Accepted condition - check that the parent Gateway's class has a controller that supports ListenerSets.";
  if (a.status === "Unknown") return `The controller has not decided yet${why(a) ? ` (${why(a)})` : ""}.`;
  if (a.stale || p.stale) return `Accepted and programmed as of generation ${Math.min(a.observedGeneration, p.observedGeneration)}; the controller has not reported on generation ${s.generation} yet.`;
  if (s.rule.role === "degraded") return "Accepted and programmed, but its controller reports a listener failing (below).";
  return "Accepted and programmed by its controller; its listeners are merged into the parent Gateway.";
}

function ListenerSetInspector({ cluster, s, stale, gone, replaced, onOpenGateway, onClose }: { cluster: string; s: ListenerSetRowDTO; stale: boolean; gone: "removed" | "unverified" | null; replaced?: () => void; onOpenGateway: (namespace: string, name: string) => void; onClose: () => void }) {
  const ai = useInspectorAI(s.uid ? { kind: "row" as const, task: "gateway", part: "listenersets", uid: s.uid } : null, `ListenerSet/${s.namespace}/${s.name}`, !gone, cluster);
  const describe = useDescribe(cluster, !gone ? { kind: "ListenerSet", namespace: s.namespace, name: s.name, uid: s.uid } : null);
  const role = roleOf(s);
  const openObject = (ns: string, name: string) => {
    const nav = useFleet.getState();
    nav.openResource({ group: "gateway.networking.k8s.io", version: "v1", plural: "listenersets", kind: "ListenerSet", scope: "Namespaced" });
    nav.openInstance(ns, name);
  };
  const openParent = () => {
    const nav = useFleet.getState();
    nav.openResource(REFS.gateway);
    nav.openInstance(s.parent.namespace, s.parent.name);
  };
  const isGateway = s.parent.kind === "Gateway" && s.parent.group === "gateway.networking.k8s.io";
  return (
    <div data-testid="listenerset-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: s.uid || undefined }} replaced={replaced} kind="ListenerSet" namespace={s.namespace} name={s.name}
        meta={[`parent ${parentKey(s)}`, readAs(s.gvr)].filter(Boolean).join(" · ")} onClose={onClose}
        verdict={gone === "removed" ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">ListenerSet removed.</span> It is no longer in the current read. The details below are its last evidence.
          </div>
        ) : gone === "unverified" ? (
          <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <span className="t-status">Not in the current ListenerSet read.</span> That read is incomplete, so this is unverified - it may still exist.
          </div>
        ) : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} />
            <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>{listenerSetVerdictDetail(s)}</div>
            {stale && <div className="t-secondary" style={{ marginTop: 4 }}>From the last good list - the latest read failed.</div>}
          </div>
        )}
        primary={!gone && isGateway && s.parentState === "found" ? { label: "Open parent topology", onSelect: () => onOpenGateway(s.parent.namespace, s.parent.name) } : undefined}
        more={[
          { label: "Open ListenerSet object", onSelect: () => openObject(s.namespace, s.name) },
          ...(isGateway ? [{ label: `Open Gateway ${parentKey(s)}`, onSelect: openParent }] : []),
          ...describe.menu, ...ai.menu]}>
        {ai.block}
        <InspectorBlock title="Parent">
          <div className="insp-kv" data-testid="listenerset-parent">
            <span>{s.parent.kind}</span>
            <span>{parentKey(s)}{PARENT_TEXT[s.parentState] ? ` - ${PARENT_TEXT[s.parentState]}` : ""}</span>
            <span>Merge order</span>
            <span data-testid="listenerset-precedence">
              {s.precedence > 0 ? `${ordinal(s.precedence)} of ${s.siblings} ListenerSet${s.siblings === 1 ? "" : "s"} naming this parent, after the Gateway's own listeners` : "-"}
              {s.precedenceComplete ? "" : " (the ListenerSets read is incomplete: one that ranks earlier may be missing)"}
            </span>
          </div>
          <div className="t-secondary" style={{ marginTop: 6 }}>
            The parent merges listeners in this order - its own, then ListenerSets oldest first, then by namespace/name. Klyx never decides a conflict: a listener that lost reports Conflicted below.
          </div>
        </InspectorBlock>
        <InspectorBlock title="Allowed by the parent">
          <div data-testid="listenerset-allowance" className="t-body" style={{ color: s.allowance.disagrees ? "var(--color-text-warning)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>
            {s.allowance.text || "-"}
          </div>
          <div className="t-secondary" style={{ marginTop: 4 }}>
            Klyx's own reading of the parent's allowedListeners - evidence beside the controller's Accepted condition, which stays the verdict.
          </div>
        </InspectorBlock>
        <InspectorBlock title="Listeners" count={s.listeners.length}>
          <ListenerList listeners={s.listeners} testid="listenerset-listeners" />
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
