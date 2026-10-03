import { inputWell } from "../chrome/primitives";
import React, { useEffect, useRef, useState, useLayoutEffect, useCallback } from "react";
import { Events } from "@wailsio/runtime";
import {
  IconArrowsDiagonal,
  IconArrowsDiagonalMinimize,
  IconCheck,
  IconCopy,
  IconFilter,
  IconX,
} from "@tabler/icons-react";
import { LogsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { newStreamId } from "../bridge/streams";
import type { ContainerSummaryDTO } from "../store/fleet";
import { parseLine, stripAnsi, splitHighlight } from "./logline";
import type { LogLevel, ParsedLine } from "./logline";

// TODO(DD5): virtualize line rendering for very large buffers. Currently capped
// at 10000 stored lines / 2000 rendered lines; lines are parsed ONCE at ingest
// (raw + parsed stored together in the buffer) so renders and level filters
// never re-run the level/prefix regexes over the whole buffer. Roadmap:
// react-virtual or a custom windowed scroller once real-world usage confirms
// the threshold.

const LOG_BUF_CAP = 10_000;
const DOM_RENDER_CAP = 2_000;

// Expanded pane sits below modals/palette (z-index 1000) and well below toasts (2000).
const EXPAND_Z = 900;

type StreamStatus = "connecting" | "streaming" | "ended" | "error";
type LogLevelFilter = Extract<LogLevel, "error" | "warn" | "info" | "debug">;

const LOG_LEVEL_FILTERS: LogLevelFilter[] = ["error", "warn", "info", "debug"];

interface LogChunkDTO {
  lines: string[];
  eof: boolean;
  error?: string;
}

interface PodRef {
  namespace: string;
  name: string;
  containers: ContainerSummaryDTO[];
}

interface LogLineDetail {
  raw: string;
  stripped: string;
  level: LogLevel;
  prefix: string;
  message: string;
  source?: string;
  hasAnsi: boolean;
}

// A buffered line carries its parse result, computed once at ingest —
// re-parsing the whole buffer per render was the hot path.
interface BufferedLine {
  raw: string;
  parsed: ParsedLine;
}

function toBuffered(line: string): BufferedLine {
  return { raw: line, parsed: parseLine(stripAnsi(line)) };
}

// Reference implementation of the level filter over raw lines. The component
// itself filters pre-parsed BufferedLines (same semantics, no re-parse); this
// export remains the documented/tested contract for that behavior.
export function filterLogLinesByLevel(lines: string[], filters: Record<LogLevelFilter, boolean>): string[] {
  const active = LOG_LEVEL_FILTERS.some((level) => filters[level]);
  if (!active) return lines;
  return lines.filter((line) => {
    const level = parseLine(stripAnsi(line)).level;
    return level !== "none" && filters[level as LogLevelFilter] === true;
  });
}

export function getLogLineDetail(line: string): LogLineDetail {
  const stripped = stripAnsi(line);
  const parsed = parseLine(stripped);
  const prefix = parsed.dimPrefixLen > 0 ? stripped.slice(0, parsed.dimPrefixLen) : "";
  const message = stripped.slice(parsed.dimPrefixLen).trimStart() || stripped;
  const sourceMatch = prefix.match(/([A-Za-z0-9_.-]+\.[A-Za-z0-9]+:\d+)\]?$/);

  return {
    raw: line,
    stripped,
    level: parsed.level,
    prefix,
    message,
    source: sourceMatch?.[1],
    hasAnsi: stripped !== line,
  };
}

export function LogsPane({
  cluster,
  pod,
  workload,
  initialContainer,
  hostedInWindow = false,
  onContainerChange,
}: {
  cluster: string;
  pod: PodRef;
  // workload switches the pane to AGGREGATE mode: the stream opens via
  // OpenWorkloadLogStream across all the workload's pods (pod.name is the
  // workload name for display; pod.namespace is used). The "previous" toggle is
  // hidden (not meaningful per-aggregate) and container "" means each pod's
  // default container.
  workload?: { kind: string; name: string };
  // initialContainer is used when the containers list is empty (the pop-out
  // window has no pod summary, only the container param). When set with an empty
  // containers list, the picker is replaced by static text and the stream opens
  // on this container directly.
  initialContainer?: string;
  // hostedInWindow hides the expand affordance (fixed-overlay expand is
  // meaningless in a dedicated OS window) and arms a beforeunload best-effort
  // stream close.
  hostedInWindow?: boolean;
  // onContainerChange notifies the host of the active container so a parent (the
  // dock) can carry it into a pop-out. Fired on initial select and every switch.
  onContainerChange?: (container: string) => void;
}) {
  // -- container picker --
  const regular = pod.containers.filter((c) => !c.init);
  const init = pod.containers.filter((c) => c.init);
  const ordered = [...regular, ...init];
  // Default selection: first regular container, else first of any, else the
  // explicit initialContainer (the pop-out case with no containers list).
  const firstRegular = regular[0]?.name ?? ordered[0]?.name ?? initialContainer ?? "";
  // staticContainer: no picker options but an explicit container was provided
  // (pop-out). Render it as static text rather than an empty select.
  const staticContainer =
    pod.containers.length === 0 && ((initialContainer ?? "") !== "" || workload !== undefined);

  const [container, setContainer] = useState(firstRegular);
  const [previous, setPrevious] = useState(false);
  const [tailLines, setTailLines] = useState(500);
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(true);
  const [search, setSearch] = useState("");
  const [levelFilters, setLevelFilters] = useState<Record<LogLevelFilter, boolean>>({
    error: false,
    warn: false,
    info: false,
    debug: false,
  });
  const [expanded, setExpanded] = useState(false);
  const [copyState, setCopyState] = useState<"idle" | "copied">("idle");
  const [selectedLine, setSelectedLine] = useState<{ index: number; text: string } | null>(null);
  const [copiedLine, setCopiedLine] = useState<{ index: number; text: string } | null>(null);

  // -- buffer (OUTSIDE zustand) --
  const bufRef = useRef<BufferedLine[]>([]);
  const droppedRef = useRef(false);
  // version counter: bump once per batch to trigger a re-render
  const [version, setVersion] = useState(0);

  // -- stream state --
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [errorMsg, setErrorMsg] = useState<string | undefined>(undefined);

  // -- scroll container --
  const scrollRef = useRef<HTMLDivElement>(null);
  const streamIdRef = useRef<string>("");
  const offRef = useRef<(() => void) | null>(null);

  // -- search nav state --
  const searchInputRef = useRef<HTMLInputElement>(null);
  const [activeMatchIdx, setActiveMatchIdx] = useState(0);

  // derive which lines to render (last DOM_RENDER_CAP from buffer). Level
  // filtering reads the parse result stored at ingest — no per-render regexes.
  const buf = bufRef.current;
  const levelFilterActive = LOG_LEVEL_FILTERS.some((level) => levelFilters[level]);
  const filteredLines = levelFilterActive
    ? buf.filter((l) => l.parsed.level !== "none" && levelFilters[l.parsed.level as LogLevelFilter] === true)
    : buf;
  const totalVisibleLines = filteredLines.length;
  const renderLines = filteredLines.length > DOM_RENDER_CAP ? filteredLines.slice(filteredLines.length - DOM_RENDER_CAP) : filteredLines;
  const truncated = totalVisibleLines > DOM_RENDER_CAP;

  // search highlight helpers
  const searchLc = search.toLowerCase();

  // match count + per-line match tracking (index of matches across renderLines)
  const matchLineIndices: number[] = [];
  if (searchLc) {
    for (let i = 0; i < renderLines.length; i++) {
      if (renderLines[i].raw.toLowerCase().includes(searchLc)) matchLineIndices.push(i);
    }
  }
  // counter shares navigation's coordinate space: only matches inside
  // renderLines are Enter-cyclable, so the denominator must be the navigable
  // set, not the whole (up to LOG_BUF_CAP) filtered buffer.
  const matchCount = matchLineIndices.length;

  // Clamp activeMatchIdx when matches change
  const clampedActive =
    matchLineIndices.length === 0 ? 0 : activeMatchIdx % matchLineIndices.length;

  // row refs for scrollIntoView
  const rowRefs = useRef<(HTMLDivElement | null)[]>([]);

  // -- auto-scroll --
  useLayoutEffect(() => {
    if (follow && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [version, follow]);

  // scroll active match into view
  useLayoutEffect(() => {
    if (!searchLc || matchLineIndices.length === 0) return;
    const targetLineIdx = matchLineIndices[clampedActive];
    if (targetLineIdx === undefined) return;
    const el = rowRefs.current[targetLineIdx];
    // guard: jsdom does not implement scrollIntoView
    if (el && typeof el.scrollIntoView === "function") {
      el.scrollIntoView({ block: "nearest" });
    }
  }, [clampedActive, searchLc, matchLineIndices.length]); // eslint-disable-line react-hooks/exhaustive-deps

  // disable follow on manual scroll-up
  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const fromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (fromBottom > 80) setFollow(false);
  }, []);

  // -- stream lifecycle --
  useEffect(() => {
    let stale = false;

    async function open() {
      // close any prior stream
      if (offRef.current) { offRef.current(); offRef.current = null; }
      if (streamIdRef.current) {
        await LogsService.CloseLogStream(streamIdRef.current).catch(() => undefined);
        streamIdRef.current = "";
      }

      // reset buffer
      bufRef.current = [];
      droppedRef.current = false;
      setSelectedLine(null);
      setCopiedLine(null);
      setVersion((v) => v + 1);
      setStatus("connecting");
      setErrorMsg(undefined);

      // Subscribe FIRST, on an id we choose: the backend emits the first batch
      // (often the whole tail) before the open call returns (review 2026-10-03
      // #8). ended guards the status against an EOF that beats the open's
      // return - a short previous-container log can finish first.
      const streamId = newStreamId("logs");
      let ended = false;
      const off = Events.On(`podlogs:${streamId}`, (ev: { data: LogChunkDTO }) => {
        if (stale) return;
        const chunk = ev.data;
        if (chunk.lines && chunk.lines.length > 0) {
          // Parse each line once at ingest, and mutate the ref'd buffer in
          // place — copying the whole 10k-line buffer per chunk was pure
          // overhead. Nothing memoizes on the array identity; the version
          // counter is the render trigger.
          const buf = bufRef.current;
          for (const line of chunk.lines) buf.push(toBuffered(line));
          if (buf.length > LOG_BUF_CAP) {
            buf.splice(0, buf.length - LOG_BUF_CAP);
            droppedRef.current = true;
          }
          setVersion((v) => v + 1);
        }
        if (chunk.eof) {
          ended = true;
          setStatus("ended");
          if (chunk.error) setErrorMsg(chunk.error);
          unsubscribe();
          streamIdRef.current = "";
        }
      });
      const unsubscribe = () => {
        if (typeof off === "function") off();
        if (offRef.current === unsubscribe) offRef.current = null;
      };
      offRef.current = unsubscribe;
      // Known before the open returns, so an unmount mid-open still closes it.
      streamIdRef.current = streamId;

      let result: { streamId: string; error?: string };
      try {
        result = workload
          ? await LogsService.OpenWorkloadLogStream(
              cluster, pod.namespace, workload.kind, workload.name, container, tailLines, streamId,
            )
          : await LogsService.OpenLogStream(
              cluster, pod.namespace, pod.name, container, previous, tailLines, streamId,
            );
      } catch (err) {
        unsubscribe();
        if (streamIdRef.current === streamId) streamIdRef.current = "";
        if (!stale) {
          setStatus("error");
          setErrorMsg(String(err));
        }
        return;
      }

      if (stale) {
        // Unmounted while the open was in flight: the cleanup's close may have
        // run before the stream registered, so close it again now.
        if (result.streamId) {
          await LogsService.CloseLogStream(streamId).catch(() => undefined);
        }
        return;
      }

      if (result.error || !result.streamId) {
        unsubscribe();
        if (streamIdRef.current === streamId) streamIdRef.current = "";
        setStatus("error");
        setErrorMsg(result.error || "failed to open stream");
        return;
      }

      if (!ended) setStatus("streaming");
    }

    void open();

    return () => {
      stale = true;
      if (offRef.current) { offRef.current(); offRef.current = null; }
      const sid = streamIdRef.current;
      if (sid) {
        streamIdRef.current = "";
        LogsService.CloseLogStream(sid).catch(() => undefined);
      }
    };
  }, [cluster, pod.namespace, pod.name, container, previous, tailLines, workload?.kind, workload?.name]);

  // Notify the host of the active container (initial + every switch) so a parent
  // can carry the selection into a pop-out window.
  useEffect(() => {
    onContainerChange?.(container);
  }, [container]); // eslint-disable-line react-hooks/exhaustive-deps

  // -- pop-out unload: best-effort close of the live stream --
  // When hosted in a dedicated OS window, the React unmount cleanup does NOT run
  // on window close (the webview is torn down without a normal React lifecycle),
  // so wire a beforeunload to close the apiserver stream. This is best-effort:
  // if the window is killed (force-quit) the close may be missed, but the
  // per-stream concurrency cap (oldest evicted on new open) and the app-quit
  // CloseAll() drain in OnShutdown are the backstops that bound leaked streams.
  useEffect(() => {
    if (!hostedInWindow) return;
    const onUnload = () => {
      const sid = streamIdRef.current;
      if (sid) LogsService.CloseLogStream(sid).catch(() => undefined);
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, [hostedInWindow]);

  // -- Esc: collapse if expanded, stop propagation so panel/list Esc handlers don't fire --
  useEffect(() => {
    if (!expanded) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setExpanded(false);
      }
    };
    document.addEventListener("keydown", onKey, true); // capture phase to beat other handlers
    return () => document.removeEventListener("keydown", onKey, true);
  }, [expanded]);

  const clearBuffer = () => {
    bufRef.current = [];
    droppedRef.current = false;
    setSelectedLine(null);
    setCopiedLine(null);
    setVersion((v) => v + 1);
  };

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(filteredLines.map((l) => l.raw).join("\n"));
      setCopyState("copied");
      setTimeout(() => setCopyState("idle"), 1500);
    } catch {
      // clipboard not available (e.g. jsdom) — silently ignore
    }
  };

  const handleCopyLine = async (line: string, index: number) => {
    try {
      await navigator.clipboard.writeText(line);
      setSelectedLine({ index, text: line });
      setCopiedLine({ index, text: line });
      setTimeout(() => setCopiedLine(null), 1500);
    } catch {
      // clipboard not available (e.g. jsdom) — silently ignore
    }
  };

  const handleCopySelected = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      // clipboard not available (e.g. jsdom) — silently ignore
    }
  };

  const selectedDetail = selectedLine ? getLogLineDetail(selectedLine.text) : null;
  const selectedLevelFilter =
    selectedDetail?.level && selectedDetail.level !== "none"
      ? selectedDetail.level as LogLevelFilter
      : null;

  const jumpMatch = useCallback((dir: 1 | -1) => {
    if (matchLineIndices.length === 0) return;
    setFollow(false);
    setActiveMatchIdx((prev) => {
      const next = (prev + dir + matchLineIndices.length) % matchLineIndices.length;
      return next;
    });
  }, [matchLineIndices.length]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSearchKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      jumpMatch(e.shiftKey ? -1 : 1);
    }
  };

  // Reset active match index when search changes
  useEffect(() => { setActiveMatchIdx(0); }, [searchLc, levelFilters.error, levelFilters.warn, levelFilters.info, levelFilters.debug]);

  // --------------------------------------------------------------------------
  // Styles
  // --------------------------------------------------------------------------

  const chipStyle = (on: boolean): React.CSSProperties => ({
    fontSize: 10, padding: "3px 9px", borderRadius: 11, cursor: "pointer",
    border: on ? "0.5px solid var(--color-text-info)" : "0.5px solid var(--color-border-tertiary)",
    background: on ? "var(--color-background-info, transparent)" : "transparent",
    color: on ? "var(--color-text-info)" : "var(--color-text-tertiary)",
  });

  const btnStyle: React.CSSProperties = {
    fontSize: 10, padding: "3px 9px", borderRadius: 4, cursor: "pointer",
    border: "0.5px solid var(--color-border-tertiary)",
    background: "var(--color-background-primary)",
    color: "var(--color-text-secondary)",
  };

  const iconBtnStyle: React.CSSProperties = {
    ...btnStyle,
    display: "flex", alignItems: "center", justifyContent: "center",
    padding: "3px 5px",
  };

  const detailBtnStyle: React.CSSProperties = {
    ...iconBtnStyle,
    minWidth: 0,
    height: 24,
  };

  const selectStyle: React.CSSProperties = {
    fontSize: 11, padding: "3px 6px",
    background: "var(--color-background-primary)",
    color: "var(--color-text-primary)",
    border: "0.5px solid var(--color-border-tertiary)",
    borderRadius: 4,
  };

  // --------------------------------------------------------------------------
  // Controls bar (shared between normal + expanded render)
  // --------------------------------------------------------------------------

  const controlsBar = (
    <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap", flexShrink: 0 }}>
      {/* Container picker — or static text when hosted with no containers list */}
      {staticContainer ? (
        <span
          style={{ fontSize: 11, color: "var(--color-text-secondary)", fontFamily: "var(--font-mono)" }}
          aria-label="container"
        >{container || "default containers"}</span>
      ) : (
        <select
          value={container}
          onChange={(e) => setContainer(e.target.value)}
          style={selectStyle}
          aria-label="container"
        >
          {regular.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
          {init.map((c) => <option key={c.name} value={c.name}>init: {c.name}</option>)}
        </select>
      )}

      {/* Tail lines */}
      <select
        value={tailLines}
        onChange={(e) => setTailLines(Number(e.target.value))}
        style={selectStyle}
        aria-label="tail lines"
      >
        <option value={100}>100 lines</option>
        <option value={500}>500 lines</option>
        <option value={1000}>1000 lines</option>
        <option value={5000}>5000 lines</option>
      </select>

      {/* Toggles */}
      {!workload && <button style={chipStyle(previous)} onClick={() => setPrevious((p) => !p)}>previous</button>}
      <button
        style={chipStyle(follow)}
        onClick={() => {
          setFollow((f) => {
            if (!f && scrollRef.current) {
              setTimeout(() => {
                if (scrollRef.current)
                  scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
              }, 0);
            }
            return !f;
          });
        }}
      >follow</button>
      <button style={chipStyle(wrap)} onClick={() => setWrap((w) => !w)}>wrap</button>

      {/* Level filters. No active level means "all"; active chips narrow output. */}
      {LOG_LEVEL_FILTERS.map((level) => (
        <button
          key={level}
          style={chipStyle(levelFilters[level])}
          onClick={() => setLevelFilters((current) => ({ ...current, [level]: !current[level] }))}
        >
          {level}
        </button>
      ))}

      {/* Search */}
      <input
        ref={searchInputRef}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        onKeyDown={handleSearchKey}
        placeholder="search"
        style={{ ...inputWell, width: 120 }}
        aria-label="search logs"
      />
      {search && (
        <>
          <span style={{ fontSize: 10, color: matchCount > 0 ? "var(--color-text-info)" : "var(--color-text-tertiary)" }}>
            {matchLineIndices.length > 0
              ? `${clampedActive + 1}/${matchCount}`
              : `0 matches`}
          </span>
          {matchLineIndices.length > 0 && (
            <>
              <button style={btnStyle} onClick={() => jumpMatch(-1)} title="previous match (Shift+Enter)">↑</button>
              <button style={btnStyle} onClick={() => jumpMatch(1)} title="next match (Enter)">↓</button>
            </>
          )}
        </>
      )}

      <button onClick={clearBuffer} style={btnStyle}>clear</button>
      <button
        onClick={() => void handleCopy()}
        style={btnStyle}
        aria-label="copy logs"
      >
        {copyState === "copied" ? "copied" : "copy visible"}
      </button>

      {/* Expand / collapse — hidden in a pop-out window (fixed-overlay expand is
          meaningless when the pane already owns the whole window). */}
      {!hostedInWindow && (
        <button
          style={iconBtnStyle}
          onClick={() => setExpanded((v) => !v)}
          aria-label={expanded ? "collapse logs" : "expand logs"}
          title={expanded ? "collapse (Esc)" : "expand"}
        >
          {expanded
            ? <IconArrowsDiagonalMinimize size={13} stroke={1.5} />
            : <IconArrowsDiagonal size={13} stroke={1.5} />}
        </button>
      )}

      {/* Status (right-aligned) */}
      <div style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 5, fontSize: 10 }}>
        {status === "streaming" && (
          <>
            <span style={{ width: 6, height: 6, borderRadius: "50%", background: "var(--color-text-success)", display: "inline-block" }} />
            <span style={{ color: "var(--color-text-success)" }}>streaming</span>
          </>
        )}
        {status === "connecting" && <span style={{ color: "var(--color-text-tertiary)" }}>connecting…</span>}
        {status === "ended" && !errorMsg && <span style={{ color: "var(--color-text-tertiary)" }}>ended</span>}
        {status === "ended" && errorMsg && <span style={{ color: "var(--color-text-danger)" }} title={errorMsg}>ended: {errorMsg}</span>}
        {status === "error" && <span style={{ color: "var(--color-text-danger)" }} title={errorMsg}>error: {errorMsg}</span>}
      </div>
    </div>
  );

  // --------------------------------------------------------------------------
  // Log viewport (shared)
  // --------------------------------------------------------------------------

  const logViewport = (
    <div
      ref={scrollRef}
      onScroll={onScroll}
      style={{
        flex: 1, minHeight: 0,
        overflowY: "auto",
        overflowX: wrap ? "hidden" : "auto",
        background: "var(--color-background-secondary)",
        border: "0.5px solid var(--color-border-tertiary)",
        borderRadius: 4,
        padding: "6px 8px",
        fontFamily: "var(--font-mono)",
        fontSize: 11,
        lineHeight: 1.55,
        color: "var(--color-text-secondary)",
      }}
    >
      {droppedRef.current && (
        <div style={{ color: "var(--color-text-tertiary)", fontSize: 10, marginBottom: 2, userSelect: "none" }}>
          · older lines dropped (buffer capped at {LOG_BUF_CAP})
        </div>
      )}
      {truncated && (
        <div style={{ color: "var(--color-text-tertiary)", fontSize: 10, marginBottom: 2, userSelect: "none" }}>
          · showing last {DOM_RENDER_CAP} of {totalVisibleLines} visible lines
        </div>
      )}

      {renderLines.map((line, i) => {
        const isActiveMatch = searchLc
          ? matchLineIndices[clampedActive] === i
          : false;
        const isSelected = selectedLine?.index === i && selectedLine.text === line.raw;
        const isCopied = copiedLine?.index === i && copiedLine.text === line.raw;
        return (
          <LogLine
            key={i}
            ref={(el) => { rowRefs.current[i] = el; }}
            line={line.raw}
            parsed={line.parsed}
            search={searchLc}
            wrap={wrap}
            activeMatch={isActiveMatch}
            selected={isSelected}
            copied={isCopied}
            onSelect={() => setSelectedLine({ index: i, text: line.raw })}
            onCopy={() => void handleCopyLine(line.raw, i)}
          />
        );
      })}
    </div>
  );

  const selectedLineInspector = selectedDetail && (
    <div
      data-testid="log-line-detail"
      style={{
        display: "grid",
        gridTemplateColumns: "minmax(0, 1fr) auto",
        gap: 8,
        border: "0.5px solid var(--color-border-tertiary)",
        borderRadius: 4,
        background: "var(--color-background-secondary)",
        padding: "8px 10px",
        fontSize: 11,
        color: "var(--color-text-secondary)",
      }}
    >
      <div style={{ minWidth: 0, display: "flex", flexDirection: "column", gap: 6 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
          <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>selected line</span>
          <span style={selectedLevelPillStyle(selectedDetail.level)}>{selectedDetail.level}</span>
          {selectedDetail.source && (
            <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-tertiary)" }}>
              {selectedDetail.source}
            </span>
          )}
          {selectedDetail.hasAnsi && (
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>ansi stripped</span>
          )}
        </div>
        {selectedDetail.prefix && (
          <div style={detailFieldStyle}>
            <span style={detailLabelStyle}>prefix</span>
            <span style={detailMonoValueStyle}>{selectedDetail.prefix}</span>
          </div>
        )}
        <div style={detailFieldStyle}>
          <span style={detailLabelStyle}>message</span>
          <span style={detailMonoValueStyle}>{selectedDetail.message}</span>
        </div>
        {selectedDetail.hasAnsi && (
          <div style={detailFieldStyle}>
            <span style={detailLabelStyle}>stripped</span>
            <span style={detailMonoValueStyle}>{selectedDetail.stripped}</span>
          </div>
        )}
      </div>
      <div style={{ display: "flex", alignItems: "flex-start", gap: 5, flexWrap: "wrap", justifyContent: "flex-end" }}>
        <button
          type="button"
          style={detailBtnStyle}
          onClick={() => void handleCopySelected(selectedDetail.raw)}
          title="copy raw line"
          aria-label="copy raw log line"
        >
          <IconCopy size={13} stroke={1.6} />
        </button>
        <button
          type="button"
          style={detailBtnStyle}
          onClick={() => void handleCopySelected(selectedDetail.message)}
          title="copy message"
          aria-label="copy log message"
        >
          msg
        </button>
        {selectedLevelFilter && (
          <button
            type="button"
            style={detailBtnStyle}
            onClick={() => setLevelFilters({
              error: selectedLevelFilter === "error",
              warn: selectedLevelFilter === "warn",
              info: selectedLevelFilter === "info",
              debug: selectedLevelFilter === "debug",
            })}
            title={`filter ${selectedLevelFilter} logs`}
            aria-label={`filter ${selectedLevelFilter} logs`}
          >
            <IconFilter size={13} stroke={1.6} />
          </button>
        )}
        <button
          type="button"
          style={detailBtnStyle}
          onClick={() => setSelectedLine(null)}
          title="close"
          aria-label="close log line detail"
        >
          <IconX size={13} stroke={1.6} />
        </button>
      </div>
    </div>
  );

  // --------------------------------------------------------------------------
  // Expanded layout
  // --------------------------------------------------------------------------

  if (expanded) {
    return (
      <div
        data-testid="logs-pane-expanded"
        style={{
          position: "fixed",
          // Start below the 40px TopBar: the titlebar (traffic lights, drag
          // region, theme toggle) stays visible and functional while expanded -
          // covering it left the window undraggable with the lights overlapping
          // the expanded header.
          inset: "40px 0 0 0",
          zIndex: EXPAND_Z,
          background: "var(--color-background-primary)",
          padding: 16,
          display: "flex",
          flexDirection: "column",
          gap: 8,
        }}
      >
        {/* Expanded header: ns/pod · container + controls */}
        <div style={{ display: "flex", alignItems: "flex-start", gap: 10, flexShrink: 0 }}>
          <span style={{
            fontFamily: "var(--font-mono)",
            fontSize: 11,
            color: "var(--color-text-secondary)",
            whiteSpace: "nowrap",
            paddingTop: 4,
          }}>
            <span style={{ color: "var(--color-text-tertiary)" }}>{pod.namespace}</span>
            /
            <span style={{ fontWeight: 500 }}>{pod.name}</span>
            {" · "}
            <span style={{ color: "var(--color-text-tertiary)" }}>{container}</span>
          </span>
          <div style={{ flex: 1 }}>
            {controlsBar}
          </div>
        </div>
        {selectedLineInspector}
        {logViewport}
      </div>
    );
  }

  // --------------------------------------------------------------------------
  // Normal (inline) layout
  // --------------------------------------------------------------------------

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ marginBottom: 8 }}>
        {controlsBar}
      </div>
      {selectedLineInspector && <div style={{ marginBottom: 8 }}>{selectedLineInspector}</div>}
      {logViewport}
    </div>
  );
}

// --------------------------------------------------------------------------
// LogLine component
// --------------------------------------------------------------------------

const LogLine = React.forwardRef<
  HTMLDivElement,
  {
    line: string;
    /** Parse result computed once at buffer ingest — never re-parsed here. */
    parsed: ParsedLine;
    search: string;
    wrap: boolean;
    activeMatch: boolean;
    selected: boolean;
    copied: boolean;
    onSelect: () => void;
    onCopy: () => void;
  }
>(function LogLine({ line, parsed, search, wrap, activeMatch, selected, copied, onSelect, onCopy }, ref) {
  const levelColor = ((): string => {
    switch (parsed.level) {
      case "error": return "var(--color-text-danger)";
      case "warn":  return "var(--color-text-warning)";
      case "debug": return "var(--color-text-tertiary)";
      default:      return "var(--color-text-secondary)";
    }
  })();

  const rowStyle: React.CSSProperties = {
    whiteSpace: wrap ? "pre-wrap" : "pre",
    wordBreak: wrap ? "break-all" : "normal",
    color: levelColor,
    paddingTop: 2,
    paddingBottom: 2,
    paddingLeft: 2,
    paddingRight: 2,
    userSelect: "text",
    background: selected
      ? "var(--color-background-info)"
      : activeMatch ? "var(--color-background-secondary)" : undefined,
    borderRadius: selected ? 3 : undefined,
    // In no-wrap mode the row must NOT scroll itself (a per-line overflow gives
    // every long line its own macOS overlay scrollbar - dark bars across the
    // text). The row grows to its content width and the VIEWPORT provides the
    // single horizontal scrollbar; minWidth keeps short rows' match-highlight
    // background spanning the full scroll width.
    width: wrap ? undefined : "max-content",
    minWidth: wrap ? undefined : "100%",
  };

  const text = parsed.text;
  const dimLen = parsed.dimPrefixLen;

  // Split text into prefix and body, then apply search highlights within each.
  const prefix = dimLen > 0 ? text.slice(0, dimLen) : "";
  const body = dimLen > 0 ? text.slice(dimLen) : text;

  if (!search) {
    return (
      <LogLineShell ref={ref} style={rowStyle} line={line} copied={copied} onSelect={onSelect} onCopy={onCopy}>
        <>
          {prefix && <span style={{ color: "var(--color-text-tertiary)" }}>{prefix || "​"}</span>}
          {body || (!prefix ? "​" : "")}
        </>
      </LogLineShell>
    );
  }

  return (
    <LogLineShell ref={ref} style={rowStyle} line={line} copied={copied} onSelect={onSelect} onCopy={onCopy}>
      <>
        {prefix && (
          <span style={{ color: "var(--color-text-tertiary)" }}>
            {renderHighlighted(prefix, search)}
          </span>
        )}
        {renderHighlighted(body, search)}
      </>
    </LogLineShell>
  );
});

const LogLineShell = React.forwardRef<
  HTMLDivElement,
  {
    style: React.CSSProperties;
    line: string;
    copied: boolean;
    onSelect: () => void;
    onCopy: () => void;
    children: React.ReactNode;
  }
>(function LogLineShell({ style, line, copied, onSelect, onCopy, children }, ref) {
  return (
    <div
      ref={ref}
      style={{ ...style, display: "flex", alignItems: "baseline", gap: 6 }}
      onClick={onSelect}
      data-testid="log-line"
    >
      <button
        type="button"
        aria-label={`copy log line: ${line}`}
        aria-pressed={copied}
        title={copied ? "copied" : "copy line"}
        onClick={(e) => {
          e.stopPropagation();
          onCopy();
        }}
        style={lineActionBtn}
      >
        {copied ? <IconCheck size={11} stroke={1.8} /> : <IconCopy size={11} stroke={1.6} />}
      </button>
      <span style={{ minWidth: 0, flex: 1 }}>{children}</span>
    </div>
  );
});

const lineActionBtn: React.CSSProperties = {
  flex: "0 0 auto",
  width: 20,
  height: 18,
  padding: 0,
  border: "0.5px solid var(--color-border-tertiary)",
  borderRadius: 3,
  background: "var(--color-background-primary)",
  color: "var(--color-text-tertiary)",
  cursor: "pointer",
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  fontFamily: "var(--font-mono)",
  fontSize: 9,
  lineHeight: 1.3,
};

const detailFieldStyle: React.CSSProperties = {
  display: "grid",
  gridTemplateColumns: "58px minmax(0, 1fr)",
  gap: 8,
  alignItems: "baseline",
};

const detailLabelStyle: React.CSSProperties = {
  color: "var(--color-text-tertiary)",
  fontSize: 10,
};

const detailMonoValueStyle: React.CSSProperties = {
  minWidth: 0,
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  fontFamily: "var(--font-mono)",
  color: "var(--color-text-secondary)",
};

function selectedLevelPillStyle(level: LogLevel): React.CSSProperties {
  const color =
    level === "error" ? "var(--color-text-danger)" :
    level === "warn" ? "var(--color-text-warning)" :
    level === "info" ? "var(--color-text-info)" :
    level === "debug" ? "var(--color-text-tertiary)" :
    "var(--color-text-tertiary)";

  return {
    border: "0.5px solid var(--color-border-tertiary)",
    borderRadius: 999,
    padding: "1px 7px",
    fontSize: 10,
    color,
    background: "var(--color-background-primary)",
  };
}

function renderHighlighted(text: string, searchLc: string): React.ReactNode {
  if (!text) return null;
  const segments = splitHighlight(text, searchLc);
  return segments.map((seg, i) =>
    seg.match ? (
      <mark
        key={i}
        style={{ background: "var(--color-text-warning)", color: "var(--color-background-primary)", borderRadius: 2 }}
      >
        {seg.value}
      </mark>
    ) : (
      <React.Fragment key={i}>{seg.value}</React.Fragment>
    ),
  );
}
