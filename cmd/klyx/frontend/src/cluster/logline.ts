/**
 * logline.ts — pure log-line parsing helpers.
 *
 * No React, no side-effects. Runs per rendered line (≤2000).
 */

export type LogLevel = "error" | "warn" | "info" | "debug" | "none";

export interface ParsedLine {
  level: LogLevel;
  /** Number of leading characters to render dimmed (timestamp / klog prefix). 0 = no dim. */
  dimPrefixLen: number;
  /** Raw text (ANSI already stripped by callers if desired). */
  text: string;
}

// ---------------------------------------------------------------------------
// ANSI stripping
// ---------------------------------------------------------------------------

// Matches ESC [ ... m colour sequences and ESC [ K (erase-to-EOL). The ESC
// control char is intentional here - that is exactly what we strip.
// eslint-disable-next-line no-control-regex
const RE_ANSI = /\x1b\[[0-9;]*[mK]/g;

export function stripAnsi(s: string): string {
  return s.replace(RE_ANSI, "");
}

// ---------------------------------------------------------------------------
// Level detection
// ---------------------------------------------------------------------------

// klog prefix: I/W/E/F followed by 4 digits and a space  e.g. "I0609 22:26:09.484068  1 controller.go:75]"
const RE_KLOG_PREFIX = /^([IWEF])\d{4}\s/;

// ISO 8601 / RFC 3339 timestamp prefix, e.g. "2024-06-09T22:26:09.123Z " or "2024-06-09 22:26:09 "
// Matches: YYYY-MM-DD followed by T or space and a time component.
const RE_ISO_PREFIX = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?\s*/;

// klog prefix ends at the first ']' character
function klogPrefixLen(raw: string): number {
  const idx = raw.indexOf("]");
  return idx >= 0 ? idx + 1 : 0;
}

// structured `level=` or `"level":"` patterns (checked against first 200 chars)
const RE_LEVEL_KEY = /(?:^|\s)"?level"?\s*[:=]\s*"?(\w+)"?/i;

// bare level tokens near the start of the line (first 80 chars)
const RE_LEVEL_TOKEN = /\b(error|fatal|panic|warn|warning|debug|trace|info)\b/i;

function tokenToLevel(tok: string): LogLevel {
  const t = tok.toLowerCase();
  if (t === "error" || t === "fatal" || t === "panic") return "error";
  if (t === "warn" || t === "warning") return "warn";
  if (t === "debug" || t === "trace") return "debug";
  if (t === "info") return "info";
  return "none";
}

// pod-prefix: "<pod-short> › " at the start of the line (aggregate stream).
// Matches 1-63 non-whitespace chars followed by " › " (space + U+203A + space).
const RE_POD_PREFIX = /^(\S{1,63}) › /;

// Marker lines injected by the aggregate batcher start with "… " (U+2026 + space).
const RE_MARKER_LINE = /^… /;

export function parseLine(raw: string): ParsedLine {
  // 0. Marker lines ("… showing N of M pods", "… stream ended") — level none,
  //    fully dim (treat the entire line as the prefix so it renders tertiary).
  if (RE_MARKER_LINE.test(raw)) {
    return { level: "none", dimPrefixLen: raw.length, text: raw };
  }

  // Pod-prefix pre-pass: strip "<pod-short> › " and record its length so that
  // the remaining klog/ISO detection runs on the payload text and the combined
  // dim length covers both the pod prefix and the log timestamp.
  let podPrefixLen = 0;
  let tail = raw;
  const pm = RE_POD_PREFIX.exec(raw);
  if (pm) {
    podPrefixLen = pm[0].length; // e.g. "7d4b9c6f9-x2x9k › ".length
    tail = raw.slice(podPrefixLen);
  }

  // 1. klog prefix check (cheapest, most common in this codebase)
  const klm = RE_KLOG_PREFIX.exec(tail);
  if (klm) {
    const ch = klm[1];
    const level: LogLevel =
      ch === "I" ? "info" :
      ch === "W" ? "warn" :
      (ch === "E" || ch === "F") ? "error" :
      "none";
    return { level, dimPrefixLen: podPrefixLen + klogPrefixLen(tail), text: raw };
  }

  // 2. structured level= / "level": key
  const head200 = tail.length > 200 ? tail.slice(0, 200) : tail;
  const lkm = RE_LEVEL_KEY.exec(head200);
  if (lkm) {
    const level = tokenToLevel(lkm[1]);
    // dim only if there's also an ISO prefix
    const isom = RE_ISO_PREFIX.exec(tail);
    return { level, dimPrefixLen: podPrefixLen + (isom ? isom[0].length : 0), text: raw };
  }

  // 3. ISO/RFC3339 prefix — detect level from token after the prefix
  const isom = RE_ISO_PREFIX.exec(tail);
  if (isom) {
    const rest80 = tail.slice(isom[0].length, isom[0].length + 80);
    const tm = RE_LEVEL_TOKEN.exec(rest80);
    const level = tm ? tokenToLevel(tm[1]) : "none";
    return { level, dimPrefixLen: podPrefixLen + isom[0].length, text: raw };
  }

  // 4. bare token near start of line (first 80 chars, from tail)
  const head80 = tail.length > 80 ? tail.slice(0, 80) : tail;
  const tm = RE_LEVEL_TOKEN.exec(head80);
  if (tm) {
    return { level: tokenToLevel(tm[1]), dimPrefixLen: podPrefixLen, text: raw };
  }

  return { level: "none", dimPrefixLen: podPrefixLen, text: raw };
}

// ---------------------------------------------------------------------------
// Highlight helpers
// ---------------------------------------------------------------------------

/**
 * Split `text` into alternating plain/match segments for search highlighting.
 * Returns an array of { value: string; match: boolean }.
 */
export interface Segment {
  value: string;
  match: boolean;
}

export function splitHighlight(text: string, searchLc: string): Segment[] {
  if (!searchLc) return [{ value: text, match: false }];
  const segments: Segment[] = [];
  const textLc = text.toLowerCase();
  let pos = 0;
  while (pos < text.length) {
    const idx = textLc.indexOf(searchLc, pos);
    if (idx === -1) {
      segments.push({ value: text.slice(pos), match: false });
      break;
    }
    if (idx > pos) segments.push({ value: text.slice(pos, idx), match: false });
    segments.push({ value: text.slice(idx, idx + searchLc.length), match: true });
    pos = idx + searchLc.length;
  }
  return segments;
}
