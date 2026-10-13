import { useMemo } from "react";
import type { MeshEdgeDTO, MeshGraphDTO, MeshNodeDTO } from "../store/fleet";

// MeshStrip renders the ClusterMesh peering graph as an arc diagram: node
// pills in a row, peering EDGES drawn as arcs above them (design principle 4:
// ClusterMesh is a visible edge between clusters, not a footnote).
//
// Visual language:
//   solid info arc     mutual peering (both sides configure each other)
//   dashed warning arc one-way (From configured it; the other side did not)
//   dashed muted arc   off-fleet peer (the far side is not connected to Klyx,
//                      so mutuality is unknowable - never shown as mutual)
// The strip reports CONFIGURED peering, not live connectivity - the honesty
// note stays in the header.

const nodeStyle: React.CSSProperties = {
  background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-info)",
  borderRadius: 3, padding: "5px 10px", fontFamily: "var(--font-mono)", fontSize: 11,
  justifySelf: "center",
};

// nodeKey matches how edges reference endpoints: fleet key for present
// clusters, Cilium name for off-fleet peers.
const nodeKey = (n: MeshNodeDTO) => n.cluster || n.name;

type ArcKind = "mutual" | "oneWay" | "offFleet";

function arcKind(e: MeshEdgeDTO, byKey: Map<string, MeshNodeDTO>): ArcKind {
  const a = byKey.get(e.a);
  const b = byKey.get(e.b);
  if ((a && !a.present) || (b && !b.present)) return "offFleet";
  return e.mutual ? "mutual" : "oneWay";
}

function arcTitle(e: MeshEdgeDTO, kind: ArcKind): string {
  if (kind === "mutual") return `${e.a} ⇄ ${e.b} - mutual peering`;
  if (kind === "offFleet") {
    const from = e.from || e.a;
    const other = from === e.a ? e.b : e.a;
    return `${from} → ${other} - off-fleet peer (not connected to Klyx; mutuality unknown)`;
  }
  const from = e.from || e.a;
  const other = from === e.a ? e.b : e.a;
  return `${from} → ${other} - one-way: ${other} does not list ${from}`;
}

const arcStroke: Record<ArcKind, { color: string; dash?: string }> = {
  mutual: { color: "var(--color-text-info)" },
  oneWay: { color: "var(--color-text-warning)", dash: "5 4" },
  offFleet: { color: "var(--color-text-tertiary)", dash: "3 4" },
};

export function MeshStrip({ graph }: { graph: MeshGraphDTO }) {
  // Render only when at least one cluster has ClusterMesh installed.
  const meshy = graph.nodes.some((n) => n.state !== "unavailable");

  const nodes = graph.nodes;
  const byKey = useMemo(() => new Map(nodes.map((n) => [nodeKey(n), n])), [nodes]);
  const indexOf = useMemo(() => new Map(nodes.map((n, i) => [nodeKey(n), i])), [nodes]);

  if (!meshy) return null;

  // Deterministic geometry: node i sits centered in column i of an equal-width
  // grid, so its arc anchor is at (i + 0.5) / n in viewBox units - no DOM
  // measurement, fully responsive.
  const n = nodes.length;
  const VBW = 1000;
  const anchorX = (i: number) => ((i + 0.5) / n) * VBW;
  // Arc height grows with the span so nested arcs don't overlap.
  const BAND = 44;
  const arcs = graph.edges
    .map((e) => {
      const ia = indexOf.get(e.a);
      const ib = indexOf.get(e.b);
      if (ia === undefined || ib === undefined) return null;
      const kind = arcKind(e, byKey);
      const x1 = anchorX(Math.min(ia, ib));
      const x2 = anchorX(Math.max(ia, ib));
      const span = Math.abs(ia - ib);
      const lift = Math.min(BAND - 6, 14 + span * 10);
      return { e, kind, x1, x2, lift };
    })
    .filter((a): a is NonNullable<typeof a> => a !== null);

  const hasOneWay = arcs.some((a) => a.kind === "oneWay");
  const hasOffFleet = arcs.some((a) => a.kind === "offFleet");

  return (
    <div style={{ marginBottom: 16 }}>
      <div style={{ display: "flex", alignItems: "baseline", gap: 8, marginBottom: 6 }}>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>clustermesh</span>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>configured peering (not live connectivity)</span>
      </div>
      <div style={{ background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 3, padding: "10px 14px 12px" }}>
        {arcs.length > 0 && (
          <svg
            data-testid="mesh-arcs"
            viewBox={`0 0 ${VBW} ${BAND}`}
            preserveAspectRatio="none"
            style={{ display: "block", width: "100%", height: BAND, overflow: "visible" }}
          >
            {arcs.map(({ e, kind, x1, x2, lift }) => {
              const stroke = arcStroke[kind];
              return (
                <path
                  key={`${e.a}|${e.b}`}
                  data-kind={kind}
                  d={`M ${x1} ${BAND} Q ${(x1 + x2) / 2} ${BAND - lift * 2} ${x2} ${BAND}`}
                  fill="none"
                  stroke={stroke.color}
                  strokeWidth={1.4}
                  strokeDasharray={stroke.dash}
                  vectorEffect="non-scaling-stroke"
                >
                  <title>{arcTitle(e, kind)}</title>
                </path>
              );
            })}
          </svg>
        )}
        <div style={{ display: "grid", gridTemplateColumns: `repeat(${n}, 1fr)`, gap: 10, alignItems: "center" }}>
          {nodes.map((node) => {
            const off = !node.present;
            // Any present cluster that isn't actively peered is "standalone" - muted with
            // a ⬡ marker. This covers both "enabled" (installed, no peers) and "unavailable"
            // (no ClusterMesh at all) so a non-mesh cluster never looks like a peer.
            const standalone = !off && node.state !== "peered";
            // A member whose mesh facts could not be READ: its peerings are
            // unknown - never present it as "no peers" (review 2026-10-03, K).
            const title = node.readNote
              ? `peering unknown - ${node.readNote}`
              : off
                ? "off-fleet peer (not connected to Klyx)"
                : node.state === "unavailable"
                  ? "no ClusterMesh"
                  : standalone
                    ? "mesh enabled, no peers"
                    : "meshed";
            return (
              <span
                key={nodeKey(node)}
                style={{
                  ...nodeStyle,
                  borderColor: off || standalone ? "var(--color-border-tertiary)" : "var(--color-border-info)",
                  color: off ? "var(--color-text-tertiary)" : standalone ? "var(--color-text-secondary)" : "var(--color-text-primary)",
                  opacity: off ? 0.6 : standalone ? 0.85 : 1,
                  maxWidth: "100%", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                }}
                title={title}
              >
                <span>{node.name || node.cluster}</span>{node.readNote ? " ?" : standalone ? " ⬡" : ""}{off ? " (off-fleet)" : ""}
              </span>
            );
          })}
        </div>
      </div>
      {(hasOneWay || hasOffFleet) && (
        <div style={{ fontSize: 9, color: hasOneWay ? "var(--color-text-warning)" : "var(--color-text-tertiary)", marginTop: 4 }}>
          {hasOneWay && "⚠︎ dashed amber = one-way peering (return side not configured)"}
          {hasOneWay && hasOffFleet && " · "}
          {hasOffFleet && "dashed grey = off-fleet peer"}
        </div>
      )}
    </div>
  );
}
