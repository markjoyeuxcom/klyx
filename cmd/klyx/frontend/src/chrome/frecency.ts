import { readStored, writeStored } from "../settings/storage";
import type { StorageKey } from "../settings/storageKeys";

// Palette frecency: frequently + recently run commands float up. Stored in
// localStorage as {commandId: {n: uses, t: last-used-ms}}, trimmed to the 300
// most recently used entries so it never grows unbounded.
//
// boost(id) = min(n, 10) * 0.5^(ageDays / 14) - capped at 10 and halving every
// two weeks, so frecency reorders the empty-query list and tiebreaks fuzzy
// matches without ever letting a stale habit outrank a clearly better match.

const KEY: StorageKey = "klyx-palette-frecency";
const MAX_ENTRIES = 300;
const HALF_LIFE_DAYS = 14;
const MAX_BOOST = 10;

type Entry = { n: number; t: number };

function load(): Record<string, Entry> {
  try {
    const raw = readStored(KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? (parsed as Record<string, Entry>) : {};
  } catch {
    return {};
  }
}

function save(entries: Record<string, Entry>): void {
  writeStored(KEY, JSON.stringify(entries)); // best-effort: never breaks the palette
}

export function recordUse(id: string, now: number = Date.now()): void {
  const entries = load();
  const cur = entries[id];
  entries[id] = { n: (cur?.n ?? 0) + 1, t: now };
  const ids = Object.keys(entries);
  if (ids.length > MAX_ENTRIES) {
    ids.sort((a, b) => entries[b].t - entries[a].t);
    for (const stale of ids.slice(MAX_ENTRIES)) delete entries[stale];
  }
  save(entries);
}

// loadBoosts snapshots all boosts once per palette open - rank() runs per
// keystroke and must not re-read localStorage each time.
export function loadBoosts(now: number = Date.now()): (id: string) => number {
  const entries = load();
  return (id: string) => {
    const e = entries[id];
    if (!e) return 0;
    const ageDays = Math.max(0, now - e.t) / 86_400_000;
    return Math.min(e.n, MAX_BOOST) * Math.pow(0.5, ageDays / HALF_LIFE_DAYS);
  };
}
