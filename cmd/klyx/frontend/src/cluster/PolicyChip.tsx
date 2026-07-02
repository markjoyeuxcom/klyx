import { useState } from "react";
import type { PolicyRefDTO } from "../store/fleet";
import { type Tone, toneColor, toneBg } from "../chrome/primitives";

const ABBREV: Record<string, string> = {
  ClientTrafficPolicy: "CTP",
  BackendTrafficPolicy: "BTP",
  SecurityPolicy: "SP",
  EnvoyExtensionPolicy: "EEP",
  BackendTLSPolicy: "BTLS",
  CiliumNetworkPolicy: "CNP",
  CiliumClusterwideNetworkPolicy: "CCNP",
};

// Policy kinds map onto the shared tone system (theme-aware in every mode) —
// the tones are categorical here, one per kind family, not semantic states.
const TONE: Record<string, Tone> = {
  ClientTrafficPolicy: "info",
  BackendTrafficPolicy: "neutral",
  SecurityPolicy: "success",
  EnvoyExtensionPolicy: "warning",
  BackendTLSPolicy: "danger",
  CiliumNetworkPolicy: "muted",
  CiliumClusterwideNetworkPolicy: "muted",
};

// The chip summary is feature names joined by " + " (the delimiter never appears
// inside a feature name). Show at most the first two features, then "+N" for the
// rest, so a busy policy collapses to e.g. "retries + timeout +1" instead of an
// ellipsis-clipped wall.
export function chipSummary(summary: string): string {
  if (!summary) return "";
  const feats = summary.split(" + ");
  if (feats.length <= 2) return feats.join(" + ");
  return `${feats.slice(0, 2).join(" + ")} +${feats.length - 2}`;
}

export function PolicyChip({ p, align = "left" }: { p: PolicyRefDTO; align?: "left" | "right" }) {
  const abbr = ABBREV[p.kind] ?? p.kind;
  const tone = TONE[p.kind] ?? "muted";
  const c = { fg: toneColor[tone], bg: toneBg[tone] };
  const [hover, setHover] = useState(false);
  return (
    <span
      style={{ position: "relative", display: "inline-flex" }}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
    >
      <span
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: 4,
          fontSize: 9,
          padding: "1px 5px",
          borderRadius: 3,
          fontFamily: "var(--font-mono)",
          color: c.fg,
          background: c.bg,
          border: p.inferred ? `0.5px dashed ${c.fg}` : "0.5px solid transparent",
          cursor: "default",
          maxWidth: 160,
          overflow: "hidden",
          textOverflow: "ellipsis",
          whiteSpace: "nowrap",
        }}
      >
        <b style={{ fontWeight: 700 }}>{abbr}</b>
        {p.summary && <span>{chipSummary(p.summary)}</span>}
        {p.inferred && <span style={{ opacity: 0.7 }}>~</span>}
      </span>
      {hover && (
        <span
          role="tooltip"
          style={{
            position: "absolute",
            bottom: "100%",
            ...(align === "right" ? { right: 0 } : { left: 0 }),
            marginBottom: 4,
            zIndex: 50,
            minWidth: 260,
            maxWidth: 360,
            padding: "8px 10px",
            borderRadius: 6,
            border: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-tertiary)",
            color: "var(--color-text-primary)",
            fontSize: 10,
            lineHeight: 1.5,
            boxShadow: "0 4px 14px rgba(0,0,0,.35)",
            pointerEvents: "none",
          }}
        >
          {p.inferred && (
            <div style={{ color: "var(--color-text-tertiary)", marginBottom: 4, fontStyle: "italic" }}>
              inferred: matched by Service selector, not a Gateway API attachment{p.match ? ` · via: ${p.match}` : ""}
            </div>
          )}
          <div style={{ fontWeight: 600, marginBottom: p.details.length ? 4 : 0 }}>
            {p.kind} {p.namespace}/{p.name}
          </div>
          {p.details.map((d, i) => (
            <div key={i} style={{ color: "var(--color-text-secondary)" }}>
              {d.key}: {d.value}
            </div>
          ))}
        </span>
      )}
    </span>
  );
}
