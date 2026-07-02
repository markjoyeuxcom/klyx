import { type Tone, toneColor } from "./styles";

// StatusDot: the small round status indicator repeated across every list/row.
// Pass an explicit `color` (e.g. a readyColor lookup) or a `tone`. A transparent
// color is honoured so callers can render "no status" without a coloured dot.
export function StatusDot({ tone, color, size = 7 }: { tone?: Tone; color?: string; size?: number }) {
  const bg = color ?? (tone ? toneColor[tone] : "var(--color-text-tertiary)");
  return <span aria-hidden style={{ width: size, height: size, borderRadius: "50%", background: bg, flexShrink: 0 }} />;
}
