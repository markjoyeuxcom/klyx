import { useState } from "react";
import { IconChevronRight } from "@tabler/icons-react";
import { StatusDot, toneBg, toneBorder, toneColor, type Tone } from "../chrome/primitives";
import { Age } from "../chrome/Age";
import { compactCount } from "../chrome/format";
import type { ConditionDTO, EventDTO } from "../store/fleet";

export const inspectorStateBox: React.CSSProperties = {
  color: "var(--color-text-secondary)",
  fontSize: 13,
  padding: 18,
  border: "0.5px solid var(--color-border-tertiary)",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-primary)",
};

export function InspectorSection({
  title,
  children,
  count,
  collapsible = false,
  defaultCollapsed = false,
}: {
  title: string;
  children: React.ReactNode;
  count?: number;
  collapsible?: boolean;
  defaultCollapsed?: boolean;
}) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const label = count === undefined ? title : `${title} (${count})`;

  if (!collapsible) {
    return (
      <div style={sectionShell}>
        <div style={sectionTitle}>{label}</div>
        <div style={sectionBody}>{children}</div>
      </div>
    );
  }

  return (
    <div style={sectionShell}>
      <button
        type="button"
        onClick={() => setCollapsed((v) => !v)}
        aria-expanded={!collapsed}
        style={sectionToggle}
      >
        <span style={{ width: 12, color: "var(--color-text-tertiary)", display: "inline-flex" }}>
          <IconChevronRight size={11} stroke={1.7} style={{ transform: collapsed ? "none" : "rotate(90deg)", transition: "transform 100ms ease" }} />
        </span>
        <span style={sectionTitle}>{label}</span>
      </button>
      {!collapsed && <div style={sectionBody}>{children}</div>}
    </div>
  );
}

export function InspectorTabButton({
  active,
  children,
  onClick,
}: {
  active: boolean;
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button type="button" onClick={onClick} style={tabButton(active)}>
      {children}
    </button>
  );
}

export function InspectorTitle({ title, sub }: { title: string; sub: string }) {
  return (
    <div>
      <div style={titleStyle} title={title}>{title}</div>
      <div style={subTitleStyle} title={sub}>{sub}</div>
    </div>
  );
}

export function InspectorEmpty({ text, style }: { text: string; style?: React.CSSProperties }) {
  return <div style={{ ...inspectorStateBox, ...style }}>{text}</div>;
}

// ---------------------------------------------------------------------------
// Shared panel anatomy. Every side panel presents the same three shapes -
// a headline state, a conditions list, an events list - and each grew its
// own markup. One vocabulary: the state is a tone Badge with an optional
// reason chip (scannable at the top, not buried in key-value rows);
// conditions and events render identically everywhere, with ages ticking
// and counts compact.
// ---------------------------------------------------------------------------

/** InspectorStatus — the state-first opening line of a panel. */
// InspectorIdle — the inspector well when nothing is selected. The panel is
// ALWAYS present (mockup rule: the board is list + inspector, permanently),
// so selecting a row never reflows the list. Idle it stays honest: the
// attention count, what selecting shows, and the keys that do it.
export function InspectorIdle({ subject, attention, hint, width }: {
  subject: string;
  attention: number;
  hint: string;
  width?: number | string;
}) {
  return (
    <aside
      data-testid="inspector-idle"
      style={{
        width: width ?? 360,
        flexShrink: 0,
        minHeight: 0,
        boxSizing: "border-box",
        // grid-hosted callers pass width="100%" and own the gutter themselves
        background: "var(--bench-well-bg, transparent)",
        boxShadow: "var(--bench-well-shadow, none)",
        borderRadius: "var(--radius-sm)",
        padding: "12px 16px",
        marginLeft: width ? 0 : 8,
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        gap: 10,
        textAlign: "center",
        fontFamily: "var(--font-mono)",
        fontSize: 12,
        color: "var(--color-text-secondary)",
      }}
    >
      <span aria-hidden style={{ width: 12, height: 12, borderRadius: "50%", background: attention > 0 ? "var(--color-text-warning)" : "var(--lamp-off, var(--color-border-secondary))", boxShadow: attention > 0 ? "var(--lamp-glow-warning, none)" : "inset 0 1px 1px rgba(0,0,0,0.35)" }} />
      <div style={{ fontWeight: 600, color: attention > 0 ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>
        {attention > 0 ? `${attention} ${subject} need attention` : `all ${subject} quiet`}
      </div>
      <div style={{ fontSize: 11, color: "var(--color-text-tertiary)", maxWidth: 260, lineHeight: 1.6 }}>{hint}</div>
      <div style={{ fontSize: 9, letterSpacing: "0.1em", textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>
        j k nav · ↵ inspect
      </div>
    </aside>
  );
}

// InspectorStatus is the mockup's verdict (Signal Bench): ONE big element -
// lamp + state word in a toned box with an inset ring - and a mono sub-line
// joining reason and detail. Not a row of disconnected chips.
export function InspectorStatus({ tone, label, reason, detail }: {
  tone: Tone;
  label: string;
  reason?: string;
  detail?: string;
}) {
  const sub = [reason, detail].filter(Boolean).join(" · ");
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 9, flexWrap: "wrap", marginBottom: 12 }}>
      <span style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 8,
        fontFamily: "var(--font-mono)",
        fontSize: 12,
        fontWeight: 700,
        padding: "5px 10px",
        borderRadius: 4,
        color: toneColor[tone],
        background: toneBg[tone],
        boxShadow: `inset 0 0 0 1px ${toneBorder[tone]}`,
        whiteSpace: "nowrap",
      }}>
        <StatusDot size={8} tone={tone} />
        <span className={tone === "danger" ? "klyx-danger-prefix" : undefined}>{label}</span>
      </span>
      {sub && (
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 10, color: "var(--color-text-secondary)", minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={sub}>
          {sub}
        </span>
      )}
    </div>
  );
}

// Conditions where True means trouble and False is the healthy state - the
// inverse of the usual Ready-style polarity (HPA ScalingLimited, node
// pressure conditions). Without this a healthy HPA renders a red dot.
export const NEGATIVE_POLARITY = new Set([
  "ScalingLimited",
  "MemoryPressure",
  "DiskPressure",
  "PIDPressure",
  "NetworkUnavailable",
]);

export function conditionColor(status: string, type?: string): string {
  const negative = type !== undefined && NEGATIVE_POLARITY.has(type);
  const healthy = negative ? status === "False" : status === "True";
  const unhealthy = negative ? status === "True" : status === "False";
  return healthy ? "var(--color-text-success)" : unhealthy ? "var(--color-text-danger)" : "var(--color-text-info)";
}

/**
 * InspectorConditions — one conditions rendering for every panel. `colorOf`
 * overrides the polarity rule where a kind has stricter semantics (nodes
 * treat Unknown pressure as a problem, not merely indeterminate).
 */
export function InspectorConditions({ conditions, colorOf }: { conditions: ConditionDTO[]; colorOf?: (c: ConditionDTO) => string }) {
  if (conditions.length === 0) {
    return <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>No conditions reported.</span>;
  }
  const dotColor = colorOf ?? ((c: ConditionDTO) => conditionColor(c.status, c.type));
  return (
    <>
      {conditions.map((c) => (
        <div key={c.type} style={{ display: "flex", gap: 8, alignItems: "baseline", fontSize: 11, padding: "1px 0" }}>
          <span style={{ width: 7, height: 7, borderRadius: "50%", background: dotColor(c), display: "inline-block", flexShrink: 0, alignSelf: "center" }} />
          <span style={{ fontWeight: 500, flexShrink: 0, whiteSpace: "nowrap" }}>{c.type}</span>
          {c.reason && (
            <span style={{ flexShrink: 0, fontSize: 10, color: "var(--color-text-tertiary)", background: "var(--color-background-secondary)", padding: "0 5px", borderRadius: 3, fontFamily: "var(--font-mono)" }}>
              {c.reason}
            </span>
          )}
          <span style={{ color: "var(--color-text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={c.message}>
            {c.message}
          </span>
        </div>
      ))}
    </>
  );
}

/** InspectorEvents — one event rendering for every panel. */
export function InspectorEvents({ events, emptyText = "No events." }: { events: EventDTO[]; emptyText?: string }) {
  if (events.length === 0) {
    return <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>{emptyText}</span>;
  }
  return (
    <>
      {events.map((e, i) => (
        <div key={i} style={{ display: "flex", gap: 6, alignItems: "baseline", fontSize: 11, padding: "1px 0", color: e.type === "Warning" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
          <span style={{ width: 7, height: 7, borderRadius: "50%", background: e.type === "Warning" ? "var(--color-text-danger)" : "var(--color-text-tertiary)", display: "inline-block", flexShrink: 0, alignSelf: "center" }} />
          <span style={{ fontWeight: 500, flexShrink: 0, maxWidth: 130, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={e.reason}>{e.reason}</span>
          <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", flex: 1, minWidth: 0 }} title={e.message}>{e.message}</span>
          {e.count > 1 && (
            <span title={`${e.count.toLocaleString()} occurrences`} style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>×{compactCount(e.count)}</span>
          )}
          <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}><Age iso={e.lastSeen} fallback="" /></span>
        </div>
      ))}
    </>
  );
}

export function MetadataChips({
  title,
  values,
  defaultCollapsed = false,
}: {
  title: string;
  values: Record<string, string> | undefined;
  defaultCollapsed?: boolean;
}) {
  const entries = Object.entries(values ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    <InspectorSection title={title} count={entries.length} collapsible defaultCollapsed={defaultCollapsed}>
      <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
        {entries.map(([k, v]) => (
          <span key={k} title={`${k}=${v}`} style={metadataChip}>
            {k}={v}
          </span>
        ))}
      </div>
    </InspectorSection>
  );
}

const sectionShell: React.CSSProperties = { marginBottom: 12 };

const sectionTitle: React.CSSProperties = {
  fontSize: 10,
  textTransform: "uppercase",
  letterSpacing: 0.5,
  color: "var(--color-text-tertiary)",
};

const sectionBody: React.CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 3,
  fontSize: 12,
};

const sectionToggle: React.CSSProperties = {
  width: "100%",
  display: "flex",
  alignItems: "center",
  gap: 2,
  padding: "0 0 4px",
  border: 0,
  background: "transparent",
  color: "var(--color-text-tertiary)",
  cursor: "pointer",
  textAlign: "left",
};

const metadataChip: React.CSSProperties = {
  maxWidth: "100%",
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  background: "var(--color-background-secondary)",
  color: "var(--color-text-secondary)",
  fontSize: 10,
  padding: "1px 6px",
  borderRadius: 3,
  fontFamily: "var(--font-mono)",
};

const titleStyle: React.CSSProperties = {
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  fontFamily: "var(--font-mono)",
  fontSize: 14,
  fontWeight: 600,
};

const subTitleStyle: React.CSSProperties = {
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  color: "var(--color-text-tertiary)",
  fontSize: 11,
  marginTop: 3,
};

const baseButton: React.CSSProperties = {
  padding: "3px 10px",
  fontSize: 11,
  borderRadius: 4,
  cursor: "pointer",
  border: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-primary)",
  color: "var(--color-text-primary)",
};

function tabButton(active: boolean): React.CSSProperties {
  return {
    ...baseButton,
    color: active ? "var(--color-text-info)" : "var(--color-text-secondary)",
    background: active ? "var(--color-background-info)" : "var(--color-background-primary)",
    border: active ? "0.5px solid var(--color-border-info)" : "0.5px solid var(--color-border-tertiary)",
  };
}
