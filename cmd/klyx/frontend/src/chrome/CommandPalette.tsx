import { useState, useEffect, useRef, useMemo, useCallback } from "react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { buildCommands, Command } from "./commands";
import { fuzzyMatch } from "./fuzzy";
import { loadBoosts, recordUse } from "./frecency";
import { vh, vw } from "../app/zoom";
import { trapTab } from "./modalFocus";
import { Aspect } from "./primitives/Aspect";
import { LinePos } from "./primitives/LinePos";
import { useFleetAxes } from "./FleetIndicator";
import { AXIS_TITLE, FLEET_AXES } from "../nav/fleetAxes";
import { entryKey, entryLabel, go, recentPlaces } from "../nav/history";
import { INSPECTOR_IDENTITY_ID, useInspectorPin } from "../nav/inspectorPin";
import { useLine } from "../store/line";
import { clusterCodes } from "../line/codes";
import { linePosOf, type LinePosModel } from "../line/linePos";
import type { IndexHit } from "../bridge/fleetIndex";
import { useIndexSearch } from "./console/useIndexSearch";
import { openHit } from "./console/openHit";
import { CONSOLE_GROUPS, consoleGroup, coverageLine, coverageNotes, coveredByIndex, hitAspect, hitNote, matchesPin, type ConsoleGroup } from "./console/consoleModel";

// CommandPalette is the ⌘K command console (design principle #6; The Line
// parent §6, slice 9 note §4.1). It mounts once in AppShell and installs a
// single window keydown handler:
//   ⌘K / Ctrl+K  toggle
//   Esc          close
// Its groups are Objects (the fleet metadata index, every connected
// cluster), Run (verbs), Go to (places, clusters, views, resources) and Ask
// (the chat). On open it reads buildCommands(useFleet.getState()) fresh for
// the commands; objects come from the index, by search, with its coverage.

// Module-level flag so useListKeys (and any other consumer) can read palette open
// state without prop-drilling or store overhead. Written by the component on
// open/close via setPaletteOpen; read via getPaletteOpen.
let _paletteOpen = false;
export const getPaletteOpen = () => _paletteOpen;
/** Exported for tests only — do not call from application code. */
export const _setPaletteOpenForTest = (v: boolean) => { _paletteOpen = v; };
const OPEN_EVENT = "klyx:open-command-palette";
export const openCommandPalette = () => window.dispatchEvent(new Event(OPEN_EVENT));

type Ranked = { cmd: Command; positions: number[] };

const eligibilityKey = (cs: ClusterDTO[]) => cs.map((c) => [c.name, c.state, c.protected, c.fluxPresent, c.argoPresent, c.certManagerPresent,
  c.externalSecretsPresent, c.gatewayAPIVersion, c.karpenterPresent, c.hasHubble, c.ciliumPresent].join("|")).join("\n");

const LIMIT = 50;

// DOM ids for the listbox options, referenced by aria-activedescendant.
const optionId = (id: string) => `command-option-${id}`;

// Returns true when the event originated in a text input we should not hijack
// — unless it's the palette's own search box (data-palette-input).
function inEditable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  if (el.getAttribute?.("data-palette-input") === "true") return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable === true;
}

// Rank commands against the query. Title is full-weight; hint contributes at
// half weight, capped, so a raw resource view's long hint ("raw resource view
// · PodDisruptionBudget · policy/v1") never outweighs a primary view's title.
// A title that starts with the query - or a word in it, or a view alias such
// as its group name ("helm" for Helm's Releases) - ranks first, and a primary
// view outranks a raw resource view on an equal match (Codex UI review
// 2026-10-13: "po" listed twelve raw kinds before Pods). Frecency
// (loadBoosts) reorders the empty-query list toward recently used commands
// and tiebreaks fuzzy matches at 0.75 weight - enough to float a habit, never
// enough to outrank a clearly better match. `total` counts every match so the
// footer can be honest about truncation.
export type RankResult = { rows: Ranked[]; total: number };

const HINT_CAP = 4;
const PREFIX = 30;
const WORD_PREFIX = 12;
const GROUP_BONUS: Partial<Record<Command["group"], number>> = { Views: 8, Application: 4, Clusters: 4 };

// wordStarts: whether a word after the first in text starts with q.
function wordStarts(text: string, q: string): boolean {
  return text.split(/[\s/·:._-]+/).slice(1).some((w) => w.startsWith(q));
}

export function rank(commands: Command[], query: string, boost: (id: string) => number = () => 0): RankResult {
  if (query.trim() === "") {
    const plain = commands.filter((c) => !c.qualifier);
    const sorted = [...plain].sort((a, b) => boost(b.id) - boost(a.id)); // stable: untouched keep source order
    return { rows: sorted.slice(0, LIMIT).map((cmd) => ({ cmd, positions: [] })), total: plain.length };
  }
  const q = query.trim();
  // A cluster-qualified result appears only when the query names its cluster.
  const tokens = q.toLowerCase().split(/\s+/).filter((t) => t.length >= 2);
  const scored: { cmd: Command; positions: number[]; score: number }[] = [];
  for (const cmd of commands) {
    if (cmd.qualifier && !tokens.some((t) => cmd.qualifier!.toLowerCase().includes(t))) continue;
    // A title match must hang together: from four characters on, letters
    // scattered across a long title ("events" in "Service Accounts") are
    // noise. Hints are long prose, so they match only as a substring.
    let t = fuzzyMatch(q, cmd.title);
    if (t && q.length >= 4 && t.score < q.length * 1.5) t = null;
    const lq = q.toLowerCase();
    const h = cmd.hint && cmd.hint.toLowerCase().includes(lq) ? fuzzyMatch(q, cmd.hint)
      : cmd.keywords?.some((k) => k.toLowerCase().includes(lq)) ? { score: 4, positions: [] } : null;
    const title = cmd.title.toLowerCase();
    let prefix = title.startsWith(lq) ? PREFIX : wordStarts(title, lq) ? WORD_PREFIX : 0;
    // An alias that starts with the query stands in for the title.
    let titleScore = t?.score ?? 0;
    if (prefix === 0) {
      const alias = cmd.keywords?.find((k) => k.toLowerCase().startsWith(lq));
      if (alias) {
        prefix = PREFIX;
        titleScore = Math.max(titleScore, fuzzyMatch(q, alias)?.score ?? 0);
      }
    }
    if (!t && !h && prefix === 0) continue;
    // Typing a result's whole title picks it: an exact match outranks any
    // frecency habit ("theme: bench" over a recently used "theme: bench light").
    const exact = title === lq ? 1000 : 0;
    const hint = h ? Math.min(h.score * 0.5, HINT_CAP) : 0;
    const primary = (GROUP_BONUS[cmd.group] ?? 0) - (cmd.disabled ? 10 : 0);
    const brevity = -Math.min(cmd.title.length, 60) * 0.02;
    const score = exact + titleScore + prefix + hint + primary + brevity + boost(cmd.id) * 0.75;
    scored.push({ cmd, positions: t?.positions ?? [], score });
  }
  scored.sort((a, b) => b.score - a.score);
  return { rows: scored.slice(0, LIMIT).map(({ cmd, positions }) => ({ cmd, positions })), total: scored.length };
}

// inDisplayOrder groups ranked rows by their group, groups in the order of
// their best result, rows in rank order within a group. The first row is
// still the top-ranked result.
export function inDisplayOrder(rows: Ranked[]): Ranked[] {
  const order: string[] = [];
  const by = new Map<string, Ranked[]>();
  for (const r of rows) {
    if (!by.has(r.cmd.group)) {
      by.set(r.cmd.group, []);
      order.push(r.cmd.group);
    }
    by.get(r.cmd.group)!.push(r);
  }
  return order.flatMap((g) => by.get(g)!);
}

// Splits a title into <mark>-highlighted runs at the matched positions.
function Highlighted({ text, positions }: { text: string; positions: number[] }) {
  if (positions.length === 0) return <>{text}</>;
  const set = new Set(positions);
  const out: React.ReactNode[] = [];
  for (let i = 0; i < text.length; i++) {
    if (set.has(i)) {
      out.push(
        <mark key={i} style={{ background: "transparent", color: "var(--color-text-info)", fontWeight: 600 }}>
          {text[i]}
        </mark>,
      );
    } else {
      out.push(<span key={i}>{text[i]}</span>);
    }
  }
  return <>{out}</>;
}

// A row of the console: a command, or an object from the fleet index.
type Row =
  | { type: "cmd"; key: string; section: string; cmd: Command; positions: number[] }
  | { type: "hit"; key: string; section: string; hit: IndexHit };

const rowKey = (r: Row) => r.key;
const rowId = (r: Row) => optionId(r.key);

// Sections of the empty query (parent §6): recent places, the fleet
// indicator's affected clusters, the verbs for the current selection.
const RECENT_MAX = 6;
const AFFECTED_MAX = 8;

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  // Focus returns to whatever had it before ⌘K - a keyboard flow keeps its
  // place - unless an object was opened beside: then to its inspector.
  const prevFocusRef = useRef<HTMLElement | null>(null);
  const besideRef = useRef(false);

  const close = useCallback(() => {
    _paletteOpen = false;
    setOpen(false);
    const identity = besideRef.current ? document.getElementById(INSPECTOR_IDENTITY_ID) : null;
    besideRef.current = false;
    (identity ?? prevFocusRef.current)?.focus?.();
    prevFocusRef.current = null;
  }, []);

  const openPalette = useCallback(() => {
    _paletteOpen = true;
    prevFocusRef.current = document.activeElement as HTMLElement | null;
    besideRef.current = false;
    setOpen(true);
  }, []);

  // Global hotkeys: ⌘K / Ctrl+K toggle, Esc close.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const isToggle = (e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K");
      if (isToggle) {
        if (inEditable(e.target) === false || open) {
          e.preventDefault();
          if (open) close();
          else openPalette();
        }
        return;
      }
      if (e.key === "Escape" && open) {
        e.preventDefault();
        close();
      }
    };
    const onOpen = () => openPalette();
    window.addEventListener("keydown", onKey);
    window.addEventListener(OPEN_EVENT, onOpen);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener(OPEN_EVENT, onOpen);
    };
  }, [open, close, openPalette]);

  // The console mounts per open: each open is a fresh session, its state and
  // its search fence its own (slice 9 note §4.1).
  return open ? <Console close={close} besideRef={besideRef} /> : null;
}

function Console({ close, besideRef }: { close: () => void; besideRef: React.MutableRefObject<boolean> }) {
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const [verbsOnly, setVerbsOnly] = useState(false);
  // An object opened beside: the console moves aside so its inspector, on
  // the right, stays readable while the list is kept.
  const [aside, setAside] = useState(false);
  // On open it reads buildCommands fresh - no stale index.
  const [commands, setCommands] = useState<Command[]>(() => buildCommands(useFleet.getState()));
  // A second step (Choose cluster): its options replace the results; the
  // first step's query comes back with Back.
  const [step, setStep] = useState<{ title: string; options: Command[]; parentQuery: string; parentId: string } | null>(null);
  // Why the last activation did nothing (the target became unavailable).
  const [blocked, setBlocked] = useState<{ id: string; why: string } | null>(null);
  // Frecency boosts snapshot, taken once per open - rank() runs per keystroke
  // and must not re-read localStorage each time.
  const [boost] = useState<(id: string) => number>(() => loadBoosts());
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const search = useIndexSearch(1, step ? "" : query);
  const pin = useInspectorPin((s) => s.held?.pin ?? null);
  const axes = useFleetAxes();
  const clusters = useFleet((s) => s.clusters);
  const line = useLine((s) => s.dto);
  const codes = useMemo(() => clusterCodes(clusters.map((c) => c.name)), [clusters]);
  const faults = useMemo(() => new Set(axes.axes.fault.map((e) => e.cluster.name)), [axes]);
  // The index follows the fleet while open: membership, connection and
  // capability changes rebuild it (and a Choose cluster step's options).
  useEffect(() => {
    return useFleet.subscribe((st, prev) => {
      // Fleet pushes arrive every few seconds; only what decides a result's
      // eligibility (membership, connection, capabilities, protection) or
      // the route rebuilds the index, so rows do not churn under the cursor.
      if (st.route === prev.route && eligibilityKey(st.clusters) === eligibilityKey(prev.clusters)) return;
      const next = buildCommands(st);
      setCommands(next);
      setStep((cur) => {
        if (!cur) return cur;
        // The step keeps the parent's current eligible clusters - one left is
        // still a choice (never auto-navigated), none says so.
        const parent = next.find((c) => c.id === cur.parentId);
        return { ...cur, options: parent?.next?.options ?? parent?.choices ?? [] };
      });
    });
  }, []);

  // Focus the input when the console opens.
  useEffect(() => { inputRef.current?.focus(); }, []);

  const visible = useMemo(() => commands.filter((c) => !coveredByIndex(c)), [commands]);
  const ranked = useMemo(() => rank(step ? step.options : visible, query, step ? () => 0 : boost), [visible, step, query, boost]);

  // The rows, in the order shown: the arrow keys, Enter and the scroll follow
  // exactly this order.
  const rows: Row[] = useMemo(() => {
    const cmdRow = (section: string, cmd: Command, positions: number[] = []): Row => ({ type: "cmd", key: `${section}:${cmd.id}`, section, cmd, positions });
    if (step) return ranked.rows.map((r) => cmdRow("Clusters", r.cmd, r.positions));
    if (query.trim() === "") {
      const out: Row[] = [];
      if (!verbsOnly) {
        for (const e of recentPlaces().slice(0, RECENT_MAX)) {
          const { cluster, place } = entryLabel(e);
          out.push(cmdRow("Recent places", { id: `recent:${entryKey(e)}`, group: "Application", title: place, hint: cluster ?? undefined, run: () => go(e) }));
        }
        const seen = new Set<string>();
        for (const axis of FLEET_AXES) {
          for (const entry of axes.axes[axis]) {
            const name = entry.cluster.name;
            if (seen.has(name) || seen.size >= AFFECTED_MAX) continue;
            seen.add(name);
            out.push(cmdRow("Affected clusters", { id: `affected:${name}`, group: "Clusters", title: name, hint: [AXIS_TITLE[axis], entry.reason].filter(Boolean).join(" · "), run: () => useFleet.getState().openCluster(name) }));
          }
        }
      }
      const verbs = visible.filter((c) => consoleGroup(c) === "Run" && matchesPin(c.target, pin));
      const heading = pin ? `Run · ${pin.kind} ${pin.namespace ? `${pin.namespace}/` : ""}${pin.name}` : "Run";
      for (const c of verbs) out.push(cmdRow(heading, c));
      return out;
    }
    const by = new Map<ConsoleGroup, Row[]>(CONSOLE_GROUPS.map((g) => [g, []]));
    for (const hit of search.data?.hits ?? []) by.get("Objects")!.push({ type: "hit", key: `hit:${hit.cluster}/${hit.uid}`, section: "Objects", hit });
    for (const r of inDisplayOrder(ranked.rows)) {
      const g = consoleGroup(r.cmd);
      by.get(g)!.push(cmdRow(g, r.cmd, r.positions));
    }
    if (verbsOnly) return by.get("Run")!;
    // A command whose title the query starts leads (typing "pods" means the
    // Pods view); otherwise objects first, then Run, Go to and Ask.
    const top = ranked.rows[0]?.cmd;
    const lead = top && top.title.toLowerCase().startsWith(query.trim().toLowerCase()) ? consoleGroup(top) : "Objects";
    return [lead, ...CONSOLE_GROUPS.filter((g) => g !== lead)].flatMap((g) => by.get(g)!);
  }, [step, ranked, query, verbsOnly, axes, visible, pin, search.data]);

  const hitsHidden = query.trim() !== "" && !step && !verbsOnly && search.data ? Math.max(0, search.data.total - search.data.hits.length) : 0;
  // the empty query lists its own sections, never the ranked commands
  const cmdsHidden = query.trim() === "" && !step ? 0 : ranked.total - ranked.rows.length;

  // Keep selection in range as the result set shrinks/grows.
  useEffect(() => {
    if (sel >= rows.length) setSel(rows.length === 0 ? 0 : rows.length - 1);
  }, [rows.length, sel]);

  // Keep the selected row visible as arrow keys move it past the list's fold.
  useEffect(() => {
    const el = listRef.current?.querySelector('[data-selected="true"]');
    // guard: jsdom does not implement scrollIntoView
    if (el && typeof (el as HTMLElement).scrollIntoView === "function") {
      (el as HTMLElement).scrollIntoView({ block: "nearest" });
    }
  }, [sel, rows]);

  const run = useCallback(
    (i: number, beside = false) => {
      const r = rows[i];
      if (!r) return;
      if (r.type === "hit") {
        // ↵ opens the object in its lens, pinned to its cluster and UID;
        // ⌘↵ opens it beside: the console stays open over the list.
        openHit(r.hit);
        if (beside) {
          besideRef.current = true;
          setAside(true);
          requestAnimationFrame(() => inputRef.current?.focus());
          return;
        }
        close();
        return;
      }
      const cmd = r.cmd;
      if (cmd.disabled) {
        // A disabled view runs nothing; its Connect action is explicit.
        if (cmd.action) { close(); cmd.action.run(); }
        return;
      }
      if (cmd.next) {
        setStep({ title: cmd.next.title, options: cmd.next.options, parentQuery: query, parentId: cmd.id });
        setQuery("");
        setSel(0);
        setBlocked(null);
        inputRef.current?.focus();
        return;
      }
      const why = cmd.guard?.();
      if (why) { setBlocked({ id: cmd.id, why }); return; }
      // Frecency records the destination that completed, not the step.
      recordUse(cmd.id);
      if (step) recordUse(step.parentId);
      close();
      cmd.run();
    },
    [rows, close, query, step, besideRef],
  );
  const backToResults = () => {
    if (!step) return;
    setQuery(step.parentQuery);
    setStep(null);
    setSel(0);
    inputRef.current?.focus();
  };
  const toggleVerbs = () => { setVerbsOnly((v) => !v); setSel(0); };

  // Within-list keyboard nav: Arrow up/down wrap, Enter runs, ⌘↵ opens beside.
  const onInputKey = (e: React.KeyboardEvent) => {
    if (e.altKey && (e.code === "KeyV" || e.key.toLowerCase() === "v")) {
      e.preventDefault();
      toggleVerbs();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => (rows.length === 0 ? 0 : (s + 1) % rows.length));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => (rows.length === 0 ? 0 : (s - 1 + rows.length) % rows.length));
    } else if (e.key === "Enter") {
      e.preventDefault();
      run(sel, e.metaKey || e.ctrlKey);
    } else if (e.key === "Backspace" && step && query === "") {
      // Back from Choose cluster to the results, query intact.
      e.preventDefault();
      backToResults();
    }
  };

  // rows are already in sections: each section's rows are contiguous.
  const sections: { section: string; rows: { r: Row; idx: number }[] }[] = [];
  rows.forEach((r, idx) => {
    const last = sections[sections.length - 1];
    if (last && last.section === r.section) last.rows.push({ r, idx });
    else sections.push({ section: r.section, rows: [{ r, idx }] });
  });
  const notes = search.data ? coverageNotes(search.data.coverage) : [];
  const noResults = step ? "No eligible cluster remains for this view." : query.trim() === "" ? (verbsOnly ? "No verbs for the current selection." : "Nothing recent, and nothing in the fleet needs you.") : "No matches.";

  return (
    <div
      data-testid="command-palette"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) close();
      }}
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 1000,
        background: aside ? "rgba(0,0,0,0.12)" : "rgba(0,0,0,0.3)",
        display: "flex",
        justifyContent: aside ? "flex-start" : "center",
        alignItems: "flex-start",
        paddingTop: 88,
        paddingLeft: aside ? 24 : 0,
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="Command console"
        onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={(e) => trapTab(e, panelRef.current)}
        style={{
          width: 760,
          maxWidth: vw(92),
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: 11,
          boxShadow: "0 16px 48px rgba(0,0,0,0.4)",
          overflow: "hidden",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 8, margin: 12 }}>
          {/* The input sits IN a well: data surfaces are inset. */}
          <div style={{ flex: 1, minWidth: 0, borderRadius: "var(--radius-sm)", background: "var(--bench-well-bg, transparent)", boxShadow: "var(--bench-well-shadow, none)", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <input
              ref={inputRef}
              data-palette-input="true"
              data-testid="command-palette-input"
              role="combobox"
              aria-expanded={true}
              aria-controls="command-palette-list"
              aria-activedescendant={rows[sel] ? rowId(rows[sel]) : undefined}
              aria-autocomplete="list"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setSel(0);
                setBlocked(null);
              }}
              onKeyDown={onInputKey}
              placeholder={step ? "filter clusters…" : verbsOnly ? "verbs: drain, sync, restart…" : "an object, a place or a verb, fleet-wide…"}
              spellCheck={false}
              autoComplete="off"
              style={{
                border: "none",
                outline: "none",
                background: "transparent",
                color: "var(--color-text-primary)",
                fontFamily: "var(--font-mono)",
                fontSize: 14,
                padding: "11px 14px",
                width: "100%",
                boxSizing: "border-box",
              }}
            />
          </div>
          <button type="button" className="l-chip" aria-pressed={verbsOnly} data-testid="verbs-only" title="Show only verbs (alt+V)"
            onMouseDown={(e) => e.preventDefault()} onClick={() => { toggleVerbs(); inputRef.current?.focus(); }}>
            Verbs only <kbd style={{ fontSize: 10, opacity: 0.7 }}>alt+V</kbd>
          </button>
        </div>

        {step && (
          <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "0 16px 4px" }}>
            <button type="button" className="l-btn" onMouseDown={(e) => e.preventDefault()} onClick={backToResults} title="Back to results (⌫ on an empty query)">‹ Results</button>
            <span className="t-section-label">{step.title}</span>
          </div>
        )}
        <div ref={listRef} id="command-palette-list" role="listbox" style={{ maxHeight: vh(55), overflowY: "auto", padding: "4px 0" }}>
          {rows.length === 0 ? (
            <div style={{ padding: "16px", fontSize: 13, color: "var(--color-text-tertiary)" }}>{noResults}</div>
          ) : (
            sections.map((g) => (
              <div key={g.section} role="group" aria-label={g.section}>
                <div className="t-legend" style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.6, color: "var(--color-text-tertiary)", padding: "8px 16px 4px" }}>
                  {g.section}
                </div>
                {g.rows.map(({ r, idx }) => (
                  <ConsoleRow key={rowKey(r)} r={r} selected={idx === sel} onHover={() => setSel(idx)} onRun={(beside) => run(idx, beside)}
                    pos={r.type === "hit" ? linePosOf(line, r.hit.cluster, codes, faults.has(r.hit.cluster)) : null} code={r.type === "hit" ? codes[r.hit.cluster] ?? r.hit.cluster : ""} />
                ))}
              </div>
            ))
          )}
        </div>

        <div
          style={{
            fontSize: 10,
            color: "var(--color-text-tertiary)",
            padding: "7px 16px",
            borderTop: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-secondary)",
            display: "grid",
            gap: 3,
          }}
        >
          <div data-testid="console-coverage">
            {search.data
              ? <>{coverageLine(search.data.searchedClusters, search.data.searchedKinds)}{notes.length > 0 && <span title={notes.join("\n")}> · {notes.slice(0, 2).join(" · ")}{notes.length > 2 ? ` · +${notes.length - 2} more` : ""}</span>}</>
              : search.error ? `The fleet index could not be searched: ${search.error}` : "Reading the fleet index…"}
          </div>
          <div>
            {blocked && <span role="status" style={{ color: "var(--color-text-warning)" }}>Unavailable: {blocked.why} · </span>}
            {hitsHidden > 0 ? `+${hitsHidden} more objects - keep typing · ` : ""}
            {cmdsHidden > 0 ? `+${cmdsHidden} more - keep typing · ` : ""}
            ↑↓ navigate · ↵ open · ⌘↵ open beside · alt+V verbs only · {step ? "⌫ back · " : ""}esc close
          </div>
        </div>
      </div>
    </div>
  );
}

function ConsoleRow({ r, selected, onHover, onRun, pos, code }: { r: Row; selected: boolean; onHover: () => void; onRun: (beside: boolean) => void; pos: LinePosModel | null; code: string }) {
  const base = {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "7px 16px",
    cursor: "pointer",
    fontSize: 13,
    background: selected ? "var(--bench-selection-bg, var(--color-background-secondary))" : "transparent",
    boxShadow: selected ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
  } as const;
  const common = {
    id: rowId(r),
    role: "option",
    "aria-selected": selected,
    "data-selected": selected ? "true" : "false",
    onMouseMove: onHover,
    onMouseDown: (e: React.MouseEvent) => { e.preventDefault(); onRun(e.metaKey || e.ctrlKey); },
  };
  if (r.type === "hit") {
    const h = r.hit;
    return (
      <div {...common} data-testid={`console-hit-${h.cluster}-${h.uid}`} style={{ ...base, color: "var(--color-text-primary)" }}>
        <Aspect kind={hitAspect(h)} label={h.role.state === "current" ? h.role.label || h.role.role : "health unverified"} />
        <span className="t-legend" style={{ flex: "0 0 auto", minWidth: 72, color: "var(--color-text-secondary)" }}>{h.kind}</span>
        <span className="t-identifier" style={{ flex: "1 1 200px", minWidth: 120, overflowWrap: "anywhere" }}>{h.name}</span>
        <span style={{ flex: "0 0 auto" }}>{pos ? <LinePos pos={pos} /> : <span className="t-identifier" title={h.cluster}>{code}</span>}</span>
        <span data-hit-note style={{ fontSize: 11, color: "var(--color-text-tertiary)", flex: "0 1 auto", minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 240 }}>{hitNote(h)}</span>
      </div>
    );
  }
  const cmd = r.cmd;
  return (
    <div {...common} aria-disabled={cmd.disabled ? true : undefined} data-testid={`command-row-${cmd.id}`}
      style={{ ...base, color: cmd.disabled ? "var(--color-text-tertiary)" : "var(--color-text-primary)" }}>
      {/* Labels keep 200px before hints and wrap to two lines; a name is
          never cut to a lone initial. */}
      <span style={{ flex: "1 1 200px", minWidth: 200, overflowWrap: "anywhere", display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical", overflow: "hidden" }}>
        <Highlighted text={cmd.title} positions={r.positions} />
      </span>
      {cmd.disabled ? (
        <span style={{ fontSize: 11, color: "var(--color-text-tertiary)", flex: "0 1 auto", minWidth: 0, textAlign: "right" }}>
          {cmd.disabled}{cmd.action ? <> · <span style={{ color: "var(--color-text-info)" }}>↵ {cmd.action.label}</span></> : null}
        </span>
      ) : cmd.hint && (
        <span style={{ fontSize: 11, color: "var(--color-text-tertiary)", whiteSpace: "nowrap", flex: "0 1 auto", minWidth: 0, overflow: "hidden", textOverflow: "ellipsis" }}>
          {cmd.hint.includes("prd lock") ? (
            <>
              {cmd.hint.slice(0, cmd.hint.indexOf("prd lock"))}
              <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>▲ prd lock</span>
              {cmd.hint.slice(cmd.hint.indexOf("prd lock") + "prd lock".length)}
            </>
          ) : cmd.hint}
        </span>
      )}
    </div>
  );
}
