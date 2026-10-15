import { useEffect, useMemo, useRef, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { Age, UpdatedAt, absoluteTime, formatAge, useNow, useReceivedAt } from "../chrome/Age";
import { compactCount } from "../chrome/format";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LiveIndicator } from "../chrome/LiveIndicator";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { useFleet } from "../store/fleet";
import type { EventRowDTO, ResourceRef } from "../store/fleet";
import { WORKLOAD_KINDS } from "../store/fleet";
import { listEvents, openLiveEvents } from "../bridge/events";
import { loadClusterPrefs, loadSearch, saveEventsViewPrefs, saveSearch } from "../store/persist";
import { WELL_KNOWN_RESOURCE_REFS } from "./resourceRefs";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { CountSentence } from "../layout/CountSentence";
import { useClusterKey } from "./useClusterKey";

// EventsView - Kubernetes events on the workspace template (spec §6 step 5).
// Events are evidence, never verdicts (health spec decision record): a
// Warning is amber text, never a lamp, and nothing here glows. The list is a
// live watch when available, otherwise a snapshot with Refresh; a failed read
// keeps the last good rows marked as such, and the apiserver's 500-event cap
// is stated in words, never tooltip-only. The full message lives in the
// inspector (no truncated-only diagnosis). No auto-select: the list is a
// stream, and selecting into it would chase new rows.

const keyOf = (e: EventRowDTO) => `${e.namespace}/${e.kind}/${e.name}/${e.reason}/${e.firstSeenUnix}`;
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };

const COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "type", label: "Type", width: 64, priority: 1 },
  { id: "count", label: "Count", width: 52, priority: 2 },
  { id: "age", label: "Last seen", width: 64, priority: 0 },
  { id: "namespace", label: "Namespace", width: 120, priority: 3, grow: 0.3 },
  { id: "object", label: "Object", width: 220, priority: 1, grow: 0.7 },
  { id: "reason", label: "Reason", width: 150, priority: 1 },
  { id: "message", label: "Message", width: 260, priority: 0, flex: true },
];

// Where an event's involved object can be opened. Events carry no API group,
// so a kind resolves only when it is unambiguous; anything else fails closed
// (no link) rather than opening the wrong object.
type Destination = { label: string; open: () => void } | null;
function refForKind(kind: string): ResourceRef | null {
  const matches = Object.values(WELL_KNOWN_RESOURCE_REFS).filter((r) => r.kind === kind);
  return matches.length === 1 ? matches[0] : null;
}
export function destinationOf(cluster: string, e: EventRowDTO): Destination {
  const nav = useFleet.getState();
  if (e.kind === "Pod") return { label: "Open Pod", open: () => { nav.setSection("pods"); nav.requestNavIntent({ kind: "select-pod", cluster, namespace: e.namespace, name: e.name }); } };
  if (e.kind === "Node") return { label: "Open Node", open: () => { nav.setSection("nodes"); nav.requestNavIntent({ kind: "select-node", cluster, node: e.name }); } };
  if ((WORKLOAD_KINDS as readonly string[]).includes(e.kind)) {
    return { label: `Open ${e.kind}`, open: () => { nav.setSection("workloads"); nav.requestNavIntent({ kind: "select-workload", cluster, workloadKind: e.kind, namespace: e.namespace, name: e.name }); } };
  }
  const ref = refForKind(e.kind);
  if (!ref) return null;
  return { label: `Open ${e.kind}`, open: () => { nav.openResource(ref); nav.openInstance(ref.scope === "Cluster" ? "" : e.namespace, e.name); } };
}

export function EventsView({ cluster }: { cluster: string }) {
  const events = useFleet((s) => s.events);
  const nowS = Math.floor(useNow() / 1000);
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const liveCleanupRef = useRef<(() => void) | null>(null);

  // Live subscription for the persisted namespace (or all). Hydration fills
  // only fields still at their reset defaults, so an intent set just before
  // mount always wins.
  useEffect(() => {
    const st = useFleet.getState();
    const prefs = loadClusterPrefs(cluster).events ?? {};
    if (prefs.warningsOnly && !st.events.warningsOnly) st.setWarningsOnly(true);
    const savedSearch = loadSearch(cluster, "events");
    if (savedSearch && st.events.search === "") st.setEventsSearch(savedSearch);
    liveCleanupRef.current = openLiveEvents(cluster, st.events.namespace || (prefs.namespace ?? ""));
    return () => {
      if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
      useFleet.getState().clearEvents();
    };
  }, [cluster]);
  useEffect(() => {
    if (events.cluster !== cluster) return; // transient during open/switch
    saveEventsViewPrefs(cluster, { namespace: events.namespace, warningsOnly: events.warningsOnly });
    saveSearch(cluster, "events", events.search);
  }, [cluster, events.cluster, events.namespace, events.warningsOnly, events.search]);

  const onNamespace = (ns: string) => {
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    setSelKey(null); setCursorKey(null); // a new scope never keeps the old selection
    liveCleanupRef.current = openLiveEvents(cluster, ns);
  };

  const mine = events.cluster === cluster;
  // rows belong to the scope they were READ for: another cluster's or another
  // namespace's rows are never shown or used as evidence here
  const scopeRead = mine && events.rowsScope === `${cluster}|${events.namespace}`;
  const items = useMemo(() => (scopeRead ? events.items : []), [scopeRead, events.items]);
  const error = mine ? events.error ?? null : null;
  // evidence of the current scope's rows: a read (live or snapshot) is
  // complete; a failure after one keeps those rows (stale); nothing read yet
  // is pending; a failure with nothing read is failed
  const evidence: "complete" | "stale" | "pending" | "failed" = scopeRead ? (error ? "stale" : "complete") : error ? "failed" : "pending";
  const receivedAt = useReceivedAt(scopeRead && !error ? events.items : null);

  const q = events.search.trim().toLowerCase();
  const visible = useMemo(() => items.filter((e) => {
    if (events.warningsOnly && e.type !== "Warning") return false;
    if (!q) return true;
    return [e.reason, e.message, e.name, e.namespace, e.kind].some((v) => (v ?? "").toLowerCase().includes(q));
  }), [items, events.warningsOnly, q]);
  const warnings = items.filter((e) => e.type === "Warning").length;

  // The component is reused across clusters: selection, cursor and last
  // evidence never carry over (a colliding row key on B is not A's event).
  const lastEvent = useRef<EventRowDTO | null>(null);
  const enteredCluster = useRef(cluster);
  if (enteredCluster.current !== cluster) { enteredCluster.current = cluster; lastEvent.current = null; }
  useEffect(() => { setSelKey(null); setCursorKey(null); }, [cluster, setSelKey]);

  const selected = selKey ? items.find((e) => keyOf(e) === selKey) : undefined;
  if (selected) lastEvent.current = selected;
  if (lastEvent.current && keyOf(lastEvent.current) !== selKey) lastEvent.current = null;
  // Aged out / deleted only when an UNCAPPED complete read of the same scope
  // lacks it. Missing from the arbitrary first 500 proves nothing: the event
  // stays as last evidence with no asserted cause.
  const missing = !!selKey && !selected && evidence === "complete";
  const removed = missing && !events.truncated;
  const partialMiss = missing && !!events.truncated;
  const inspected = selected ?? (missing ? lastEvent.current : null);
  const close = () => setSelKey(null);
  // later operator edits win: a filter that excludes the selection closes it
  useEffect(() => { if (selected && !visible.includes(selected)) setSelKey(null); }, [selected, visible, setSelKey]);

  const cursorIdx = cursorKey === null ? -1 : visible.findIndex((e) => keyOf(e) === cursorKey);
  useListKeys({
    count: visible.length,
    selected: cursorIdx,
    onSelect: (idx) => { const e = visible[idx]; if (e) { setCursorKey(keyOf(e)); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const e = visible[idx]; if (e) { setCursorKey(keyOf(e)); setSelKey(keyOf(e)); } },
    onEscape: close,
    searchRef,
  });

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const clearFilters = () => { useFleet.getState().setEventsSearch(""); useFleet.getState().setWarningsOnly(false); };
  let state: DatasetState = "ready";
  if (evidence === "failed" && items.length === 0) state = "unreadable";
  else if (evidence === "pending" && items.length === 0) state = "loading";
  else if (items.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";
  // "+" = the 500-event cap (a lower bound); "*" = the last good read
  const marker = evidence === "stale" ? "*" : events.truncated ? "+" : "";
  const scopeName = events.namespace ? `in ${events.namespace}` : "in all namespaces";

  const stateView = state === "loading" ? <SkeletonRows rows={8} label="reading events" />
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Could not read events." hint={error ?? undefined} action={{ label: "Retry", onClick: () => void listEvents(cluster, events.namespace) }} />
    : state === "empty" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title={`No events ${scopeName}.`} hint="Events age out of the API server after about an hour - quiet can be real." />
    : <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="No events match the current filters." hint={`${items.length}${marker} events ${scopeName}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  return (
    <LensLayout
      label="Events"
      title="Events"
      summary={state === "loading" || state === "unreadable" ? null : (
        <>
          <CountSentence total={items.length} marker={marker} noun={["event", "events"]} />
          <span className="t-secondary">{scopeName}</span>
          {/* warnings are evidence: amber text, never a lamp */}
          {warnings > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{warnings}{marker} warnings</span>}
        </>
      )}
      headerEnd={
        <>
          <LiveIndicator live={mine && events.live} />
          {!(mine && events.live) && <UpdatedAt at={receivedAt} />}
          <button className="l-btn" onClick={() => void listEvents(cluster, events.namespace)}>Refresh</button>
        </>
      }
      controls={
        <>
          <label style={{ display: "flex" }}>
            <span className="sr-only">Namespace</span>
            <select className="l-input" value={events.namespace} onChange={(e) => onNamespace(e.target.value)}>
              <option value="">All namespaces</option>
              {events.namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
            </select>
          </label>
          <div role="group" aria-label="Event type" className="l-seg">
            <button className="l-chip" aria-pressed={!events.warningsOnly} onClick={() => useFleet.getState().setWarningsOnly(false)}>All</button>
            <button className="l-chip" aria-pressed={events.warningsOnly} onClick={() => useFleet.getState().setWarningsOnly(true)}>Warnings {warnings > 0 && <span style={{ color: "var(--color-text-warning)" }}>{warnings}</span>}</button>
          </div>
          <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
            <span className="sr-only">Search events</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={events.search} onChange={(e) => useFleet.getState().setEventsSearch(e.target.value)} placeholder="Search reason, object, message" />
          </label>
        </>
      }
      notice={error && items.length > 0 ? <WatchErrorBanner tool="Events" failure="read failed - showing the last good read" error={error} />
        : events.truncated && mine ? <div role="status" className="t-secondary" style={{ minHeight: 28, display: "flex", alignItems: "center", color: "var(--color-text-warning)" }}>First 500 events only - the API server cap was hit and returns them in no particular order, so the newest warnings may be missing. Narrow to a namespace for the full set.</div>
        : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList
            ref={listRef}
            items={visible}
            rowHeight={TABLE_ROW_HEIGHT}
            rowKey={keyOf}
            style={{ flex: 1, minHeight: 0 }}
            render={(e) => {
              const k = keyOf(e);
              const warn = e.type === "Warning";
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
                  {show("type") && <span style={{ color: warn ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{e.type}</span>}
                  {show("count") && <span style={{ color: e.count > 1 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{e.count > 1 ? `×${compactCount(e.count)}` : ""}</span>}
                  {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}><Age unixSeconds={e.lastSeenUnix} /></span>}
                  {show("namespace") && <span style={{ ...cellStyle, color: "var(--color-text-tertiary)" }}>{e.namespace || "-"}</span>}
                  {show("object") && <span className="lens-cell-name" title={`${e.kind}/${e.name}`}><span style={{ color: "var(--color-text-tertiary)" }}>{e.kind}/</span>{e.name}</span>}
                  {show("reason") && <span style={{ ...cellStyle, color: warn ? "var(--color-text-warning)" : "var(--color-text-secondary)", fontWeight: 500 }}>{e.reason}</span>}
                  {show("message") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{e.message}</span>}
                </div>
              );
            }}
          />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length} of {items.length}{marker} events · newest first</span>}
      inspector={selKey && inspected ? (
        <EventInspector key={selKey} cluster={cluster} e={inspected} removed={removed} partialMiss={partialMiss} stale={evidence === "stale"} nowS={nowS} onClose={close} />
      ) : undefined}
      inspectorLabel="Event details"
      onCloseInspector={close}
    />
  );
}

function EventInspector({ cluster, e, removed, partialMiss, stale, nowS, onClose }: {
  cluster: string;
  e: EventRowDTO;
  removed: boolean;
  partialMiss: boolean;
  stale: boolean;
  nowS: number;
  onClose: () => void;
}) {
  const warn = e.type === "Warning";
  const dest = removed || partialMiss ? null : destinationOf(cluster, e);
  const span = e.firstSeenUnix > 0 && e.lastSeenUnix > e.firstSeenUnix ? ` over ${formatAge(e.lastSeenUnix - e.firstSeenUnix)}` : "";
  const more: MenuItem[] = [{ label: "Copy message", onSelect: () => void navigator.clipboard?.writeText(e.message) }];
  return (
    <div data-testid="event-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster }}
        kind="Event"
        namespace={e.namespace || undefined}
        name={e.reason}
        meta={`${e.kind}/${e.name}`}
        onClose={onClose}
        verdict={
          <div className="t-body" style={{ display: "grid", gap: 4 }}>
            {removed && <div role="status"><span className="t-status">Event no longer listed.</span> It aged out of the API server or was deleted; this is its last evidence.</div>}
            {partialMiss && <div role="status"><span className="t-status">Not in the current partial read.</span> The list is capped at the first 500 events, so its absence proves nothing; this is its last evidence.</div>}
            {/* evidence, not a verdict: no lamp */}
            <div>
              <span className="t-status" style={{ color: warn ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{e.type}</span>
              <span className="t-secondary"> · {e.count > 1 ? `${e.count.toLocaleString()} times${span}` : "once"} · last {e.lastSeenUnix > 0 ? `${formatAge(nowS - e.lastSeenUnix)} ago` : "unknown"}{stale ? " (last good read)" : ""}</span>
            </div>
          </div>
        }
        primary={dest ? { label: dest.label, onSelect: dest.open } : undefined}
        more={more}
      >
        <InspectorBlock title="Message">
          <div className="t-body" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{e.message || <span className="t-secondary">No message.</span>}</div>
        </InspectorBlock>
        <InspectorBlock title="Occurrence">
          <div className="insp-kv">
            <span>object</span><span style={{ overflowWrap: "anywhere" }}>{e.kind}/{e.name}{dest ? "" : <span className="t-secondary"> (no unambiguous destination)</span>}</span>
            <span>count</span><span>{e.count.toLocaleString()}</span>
            <span>first seen</span><span>{e.firstSeenUnix > 0 ? `${absoluteTime(e.firstSeenUnix * 1000)} (${formatAge(nowS - e.firstSeenUnix)} ago)` : "unknown"}</span>
            <span>last seen</span><span>{e.lastSeenUnix > 0 ? `${absoluteTime(e.lastSeenUnix * 1000)} (${formatAge(nowS - e.lastSeenUnix)} ago)` : "unknown"}</span>
          </div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}
