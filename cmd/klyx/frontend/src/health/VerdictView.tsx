import { Aspect, aspectOfVerdict } from "../chrome/primitives/Aspect";
import { toneBg, toneBorder, toneColor } from "../chrome/primitives/styles";
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
  return (
    <div data-testid="verdict-status" style={{ display: "flex", flexDirection: "column", gap: 6, marginBottom: 12 }}>
      <span
        style={{
          alignSelf: "flex-start",
          display: "inline-flex",
          alignItems: "center",
          gap: 8,
          fontFamily: "var(--font-mono)",
          fontSize: 12,
          fontWeight: 700,
          padding: "5px 10px",
          borderRadius: 4,
          color: lit ? toneColor[tone] : "var(--color-text-primary)",
          background: lit ? toneBg[tone] : "var(--color-background-secondary)",
          boxShadow: `inset 0 0 0 1px ${lit ? toneBorder[tone] : "var(--color-border-tertiary)"}`,
        }}
      >
        <VerdictLamp health={health} size={8} />
        <span>{labelOf(health)}</span>
      </span>
      {health?.message ? (
        <ClampedText key={health.message} text={health.message} style={{ fontSize: 12, lineHeight: 1.45, color: lit ? toneColor[tone] : "var(--color-text-primary)", overflowWrap: "anywhere", userSelect: "text" }} />
      ) : pendingMessage ? (
        <span data-testid="verdict-message-pending" style={{ fontSize: 12, color: "var(--color-text-tertiary)" }}>{pendingMessage}</span>
      ) : null}
      {secondary && (
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{secondary}</span>
      )}
      {health?.note && (
        <span style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>{health.note}</span>
      )}
    </div>
  );
}
