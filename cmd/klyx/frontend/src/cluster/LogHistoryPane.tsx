import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { IconCopy } from "@tabler/icons-react";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { closeHistoryView, readPodHistory, type HistoryMarker, type HistoryPage, type PodHistoryRequest } from "../bridge/logHistory";
import { newStreamId } from "../bridge/streams";
import { emptyHeld, withOlder, type Held, HISTORY_LINES_MAX } from "./logHistoryModel";

// Coverage S8b §7: a pod's log history from the cluster's Loki. Klyx builds
// the LogQL and says which selector it used and how strong it is; the mode,
// range and container are a page set's, so changing any starts again from the
// newest. Rows are single lines (a click opens the whole line), so every held
// line renders through the virtual list - older pages stay reachable.

export type HistoryTarget = {
  namespace: string; name: string; uid: string; createdUnix?: number; gone?: boolean;
  containers: { name: string; init?: boolean }[];
};

const ROW = 18;
const RANGES: { label: string; seconds: number }[] = [
  { label: "1 hour", seconds: 3600 }, { label: "6 hours", seconds: 6 * 3600 },
  { label: "24 hours", seconds: 24 * 3600 }, { label: "7 days", seconds: 7 * 24 * 3600 },
];
const clock = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().replace("T", " ").replace(/\.(\d{3})\d*Z$/, ".$1Z");
};

export function LogHistoryPane({ cluster, target }: { cluster: string; target: HistoryTarget }) {
  const view = useMemo(() => newStreamId("history"), []);
  const [container, setContainer] = useState("");
  const [range, setRange] = useState(target.createdUnix ? 0 : 24 * 3600); // 0: since created
  const [mode, setMode] = useState<"auto" | "name" | "uid">("auto");
  const [held, setHeld] = useState<Held>(emptyHeld);
  const [page, setPage] = useState<HistoryPage | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [showSelectors, setShowSelectors] = useState(false);
  const listRef = useRef<VirtualListHandle>(null);
  const gen = useRef(0);
  // what the held lines are of: a failed Refresh of the same target keeps
  // them (not refreshed); a different target never shows them
  const heldKey = useRef("");

  useEffect(() => () => closeHistoryView(view), [view]);

  const request = useCallback(async (restart: boolean, skip?: number[]) => {
    const g = ++gen.current;
    setBusy(true);
    const req: PodHistoryRequest = {
      cluster, view, restart, namespace: target.namespace, pod: target.name, uid: target.uid, container: container || undefined,
      createdUnix: target.createdUnix, gone: target.gone, rangeSeconds: range || undefined, mode, skip,
    };
    const key = `${cluster}|${target.uid}|${container}|${range}|${mode}`;
    try {
      const p = await readPodHistory(req);
      if (g !== gen.current) return; // a newer request owns the view
      if (restart && p.error && !p.reset && key === heldKey.current) {
        // a failed Refresh keeps the last good lines and what they were read
        // by, marked not refreshed (§7)
        setPage((old) => (old ? { ...old, error: p.error } : p));
        return;
      }
      setPage(p);
      if (p.reset) setNotice("The Loki source changed; reading the newest again.");
      else if (restart) setNotice(null);
      if (restart || p.reset) {
        heldKey.current = key;
        setHeld(withOlder(emptyHeld(), p.lines));
        setOpen(null);
        requestAnimationFrame(() => listRef.current?.scrollToIndex(Math.max(0, p.lines.length - 1)));
      } else if (!p.error) {
        setHeld((h) => withOlder(h, p.lines));
        setOpen(null);
        // the older page goes before what was held: the anchor, the first line
        // held before, is now at the page's length
        requestAnimationFrame(() => listRef.current?.scrollToIndex(Math.min(p.lines.length, HISTORY_LINES_MAX - 1)));
      }
    } catch (e) {
      if (g === gen.current) setPage((p) => ({ ...(p ?? emptyPage(view)), error: e instanceof Error ? e.message : String(e) }));
    } finally {
      if (g === gen.current) setBusy(false);
    }
  }, [cluster, view, target.namespace, target.name, target.uid, target.createdUnix, target.gone, container, range, mode]);

  // a new page set whenever what request reads changes: the cluster, the
  // pod, its container, the range or the mode
  useEffect(() => { void request(true); }, [request]);

  const copy = () => { if (page?.copyable) void navigator.clipboard?.writeText(page.selectors.join("\n")); };
  const containers = target.containers.filter((c) => !c.init).concat(target.containers.filter((c) => c.init));
  const multi = new Set(held.lines.map((l) => l.container ?? "")).size > 1;

  return (
    <div data-testid="log-history" style={{ display: "flex", flexDirection: "column", gap: 6, minHeight: 0, flex: 1 }}>
      <div className="t-control" style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap", flexShrink: 0 }}>
        <select className="l-input" aria-label="container" value={container} onChange={(e) => setContainer(e.target.value)}>
          <option value="">all containers</option>
          {containers.map((c) => <option key={c.name} value={c.name}>{c.init ? `init: ${c.name}` : c.name}</option>)}
        </select>
        <select className="l-input" aria-label="range" value={range} onChange={(e) => setRange(Number(e.target.value))}>
          {target.createdUnix ? <option value={0}>since this pod was created</option> : null}
          {RANGES.map((r) => <option key={r.seconds} value={r.seconds}>{r.label}</option>)}
        </select>
        {page?.mode === "uid" && <button className="l-btn" onClick={() => setMode("name")} data-testid="history-by-name">Select by name instead</button>}
        {page?.mode === "name" && page.uidOffer && <button className="l-btn" onClick={() => setMode("uid")} data-testid="history-by-uid">Select by UID</button>}
        <button className="l-btn" onClick={() => void request(true)} disabled={busy}>Refresh</button>
        <button className="l-btn" onClick={copy} disabled={!page?.copyable} aria-label="Copy LogQL"
          title={page && !page.copyable ? "The query names a value equal to the endpoint's credential; Klyx does not copy it." : "Copy the LogQL Klyx sent"}>
          <IconCopy size={13} stroke={1.5} /> Copy LogQL
        </button>
        <span className="t-secondary" style={{ marginLeft: "auto" }}>{busy ? "reading…" : page && !page.error ? "history from Loki" : ""}</span>
      </div>
      {page && (page.selectors.length > 0 || page.strength) && (
        <div className="t-secondary" data-testid="history-selector" style={{ flexShrink: 0, overflowWrap: "anywhere" }}>
          <code style={{ fontSize: 11 }}>{page.selectors[0]}</code>
          {page.selectors.length > 1 && (
            <button className="l-chip" style={{ marginLeft: 6 }} onClick={() => setShowSelectors((s) => !s)}>{page.selectors.length} selectors</button>
          )}
          {showSelectors && page.selectors.slice(1).map((s) => <div key={s}><code style={{ fontSize: 11 }}>{s}</code></div>)}
          <div data-testid="history-strength">{page.strength}{page.source ? ` · ${page.source}` : ""}{page.tenant ? ` · tenant ${page.tenant}` : ""}</div>
        </div>
      )}
      {notice && <div role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{notice}</div>}
      {page?.error && <div role="alert" data-testid="history-error" style={{ color: "var(--color-text-danger)" }}>▲ {page.error}{held.lines.length ? " - the lines below are the last read, not refreshed" : ""}</div>}
      {page?.noStreams && <div role="status" data-testid="history-nostreams" className="t-secondary">{page.noStreams}. Nothing is claimed about this pod's lines.</div>}
      {held.newerDropped > 0 && (
        <div role="status" className="t-secondary" data-testid="history-cap">
          Newer lines dropped ({held.lines.length.toLocaleString()} held); Refresh returns to the newest.
        </div>
      )}
      {(page?.markers ?? []).map((m) => <MarkerLine key={`${m.branch}-${m.ts}`} m={m} onSkip={() => void request(false, [m.branch])} />)}
      <div style={{ flexShrink: 0, display: "flex", gap: 8, alignItems: "center" }}>
        {page && !page.error && !page.noStreams && (page.done
          ? <span className="t-secondary" data-testid="history-start">Start of the range reached ({clock(page.start)}).</span>
          : page.more !== false
            ? <button className="l-btn" disabled={busy} onClick={() => void request(false)} data-testid="history-older">Load older</button>
            : null /* every branch left is stopped at a marker: Skip past is the way on */)}
      </div>
      <VirtualList ref={listRef} items={held.lines} rowHeight={ROW} style={{ flex: 1, minHeight: 0, fontFamily: "var(--font-mono)", fontSize: 12 }}
        rowKey={(l, i) => `${l.ts}-${l.branch}-${i}`}
        render={(l, i) => (
          <div key={i} role="button" tabIndex={0} data-testid="history-line" onClick={() => setOpen(open === i ? null : i)}
            style={{ height: ROW, lineHeight: `${ROW}px`, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", cursor: "pointer" }}>
            <span style={{ color: "var(--color-text-tertiary)" }}>{clock(l.ts)} </span>
            {multi && l.container ? <span style={{ color: "var(--color-text-secondary)" }}>{l.container} › </span> : null}
            {l.line}{l.clipped ? <span className="t-secondary"> … (clipped at 64 KiB)</span> : null}
          </div>
        )} />
      {open !== null && held.lines[open] && (
        <div data-testid="history-line-detail" style={{ flexShrink: 0, maxHeight: "40%", overflow: "auto", borderTop: "0.5px solid var(--color-border-tertiary)", paddingTop: 6 }}>
          <div className="t-secondary">{clock(held.lines[open].ts)}{held.lines[open].container ? ` · ${held.lines[open].container}` : ""}
            <button className="l-btn" style={{ marginLeft: 8 }} onClick={() => void navigator.clipboard?.writeText(held.lines[open].line)}>Copy line</button>
          </div>
          <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", margin: 0 }}>{held.lines[open].line}</pre>
        </div>
      )}
    </div>
  );
}

function MarkerLine({ m, onSkip }: { m: HistoryMarker; onSkip: () => void }) {
  return (
    <div role="status" data-testid="history-marker" className="t-secondary" style={{ color: "var(--color-text-warning)", flexShrink: 0 }}>
      At least {m.count.toLocaleString()} lines share the timestamp {clock(m.ts)} on selector {m.branch + 1}; Klyx cannot establish whether more remain.
      {m.refused ? ` Loki refused a larger read: ${m.refused}.` : ""}
      <button className="l-btn" style={{ marginLeft: 8 }} onClick={onSkip}>Skip past {clock(m.ts)}</button>
      <span> (the lines at that time not shown are left out)</span>
    </div>
  );
}

const emptyPage = (view: string): HistoryPage => ({ view, lines: [], selectors: [], copyable: false, mode: "", uidOffer: false, strength: "", source: "", start: "", end: "", done: false });
