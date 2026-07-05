import { useEffect, useState, useSyncExternalStore } from "react";

// Age — relative time that stays true. Every age cell used to render a
// static "5m" computed once; on manually-refreshed views it silently rotted.
// One module-level 30s ticker (a single interval app-wide, not one per cell)
// re-renders subscribed cells, and the absolute instant is always one hover
// away via the title attribute.
//
// Only true timestamps tick. DTO fields like ageSeconds are "age at emit" -
// on watch-backed views re-emits keep them fresh, and ticking them without
// the arrival instant would fabricate time. Those cells keep their static
// formatter (formatAge below, shared so the string shape stays identical).

const TICK_MS = 30_000;
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | null = null;

// The clock is quantized to the tick so getSnapshot stays stable within a
// window (useSyncExternalStore requires a cached snapshot) while unsubscribed
// reads - the first render before the subscribe effect runs - are still fresh.
const quantize = () => Math.floor(Date.now() / TICK_MS) * TICK_MS;
let nowMs = quantize();

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  if (timer === null) {
    nowMs = quantize();
    timer = setInterval(() => {
      nowMs = quantize();
      listeners.forEach((l) => l());
    }, TICK_MS);
  }
  return () => {
    listeners.delete(cb);
    if (listeners.size === 0 && timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  };
}

const getNow = () => (timer !== null ? nowMs : quantize());

export function useNow(): number {
  return useSyncExternalStore(subscribe, getNow);
}

// formatAge renders seconds in the app's terse age vocabulary ("42s", "5m",
// "3h", "2d") - the exact shape every view's private ago() produced.
export function formatAge(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

function absolute(ms: number): string {
  return new Date(ms).toLocaleString(undefined, {
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
}

/**
 * Age — live-ticking relative time for a real timestamp.
 * Pass `unixSeconds` (epoch seconds) or `iso` (RFC3339 string). Zero /
 * unparsable values render the `fallback` ("—" by default). `suffix`
 * appends " ago" where a view's vocabulary uses it.
 */
export function Age({ unixSeconds, iso, fallback = "—", suffix = "" }: {
  unixSeconds?: number;
  iso?: string;
  fallback?: string;
  suffix?: string;
}) {
  const now = useNow();
  let ms: number | null = null;
  if (unixSeconds !== undefined && unixSeconds > 0) ms = unixSeconds * 1000;
  else if (iso) {
    const parsed = Date.parse(iso);
    if (!Number.isNaN(parsed)) ms = parsed;
  }
  if (ms === null) return <>{fallback}</>;
  const diff = (now - ms) / 1000;
  if (diff < 0) return <span title={absolute(ms)}>just now</span>;
  return <span title={absolute(ms)}>{formatAge(diff)}{suffix}</span>;
}

// useReceivedAt stamps when data last arrived (the dep's identity changes per
// fetch). Client-receipt time, honestly labelled - manual-refresh views use
// it for the "updated HH:MM:SS" caption so age rot is visible.
export function useReceivedAt(dep: unknown): number | null {
  const [at, setAt] = useState<number | null>(null);
  useEffect(() => {
    if (dep != null) setAt(Date.now());
  }, [dep]);
  return at;
}

/** UpdatedAt — the "updated 14:32:05" caption beside a manual refresh button. */
export function UpdatedAt({ at }: { at: number | null }) {
  useNow(); // re-render with the ticker so a stale caption is noticeable on hover
  if (at === null) return null;
  return (
    <span
      title={`data received ${absolute(at)}`}
      style={{ fontSize: 10, color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)" }}
    >
      updated {new Date(at).toTimeString().slice(0, 8)}
    </span>
  );
}
