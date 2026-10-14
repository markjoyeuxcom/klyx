import type { CSSProperties } from "react";

// Aspect is The Line's status mark (spec 2026-10-14 §1, §2.4): healthy carries
// no mark, and every other state differs by SHAPE as well as colour, so fault
// and look-at survive colour blindness. Nothing glows and nothing blinks.
//   fault      a filled red square
//   attention  a hollow amber square (look at)
//   checking   a dotted grey hollow square (unread or unknown)
//   syncing    a dashed grey hollow square (not yet realized)
//   paused     a solid grey hollow square (intentional, not broken)
//   none       healthy: an empty box of the same footprint, so rows align
export type AspectKind = "fault" | "attention" | "checking" | "syncing" | "paused" | "none";

export function Aspect({ kind, size = 8, label }: { kind: AspectKind; size?: number; label?: string }) {
  const edge = size >= 10 ? 2 : 1.5;
  const base: CSSProperties = { display: "inline-block", width: size, height: size, flexShrink: 0, boxSizing: "border-box", borderRadius: 1 };
  const style: CSSProperties = { ...base, ...shape(kind, edge) };
  return label
    ? <span role="img" aria-label={label} data-aspect={kind} style={style} />
    : <span aria-hidden data-aspect={kind} style={style} />;
}

function shape(kind: AspectKind, edge: number): CSSProperties {
  switch (kind) {
    case "fault":
      return { background: "var(--line-danger, var(--color-text-danger))" };
    case "attention":
      return { border: `${edge}px solid var(--line-caution, var(--color-text-warning))` };
    case "checking":
      return { border: `${edge}px dotted var(--color-text-tertiary)` };
    case "syncing":
      return { border: `${edge}px dashed var(--color-text-tertiary)` };
    case "paused":
      return { border: `${edge}px solid var(--color-text-tertiary)` };
    case "none":
      return {};
  }
}
