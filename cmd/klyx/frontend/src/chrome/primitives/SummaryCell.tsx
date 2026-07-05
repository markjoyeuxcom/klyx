import { type Tone, toneColor, ellipsis } from "./styles";

// SummaryCell: one cell in a summary strip (GitOps/NodesView/GatewayApiView/
// CertsView/Helm). Signal Bench turns it into a counter well: the value sits
// in an inset window with a tracked micro-label beneath - numbers read like
// instrument readouts. Tones: `neutral` reads at primary weight, `muted` dims
// (zero/idle counts), a status tone colours the readout. Non-bench themes see
// the old flat cell via the token fallbacks.
export function SummaryCell({ label, value, tone = "neutral" }: { label: string; value: string; tone?: Tone }) {
  return (
    <div style={{ padding: "8px 12px", minWidth: 0, display: "flex", flexDirection: "column", gap: 4 }}>
      <div style={{
        padding: "3px 10px 2px",
        textAlign: "right",
        fontFamily: "var(--font-mono)",
        fontVariantNumeric: "tabular-nums",
        fontSize: "var(--text-md)",
        fontWeight: 600,
        letterSpacing: "0.04em",
        color: toneColor[tone],
        borderRadius: "var(--radius-sm)",
        background: "var(--bench-well-bg, transparent)",
        boxShadow: "var(--bench-well-shadow, none)",
        ...ellipsis,
      }}>{value}</div>
      <div style={{ fontSize: 9, letterSpacing: "0.12em", textTransform: "uppercase", fontWeight: 600, textAlign: "right", paddingRight: 2, color: "var(--color-text-tertiary)", ...ellipsis }}>{label}</div>
    </div>
  );
}
