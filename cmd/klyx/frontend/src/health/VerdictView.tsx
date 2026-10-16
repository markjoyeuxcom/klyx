import { Aspect, aspectOfVerdict } from "../chrome/primitives/Aspect";
import { toneColor } from "../chrome/primitives/styles";
import { type HealthLike, kindOf, labelOf, verdictTone } from "./verdict";
import { ClampedText } from "../chrome/ClampedText";

// VerdictLamp is a row's single status mark, an Aspect (The Line, spec
// 2026-10-14 §2.4): fault is a filled square, degraded a hollow one, unknown
// dotted so "cannot tell" never looks like "fine", progressing dashed (not
// yet realized), paused a plain outline (intentional, not broken), and ok
// carries no mark.
export function VerdictLamp({ health, size = 7 }: { health: HealthLike | null | undefined; size?: number }) {
  return <Aspect kind={aspectOfVerdict(kindOf(health))} size={size} />;
}

// VerdictMark is a row's mark in a table's first column (The Line slice
// 11a): the verdict's Aspect, named for a screen reader. A healthy row's
// empty mark is not announced - its verdict word, in its own column, is. A
// row whose changes are not yet realized (generation > observedGeneration)
// and that would otherwise draw nothing takes the dashed syncing mark; any
// other verdict keeps its own mark at full strength.
export function VerdictMark({ health, stale = false, size = 10, label }: { health: HealthLike | null | undefined; stale?: boolean; size?: number; label?: string }) {
  const own = aspectOfVerdict(kindOf(health));
  const kind = stale && own === "none" ? "syncing" : own;
  if (kind === "none") return <span className="row-mark" aria-hidden="true" />;
  return (
    <span className="row-mark" role="img" aria-label={label ?? (own === "none" ? "Syncing: changes not yet realized" : labelOf(health))} title={label}>
      <Aspect kind={kind} size={size} />
    </span>
  );
}

// ROW_MARK is the mark column every table starts with.
export const ROW_MARK = { id: "mark", label: "", width: 20, priority: 0 } as const;

// VerdictLabel is a row's status word in the verdict's tone. The row's mark
// column carries the shape (The Line slice 11a), so the word never repeats
// it as ▲.
export function VerdictLabel({ health, mono = false }: { health: HealthLike | null | undefined; mono?: boolean }) {
  const k = kindOf(health);
  const tone = verdictTone[k];
  const color = k === "ok" || k === "progressing" || k === "paused" ? "var(--color-text-secondary)" : toneColor[tone];
  return (
    <span
      title={health?.message || undefined}
      style={{ color, fontFamily: mono ? "var(--font-mono)" : "var(--font-sans)" }}
    >
      {labelOf(health)}
    </span>
  );
}

// VerdictStatus is the inspector's verdict head: one chip (lamp + label in the
// verdict tone), then the FULL diagnostic message, wrapped - never truncated
// behind a tooltip - then the native state and any history note, secondary.
// pendingMessage stands where the message will be while it is still being
// read (a row that does not carry its message); it is never the message.
export function VerdictStatus({ health, detail, pendingMessage }: { health: HealthLike | null | undefined; detail?: string; pendingMessage?: string }) {
  const k = kindOf(health);
  const tone = verdictTone[k];
  const lit = k === "fault" || k === "degraded";
  const secondary = [health?.native, detail].filter(Boolean).join(" · ");
  // The Line slice 13c.2: the mark, then the evaluated label as the
  // headline in ink-1 (the mark carries the tone); the controller's own
  // words follow in ink-2, clamped with "Show full message"; the native
  // state and any note stay visible under it.
  return (
    <div data-testid="verdict-status" className="verdict-head" data-tone={lit ? tone : undefined}>
      <span className="verdict-mark"><VerdictLamp health={health} size={12} /></span>
      <div className="verdict-text">
        <p className="verdict-headline">{labelOf(health)}</p>
        {health?.message ? (
          <ClampedText key={health.message} className="verdict-message" text={health.message} style={{ overflowWrap: "anywhere", userSelect: "text" }} />
        ) : pendingMessage ? (
          <span data-testid="verdict-message-pending" className="verdict-message" style={{ color: "var(--color-text-tertiary)" }}>{pendingMessage}</span>
        ) : null}
        {secondary && <span className="verdict-sub">{secondary}</span>}
        {health?.note && <span className="verdict-note">{health.note}</span>}
      </div>
    </div>
  );
}
