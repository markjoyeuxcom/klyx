import type { CSSProperties, ReactNode } from "react";
import { IconLock } from "@tabler/icons-react";
import type { ClusterDTO, FleetBoardEntry, MeshGraphDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { type Tone, StatusDot, toneColor, toneBg, toneBorder } from "../chrome/primitives";
import { asOfText, attentionTone, boardAttention } from "../health/verdict";

type Signal = {
  label: string;
  value: string;
  tone: Tone;
  fraction: number | null;
  title?: string;
};

export function clusterDiagnosticScore(c: ClusterDTO, board: FleetBoardEntry | undefined): number {
  if (c.state === "Failed" || c.state === "Unconnected") return 1000;
  let score = 0;
  const attention = boardAttention(board) ?? 0;
  if (attention > 0) score += 500 + Math.min(100, attention * 10);
  if (c.state === "Stale") score += 420;
  if (c.state === "Degraded") score += 360;
  if ((board?.flux?.notReady ?? 0) > 0) score += 260;
  if ((board?.argo?.broken ?? 0) > 0) score += 240;
  if ((board?.gateway?.brokenRoutes ?? 0) > 0 || (board?.gateway?.unprogrammed ?? 0) > 0) score += 180;
  if (c.nodesReady < c.nodesTotal) score += 140;
  if (c.protected) score += 10;
  return score;
}

// ClusterCard - one dense fleet-board ledger. The rows mirror the mockup:
// workload health, GitOps, Gateway API, ClusterMesh, and freshness.
export function ClusterCard({ c }: { c: ClusterDTO }) {
  const openCluster = useFleet((s) => s.openCluster);
  const mesh = useFleet((s) => s.mesh);
  const board: FleetBoardEntry | undefined = useFleet((s) => s.fleetBoard[c.name]);
  const unreachable = c.state === "Failed" || c.state === "Unconnected";

  if (unreachable) {
    const displayName = clusterDisplayName(c.name);
    return (
      <div
        data-testid={`cluster-card-${c.name}`}
        onClick={() => openCluster(c.name)}
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
    workloadSignal(board),
    gitopsSignal(board),
    gatewaySignal(c, board),
    meshSignal(mesh, c.name),
    freshnessSignal(c),
  ];
  const rail = cardRail(c, board, signals);
  const badge = cardBadge(c, board, signals);
  const displayName = clusterDisplayName(c.name);

  return (
    <div
      data-testid={`cluster-card-${c.name}`}
      onClick={() => openCluster(c.name)}
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
              <span style={{ display: "block", fontFamily: "var(--font-mono)", fontSize: 13, fontWeight: 600 }}>{c.pods}</span>
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
        {diagnosticReason(c, board, signals)}
      </div>
    </div>
  );
}

// SignalRow: label and value in words. No bar: these signals have no honest
// denominator, and a bar without one is decoration posing as measurement.
// Only warnings and faults take colour.
function SignalRow({ signal }: { signal: Signal }) {
  const lit = signal.tone === "danger" || signal.tone === "warning";
  return (
    <div title={signal.title ?? signal.value} style={{ display: "grid", gridTemplateColumns: "86px minmax(0, 1fr)", alignItems: "center", gap: 10, fontFamily: "var(--font-mono)", fontSize: 11, minWidth: 0 }}>
      <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{signal.label}</span>
      <span style={{ color: lit ? toneColor[signal.tone] : signal.tone === "muted" ? "var(--color-text-tertiary)" : "var(--color-text-secondary)", ...ellipsis }}>{signal.value}</span>
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

// workloadSignal: workload verdict counts with their entity and coverage -
// "3 of 8 need attention", never a bar without a denominator.
function workloadSignal(board: FleetBoardEntry | undefined): Signal {
  const w = board?.workloads;
  if (!w) return { label: "workloads", value: "unreadable", tone: "muted", fraction: null };
  const { counts, coverage } = w;
  const partial = coverage === "partial" ? " (partial)" : "";
  if (counts.attention > 0) {
    return {
      label: "workloads",
      value: `${counts.attention} of ${counts.total} need attention${partial}`,
      tone: attentionTone(counts) ?? "warning",
      fraction: null,
      title: [`${counts.fault} fault · ${counts.degraded} degraded${counts.unknown ? ` · ${counts.unknown} unknown` : ""}`, asOfText(w)].filter(Boolean).join(" · "),
    };
  }
  if (counts.unknown > 0) {
    return { label: "workloads", value: `${counts.unknown} of ${counts.total} unverified${partial}`, tone: "muted", fraction: null, title: asOfText(w) || undefined };
  }
  return { label: "workloads", value: `${counts.total} · none need attention${partial}`, tone: "neutral", fraction: null, title: asOfText(w) || undefined };
}

function gitopsSignal(board: FleetBoardEntry | undefined): Signal {
  if (!board) return { label: "gitops", value: "unreadable", tone: "muted", fraction: null };
  const total = (board.flux?.total ?? 0) + (board.argo?.total ?? 0);
  const bad = (board.flux?.notReady ?? 0) + (board.argo?.broken ?? 0);
  if (!board.flux && !board.argo) return { label: "gitops", value: "not detected", tone: "muted", fraction: null };
  if (bad > 0) {
    const bits: string[] = [];
    if ((board.flux?.notReady ?? 0) > 0) bits.push(`${board.flux?.notReady} flux`);
    if ((board.argo?.broken ?? 0) > 0) bits.push(`${board.argo?.broken} argo`);
    return { label: "gitops", value: `${bits.join(" / ")} bad`, tone: "warning", fraction: total > 0 ? Math.max(0.12, 1 - bad / total) : 0.55 };
  }
  const bits: string[] = [];
  if (board.flux) bits.push(`flux ${board.flux.total} ready`);
  if (board.argo) bits.push(`argo ${board.argo.total} synced`);
  return { label: "gitops", value: bits.join(" · "), tone: "success", fraction: 1 };
}

function gatewaySignal(c: ClusterDTO, board: FleetBoardEntry | undefined): Signal {
  const g = board?.gateway;
  if (!g) {
    if (c.networkTier === "Absent") return { label: "gateway", value: "not detected", tone: "muted", fraction: null };
    if (c.networkTier && c.networkTier !== "Healthy") return { label: "gateway", value: c.networkTier.toLowerCase(), tone: "warning", fraction: 0.58, title: c.networkReason };
    return { label: "gateway", value: "ready", tone: "success", fraction: 1 };
  }
  if (!g.served) return { label: "gateway", value: "not served", tone: "muted", fraction: null };
  if (g.unprogrammed > 0) {
    return { label: "gateway", value: `${g.unprogrammed} pending`, tone: "warning", fraction: 0.58 };
  }
  if ((g.brokenRoutes ?? 0) > 0) {
    return { label: "gateway", value: `${g.brokenRoutes} bad routes`, tone: "warning", fraction: g.routes && g.routes > 0 ? Math.max(0.12, 1 - (g.brokenRoutes ?? 0) / g.routes) : 0.5 };
  }
  if (g.routes != null) return { label: "gateway", value: `${g.routes} routes`, tone: "success", fraction: 1 };
  return { label: "gateway", value: `${g.gateways} gateways`, tone: "success", fraction: 1 };
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

// cardBadge names workload attention as exactly that - it is not a
// whole-cluster fault total (other domains are their own signal rows).
// No "healthy" badge: connection alone never asserts health.
function cardBadge(_c: ClusterDTO, board: FleetBoardEntry | undefined, signals: Signal[]): { label: string; tone: Tone } | null {
  const w = board?.workloads;
  if (w && w.counts.attention > 0) return { label: `${w.counts.attention} workload attention`, tone: attentionTone(w.counts) ?? "warning" };
  const warning = signals.find((s) => s.tone === "warning");
  if (warning) return { label: `${warning.label}: ${warning.value}`, tone: "warning" };
  return null;
}

function cardRail(c: ClusterDTO, board: FleetBoardEntry | undefined, signals: Signal[]): Tone {
  const w = board?.workloads;
  if (w && w.counts.attention > 0) return attentionTone(w.counts) ?? "warning";
  if (c.state === "Stale" || c.state === "Degraded" || signals.some((s) => s.tone === "warning")) return "warning";
  return "muted";
}

function diagnosticReason(c: ClusterDTO, board: FleetBoardEntry | undefined, signals: Signal[]): string {
  const attention = boardAttention(board) ?? 0;
  if (c.protected && attention > 0) return "first because a protected cluster has workloads needing attention";
  if (attention > 0) return "raised because workloads need attention";
  if (c.state === "Stale") return "raised because cluster watches are stale";
  if (c.state === "Degraded") return c.reason || "raised because cluster capability health is degraded";
  if ((board?.flux?.notReady ?? 0) > 0 || (board?.argo?.broken ?? 0) > 0) return "raised because GitOps needs attention";
  if (signals.some((s) => s.label === "gateway" && s.tone === "warning")) return "raised because Gateway API needs attention";
  if (c.nodesReady < c.nodesTotal) return "raised because node readiness is below target";
  // Say what is known - never "healthy" on missing or partial evidence.
  const w = board?.workloads;
  if (!w) return "workload health unreadable";
  if (w.coverage !== "complete" || w.counts.unknown > 0) return "workload health partly unverified";
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
