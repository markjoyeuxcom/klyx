import { type Tone, toneColor } from "./styles";

// StatusDot: the small round status indicator repeated across every list/row.
// Pass an explicit `color` (e.g. a readyColor lookup) or a `tone`. A transparent
// color is honoured so callers can render "no status" without a coloured dot.
//
// Signal Bench lamp physics (design/signal-bench-implementation-notes.md):
// ONLY faults glow. Danger/warning lamps carry the glow tokens; ok lamps sit
// dim at 0.75; muted lamps read as unlit glass (--lamp-off). The tone is
// inferred from the colour var the caller already passes, so every existing
// call site gets the physics without changing. Non-bench themes have no glow
// tokens - the var() fallbacks render the old flat dot. `blink` is legal ONLY
// in FleetSpine (the peripheral pager); in-content lamps are steady.
function toneFromColor(color: string): Tone | null {
  if (color === "transparent") return null;
  if (color.includes("-danger")) return "danger";
  if (color.includes("-warning")) return "warning";
  if (color.includes("-success")) return "success";
  if (color.includes("-info")) return "info";
  return "muted";
}

export function StatusDot({ tone, color, size = 7, blink = false }: {
  tone?: Tone;
  color?: string;
  size?: number;
  blink?: boolean;
}) {
  const effectiveTone = tone ?? (color ? toneFromColor(color) : "muted");
  let bg = color ?? (tone ? toneColor[tone] : "var(--color-text-tertiary)");
  let boxShadow: string | undefined;
  let opacity: number | undefined;

  switch (effectiveTone) {
    case "danger":
      boxShadow = "var(--lamp-glow-danger, none)";
      break;
    case "warning":
      boxShadow = "var(--lamp-glow-warning, none)";
      break;
    case "success":
      boxShadow = "inset 0 -1px 1px rgba(0,0,0,0.25)";
      opacity = 0.75;
      break;
    case "info":
      opacity = 0.8;
      break;
    case "muted":
    case "neutral":
      bg = `var(--lamp-off, ${bg})`;
      boxShadow = "inset 0 1px 1px rgba(0,0,0,0.35)";
      break;
    default:
      // null tone = transparent "no status" - stays invisible
      boxShadow = undefined;
  }

  return (
    <span
      aria-hidden
      className={blink ? "klyx-lamp-blink" : undefined}
      style={{ width: size, height: size, borderRadius: "50%", background: bg, flexShrink: 0, boxShadow, opacity }}
    />
  );
}
