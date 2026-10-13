import { StatusDot } from "../chrome/primitives/StatusDot";
import { toneBg, toneBorder, toneColor } from "../chrome/primitives/styles";
import { type HealthLike, kindOf, labelOf, verdictTone } from "./verdict";
import { ClampedText } from "../chrome/ClampedText";

// VerdictLamp is a row's single status lamp. Only fault/degraded glow (Signal
// Bench rule); unknown reads as a dashed, unlit ring so "cannot tell" never
// looks like "fine"; paused is an outlined ring (intentional, not broken).
export function VerdictLamp({ health, size = 7 }: { health: HealthLike | null | undefined; size?: number }) {
  const k = kindOf(health);
  if (k === "unknown" || k === "paused") {
    return (
      <span
        aria-hidden
        style={{
          width: size,
          height: size,
          borderRadius: "50%",
          flexShrink: 0,
          boxSizing: "border-box",
          border: `1px ${k === "unknown" ? "dashed" : "solid"} var(--color-text-tertiary)`,
        }}
      />
    );
  }
  return <StatusDot tone={verdictTone[k]} size={size} />;
}

// VerdictLabel is the headline word in the verdict's tone. Danger carries the
// ▲ shape (never hue-only) via the existing danger-tone CSS convention.
export function VerdictLabel({ health, mono = false }: { health: HealthLike | null | undefined; mono?: boolean }) {
  const k = kindOf(health);
  const tone = verdictTone[k];
  const color = k === "ok" || k === "progressing" || k === "paused" ? "var(--color-text-secondary)" : toneColor[tone];
  return (
    <span
      title={health?.message || undefined}
      style={{ color, fontWeight: k === "fault" ? 500 : undefined, fontFamily: mono ? "var(--font-mono)" : undefined }}
    >
      {k === "fault" ? "▲ " : ""}
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
        <span>{k === "fault" ? "▲ " : ""}{labelOf(health)}</span>
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
