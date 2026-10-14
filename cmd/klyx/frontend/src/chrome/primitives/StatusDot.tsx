import { type Tone } from "./styles";
import { Aspect, type AspectKind } from "./Aspect";

// StatusDot keeps its call-site API and renders The Line's Aspect (spec
// 2026-10-14 §2.4): the tone, or the colour var the caller already passes,
// picks the shape. Success is healthy and carries no mark; info is work in
// progress (not yet realized); muted and neutral are "cannot tell". A
// transparent colour renders "no status". `blink` is ignored: nothing blinks.
function toneFromColor(color: string): Tone | null {
  if (color === "transparent") return null;
  if (color.includes("-danger")) return "danger";
  if (color.includes("-warning")) return "warning";
  if (color.includes("-success")) return "success";
  if (color.includes("-info")) return "info";
  return "muted";
}

const KIND: Record<Tone, AspectKind> = {
  danger: "fault",
  warning: "attention",
  success: "none",
  info: "syncing",
  muted: "checking",
  neutral: "checking",
};

export function StatusDot({ tone, color, size = 7 }: {
  tone?: Tone;
  color?: string;
  size?: number;
  blink?: boolean;
}) {
  const t = tone ?? (color ? toneFromColor(color) : "muted");
  return <Aspect kind={t ? KIND[t] : "none"} size={size} />;
}
