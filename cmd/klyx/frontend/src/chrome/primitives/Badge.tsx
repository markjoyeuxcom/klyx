import type { ReactNode } from "react";
import { type Tone, toneColor, toneBg, toneBorder, ellipsis } from "./styles";

type Variant = "outline" | "soft" | "filled";

// Badge: one small status pill, replacing the four divergent local Badges
// (GitOps/Overview/CertsView/ClusterCard). variant picks the surface:
//   outline — toned border + text (default; GitOps/CertsView)
//   soft    — toned background + text, no border (Overview's gray label)
//   filled  — toned background + border + text (ClusterCard's verdict chip)
export function Badge({ tone = "neutral", variant = "outline", clip = false, children }: {
  tone?: Tone;
  variant?: Variant;
  clip?: boolean;
  children: ReactNode;
}) {
  const base = {
    fontSize: "var(--text-xs)",
    padding: "2px 8px",
    borderRadius: "var(--radius-xs)",
    color: toneColor[tone],
    whiteSpace: "nowrap" as const,
    ...(clip ? { ...ellipsis, display: "inline-block", maxWidth: "100%" } : {}),
  };
  // Tone edges draw as a 1px INSET RING (Signal Bench) rather than a border:
  // on bench themes the border-tone vars are the full tone colour, so the
  // ring reads as a machined edge; prior themes keep their translucent tint.
  if (variant === "soft") return <span style={{ ...base, background: toneBg[tone] }}>{children}</span>;
  if (variant === "filled") return <span style={{ ...base, background: toneBg[tone], boxShadow: `inset 0 0 0 1px ${toneBorder[tone]}` }}>{children}</span>;
  return <span style={{ ...base, boxShadow: `inset 0 0 0 1px ${toneBorder[tone]}` }}>{children}</span>;
}
