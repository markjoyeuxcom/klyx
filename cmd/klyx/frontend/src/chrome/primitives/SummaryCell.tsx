import { type Tone, toneColor, ellipsis } from "./styles";

// SummaryCell: one cell in a summary strip (GitOps/NodesView/NetworkView/
// CertsView). Uniform sizing. The value colour follows the tone: the default
// `neutral` reads at primary weight, `muted` dims to tertiary (for zero/idle
// counts), and a status tone colours it.
export function SummaryCell({ label, value, tone = "neutral" }: { label: string; value: string; tone?: Tone }) {
  return (
    <div style={{ padding: "8px 12px", borderRight: "var(--border-hairline) solid var(--color-border-tertiary)", minWidth: 0 }}>
      <div style={{ fontSize: "var(--text-2xs)", color: "var(--color-text-tertiary)", ...ellipsis }}>{label}</div>
      <div style={{ fontSize: "var(--text-md)", fontWeight: 500, color: toneColor[tone], ...ellipsis }}>{value}</div>
    </div>
  );
}
