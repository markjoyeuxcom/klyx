import type { HistoryLine } from "../bridge/logHistory";

// Coverage S8b §5: what a History view holds. Pages arrive newest first and
// each is older than everything held, so a page goes on the OLD end. The view
// holds at most 10,000 lines and 32 MiB of line text; past either, lines go
// from the NEWEST end - so Load older always reaches older pages, and the view
// says newer lines were dropped (Refresh returns to the newest).

export const HISTORY_LINES_MAX = 10_000;
export const HISTORY_BYTES_MAX = 32 << 20;

export type Held = { lines: HistoryLine[]; bytes: number; newerDropped: number }; // lines oldest first

export const emptyHeld = (): Held => ({ lines: [], bytes: 0, newerDropped: 0 });

const size = (l: HistoryLine) => l.line.length * 2 + (l.container?.length ?? 0) * 2 + 64; // UTF-16, with the row's fields

/** withOlder adds an older page (newest first) and evicts from the newest
 * end - into the incoming page itself when it alone is over a bound (Codex's
 * review of b2f945c), keeping its oldest lines, where the cursor continues. */
export function withOlder(h: Held, page: HistoryLine[], max = HISTORY_LINES_MAX, maxBytes = HISTORY_BYTES_MAX): Held {
  const older = [...page].reverse(); // oldest first
  const lines = [...older, ...h.lines];
  let bytes = h.bytes + older.reduce((n, l) => n + size(l), 0);
  let drop = 0;
  while (lines.length - drop > 1 && (lines.length - drop > max || bytes > maxBytes)) {
    bytes -= size(lines[lines.length - 1 - drop]);
    drop++;
  }
  return { lines: drop ? lines.slice(0, lines.length - drop) : lines, bytes, newerDropped: h.newerDropped + drop };
}
