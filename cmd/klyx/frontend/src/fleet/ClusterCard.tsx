import type { CSSProperties, ReactNode } from "react";
import { IconLock } from "@tabler/icons-react";
import type { ClusterDTO, FleetBoardEntry, MeshGraphDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { type Tone, toneColor, toneBg, toneBorder } from "../chrome/primitives";

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
  if ((board?.broken ?? 0) > 0) score += 500 + Math.min(100, (board?.broken ?? 0) * 10);
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
        background: "var(--color-background-primary)",
        border: "0.5px solid var(--color-border-tertiary)",
        borderLeft: `3px solid ${toneColor[rail]}`,
        borderRadius: 3,
        padding: 0,
        cursor: "pointer",
        display: "grid",
        gridTemplateRows: "auto 1fr auto",
        minHeight: 232,
        minWidth: 0,
        overflow: "hidden",
        boxSizing: "border-box",
      }}
      onMouseEnter={(e) => { e.currentTarget.style.borderColor = "var(--color-border-secondary)"; e.currentTarget.style.borderLeftColor = toneColor[rail]; }}
      onMouseLeave={(e) => { e.currentTarget.style.borderColor = "var(--color-border-tertiary)"; e.currentTarget.style.borderLeftColor = toneColor[rail]; }}
    >
      <div style={{ display: "flex", alignItems: "flex-start", gap: 8, padding: "11px 12px 10px", borderBottom: "0.5px solid var(--color-border-tertiary)", minWidth: 0 }}>
        <div style={{ minWidth: 0, flex: 1 }}>
          <div title={c.name} style={{ fontFamily: "var(--font-mono)", fontWeight: 600, fontSize: 14, ...ellipsis }}>{displayName}</div>
          <div style={{ marginTop: 3, fontSize: 11, color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)", ...ellipsis }}>
            {clusterMeta(c)}
          </div>
          <div style={{ marginTop: 8, display: "flex", gap: 14, fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)", minWidth: 0 }}>
            <span style={{ color: c.nodesReady < c.nodesTotal ? "var(--color-text-warning)" : undefined, flexShrink: 0 }}>{c.nodesReady}/{c.nodesTotal} nodes</span>
            <span style={{ flexShrink: 0 }}>{c.pods} pods</span>
            {c.env && <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{c.env}</span>}
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

function SignalRow({ signal }: { signal: Signal }) {
  const width = `${Math.round((signal.fraction ?? 0) * 100)}%`;
  return (
    <div title={signal.title ?? signal.value} style={{ display: "grid", gridTemplateColumns: "86px minmax(44px, 1fr) minmax(0, 46%)", alignItems: "center", gap: 10, fontFamily: "var(--font-mono)", fontSize: 11, minWidth: 0 }}>
      <span style={{ color: "var(--color-text-secondary)", ...ellipsis }}>{signal.label}</span>
      <span style={{ height: 5, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", overflow: "hidden", minWidth: 0 }}>
        <span style={{ display: "block", width, maxWidth: "100%", height: "100%", background: toneColor[signal.tone], opacity: signal.fraction == null ? 0.25 : 1 }} />
      </span>
      <span style={{ color: toneColor[signal.tone], ...ellipsis }}>{signal.value}</span>
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

function workloadSignal(board: FleetBoardEntry | undefined): Signal {
  if (!board || board.broken == null) {
    return { label: "workloads", value: "unreadable", tone: "muted", fraction: null };
  }
  const total = board.workloadsTotal ?? null;
  if (board.broken > 0) {
    return {
      label: "workloads",
      value: `${board.broken} bad`,
      tone: "danger",
      fraction: total && total > 0 ? Math.max(0.12, Math.min(1, board.broken / total)) : 0.42,
      title: total ? `${board.broken} of ${total} workloads need attention` : undefined,
    };
  }
  return {
    label: "workloads",
    value: total != null ? `${total} ok` : "quiet",
    tone: "success",
    fraction: 1,
  };
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

function freshnessSignal(c: ClusterDTO): Signal {
  if (c.state === "Stale") return { label: "freshness", value: "watch stale", tone: "warning", fraction: 0.62, title: c.reason };
  if (c.state === "Degraded") return { label: "freshness", value: "degraded", tone: "warning", fraction: 0.72, title: c.reason };
  if (c.state === "Synced") return { label: "freshness", value: "live", tone: "success", fraction: 1 };
  if (c.state === "Connecting") return { label: "freshness", value: "connecting", tone: "info", fraction: 0.34 };
  return { label: "freshness", value: c.state.toLowerCase(), tone: "muted", fraction: null };
}

function cardBadge(c: ClusterDTO, board: FleetBoardEntry | undefined, signals: Signal[]): { label: string; tone: Tone } | null {
  if ((board?.broken ?? 0) > 0) return { label: `${board?.broken} critical`, tone: "danger" };
  const warning = signals.find((s) => s.tone === "warning");
  if (warning) return { label: warning.value, tone: "warning" };
  if (c.state === "Synced") return { label: "healthy", tone: "success" };
  return { label: c.state.toLowerCase(), tone: "info" };
}

function cardRail(c: ClusterDTO, board: FleetBoardEntry | undefined, signals: Signal[]): Tone {
  if ((board?.broken ?? 0) > 0) return "danger";
  if (c.state === "Stale" || c.state === "Degraded" || signals.some((s) => s.tone === "warning")) return "warning";
  return "success";
}

function diagnosticReason(c: ClusterDTO, board: FleetBoardEntry | undefined, signals: Signal[]): string {
  if (c.protected && (board?.broken ?? 0) > 0) return "first because protected cluster has bad workloads";
  if ((board?.broken ?? 0) > 0) return "raised because workloads need attention";
  if (c.state === "Stale") return "raised because cluster watches are stale";
  if (c.state === "Degraded") return c.reason || "raised because cluster capability health is degraded";
  if ((board?.flux?.notReady ?? 0) > 0 || (board?.argo?.broken ?? 0) > 0) return "raised because GitOps needs attention";
  if (signals.some((s) => s.label === "gateway" && s.tone === "warning")) return "raised because Gateway API needs attention";
  if (c.nodesReady < c.nodesTotal) return "raised because node readiness is below target";
  return "healthy clusters stay visible without stealing attention";
}

function clusterMeta(c: ClusterDTO): string {
  return [c.provider, c.region, c.protected ? "protected" : "", c.version].filter(Boolean).join(" / ");
}

function clusterDisplayName(name: string): string {
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
