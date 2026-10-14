import { useMemo, useRef, useState } from "react";
import { CopyButton } from "./CopyButton";

// YamlBlock — the read-only manifest surface: hand-rolled YAML syntax
// highlighting (keys/strings/numbers/comments - ~60 lines, no dependency,
// CSP-safe) plus find-in-manifest with match count and Enter/shift+Enter
// cycling. Replaces the monochrome <pre> in the pod/node YAML tabs and helm
// values. The editable YAML surface (InstanceDetail's editor) stays a plain
// textarea - highlighting an editor is a different project.

type Tok = { text: string; color?: string };

// Syntax colours are not status: the line themes give code its own
// (--line-syntax-*), so a number never reads as "look at" (spec §2.1).
const C_KEY = "var(--line-syntax-key, var(--color-text-info))";
const C_STR = "var(--line-syntax-str, var(--color-text-success))";
const C_NUM = "var(--line-syntax-num, var(--color-text-warning))";
const C_CMT = "var(--color-text-tertiary)";
const C_PUNC = "var(--color-text-tertiary)";

// tokenizeYamlLine splits one line into colored runs. Line-based and
// deliberately simple: good enough for kubectl-shaped YAML, never wrong in a
// way that misleads (worst case a token renders in the default color).
export function tokenizeYamlLine(line: string): Tok[] {
  const out: Tok[] = [];
  let rest = line;

  // full-line comment (allowing indent)
  const cmt = rest.match(/^(\s*)(#.*)$/);
  if (cmt) {
    if (cmt[1]) out.push({ text: cmt[1] });
    out.push({ text: cmt[2], color: C_CMT });
    return out;
  }

  // leading "key:" (incl. "- key:") - keys may be quoted or dotted
  const key = rest.match(/^(\s*-?\s*)("[^"]*"|'[^']*'|[\w.\-/[\]]+)(\s*:)(\s|$)/);
  if (key) {
    if (key[1]) out.push({ text: key[1], color: C_PUNC });
    out.push({ text: key[2], color: C_KEY });
    out.push({ text: key[3], color: C_PUNC });
    rest = rest.slice(key[1].length + key[2].length + key[3].length);
  } else {
    const dash = rest.match(/^(\s*-\s+)/);
    if (dash) {
      out.push({ text: dash[1], color: C_PUNC });
      rest = rest.slice(dash[1].length);
    }
  }

  // value: quoted string, number/bool/null, or plain scalar
  if (rest.length > 0) {
    const trailingCmt = rest.match(/^(.*?)(\s#.*)$/);
    const value = trailingCmt ? trailingCmt[1] : rest;
    const trimmed = value.trim();
    let color: string | undefined;
    if (/^("|').*\1$/.test(trimmed)) color = C_STR;
    else if (/^-?\d+(\.\d+)?(e[+-]?\d+)?$/i.test(trimmed)) color = C_NUM;
    else if (/^(true|false|null|~|yes|no|on|off)$/i.test(trimmed)) color = C_NUM;
    if (value) out.push({ text: value, color });
    if (trailingCmt) out.push({ text: trailingCmt[2], color: C_CMT });
  }

  return out.length > 0 ? out : [{ text: line }];
}

function LineView({ line, query, isCurrentLine }: { line: string; query: string; isCurrentLine: boolean }) {
  const toks = tokenizeYamlLine(line);
  const q = query.toLowerCase();
  return (
    <div
      data-yaml-current={isCurrentLine ? "true" : undefined}
      style={{ background: isCurrentLine ? "var(--color-background-info)" : undefined }}
    >
      {toks.map((t, i) => {
        if (!q || !t.text.toLowerCase().includes(q)) {
          return <span key={i} style={{ color: t.color }}>{t.text}</span>;
        }
        // split the token around case-insensitive matches
        const parts: React.ReactNode[] = [];
        let s = t.text;
        let li = 0;
        while (true) {
          const at = s.toLowerCase().indexOf(q);
          if (at < 0) {
            parts.push(<span key={li}>{s}</span>);
            break;
          }
          if (at > 0) parts.push(<span key={li++}>{s.slice(0, at)}</span>);
          parts.push(
            <mark key={li++} style={{ background: "var(--color-background-warning)", color: "var(--color-text-warning)", borderRadius: 2 }}>
              {s.slice(at, at + q.length)}
            </mark>,
          );
          s = s.slice(at + q.length);
        }
        return <span key={i} style={{ color: t.color }}>{parts}</span>;
      })}
      {line === "" ? "​" : null}
    </div>
  );
}

// Manifests read unwrapped by default - indentation is the structure, and
// UIDs and timestamps never break mid-token - with a Wrap toggle the viewer's
// choice remembers (UI review 2026-10-13).
const WRAP_KEY = "klyx.yaml.wrap";
function loadWrap(): boolean {
  try {
    return localStorage.getItem(WRAP_KEY) === "1";
  } catch {
    return false;
  }
}

export function YamlBlock({ text, copyLabel, maxHeight }: { text: string; copyLabel: string; maxHeight?: number }) {
  const [query, setQuery] = useState("");
  const [wrap, setWrap] = useState(loadWrap);
  const toggleWrap = () => {
    setWrap((w) => {
      try {
        localStorage.setItem(WRAP_KEY, w ? "0" : "1");
      } catch {
        // a per-viewer convenience: without storage the toggle still works
      }
      return !w;
    });
  };
  const [current, setCurrent] = useState(0);
  const scrollRef = useRef<HTMLPreElement>(null);

  const lines = useMemo(() => text.split("\n"), [text]);
  const matchLines = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return [];
    const out: number[] = [];
    lines.forEach((l, i) => {
      if (l.toLowerCase().includes(q)) out.push(i);
    });
    return out;
  }, [lines, query]);
  const currentLine = matchLines.length > 0 ? matchLines[Math.min(current, matchLines.length - 1)] : -1;

  const cycle = (dir: 1 | -1) => {
    if (matchLines.length === 0) return;
    const next = (Math.min(current, matchLines.length - 1) + dir + matchLines.length) % matchLines.length;
    setCurrent(next);
    // scroll the newly-current line into view (jsdom has no scrollIntoView)
    requestAnimationFrame(() => {
      const el = scrollRef.current?.querySelector('[data-yaml-current="true"]');
      (el as HTMLElement | null)?.scrollIntoView?.({ block: "center" });
    });
  };

  return (
    <div style={{ position: "relative" }}>
      <div style={{ position: "absolute", top: 6, right: 6, display: "flex", alignItems: "center", gap: 4, background: "var(--color-background-secondary)", borderRadius: 4 }}>
        <input
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setCurrent(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              cycle(e.shiftKey ? -1 : 1);
            }
            e.stopPropagation(); // never trigger list nav underneath
          }}
          placeholder="find"
          aria-label={`find in ${copyLabel}`}
          style={{ width: 110, fontSize: 10, padding: "2px 6px", borderRadius: 3, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)" }}
        />
        {query.trim() && (
          <span style={{ fontSize: 9, color: matchLines.length > 0 ? "var(--color-text-secondary)" : "var(--color-text-danger)", fontFamily: "var(--font-mono)", whiteSpace: "nowrap" }}>
            {matchLines.length > 0 ? `${Math.min(current, matchLines.length - 1) + 1}/${matchLines.length}` : "0"}
          </span>
        )}
        <button type="button" className="l-chip" aria-pressed={wrap} onClick={toggleWrap} title={wrap ? "Lines wrap - click to scroll sideways instead" : "Long lines scroll sideways - click to wrap them"} style={{ fontSize: 10, padding: "1px 6px" }}>Wrap</button>
        <CopyButton text={text} label={`copy ${copyLabel}`} />
      </div>
      <pre
        ref={scrollRef}
        style={{
          margin: 0, padding: 10, paddingTop: 28,
          background: "var(--color-background-secondary)",
          border: "0.5px solid var(--color-border-tertiary)",
          borderRadius: 4,
          fontFamily: "var(--font-mono)", fontSize: 11, lineHeight: 1.5,
          overflowX: "auto", overflowY: maxHeight ? "auto" : undefined, maxHeight,
          whiteSpace: wrap ? "pre-wrap" : "pre", wordBreak: wrap ? "break-word" : "normal",
          color: "var(--color-text-primary)",
        }}
      >
        {lines.map((l, i) => (
          <LineView key={i} line={l} query={query.trim()} isCurrentLine={i === currentLine} />
        ))}
      </pre>
    </div>
  );
}
