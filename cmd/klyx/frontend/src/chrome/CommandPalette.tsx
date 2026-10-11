import { useState, useEffect, useRef, useMemo, useCallback } from "react";
import { useFleet } from "../store/fleet";
import { buildCommands, Command } from "./commands";
import { fuzzyMatch } from "./fuzzy";
import { loadBoosts, recordUse } from "./frecency";

// CommandPalette is the ⌘K primary-nav surface (design principle #6). It mounts
// once in AppShell and installs a single window keydown handler:
//   ⌘K / Ctrl+K  toggle
//   Esc          close
// On open it reads buildCommands(useFleet.getState()) fresh — no stale index —
// indexing only what's already loaded (footer: "showing loaded data").

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
// half weight. Frecency (loadBoosts) reorders the empty-query list toward
// recently used commands and tiebreaks fuzzy matches at 0.75 weight - enough
// to float a habit, never enough to outrank a clearly better match.
// `total` counts every match so the footer can be honest about truncation.
export type RankResult = { rows: Ranked[]; total: number };

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
    const h = cmd.hint && cmd.hint.toLowerCase().includes(q.toLowerCase()) ? fuzzyMatch(q, cmd.hint) : null;
    if (!t && !h) continue;
    const score = (t?.score ?? 0) + (h ? h.score * 0.5 : 0) + boost(cmd.id) * 0.75;
    scored.push({ cmd, positions: t?.positions ?? [], score });
  }
  scored.sort((a, b) => b.score - a.score);
  return { rows: scored.slice(0, LIMIT).map(({ cmd, positions }) => ({ cmd, positions })), total: scored.length };
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

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const [commands, setCommands] = useState<Command[]>([]);
  // A second step (Choose cluster): its options replace the results; the
  // first step's query comes back with Back.
  const [step, setStep] = useState<{ title: string; options: Command[]; parentQuery: string } | null>(null);
  // Frecency boosts snapshot, taken once per open - rank() runs per keystroke
  // and must not re-read localStorage each time.
  const [boost, setBoost] = useState<(id: string) => number>(() => () => 0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  // Focus returns to whatever had it before ⌘K - a keyboard flow keeps its place.
  const prevFocusRef = useRef<HTMLElement | null>(null);

  const close = useCallback(() => {
    _paletteOpen = false;
    setOpen(false);
    setQuery("");
    setSel(0);
    setStep(null);
    prevFocusRef.current?.focus?.();
    prevFocusRef.current = null;
  }, []);

  const openPalette = useCallback(() => {
    _paletteOpen = true;
    prevFocusRef.current = document.activeElement as HTMLElement | null;
    setCommands(buildCommands(useFleet.getState())); // fresh index, no stale data
    setBoost(() => loadBoosts());
    setQuery("");
    setSel(0);
    setStep(null);
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

  // Focus the input whenever the palette opens.
  useEffect(() => {
    if (open) inputRef.current?.focus();
  }, [open]);

  const ranked = useMemo(() => rank(step ? step.options : commands, query, step ? () => 0 : boost), [commands, step, query, boost]);
  const results = ranked.rows;
  const dropped = ranked.total - results.length;

  // Keep selection in range as the result set shrinks/grows.
  useEffect(() => {
    if (sel >= results.length) setSel(results.length === 0 ? 0 : results.length - 1);
  }, [results.length, sel]);

  // Keep the selected row visible as arrow keys move it past the list's fold.
  useEffect(() => {
    const el = listRef.current?.querySelector('[data-selected="true"]');
    // guard: jsdom does not implement scrollIntoView
    if (el && typeof (el as HTMLElement).scrollIntoView === "function") {
      (el as HTMLElement).scrollIntoView({ block: "nearest" });
    }
  }, [sel, results]);

  const run = useCallback(
    (i: number) => {
      const r = results[i];
      if (!r) return;
      const cmd = r.cmd;
      if (cmd.disabled) {
        // A disabled view runs nothing; its Connect action is explicit.
        if (cmd.action) { close(); cmd.action.run(); }
        return;
      }
      if (cmd.next) {
        recordUse(cmd.id);
        setStep({ title: cmd.next.title, options: cmd.next.options, parentQuery: query });
        setQuery("");
        setSel(0);
        inputRef.current?.focus();
        return;
      }
      recordUse(cmd.id);
      close();
      cmd.run();
    },
    [results, close, query],
  );
  const backToResults = () => {
    if (!step) return;
    setQuery(step.parentQuery);
    setStep(null);
    setSel(0);
    inputRef.current?.focus();
  };

  // Within-list keyboard nav: Arrow up/down wrap, Enter runs.
  const onInputKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => (results.length === 0 ? 0 : (s + 1) % results.length));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => (results.length === 0 ? 0 : (s - 1 + results.length) % results.length));
    } else if (e.key === "Enter") {
      e.preventDefault();
      run(sel);
    } else if (e.key === "Backspace" && step && query === "") {
      // Back from Choose cluster to the results, query intact.
      e.preventDefault();
      backToResults();
    }
  };

  if (!open) return null;

  // Group results in first-appearance order, preserving the flat selection index.
  const groups: { group: string; rows: { r: Ranked; idx: number }[] }[] = [];
  results.forEach((r, idx) => {
    const name = r.cmd.group;
    let g = groups.find((x) => x.group === name);
    if (!g) {
      g = { group: name, rows: [] };
      groups.push(g);
    }
    g.rows.push({ r, idx });
  });

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
        background: "rgba(0,0,0,0.3)",
        display: "flex",
        justifyContent: "center",
        alignItems: "flex-start",
        paddingTop: "12vh",
      }}
    >
      <div
        onMouseDown={(e) => e.stopPropagation()}
        style={{
          width: 560,
          maxWidth: "92vw",
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: 11,
          boxShadow: "0 16px 48px rgba(0,0,0,0.4)",
          overflow: "hidden",
          display: "flex",
          flexDirection: "column",
        }}
      >
        {/* The input sits IN a well (Signal Bench): data surfaces are inset. */}
        <div style={{ margin: 12, borderRadius: "var(--radius-sm)", background: "var(--bench-well-bg, transparent)", boxShadow: "var(--bench-well-shadow, none)", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
          <input
            ref={inputRef}
            data-palette-input="true"
            data-testid="command-palette-input"
            role="combobox"
            aria-expanded={true}
            aria-controls="command-palette-list"
            aria-activedescendant={results[sel] ? optionId(results[sel].cmd.id) : undefined}
            aria-autocomplete="list"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setSel(0);
            }}
            onKeyDown={onInputKey}
            placeholder={step ? "filter clusters…" : "jump or act: view, cluster, pod, drain, sync…"}
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

        {step && (
          <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "0 16px 4px" }}>
            <button type="button" className="l-btn" onMouseDown={(e) => { e.preventDefault(); backToResults(); }} title="Back to results (⌫ on an empty query)">‹ Results</button>
            <span className="t-section-label">{step.title}</span>
          </div>
        )}
        <div ref={listRef} id="command-palette-list" role="listbox" style={{ maxHeight: "50vh", overflowY: "auto", padding: "4px 0" }}>
          {results.length === 0 ? (
            <div style={{ padding: "16px", fontSize: 13, color: "var(--color-text-tertiary)" }}>
              No matches.
            </div>
          ) : (
            groups.map((g) => (
              <div key={g.group}>
                <div
                  style={{
                    fontSize: 10,
                    textTransform: "uppercase",
                    letterSpacing: 0.6,
                    color: "var(--color-text-tertiary)",
                    padding: "8px 16px 4px",
                  }}
                >
                  {g.group}
                </div>
                {g.rows.map(({ r, idx }) => {
                  const selected = idx === sel;
                  return (
                    <div
                      key={r.cmd.id}
                      id={optionId(r.cmd.id)}
                      role="option"
                      aria-selected={selected}
                      aria-disabled={r.cmd.disabled ? true : undefined}
                      data-testid={`command-row-${r.cmd.id}`}
                      data-selected={selected ? "true" : "false"}
                      onMouseMove={() => setSel(idx)}
                      onMouseDown={(e) => {
                        e.preventDefault();
                        run(idx);
                      }}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 12,
                        padding: "7px 16px",
                        cursor: "pointer",
                        fontSize: 13,
                        color: r.cmd.disabled ? "var(--color-text-tertiary)" : "var(--color-text-primary)",
                        background: selected ? "var(--bench-selection-bg, var(--color-background-secondary))" : "transparent",
                        boxShadow: selected ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
                      }}
                    >
                      {/* Labels keep 200px before hints and wrap to two
                          lines; a name is never cut to a lone initial. */}
                      <span style={{ flex: "1 1 200px", minWidth: 200, overflowWrap: "anywhere", display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical", overflow: "hidden" }}>
                        <Highlighted text={r.cmd.title} positions={r.positions} />
                      </span>
                      {r.cmd.disabled ? (
                        <span style={{ fontSize: 11, color: "var(--color-text-tertiary)", flex: "0 1 auto", minWidth: 0, textAlign: "right" }}>
                          {r.cmd.disabled}{r.cmd.action ? <> · <span style={{ color: "var(--color-text-info)" }}>↵ {r.cmd.action.label}</span></> : null}
                        </span>
                      ) : r.cmd.hint && (
                        <span
                          style={{
                            fontSize: 11,
                            color: "var(--color-text-tertiary)",
                            whiteSpace: "nowrap",
                            flex: "0 1 auto",
                            minWidth: 0,
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                          }}
                        >
                          {r.cmd.hint.includes("prd lock") ? (
                            <>
                              {r.cmd.hint.slice(0, r.cmd.hint.indexOf("prd lock"))}
                              <span style={{ color: "var(--color-text-warning)", fontWeight: 600 }}>prd lock</span>
                              {r.cmd.hint.slice(r.cmd.hint.indexOf("prd lock") + "prd lock".length)}
                            </>
                          ) : r.cmd.hint}
                        </span>
                      )}
                    </div>
                  );
                })}
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
          }}
        >
          {dropped > 0 ? `+${dropped} more — keep typing · ` : ""}↑↓ navigate · ↵ open · {step ? "⌫ back · " : ""}esc close · views always searchable, objects from loaded data
        </div>
      </div>
    </div>
  );
}
