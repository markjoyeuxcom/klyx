import { Fragment, useContext, useState } from "react";
import { InspectorBlock, InspectorTemplate } from "../layout/InspectorLayout";
import { IconChevronRight } from "@tabler/icons-react";
import { Aspect, aspectOfVerdict, StatusDot, toneColor, type Tone } from "../chrome/primitives";
import type { AspectKind } from "../chrome/primitives/Aspect";
import { ClampedText } from "../chrome/ClampedText";
import { conditionAspect, conditionState, NEGATIVE_CONDITIONS } from "../chrome/conditions";
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
  const inTemplate = useContext(InspectorTemplate);
  const label = count === undefined ? title : `${title} (${count})`;
  if (inTemplate) {
    return <InspectorBlock title={title} count={count} defaultOpen={!(collapsible && defaultCollapsed)}>{children}</InspectorBlock>;
  }

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
// InspectorFacts — the panel's numeric facts as mini counter wells (Signal
// Bench): value in an inset window with tabular numerals, engraved label
// beneath, tone carrying state. Prose stays in kv rows; NUMBERS get physical
// form so the eye finds "7 restarts" without reading four lines of text.
export function InspectorFacts({ facts }: {
  facts: ReadonlyArray<{ label: string; value: string | number; tone?: Tone } | null | false>;
}) {
  const cells = facts.filter((f): f is { label: string; value: string | number; tone?: Tone } => Boolean(f));
  if (cells.length === 0) return null;
  return (
    <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
      {cells.map((f) => (
        <span key={f.label} style={{ minWidth: 64, display: "flex", flexDirection: "column", gap: 3 }}>
          <span style={{
            padding: "3px 9px 2px",
            textAlign: "right",
            fontFamily: "var(--font-mono)",
            fontVariantNumeric: "tabular-nums",
            fontSize: 13,
            fontWeight: 600,
            color: f.tone ? toneColor[f.tone] : "var(--color-text-primary)",
            borderRadius: "var(--radius-sm)",
            background: "var(--line-sunk, var(--color-background-secondary))",
            boxShadow: "var(--line-well-ring, inset 0 0 0 1px var(--color-border-tertiary))",
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
          }} title={String(f.value)}>{f.value}</span>
          <span style={{ fontSize: 8, letterSpacing: "0.1em", textTransform: "uppercase", fontWeight: 600, textAlign: "right", paddingRight: 2, color: "var(--color-text-tertiary)" }}>{f.label}</span>
        </span>
      ))}
    </div>
  );
}

// InspectorKV — labeled prose rows for facts that are lists or free text
// (hostnames, backends, provisioner). Numbers get physical form via
// InspectorFacts; prose stays in kv rows. The shared row primitive every kind
// reuses, so a new kind's facts never need a bespoke React section.
export function InspectorKV({ rows }: {
  rows: ReadonlyArray<{ label: string; value: string; tone?: Tone } | null | false>;
}) {
  const cells = rows.filter((r): r is { label: string; value: string; tone?: Tone } => Boolean(r));
  const inTemplate = useContext(InspectorTemplate);
  if (cells.length === 0) return null;
  if (inTemplate) {
    // Template anatomy: labels keep their real case (a ConfigMap key "a" is
    // not "A" - keys are case-sensitive identifiers) and values wrap instead
    // of hiding behind a tooltip.
    return (
      <div className="insp-kv">
        {cells.map((r, i) => (
          <Fragment key={`${r.label}-${i}`}>
            <span className="t-identifier" style={{ overflowWrap: "anywhere" }}>{r.label}</span>
            <span className="t-identifier" style={{ color: r.tone ? toneColor[r.tone] : "var(--color-text-primary)", overflowWrap: "anywhere" }}>{r.value}</span>
          </Fragment>
        ))}
      </div>
    );
  }
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 3 }}>
      {cells.map((r, i) => (
        <div key={`${r.label}-${i}`} style={{ display: "flex", gap: 8, alignItems: "baseline", fontSize: 11, minWidth: 0 }}>
          <span style={{ fontSize: 9, letterSpacing: "0.08em", textTransform: "uppercase", fontWeight: 600, color: "var(--color-text-tertiary)", flexShrink: 0, minWidth: 92 }}>{r.label}</span>
          <span style={{ fontFamily: "var(--font-mono)", color: r.tone ? toneColor[r.tone] : "var(--color-text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={r.value}>{r.value}</span>
        </div>
      ))}
    </div>
  );
}

// InspectorMeters — used/total ratios as inset bars (Signal Bench: data in a
// well). The value text is kept for the precise reading; the bar is the glance.
// Fill is clamped to 100% so an over-budget quota never overflows the track -
// the danger tone + the real "(NNN%)" in the value still convey the overage.
export function InspectorMeters({ meters }: {
  meters: ReadonlyArray<{ label: string; value: string; pct: number; tone?: Tone }>;
}) {
  if (meters.length === 0) return null;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      {meters.map((m, i) => {
        const fill = Math.max(0, Math.min(100, m.pct));
        return (
          <div key={`${m.label}-${i}`} style={{ display: "flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
            <div style={{ display: "flex", gap: 8, alignItems: "baseline", fontSize: 11, minWidth: 0 }}>
              <span style={{ fontSize: 9, letterSpacing: "0.08em", textTransform: "uppercase", fontWeight: 600, color: "var(--color-text-tertiary)", flexShrink: 0, minWidth: 92, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={m.label}>{m.label}</span>
              <span style={{ marginLeft: "auto", fontFamily: "var(--font-mono)", fontVariantNumeric: "tabular-nums", color: m.tone ? toneColor[m.tone] : "var(--color-text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={m.value}>{m.value}</span>
            </div>
            <span style={{
              height: 5,
              borderRadius: "var(--radius-sm)",
              overflow: "hidden",
              background: "var(--line-sunk, var(--color-background-secondary))",
              boxShadow: "var(--line-well-ring, inset 0 0 0 1px var(--color-border-tertiary))",
            }}>
              <span style={{ display: "block", height: "100%", width: `${fill}%`, background: m.tone ? toneColor[m.tone] : "var(--color-text-success)" }} />
            </span>
          </div>
        );
      })}
    </div>
  );
}

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
        background: "var(--line-sunk, var(--color-background-secondary))",
        boxShadow: "var(--line-well-ring, inset 0 0 0 1px var(--color-border-tertiary))",
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
      <span aria-hidden style={{ width: 12, height: 12, borderRadius: "50%", background: attention > 0 ? "var(--color-text-warning)" : "var(--line-track, var(--color-border-secondary))" }} />
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

// InspectorStatus is the inspector's verdict (The Line slice 13c.2): its
// mark, then the evaluated label as the headline in ink-1 - the mark alone
// carries the danger or look-at colour - and under it every reason and
// qualifier the lens passes, whole, never cut to a tooltip. `kind` is the
// verdict's role kind when the caller has one: it picks the mark (The Line
// aspects), so a pause or work in progress keeps its own shape (Codex
// review of f2e47cc).
export function InspectorStatus({ tone, kind, label, reason, detail }: {
  tone: Tone;
  kind?: string;
  label: string;
  reason?: string;
  detail?: string;
}) {
  const sub = [reason, detail].filter(Boolean).join(" · ");
  return (
    <div className="verdict-head" data-tone={tone}>
      <span className="verdict-mark">{kind ? <Aspect kind={aspectOfVerdict(kind)} size={12} /> : <StatusDot size={10} tone={tone} />}</span>
      <div className="verdict-text">
        <p className="verdict-headline">{label}</p>
        {sub && <span className="verdict-sub">{sub}</span>}
      </div>
    </div>
  );
}

// Condition polarity lives in one registry (chrome/conditions.ts, The Line
// slice 15 design review 1): a known type in its bad state is danger, in its
// good state success, and Unknown or an unknown type is info - never assumed
// positive. NEGATIVE_POLARITY stays exported for older callers.
export const NEGATIVE_POLARITY = NEGATIVE_CONDITIONS;

export function conditionColor(status: string, type?: string): string {
  const s = conditionState(type ?? "", status);
  return s === "good" ? "var(--color-text-success)" : s === "bad" ? "var(--color-text-danger)" : "var(--color-text-info)";
}

// aspectOfColor reads a caller's colour override as a mark: a caller with
// stricter semantics (a node treats Unknown pressure as a problem) keeps its
// rule, and only the presentation changes.
function aspectOfColor(color: string): AspectKind {
  if (color.includes("danger")) return "fault";
  if (color.includes("warning")) return "attention";
  if (color.includes("tertiary")) return "checking";
  return "none";
}

/**
 * InspectorConditions - one conditions rendering for every panel, at the
 * slice-13 scale (The Line slice 15b). Each condition is a row: its mark,
 * type, status word, reason and the age of its last transition; the message
 * sits below and wraps, clamped past three lines. A condition recorded for
 * an older generation says so. Marks come from the polarity registry;
 * `colorOf` overrides it where a kind has stricter semantics.
 */
export function InspectorConditions({ conditions, colorOf, generation }: { conditions: ConditionDTO[]; colorOf?: (c: ConditionDTO) => string; generation?: number }) {
  if (conditions.length === 0) {
    return <span className="cond-empty">No conditions reported.</span>;
  }
  const aspectOf = (c: ConditionDTO): AspectKind => (colorOf ? aspectOfColor(colorOf(c)) : conditionAspect(c.type, c.status));
  return (
    <ul className="conds">
      {conditions.map((c, i) => {
        const aspect = aspectOf(c);
        const og = c.observedGeneration ?? 0;
        const behind = generation !== undefined && generation > 0 && og > 0 && og < generation;
        return (
          <li key={`${c.scope ?? ""}/${c.type}/${i}`} className="cond" data-aspect={aspect}>
            <span className="cond-mark"><Aspect kind={aspect} label={aspect === "none" ? undefined : `${c.type} ${c.status}`} /></span>
            <div className="cond-main">
              <div className="cond-line">
                <span className="cond-type">{c.type}</span>
                {/* Scope attributes a nested condition to its sub-object (Gateway API
                    parent/listener) so two "Accepted" blocks are distinguishable. */}
                {c.scope && <span className="cond-scope">{c.scope}</span>}
                {c.status && <span className="cond-status">{c.status}</span>}
                {c.reason && <span className="cond-reason">{c.reason}</span>}
                {c.lastTransition && <span className="cond-age"><Age iso={c.lastTransition} suffix=" ago" /></span>}
                {behind && <span className="cond-gen" title="The condition was last written for an older generation than the object's.">for generation {og} of {generation}</span>}
              </div>
              {c.message && <ClampedText className="cond-msg" text={c.message} lines={3} />}
            </div>
          </li>
        );
      })}
    </ul>
  );
}

/** InspectorEvents — one event rendering for every panel. */
// wrap=true (migrated inspectors, redesign spec §3): reason and message wrap in
// full instead of truncating behind a tooltip.
export function InspectorEvents({ events, emptyText = "No events.", wrap = false }: { events: EventDTO[]; emptyText?: string; wrap?: boolean }) {
  if (events.length === 0) {
    return <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>{emptyText}</span>;
  }
  return (
    <>
      {events.map((e, i) => (
        <div key={i} style={{ display: "flex", gap: 6, alignItems: "baseline", fontSize: 11, padding: "1px 0", color: e.type === "Warning" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
          <span style={{ width: 7, height: 7, borderRadius: "50%", background: e.type === "Warning" ? "var(--color-text-danger)" : "var(--color-text-tertiary)", display: "inline-block", flexShrink: 0, alignSelf: "center" }} />
          {wrap ? (
            <span style={{ flex: 1, minWidth: 0, overflowWrap: "anywhere", lineHeight: "16px" }}>
              <span style={{ fontWeight: 600 }}>{e.reason}</span> {e.message}
            </span>
          ) : (
            <>
              <span style={{ fontWeight: 500, flexShrink: 0, maxWidth: 130, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={e.reason}>{e.reason}</span>
              <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", flex: 1, minWidth: 0 }} title={e.message}>{e.message}</span>
            </>
          )}
          {e.count > 1 && (
            <span title={`${e.count.toLocaleString()} occurrences`} style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>×{compactCount(e.count)}</span>
          )}
          <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}><Age iso={e.lastSeen} fallback="" /></span>
        </div>
      ))}
    </>
  );
}

// Kbd — the inline key hint on a primary action button. Only advertise a key
// that a live binding in the same view actually handles (never a dead key).
export function Kbd({ k }: { k: string }) {
  return (
    <kbd style={{
      fontFamily: "var(--font-mono)",
      fontSize: 9,
      padding: "0 4px",
      background: "var(--color-background-secondary)",
      border: "1px solid var(--color-border-secondary)",
      borderRadius: 3,
      color: "var(--color-text-tertiary)",
    }}>{k}</kbd>
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
