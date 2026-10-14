import type { ReactNode } from "react";
import { Browser } from "@wailsio/runtime";

// SafeMarkdown renders a model's answer (spec 2026-10-14 §8): Klyx's own
// small Markdown subset as React elements - paragraphs, headings, lists,
// quotes, emphasis, inline code and code blocks. There is no
// dangerouslySetInnerHTML and no third-party renderer, so raw HTML, SVG and
// embedded content stay literal text. Images are never loaded: an image is its
// alt text and its URL, as text. Only https: and http: links are clickable;
// each shows its real destination host and opens only on an explicit click,
// through the OS browser - the webview is never navigated, and no answer text
// can trigger an app action.

export function safeLinkURL(raw: string): URL | null {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return null;
  }
  return u.protocol === "https:" || u.protocol === "http:" ? u : null;
}

function ExternalLink({ label, href }: { label: string; href: string }) {
  const u = safeLinkURL(href);
  if (!u) {
    // Any other scheme (javascript:, data:, file:, an app scheme) is text.
    return <span data-testid="md-unsafe-link">{label} ({href})</span>;
  }
  return (
    <span>
      {/* A button, not an anchor: no href exists for the webview to follow
          on a middle-click or drag - only this explicit click opens it. */}
      <button
        type="button"
        title={u.href}
        data-testid="md-link"
        className="md-link"
        onClick={() => void Browser.OpenURL(u.href)}
      >
        {label}
      </button>
      <span className="t-legend" style={{ color: "var(--color-text-tertiary)" }}> ({u.host})</span>
    </span>
  );
}

// Inline tokens, earliest match wins: code, image, link, bold, italic, bare URL.
const INLINE = /(`[^`\n]+`)|(!\[([^\]\n]*)\]\(([^)\s]+)\))|(\[([^\]\n]+)\]\(([^)\s]+)\))|(\*\*([^*\n]+)\*\*)|(\*([^*\n]+)\*|_([^_\n]+)_)|(https?:\/\/[^\s)<>"']+)/;

export function inline(text: string, keyPrefix = "i"): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = text;
  let k = 0;
  while (rest.length > 0) {
    const m = INLINE.exec(rest);
    if (!m) {
      out.push(rest);
      break;
    }
    if (m.index > 0) out.push(rest.slice(0, m.index));
    const key = `${keyPrefix}-${k++}`;
    if (m[1]) out.push(<code key={key} className="md-code">{m[1].slice(1, -1)}</code>);
    else if (m[2]) out.push(<span key={key} data-testid="md-image">[image: {m[3] || "no description"}] ({m[4]})</span>);
    else if (m[5]) out.push(<ExternalLink key={key} label={m[6]} href={m[7]} />);
    else if (m[8]) out.push(<strong key={key}>{inline(m[9], key)}</strong>);
    else if (m[10]) out.push(<em key={key}>{inline(m[11] ?? m[12] ?? "", key)}</em>);
    else if (m[13]) out.push(<ExternalLink key={key} label={m[13]} href={m[13]} />);
    rest = rest.slice(m.index + m[0].length);
  }
  return out;
}

type Block =
  | { t: "code"; text: string }
  | { t: "heading"; level: number; text: string }
  | { t: "list"; ordered: boolean; items: string[] }
  | { t: "quote"; text: string }
  | { t: "para"; text: string };

export function blocks(src: string): Block[] {
  const lines = src.replace(/\r\n/g, "\n").split("\n");
  const out: Block[] = [];
  let para: string[] = [];
  const flush = () => {
    if (para.length > 0) out.push({ t: "para", text: para.join("\n") });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      flush();
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) body.push(lines[i++]);
      out.push({ t: "code", text: body.join("\n") });
      continue;
    }
    const h = /^(#{1,4})\s+(.*)$/.exec(line);
    if (h) {
      flush();
      out.push({ t: "heading", level: h[1].length, text: h[2] });
      continue;
    }
    const li = /^\s*(?:([-*+])|(\d+)[.)])\s+(.*)$/.exec(line);
    if (li) {
      flush();
      const ordered = !!li[2];
      const last = out[out.length - 1];
      if (last && last.t === "list" && last.ordered === ordered) last.items.push(li[3]);
      else out.push({ t: "list", ordered, items: [li[3]] });
      continue;
    }
    const q = /^>\s?(.*)$/.exec(line);
    if (q) {
      flush();
      const last = out[out.length - 1];
      if (last && last.t === "quote") last.text += "\n" + q[1];
      else out.push({ t: "quote", text: q[1] });
      continue;
    }
    if (line.trim() === "") {
      flush();
      continue;
    }
    para.push(line);
  }
  flush();
  return out;
}

export function SafeMarkdown({ text }: { text: string }) {
  return (
    <div className="md" data-testid="safe-markdown">
      {blocks(text).map((b, i) => {
        const key = `b${i}`;
        switch (b.t) {
          case "code":
            return <pre key={key} className="md-pre"><code>{b.text}</code></pre>;
          case "heading":
            return <div key={key} role="heading" aria-level={b.level + 2} className="md-heading">{inline(b.text, key)}</div>;
          case "list":
            return b.ordered
              ? <ol key={key} className="md-list">{b.items.map((it, j) => <li key={j}>{inline(it, `${key}-${j}`)}</li>)}</ol>
              : <ul key={key} className="md-list">{b.items.map((it, j) => <li key={j}>{inline(it, `${key}-${j}`)}</li>)}</ul>;
          case "quote":
            return <blockquote key={key} className="md-quote">{inline(b.text, key)}</blockquote>;
          default:
            return <p key={key} className="md-para">{inline(b.text, key)}</p>;
        }
      })}
    </div>
  );
}
