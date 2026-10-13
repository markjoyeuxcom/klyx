import type { CSSProperties, ReactNode } from "react";
import { IconLock } from "@tabler/icons-react";
import { useMemo } from "react";
import type { ClusterDTO, MeshGraphDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { useHealth } from "../store/health";
import { type Tone, StatusDot, toneColor, toneBg, toneBorder } from "../chrome/primitives";
import { checkingText, clusterAttention, lineText, mastReading, provenance, type Area, type ClusterAttention, type Line } from "../nav/clusterAttention";
import { healthGaps, healthReading, spineLamp, unreachable as isUnreachable } from "../nav/fleetAttention";

type Signal = {
  label: string;
  value: string;
  tone: Tone;
  fraction: number | null;
  title?: string;
  // lines: a list shown one entry per line (the unverified areas), each
  // clipped to its line with its full text on hover.
  lines?: string[];
};

// UNVERIFIED_LINES caps the unverified list on a card; the rest are on the
// cluster's Overview.
const UNVERIFIED_LINES = 6;

// clusterDiagnosticScore orders the board (health 1c slice 5 §4) in bands:
// unreachable, danger areas, warning areas, a stale or degraded connection,
// gaps (a checking part is one), quiet; within a band, more areas needing attention
// first (the board then sorts by name).
export function clusterDiagnosticScore(c: ClusterDTO, m: ClusterAttention | undefined): number {
  if (isUnreachable(c)) return 10000;
  const r = m && !m.ended ? mastReading(m) : null;
  if (r?.state === "attention") return (r.tone === "danger" ? 5000 : 4000) + Math.min(99, r.areas) * 10;
  if (c.state === "Stale" || c.state === "Degraded") return 3000;
  if (!m || healthGaps(m)) return 2000;
  return 0;
}

// useClusterModel is one cluster's attention model (the slice 4 model; the
// open cluster's includes its lens rows).
function useClusterModel(c: ClusterDTO): { m: ClusterAttention; summaryAt: string | null } {
  const st = useHealth((s) => s);
  return useMemo(() => ({ m: clusterAttention(st, c.name, c), summaryAt: st.summaries[c.name]?.evaluatedAt ?? null }), [st, c]);
}

// ClusterCard - one dense fleet-board ledger. The rows mirror the mockup:
// workload health, GitOps, Gateway API, ClusterMesh, and freshness.
export function ClusterCard({ c }: { c: ClusterDTO }) {
  const openCluster = useFleet((s) => s.openCluster);
  const mesh = useFleet((s) => s.mesh);
  const { m, summaryAt } = useClusterModel(c);
  const unreachable = isUnreachable(c);
  // The card is the cluster's open control: a button for the keyboard too
  // (Codex UI review 2026-10-13), with Enter and Space like any button. Its
  // contents are text only, so nothing interactive nests inside it.
  const asButton = {
    role: "button" as const,
    tabIndex: 0,
    "aria-label": `Open cluster ${c.name}`,
    onClick: () => openCluster(c.name),
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        openCluster(c.name);
      }
    },
  };

  if (unreachable) {
    const displayName = clusterDisplayName(c.name);
    return (
      <div
        data-testid={`cluster-card-${c.name}`}
        {...asButton}
        style={{
          background: "var(--color-background-primary)",
          border: "0.5px dashed var(--color-border-secondary)",
          borderRadius: 3,
          padding: "10px 12px",
          cursor: "pointer",
          color: "var(--color-text-tertiary)",
          minWidth: 0,
          overflow: "hidden",
          boxSizing: "border-box",
        }}
      >
        <div style={{ display: "flex", alignItems: "baseline", gap: 6, minWidth: 0 }}>
          <span title={c.name} style={{ fontFamily: "var(--font-mono)", fontWeight: 500, fontSize: 13, color: "var(--color-text-secondary)", ...ellipsis }}>{displayName}</span>
          <span style={{ fontSize: 10, flexShrink: 0 }}>{c.state.toLowerCase()} · {ago(c.ageSeconds)}</span>
          {c.protected && <span title="protected" style={{ marginLeft: "auto", display: "inline-flex" }}><IconLock size={13} stroke={1.5} /></span>}
        </div>
        <div style={{ fontSize: 10, marginTop: 6, ...ellipsis }}>{c.reason || "no connection"}</div>
        <div style={{ fontSize: 10, marginTop: 14 }}>reconnects automatically when reachable</div>
      </div>
    );
  }

  const signals = [
    ...healthSignals(m),
    meshSignal(mesh, c.name),
    freshnessSignal(c),
  ];
  const lamp = spineLamp(c, m);
  const rail = lamp.tone;
  const badge = cardBadge(m);
  const displayName = clusterDisplayName(c.name);

  return (
    <div
      data-testid={`cluster-card-${c.name}`}
      {...asButton}
      style={{
        // Module face (Signal Bench): the lamp in the name plate carries the
        // severity the 3px rail used to - the machining is the card's edge.
        background: "var(--color-background-primary)",
        border: "0.5px solid var(--color-border-tertiary)",
        borderTop: "1px solid var(--bench-edge-hi, var(--color-border-tertiary))",
        boxShadow: "var(--bench-module-shadow, none)",
        borderRadius: 6,
        padding: 0,
        cursor: "pointer",
        display: "grid",
        gridTemplateRows: "auto 1fr auto",
        minHeight: 232,
        minWidth: 0,
        overflow: "hidden",
        boxSizing: "border-box",
      }}
    >
      <div style={{ display: "flex", alignItems: "flex-start", gap: 9, padding: "11px 12px 10px", borderBottom: "0.5px solid var(--color-border-tertiary)", minWidth: 0 }}>
        <span style={{ marginTop: 4 }}><StatusDot size={8} tone={rail} /></span>
        <div style={{ minWidth: 0, flex: 1 }}>
          <div title={c.name} style={{ fontFamily: "var(--font-mono)", fontWeight: 700, fontSize: 13, ...ellipsis }}>{displayName}</div>
          <div style={{ marginTop: 3, fontSize: 10, color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)", ...ellipsis }}>
            {clusterMeta(c)}
          </div>
          {/* Counter wells: the two numbers an operator reads first sit IN
              the bench, not in a text run. */}
          <div style={{ marginTop: 8, display: "flex", gap: 6, minWidth: 0 }}>
            <span style={{ flex: 1, minWidth: 0, textAlign: "right", padding: "3px 8px 2px", borderRadius: "var(--radius-sm)", background: "var(--bench-well-bg, var(--color-background-secondary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))" }}>
              <span style={{ display: "block", fontFamily: "var(--font-mono)", fontSize: 13, fontWeight: 600, color: c.nodesReady < c.nodesTotal ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>{c.nodesReady}/{c.nodesTotal}</span>
              <span style={{ display: "block", fontSize: 8, letterSpacing: "0.1em", textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>nodes</span>
            </span>
            <span style={{ flex: 1, minWidth: 0, textAlign: "right", padding: "3px 8px 2px", borderRadius: "var(--radius-sm)", background: "var(--bench-well-bg, var(--color-background-secondary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))" }}>
              <span style={{ display: "block", fontFamily: "var(--font-mono)", fontSize: 13, fontWeight: 600, color: c.podsStale ? "var(--color-text-tertiary)" : undefined }}
                title={c.podsKnown === false ? "no complete pod read yet" : c.podsStale ? "last good count - the pod read is failing" : undefined}>{c.podsKnown === false ? "—" : c.pods}</span>
              <span style={{ display: "block", fontSize: 8, letterSpacing: "0.1em", textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>pods</span>
            </span>
          </div>
        </div>
        <div style={{ display: "flex", alignItems: "flex-start", gap: 6, flexShrink: 0, maxWidth: "46%", minWidth: 0 }}>
          {badge && <Badge tone={badge.tone}>{badge.label}</Badge>}
          {c.protected && (
            <span title="protected" style={{
              display: "inline-flex",
              alignItems: "center",
              gap: 4,
              color: "var(--color-text-warning)",
              border: "0.5px solid var(--color-border-warning)",
              background: "var(--color-background-warning)",
              fontFamily: "var(--font-mono)",
              fontSize: 10,
              padding: "2px 6px",
            }}>
              <IconLock size={12} stroke={1.5} /> prd lock
            </span>
          )}
        </div>
      </div>

      <div style={{ padding: "11px 12px 12px", display: "grid", gap: 8 }}>
        {signals.map((s) => <SignalRow key={s.label} signal={s} />)}
      </div>

      <div style={{ padding: "8px 12px", borderTop: "0.5px solid var(--color-border-tertiary)", fontSize: 11, fontFamily: "var(--font-mono)", color: "var(--color-text-tertiary)", ...ellipsis }}>
        {diagnosticReason(c, m)}{summaryAt && !m.ended ? ` · summary as of ${new Date(summaryAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}` : ""}
      </div>
    </div>
  );
}

// SignalRow: label and value in words. No bar: these signals have no honest
// denominator, and a bar without one is decoration posing as measurement.
// Only warnings and faults take colour.
// A value wraps to two lines before it clips: what is wrong stays visible on
// the card, not only in a tooltip (UI review 2026-10-13).
function SignalRow({ signal }: { signal: Signal }) {
  const lit = signal.tone === "danger" || signal.tone === "warning";
  return (
    <div title={signal.title ?? signal.value} style={{ display: "grid", gridTemplateColumns: "112px minmax(0, 1fr)", alignItems: "baseline", gap: 10, fontFamily: "var(--font-mono)", fontSize: 11, minWidth: 0 }}>
      <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{signal.label}</span>
      {signal.lines ? (
        <span style={{ display: "grid", gap: 2, minWidth: 0, color: lit ? toneColor[signal.tone] : signal.tone === "muted" ? "var(--color-text-tertiary)" : "var(--color-text-secondary)" }}>
          {signal.lines.slice(0, UNVERIFIED_LINES).map((l) => <span key={l} title={l} style={{ ...ellipsis }}>{l}</span>)}
          {signal.lines.length > UNVERIFIED_LINES && <span>+{signal.lines.length - UNVERIFIED_LINES} more on the cluster's Overview</span>}
        </span>
      ) : (
        <span style={{ color: lit ? toneColor[signal.tone] : signal.tone === "muted" ? "var(--color-text-tertiary)" : "var(--color-text-secondary)",
          overflow: "hidden", overflowWrap: "anywhere", display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical" }}>{signal.value}</span>
      )}
    </div>
  );
}

// Deliberately NOT the Badge primitive: the fleet-board verdict chip is mono,
// 10px, square-cornered, and ellipsis-clipped inside a 46%-max header slot —
// visually distinct from Badge's filled variant. It shares the tone maps.
function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span style={{
      color: toneColor[tone],
      border: `0.5px solid ${toneBorder[tone]}`,
      background: toneBg[tone],
      fontFamily: "var(--font-mono)",
      fontSize: 10,
      padding: "2px 7px",
      ...ellipsis,
    }}>
      {children}
    </span>
  );
}

// The health signals (health 1c slice 5 §4): each from the model, in its
// area's own vocabulary and never summed across areas or tools; each line's
// title is its own provenance. Quiet is neutral, never green.
const toneOf = (l: Line): Tone => (l.state === "attention" ? l.tone ?? "warning" : l.state === "checking" || l.state === "unreadable" ? "muted" : "neutral");
const worst = (tones: Tone[]): Tone => (tones.includes("danger") ? "danger" : tones.includes("warning") ? "warning" : tones.every((t) => t === "muted") && tones.length > 0 ? "muted" : "neutral");
// shown: an area's lit lines, else its primary line
const shown = (a: Area) => (a.lines.some((l) => l.state === "attention") ? a.lines.filter((l) => l.state === "attention") : a.lines.slice(0, 1));
const lineOf = (a: Area, l: Line, prefix = "") => `${prefix}${a.lines.length > 1 ? `${l.label.toLowerCase()} ` : ""}${lineText(l)}`;

// toolValue: one tool's reading - its lines, "checking…" while nothing has
// answered, or null: "not detected" only on confirmed absence (no area).
function toolValue(m: ClusterAttention, task: string, prefix: string): { value: string; tones: Tone[]; title: string } | null {
  const a = m.areas.find((x) => x.id === task);
  const checking = a ? checkingText(a) : null;
  if (a && checking) return { value: `${prefix}${checking}`, tones: ["muted"], title: a.lines.map((l) => provenance(a, l)).join("; ") };
  if (a) {
    const ls = shown(a);
    const ctl = a.controller?.state === "not ready" ? [`${prefix}controller not ready`] : [];
    return { value: [...ctl, ...ls.map((l) => lineOf(a, l, prefix))].join(" · ") || `${prefix}controller ${a.controller?.state ?? ""}`, tones: [...(ctl.length ? ["danger" as Tone] : []), ...ls.map(toneOf)], title: ls.map((l) => provenance(a, l)).join("; ") };
  }
  return null;
}

function healthSignals(m: ClusterAttention): Signal[] {
  if (m.ended) return [{ label: "health", value: healthReading(m)[0], tone: "muted", fraction: null }];
  if (mastReading(m).state === "checking") return [{ label: "health", value: "checking…", tone: "muted", fraction: null }];
  const out: Signal[] = [];
  const w = toolValue(m, "workloads", "");
  out.push(w ? { label: "workloads", value: w.value, tone: worst(w.tones), fraction: null, title: w.title } : { label: "workloads", value: "checking…", tone: "muted", fraction: null });
  // One line per GitOps tool, in its own vocabulary - never combined or
  // summed (UI review 2026-10-13: the shared gitops line clipped Argo off).
  const flux = toolValue(m, "flux", "");
  const argo = toolValue(m, "argo", "");
  if (flux) out.push({ label: "flux", value: flux.value, tone: worst(flux.tones), fraction: null, title: flux.title });
  if (argo) out.push({ label: "argo cd", value: argo.value, tone: worst(argo.tones), fraction: null, title: argo.title });
  if (!flux && !argo) out.push({ label: "gitops", value: "not detected", tone: "muted", fraction: null });
  const gw = toolValue(m, "gateway", "");
  out.push(gw ? { label: "gateway", value: gw.value, tone: worst(gw.tones), fraction: null, title: gw.title } : { label: "gateway", value: "not detected", tone: "muted", fraction: null });
  // Every other area needing attention gets its own line, by name (evidence
  // is never attention); each keeps its own provenance (§4).
  for (const a of m.areas) {
    if (["workloads", "flux", "argo", "gateway"].includes(a.id) || a.state !== "attention") continue;
    const parts = (a.controller?.state === "not ready" ? [`controller not ready${a.controller.reason ? ` (${a.controller.reason})` : ""}`] : []).concat(a.lines.filter((l) => l.state === "attention").map((l) => lineOf(a, l)));
    out.push({ label: a.label.toLowerCase(), value: parts.join(" · "), tone: a.tone === "danger" ? "danger" : "warning", fraction: null,
      title: a.lines.filter((l) => l.state === "attention").map((l) => `${a.label}${a.lines.length > 1 ? ` ${l.label}` : ""}: ${provenance(a, l)}`).concat(a.controller?.reason ? [a.controller.reason] : []).join("\n") });
  }
  // every expected area's coverage, lit or not (§4): each gap line in full
  // (lineText: its gaps, re-read state and failure) with its provenance, an
  // unknown controller with its reason
  const gaps: { text: string; prov: string }[] = [];
  for (const a of m.areas) {
    if (a.controller?.state === "unknown") gaps.push({ text: `${a.label}: controller health unknown${a.controller.reason ? ` (${a.controller.reason})` : ""}`, prov: "" });
    for (const l of a.lines) {
      if (l.state === "attention") continue; // on its tool's row or under "also", gaps included
      if (l.state !== "checking" && l.state !== "unreadable" && l.gaps.length === 0) continue;
      gaps.push({ text: `${a.label}: ${a.lines.length > 1 ? `${l.label} ` : ""}${lineText(l)}`, prov: provenance(a, l) });
    }
  }
  // One entry per line, each named with its reason (§4); a long list ends
  // with how many more the cluster's Overview shows.
  if (gaps.length > 0) out.push({ label: "unverified", value: gaps.map((g) => g.text).join(" · "), lines: gaps.map((g) => g.text), tone: "muted", fraction: null, title: gaps.map((g) => (g.prov ? `${g.text} (${g.prov})` : g.text)).join("\n") });
  return out;
}

function meshSignal(graph: MeshGraphDTO | null, cluster: string): Signal {
  if (!graph) return { label: "mesh", value: "unreadable", tone: "muted", fraction: null };
  const node = graph.nodes.find((n) => n.cluster === cluster);
  if (!node || node.state === "unavailable") return { label: "mesh", value: "no ClusterMesh", tone: "muted", fraction: null };
  if (node.state === "enabled") return { label: "mesh", value: "no peers", tone: "info", fraction: 0.35 };

  const peers: string[] = [];
  let asym = false;
  let offFleet = 0;
  for (const e of graph.edges) {
    const other = e.a === cluster ? e.b : e.b === cluster ? e.a : null;
    if (!other) continue;
    const on = graph.nodes.find((n) => (n.cluster || n.name) === other);
    if (on && !on.present) { offFleet++; continue; }
    peers.push(other);
    if (!e.mutual) asym = true;
  }
  const suffix = peers.length === 1 ? "peer" : "peers";
  return {
    label: "mesh",
    value: `${peers.join(", ") || "enabled"} ${suffix}${offFleet > 0 ? ` +${offFleet}` : ""}`,
    tone: asym || offFleet > 0 ? "warning" : "success",
    fraction: asym || offFleet > 0 ? 0.72 : 1,
    title: asym ? "asymmetric peering detected" : undefined,
  };
}

// freshnessSignal is the CONNECTION axis in words - never a bar, never
// "healthy". A degraded connection is connected with subsystem notes.
function freshnessSignal(c: ClusterDTO): Signal {
  if (c.state === "Stale") return { label: "connection", value: "stale", tone: "warning", fraction: null, title: c.reason };
  if (c.state === "Degraded") return { label: "connection", value: "connected · notes", tone: "warning", fraction: null, title: c.reason };
  if (c.state === "Synced") return { label: "connection", value: "connected", tone: "neutral", fraction: null };
  if (c.state === "Connecting") return { label: "connection", value: "connecting", tone: "muted", fraction: null };
  return { label: "connection", value: c.state.toLowerCase(), tone: "muted", fraction: null };
}

// cardBadge counts AREAS needing attention - never objects, never a
// whole-cluster sum. No "healthy" badge: nothing here asserts health.
function cardBadge(m: ClusterAttention): { label: string; tone: Tone } | null {
  if (m.ended) return null;
  const r = mastReading(m);
  if (r.state !== "attention") return null;
  return { label: `${r.areas} area${r.areas === 1 ? "" : "s"} need${r.areas === 1 ? "s" : ""} attention`, tone: r.tone ?? "warning" };
}

// diagnosticReason says why the card sits where it does - what is known,
// never "healthy" on missing or partial evidence.
function diagnosticReason(c: ClusterDTO, m: ClusterAttention): string {
  const r = mastReading(m);
  if (m.ended) return healthReading(m)[0];
  if (r.state === "attention") return `sorted first: ${r.areas} area${r.areas === 1 ? "" : "s"} need${r.areas === 1 ? "s" : ""} attention`;
  if (c.state === "Stale") return "sorted early: cluster watches are stale";
  if (c.state === "Degraded") return c.reason || "sorted early: cluster capability health is degraded";
  if (r.state === "checking") return "health checking…";
  if (healthGaps(m)) return "health partly unverified";
  return "nothing raised";
}

function clusterMeta(c: ClusterDTO): string {
  return [c.provider, c.region, c.protected ? "protected" : "", c.version].filter(Boolean).join(" / ");
}

export function clusterDisplayName(name: string): string {
  const eksClusterMarker = ":cluster/";
  const markerAt = name.lastIndexOf(eksClusterMarker);
  if (markerAt < 0) return name;
  return name.slice(markerAt + eksClusterMarker.length) || name;
}

// Deliberately NOT the primitive ellipsis: this one adds minWidth: 0 because
// every use here is a grid/flex child that must be allowed to shrink.
const ellipsis: CSSProperties = {
  minWidth: 0,
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
};

function ago(s: number): string {
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : `${Math.floor(s / 3600)}h`;
}
