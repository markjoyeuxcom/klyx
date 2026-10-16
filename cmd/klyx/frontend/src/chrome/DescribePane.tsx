import { createContext, useCallback, useContext, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { IconChevronDown, IconChevronRight, IconChevronUp, IconCopy, IconRefresh, IconX } from "@tabler/icons-react";
import { describeResource, type DescribeResultDTO } from "../bridge/exec";
import { copyText } from "../bridge/crd";
import { Aspect } from "./primitives/Aspect";
import { Age, absoluteTime } from "./Age";
import { useBelow } from "./useBelow";
import { useRestoreFocus } from "./useRestoreFocus";
import { trapTab } from "./modalFocus";
import { vh } from "../app/zoom";
import { parseDescribe } from "./describe/parse";
import { buildModel, timeOf, type Run, type VNode, type VSection, type VTable } from "./describe/model";

// DescribePane is the Describe sheet (The Line slice 14b): kubectl describe
// for one object, read-only, on the shared wide dialog. The Formatted view is
// a best-effort reading of kubectl's text (chrome/describe/parse.ts); the
// Text view is that text exactly, and Copy always copies it.
//
// Runs (design review 1): a run belongs to its identity - cluster, kind
// argument, namespace, name and expected UID - and a generation. Its output,
// command, time, error and UID notice change together; a reply for another
// identity or an older generation never shows. A changed identity clears the
// sheet and cancels the run in flight, closing cancels it too (the generated
// call's cancel() reaches Go and stops kubectl), and Refresh waits for the
// run in flight.

// A reply carries the identity it was run for (review 1): render shows only a
// reply for the current identity, so the first render after a new object
// never draws the old object's report under the new heading.
type Reply = { id: string; gen: number; at: number; dto: DescribeResultDTO };

const WIDE = [820] as const;

export function DescribePane({
  cluster,
  kindArg,
  namespace,
  name,
  kind,
  expectUid,
  onClose,
}: {
  cluster: string;
  // kindArg is the kubectl resource argument: "pod"/"node" for the core
  // panels, plural.group for typed instances, singular.group from a lens.
  kindArg: string;
  // namespace is "" for cluster-scoped objects (nodes, clusterroles, ...).
  namespace: string;
  name: string;
  // kind names the object in the head ("Gateway"); kindArg when absent.
  kind?: string;
  // expectUid is the inspected object's UID: a report naming another UID
  // describes a different object at this name.
  expectUid?: string;
  onClose: () => void;
}) {
  useRestoreFocus();
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const findRef = useRef<HTMLInputElement | null>(null);
  const titleId = useId();

  // ---- runs ----
  const identity = [cluster, kindArg, namespace, name, expectUid ?? ""].join("\u0000");
  const [shown, setShown] = useState<Reply | null>(null);
  const [prior, setPrior] = useState<Reply | null>(null);
  // The same two, for the reply handler: it decides from what is on screen.
  const shownRef = useRef<Reply | null>(null);
  const priorRef = useRef<Reply | null>(null);
  const show = (next: Reply | null, keep: Reply | null) => {
    shownRef.current = next;
    priorRef.current = keep;
    setShown(next);
    setPrior(keep);
  };
  const [running, setRunning] = useState(false);
  const gen = useRef(0);
  const call = useRef<{ cancel?: () => void } | null>(null);

  const run = useCallback(() => {
    call.current?.cancel?.();
    const mine = ++gen.current;
    const id = identity;
    setRunning(true);
    const p = describeResource(cluster, kindArg, namespace, name);
    call.current = p as { cancel?: () => void };
    const settle = (dto: DescribeResultDTO) => {
      if (gen.current !== mine) return; // superseded, cancelled or closed
      const reply: Reply = { id, gen: mine, at: Date.now(), dto };
      // A failed refresh with no stdout keeps the last report on screen,
      // labelled - through any number of failed refreshes; a reply with
      // output (partial or whole) is the newest evidence and replaces it.
      const prev = shownRef.current?.id === id ? shownRef.current : null;
      const kept = priorRef.current?.id === id ? priorRef.current : null;
      const last = prev && prev.dto.output !== "" ? prev : kept;
      show(reply, !dto.ok && dto.output === "" ? last : null);
      setRunning(false);
      call.current = null;
    };
    Promise.resolve(p).then(settle, (e: unknown) => {
      if (gen.current !== mine) return;
      settle({ ok: false, output: "", error: e instanceof Error ? e.message : String(e), command: "", commandError: "" });
    });
    // identity carries cluster, kindArg, namespace, name and expectUid
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity]);

  useEffect(() => {
    show(null, null);
    run();
    return () => {
      // gen is a counter, not a node: the cleanup must bump the latest value
      // eslint-disable-next-line react-hooks/exhaustive-deps
      gen.current++;
      call.current?.cancel?.();
      call.current = null;
    };
    // identity carries expectUid too: a new expected object is a new run
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity]);

  // Initial focus on the sheet, so Escape and the find key work at once.
  useEffect(() => {
    dialogRef.current?.focus();
  }, []);

  // Only this identity's replies exist for the render.
  const mine = shown?.id === identity ? shown : null;
  const kept = prior?.id === identity ? prior : null;
  const report = mine && !mine.dto.ok && mine.dto.output === "" && kept ? kept : mine;
  // A new identity is loading from its first render, before its run starts.
  const busy = running || !mine;
  const output = report?.dto.output ?? "";
  const tree = useMemo(() => parseDescribe(output), [output]);
  const model = useMemo(() => buildModel(tree), [tree]);
  // The Text view is stdout exactly (review 1): each line's own ending -
  // "\n", "\r\n" or none at the end - is drawn after its run, so the view's
  // text is byte for byte what kubectl printed.
  const textLines = useMemo(() => (output.match(/[^\n]*\n|[^\n]+$/g) ?? []).map((seg, i) => {
    const end = /\r?\n$/.exec(seg)?.[0] ?? "";
    return { run: { id: i, text: seg.slice(0, seg.length - end.length) } as Run, end };
  }), [output]);
  const textRuns = useMemo(() => textLines.map((l) => l.run), [textLines]);

  // ---- view and find ----
  const [view, setView] = useState<"formatted" | "text">("formatted");
  const [query, setQuery] = useState("");
  const [cur, setCur] = useState(0);
  const runs = view === "formatted" ? model.runs : textRuns;
  const hits = useMemo(() => findHits(runs, query), [runs, query]);
  // Re-index atomically: a new query, view or report starts at its first hit.
  useEffect(() => setCur(0), [hits]);
  const step = (d: number) => hits.list.length && setCur((c) => (c + d + hits.list.length) % hits.list.length);

  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  useEffect(() => setCollapsed(new Set()), [identity]);
  const forcedOpen = useMemo(() => {
    const open = new Set<string>();
    if (view !== "formatted" || hits.list.length === 0) return open;
    for (const s of model.sections) if (hits.list.some((h) => h.run >= s.from && h.run < s.to)) open.add(s.id);
    return open;
  }, [view, hits, model]);
  const isOpen = (id: string) => forcedOpen.has(id) || !collapsed.has(id);
  const toggle = (id: string) => setCollapsed((c) => {
    const next = new Set(c);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return next;
  });

  const scrollRef = useRef<HTMLDivElement | null>(null);
  useLayoutEffect(() => {
    const el = scrollRef.current?.querySelector<HTMLElement>('mark[data-cur="true"]');
    el?.scrollIntoView?.({ block: "center", inline: "nearest" });
  }, [cur, hits, view]);

  // ---- the index ----
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const narrow = useBelow(bodyRef, WIDE).includes("820");
  const [here, setHere] = useState<string | null>(null);
  const onScroll = () => {
    const box = scrollRef.current;
    if (!box) return;
    const top = box.getBoundingClientRect().top;
    const all = [...box.querySelectorAll<HTMLElement>("[data-section]")];
    let at: string | null = all[0]?.dataset.section ?? null;
    for (const el of all) if (el.getBoundingClientRect().top - top <= 24) at = el.dataset.section ?? null;
    // At the end, the last section is the one being read, however short.
    if (box.scrollTop + box.clientHeight >= box.scrollHeight - 2 && box.scrollTop > 0) at = [...all].reverse().find((el) => model.sections.some((s) => s.id === el.dataset.section && s.label))?.dataset.section ?? at;
    setHere(at);
  };
  useEffect(() => setHere(model.sections[0]?.id ?? null), [model]);
  const jump = (id: string) => {
    const el = scrollRef.current?.querySelector<HTMLElement>(`[data-section="${id}"]`);
    if (!el) return;
    setCollapsed((c) => {
      const next = new Set(c);
      next.delete(id);
      return next;
    });
    el.scrollIntoView?.({ block: "start" });
    setHere(id);
  };

  // ---- copy ----
  const [copied, setCopied] = useState<"report" | "command" | null>(null);
  const copy = (what: "report" | "command", text: string) => {
    void copyText(text).then(() => {
      setCopied(what);
      setTimeout(() => setCopied(null), 1200);
    });
  };

  const qualified = namespace ? `${namespace} / ${name}` : name;
  const uidNotice = expectUid && model.uid && model.uid !== expectUid ? model.uid : null;
  const command = mine?.dto.command ?? "";
  const commandError = mine?.dto.commandError ?? "";

  return (
    <div className="l-dialog-scrim describe-scrim" onClick={onClose}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className="l-dialog l-dialog-wide describe"
        style={{ height: `min(${vh(88)}, 1000px)` }}
        data-testid="describe-sheet"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          // Keys stop at the sheet, so lists behind it never act.
          e.stopPropagation();
          trapTab(e, dialogRef.current);
          if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f") {
            e.preventDefault();
            findRef.current?.focus();
            findRef.current?.select();
            return;
          }
          if (e.key === "Escape") {
            e.preventDefault();
            if (query !== "") setQuery("");
            else onClose();
          }
        }}
      >
        <header className="describe-head">
          <div className="describe-id">
            <div className="t-section-label">Describe</div>
            <h2 id={titleId} className="describe-title">
              <span className="describe-kind">{kind ?? kindArg}</span>
              <span className="t-object-title">{qualified}</span>
            </h2>
            <div className="describe-meta">
              {command ? <code className="describe-command">{command}</code> : <code className="describe-command">kubectl describe {kindArg}{namespace ? ` -n ${namespace}` : ""} {name}</code>}
              <span className="describe-when">
                {busy ? (mine ? "refreshing…" : "running…") : report ? <>ran {absoluteTime(report.at)} · <Age unixSeconds={report.at / 1000} suffix=" ago" /></> : null}
              </span>
            </div>
          </div>
          <div className="describe-actions">
            <div className="l-seg" role="group" aria-label="Describe view">
              <button className="l-chip" aria-pressed={view === "formatted"} onClick={() => setView("formatted")}>Formatted</button>
              <button className="l-chip" aria-pressed={view === "text"} onClick={() => setView("text")}>Text</button>
            </div>
            <button className="l-btn" disabled={output === ""} onClick={() => copy("report", output)} title={report?.dto.scrubbed ? "Copy the output shown (user info removed from URLs)" : "Copy kubectl's output"}>
              <IconCopy size={16} stroke={1.5} aria-hidden />{copied === "report" ? "Copied" : "Copy"}
            </button>
            <button className="l-btn" disabled={!command} onClick={() => copy("command", command)} title={command ? "Copy the command, with this cluster's kubeconfig" : commandError || "No command to copy"}>
              {copied === "command" ? "Copied" : "Copy command"}
            </button>
            <button className="l-btn" aria-label="Refresh" title="Run kubectl describe again" disabled={running} onClick={run}>
              <IconRefresh size={16} stroke={1.5} aria-hidden />
            </button>
            <button className="l-btn" aria-label="Close describe" onClick={onClose}>
              <IconX size={16} stroke={1.5} aria-hidden />
            </button>
          </div>
        </header>

        <div className="describe-find">
          <input
            ref={findRef}
            className="l-input"
            type="search"
            placeholder="Find in report"
            aria-label="Find in report"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                step(e.shiftKey ? -1 : 1);
              }
            }}
          />
          {query.trim() !== "" && (
            <span className="describe-count" aria-live="polite">
              {hits.list.length === 0 ? "No matches" : `${Math.min(cur, hits.list.length - 1) + 1} of ${hits.list.length}`}
            </span>
          )}
          <button className="l-btn l-btn-sm" aria-label="Previous match" disabled={hits.list.length === 0} onClick={() => step(-1)}><IconChevronUp size={16} stroke={1.5} aria-hidden /></button>
          <button className="l-btn l-btn-sm" aria-label="Next match" disabled={hits.list.length === 0} onClick={() => step(1)}><IconChevronDown size={16} stroke={1.5} aria-hidden /></button>
        </div>

        <div ref={bodyRef} className="describe-body" data-narrow={narrow ? "true" : undefined}>
          {mine?.dto.withheld && (
            <div role="status" className="evidence-panel" data-tone="neutral" data-testid="describe-withheld">
              <div className="evidence-panel-head"><Aspect kind="attention" size={10} />Describe withheld</div>
              <div className="evidence-panel-text">{mine.dto.withheld}</div>
            </div>
          )}
          {mine && mine.dto.error !== "" && (
            <div role="alert" className="evidence-panel describe-error" data-tone="danger">
              <div className="evidence-panel-head">
                <Aspect kind="fault" size={10} />
                {report && report !== mine ? "The refresh failed" : "kubectl describe failed"}
              </div>
              <div className="evidence-panel-text describe-pre">{mine.dto.error}</div>
            </div>
          )}
          {uidNotice && (
            <div role="status" className="evidence-panel" data-tone="warning">
              <div className="evidence-panel-head"><Aspect kind="attention" size={10} />A different object</div>
              <div className="evidence-panel-text">This describes a different object at this name (UID {uidNotice}), not the one in the inspector.</div>
            </div>
          )}
          {report?.dto.scrubbed && output !== "" && (
            <div className="describe-caption" data-testid="describe-scrubbed">
              User info removed from URLs{report.dto.linesWithheld ? `; ${report.dto.linesWithheld} ${report.dto.linesWithheld === 1 ? "line" : "lines"} withheld` : ""}. This is not kubectl's exact output.
            </div>
          )}
          {report && output !== "" && (report !== mine || !report.dto.ok) && (
            <div className="describe-caption">
              {report !== mine ? `Output from ${absoluteTime(report.at)}, before the failed refresh` : "Partial output: kubectl exited with an error"}
            </div>
          )}
          {!report && busy ? (
            <div className="describe-state">Running kubectl describe…</div>
          ) : report && output === "" && report.dto.ok ? (
            <div className="describe-state">kubectl printed nothing.</div>
          ) : output !== "" ? (
            <HitContext.Provider value={hits.byRun.size ? { byRun: hits.byRun, cur: Math.min(cur, hits.list.length - 1) } : NO_HITS}>
              {view === "formatted" ? (
                <div className="describe-split">
                  <nav className="describe-index" aria-label="Sections">
                    {model.sections.filter((s) => s.label).map((s) => (
                      <button key={s.id} className="describe-index-item" aria-current={here === s.id ? "true" : undefined} onClick={() => jump(s.id)}>
                        {s.aspect ? <Aspect kind={s.aspect} label={s.aspectLabel} /> : <span className="describe-index-gap" aria-hidden />}
                        <span className="describe-index-label">{s.label}</span>
                        {s.count !== undefined && <span className="describe-index-count">{s.count}</span>}
                      </button>
                    ))}
                  </nav>
                  <div ref={scrollRef} className="describe-scroll" onScroll={onScroll} data-testid="describe-formatted">
                    {model.sections.map((s) => <Section key={s.id} s={s} open={isOpen(s.id)} onToggle={() => toggle(s.id)} />)}
                  </div>
                </div>
              ) : (
                <div ref={scrollRef} className="describe-scroll describe-text" data-testid="describe-text">
                  <pre>{textLines.map((l) => <span key={l.run.id}><Hl run={l.run} />{l.end}</span>)}</pre>
                </div>
              )}
            </HitContext.Provider>
          ) : null}
        </div>
      </div>
    </div>
  );
}

// ---- find ----

type Hit = { run: number; start: number; end: number };
type Hits = { list: Hit[]; byRun: Map<number, { start: number; end: number; i: number }[]> };

export function findHits(runs: Run[], query: string): Hits {
  const q = query.trim().toLowerCase();
  const list: Hit[] = [];
  const byRun = new Map<number, { start: number; end: number; i: number }[]>();
  if (!q) return { list, byRun };
  for (const r of runs) {
    const t = r.text.toLowerCase();
    for (let at = t.indexOf(q); at >= 0; at = t.indexOf(q, at + q.length)) {
      const h = { run: r.id, start: at, end: at + q.length };
      const mine = byRun.get(r.id) ?? [];
      mine.push({ start: h.start, end: h.end, i: list.length });
      byRun.set(r.id, mine);
      list.push(h);
    }
  }
  return { list, byRun };
}

const NO_HITS = { byRun: new Map<number, { start: number; end: number; i: number }[]>(), cur: -1 };
const HitContext = createContext(NO_HITS);

function Hl({ run }: { run: Run }) {
  const { byRun, cur } = useContext(HitContext);
  const mine = byRun.get(run.id);
  if (!mine) return <>{run.text}</>;
  const out: ReactNode[] = [];
  let at = 0;
  for (const h of mine) {
    if (h.start > at) out.push(run.text.slice(at, h.start));
    out.push(<mark key={h.i} className="describe-hit" data-cur={h.i === cur ? "true" : undefined}>{run.text.slice(h.start, h.end)}</mark>);
    at = h.end;
  }
  if (at < run.text.length) out.push(run.text.slice(at));
  return <>{out}</>;
}

// Value: one of kubectl's values, with an age beside a time - the sheet's
// own words, outside the runs, so find never counts them.
function Value({ run }: { run: Run }) {
  const ms = run.timeMs ?? timeOf(run.text);
  return (
    <>
      <span className={run.muted ? "describe-muted" : undefined}><Hl run={run} /></span>
      {ms !== undefined && <span className="describe-age"> · <Age unixSeconds={ms / 1000} suffix=" ago" /></span>}
    </>
  );
}

// ---- the formatted report ----

function Section({ s, open, onToggle }: { s: VSection; open: boolean; onToggle: () => void }) {
  return (
    <section className="describe-section" data-section={s.id} aria-label={s.label ?? undefined}>
      {s.title && (
        <button className="describe-section-head" aria-expanded={open} onClick={onToggle}>
          {s.aspect && <Aspect kind={s.aspect} label={s.aspectLabel} />}
          <span className="t-section-label"><Hl run={s.title} /></span>
          {s.value.length > 0 && <span className="describe-section-value">{s.value.map((v) => <Value key={v.id} run={v} />)}</span>}
          <IconChevronRight className="describe-chevron" size={14} stroke={1.5} aria-hidden />
        </button>
      )}
      {open && <div className="describe-grid">{s.nodes.map((n, i) => <Node key={i} n={n} />)}</div>}
    </section>
  );
}

function Node({ n }: { n: VNode }) {
  switch (n.k) {
    case "kv":
      return (
        <>
          <div className="describe-key"><Hl run={n.key} /></div>
          <div className="describe-val">
            {n.values.map((v) => <div key={v.id}><Value run={v} /></div>)}
          </div>
          {n.children.length > 0 && <div className="describe-nest describe-grid">{n.children.map((c, i) => <Node key={i} n={c} />)}</div>}
        </>
      );
    case "item":
      return <div className="describe-item describe-grid" data-first={n.first ? "true" : undefined}>{n.children.map((c, i) => <Node key={i} n={c} />)}</div>;
    case "line":
      return <div className="describe-line"><Value run={n.run} /></div>;
    case "table":
      return <Table t={n} />;
    case "opaque":
      return <pre className="describe-opaque">{n.lines.map((r) => <span key={r.id}><Hl run={r} />{"\n"}</span>)}</pre>;
    default:
      return null;
  }
}

function Table({ t }: { t: VTable }) {
  const marked = t.rows.some((r) => r.aspect);
  return (
    <div className="describe-table-wrap">
      <table className="describe-table" data-kind={t.kind}>
        <thead>
          <tr>
            {marked && <th aria-hidden className="describe-mark-col" />}
            {t.head.map((h) => <th key={h.id} scope="col"><Hl run={h} /></th>)}
          </tr>
        </thead>
        <tbody>
          {t.rows.map((r, i) => (
            <tr key={i}>
              {marked && <td className="describe-mark-col">{r.aspect ? <Aspect kind={r.aspect} label={r.aspectLabel} /> : null}</td>}
              {r.cells.map((c, j) => (
                <td key={c.id} data-wrap={j === t.wrapCol ? "true" : undefined}>
                  <Value run={c} />
                  {j === t.extraCol && r.extra.map((x) => <div key={x.id}><Hl run={x} /></div>)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
