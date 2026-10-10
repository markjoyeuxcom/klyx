import { useEffect, useState, useCallback, useRef } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { Age } from "../chrome/Age";
import { compactCount } from "../chrome/format";
import { inputWell, selectWell, MetersStrip, Chip, EmptyState, LiveIndicator, SkeletonRows, btn, ellipsis } from "../chrome/primitives";
import { useFleet } from "../store/fleet";
import type { EventRowDTO } from "../store/fleet";
import { listEvents, openLiveEvents } from "../bridge/events";
import { openPodDetail } from "../bridge/pods";
import { VirtualList } from "../chrome/VirtualList";
import type { VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { loadClusterPrefs, loadSearch, saveEventsViewPrefs, saveSearch } from "../store/persist";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";

// type dot · count · age · ns · kind/name · reason · message
// count is 40px: the widest compact render is "×999k" - never the raw number,
// which a year-old flux reconcile event overflows (see chrome/format.ts).
const gridCols = "10px 40px 44px minmax(0,0.9fr) minmax(0,1.1fr) 90px minmax(0,2fr)";

// EventsViewLegacy - the pre-redesign Events lens, kept behind
// klyx.layout.v1.legacyEvents=1 until the migrated lens is accepted.
export function EventsViewLegacy({ cluster }: { cluster: string }) {
  const events = useFleet((s) => s.events);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [selectedIdx, setSelectedIdx] = useState(-1);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  // Holds the cleanup for the current live subscription so namespace changes
  // can close the old sub before opening the new one.
  const liveCleanupRef = useRef<(() => void) | null>(null);

  // Live subscription effect — opens the sub on mount for the persisted
  // namespace (or all namespaces). Hydration fills only fields still at their
  // reset defaults, so an intent set just before mount always wins.
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

  // Mirror filters into per-cluster prefs (namespace + toggles survive a
  // relaunch; search survives section switches within this run).
  useEffect(() => {
    if (events.cluster !== cluster) return; // transient state during open/switch
    saveEventsViewPrefs(cluster, { namespace: events.namespace, warningsOnly: events.warningsOnly });
    saveSearch(cluster, "events", events.search);
  }, [cluster, events.cluster, events.namespace, events.warningsOnly, events.search]);

  const onNamespace = (ns: string) => {
    // Close the current live sub, then reopen for the selected namespace.
    if (liveCleanupRef.current) { liveCleanupRef.current(); liveCleanupRef.current = null; }
    liveCleanupRef.current = openLiveEvents(cluster, ns);
  };
  const onRefresh = () => { void listEvents(cluster, events.namespace); };

  const warningCount = events.items.filter((e) => e.type === "Warning").length;

  const filtered = events.items.filter((e) => {
    if (events.warningsOnly && e.type !== "Warning") return false;
    if (events.search) {
      const q = events.search.toLowerCase();
      return (
        e.reason.toLowerCase().includes(q) ||
        e.message.toLowerCase().includes(q) ||
        e.name.toLowerCase().includes(q) ||
        e.namespace.toLowerCase().includes(q)
      );
    }
    return true;
  });

  // Stable identity — never the array index (live prepends would shift every
  // key and make the expanded row jump). firstSeenUnix disambiguates repeats of
  // the same reason and, unlike lastSeenUnix, does not change on recurrence.
  const rowKey = (e: EventRowDTO) => `${e.namespace}/${e.kind}/${e.name}/${e.reason}/${e.firstSeenUnix}`;

  const toggleExpand = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  // Clamp selection when list changes.
  const effectiveIdx = filtered.length === 0 ? -1 : selectedIdx >= filtered.length ? filtered.length - 1 : selectedIdx;

  const handleSelect = useCallback((idx: number) => {
    setSelectedIdx(idx);
    listRef.current?.scrollToIndex(idx);
  }, []);

  const handleActivate = useCallback((idx: number) => {
    const e = filtered[idx];
    if (e) toggleExpand(rowKey(e));
  }, [filtered]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleEscape = useCallback(() => {
    // Collapse selected row if expanded, else no-op.
    if (effectiveIdx >= 0) {
      const e = filtered[effectiveIdx];
      if (e) {
        const key = rowKey(e);
        if (expanded.has(key)) toggleExpand(key);
      }
    }
  }, [effectiveIdx, filtered, expanded]); // eslint-disable-line react-hooks/exhaustive-deps

  useListKeys({
    count: filtered.length,
    selected: effectiveIdx,
    onSelect: handleSelect,
    onActivate: handleActivate,
    onEscape: handleEscape,
    searchRef,
  });

  // Reset selection on filter change.
  useEffect(() => {
    setSelectedIdx((prev) => {
      if (filtered.length === 0) return -1;
      return prev >= filtered.length ? filtered.length - 1 : prev;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events.search, events.warningsOnly, events.namespace]);

  return (
    <div style={{ padding: "16px 20px", display: "flex", flexDirection: "column", height: "100%", boxSizing: "border-box" }}>
      {/* Controls row */}
      {/* Meters: the event stream at a glance - warnings are the fault lamp. */}
      <MetersStrip
        note="newest first"
        meters={[
          { label: "events", value: events.items.length },
          warningCount > 0 && { label: "warnings", value: warningCount, tone: "warning" as const },
          { label: "namespaces", value: events.namespaces.length },
        ]}
      />
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12, flexWrap: "wrap" }}>
        <select
          value={events.namespace}
          onChange={(e) => onNamespace(e.target.value)}
          style={selectWell}
        >
          <option value="">all namespaces</option>
          {events.namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
        </select>
        <Chip on={events.warningsOnly} tone="warning" onClick={() => useFleet.getState().toggleWarningsOnly()}>warnings only</Chip>
        <input
          ref={searchRef}
          value={events.search}
          onChange={(e) => useFleet.getState().setEventsSearch(e.target.value)}
          placeholder="filter events"
          style={{ ...inputWell, width: 160 }}
        />
        <button onClick={onRefresh} style={btn}>refresh</button>
        <LiveIndicator live={events.live} />
        {events.truncated && (
          <span title="The apiserver returned the 500-event cap in arbitrary order - the newest warnings may be missing. Narrow to a namespace for the full set." style={{ fontSize: 10, color: "var(--color-text-warning)" }}>
            first 500 only
          </span>
        )}
      </div>

      {/* Read failure - never pass as "no events" (review 2026-10-03 #10). */}
      {events.error && (
        <div style={{ marginBottom: 10 }}>
          <WatchErrorBanner tool="Events" failure="read failed" error={events.error} />
        </div>
      )}

      {/* Table */}
      {events.loading && events.items.length === 0 ? (
        <SkeletonRows rows={8} label="loading events" />
      ) : filtered.length === 0 ? (
        <EmptyState
          icon={<IconAlertTriangle size={28} stroke={1.2} />}
          title={events.items.length === 0
            ? `No events${events.namespace ? ` in ${events.namespace}` : ""}.`
            : "No events match the current filter."}
          hint={events.items.length === 0 ? "Events age out of the API server after roughly an hour - quiet can be real." : undefined}
          action={events.items.length > 0 ? {
            label: "Clear filters",
            onClick: () => {
              useFleet.getState().setEventsSearch("");
              useFleet.getState().setWarningsOnly(false);
            },
          } : undefined}
        />
      ) : (
        <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, flex: 1, display: "flex", flexDirection: "column", minHeight: 0 }}>
          {/* Header */}
          <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 8, padding: "0 8px 6px", fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)", flexShrink: 0 }}>
            <span /><span>×</span><span>age</span><span>namespace</span><span>involved</span><span>reason</span><span>message</span>
          </div>
          {/*
           * VirtualList windows >=100 rows. An expanded row has variable height
           * which breaks fixed-row windowing math, so any expansion forces the
           * plain path (forcePlain) - users expand rows to read details, at
           * which point full rendering is the correct tradeoff.
           */}
          <VirtualList
            ref={listRef}
            items={filtered}
            rowHeight={30}
            rowKey={rowKey}
            forcePlain={expanded.size > 0}
            style={{ flex: 1, minHeight: 0, ...(expanded.size > 0 ? { overflowY: "auto" } : {}) }}
            render={(e, i) => {
              const key = rowKey(e);
              const isKbSelected = i === effectiveIdx;
              const isExpanded = expanded.has(key);
              return (
                <EventRow
                  key={key}
                  event={e}
                  cluster={cluster}
                  expanded={isExpanded}
                  kbSelected={isKbSelected}
                  onToggle={() => toggleExpand(key)}
                  onKbSelect={() => handleSelect(i)}
                />
              );
            }}
          />
        </div>
      )}
    </div>
  );
}

function EventRow({ event: e, cluster, expanded, kbSelected, onToggle, onKbSelect }: {
  event: EventRowDTO;
  cluster: string;
  expanded: boolean;
  kbSelected: boolean;
  onToggle: () => void;
  onKbSelect: () => void;
}) {
  const isWarning = e.type === "Warning";

  const handlePodLink = (ev: React.MouseEvent) => {
    ev.stopPropagation();
    // Navigate to pods section first, then open pod detail
    useFleet.getState().setSection("pods");
    void openPodDetail(cluster, e.namespace, e.name);
  };

  const involvedCell = e.kind === "Pod" ? (
    <button
      onClick={handlePodLink}
      style={{
        background: "none", border: "none", padding: 0, cursor: "pointer",
        color: "var(--color-text-info)", fontFamily: "inherit", fontSize: "inherit",
        textAlign: "left", ...ellipsis,
      }}
      title={`${e.kind}/${e.name}`}
    >
      <span style={{ color: "var(--color-text-tertiary)" }}>{e.kind}/</span>
      {e.name}
    </button>
  ) : (
    <span style={{ ...ellipsis, color: "var(--color-text-secondary)" }} title={`${e.kind}/${e.name}`}>
      <span style={{ color: "var(--color-text-tertiary)" }}>{e.kind}/</span>
      {e.name}
    </span>
  );

  return (
    <>
      <div
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        aria-selected={kbSelected}
        onClick={() => { onKbSelect(); onToggle(); }}
        onKeyDown={(ev) => {
          if (ev.key === "Enter" || ev.key === " ") {
            ev.preventDefault();
            onKbSelect();
            onToggle();
          }
        }}
        style={{
          display: "grid", gridTemplateColumns: gridCols, gap: 8, alignItems: "center",
          padding: "6px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)",
          cursor: "pointer",
          background: kbSelected
            ? "var(--color-background-secondary)"
            : expanded ? "var(--color-background-secondary)" : undefined,
          boxShadow: kbSelected ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
          outline: "none",
        }}
      >
        {/* Type dot */}
        <span
          style={{
            width: 7, height: 7, borderRadius: "50%", flexShrink: 0,
            background: isWarning ? "var(--color-text-warning)" : "var(--color-text-tertiary)",
          }}
          title={e.type}
        />
        {/* Count */}
        <span
          title={e.count > 1 ? `${e.count.toLocaleString()} occurrences` : undefined}
          style={{ color: e.count > 1 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}
        >
          {e.count > 1 ? `×${compactCount(e.count)}` : ""}
        </span>
        {/* Age */}
        <span style={{ color: "var(--color-text-tertiary)" }}>
          <Age unixSeconds={e.lastSeenUnix} />
        </span>
        {/* Namespace */}
        <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={e.namespace}>{e.namespace}</span>
        {/* Involved kind/name */}
        <span style={{ fontFamily: "var(--font-mono)", minWidth: 0 }}>
          {involvedCell}
        </span>
        {/* Reason */}
        <span style={{ ...ellipsis, fontWeight: 500, color: isWarning ? "var(--color-text-warning)" : "var(--color-text-secondary)" }} title={e.reason}>{e.reason}</span>
        {/* Message (truncated) */}
        <span style={{ ...ellipsis, color: "var(--color-text-secondary)" }} title={e.message}>{e.message}</span>
      </div>
      {/* Expanded message */}
      {expanded && (
        <div style={{
          padding: "6px 8px 8px 28px",
          borderBottom: "0.5px solid var(--color-border-tertiary)",
          background: "var(--color-background-secondary)",
          fontSize: 11, color: "var(--color-text-primary)",
          lineHeight: 1.5,
          wordBreak: "break-word",
        }}>
          {e.message}
        </div>
      )}
    </>
  );
}
