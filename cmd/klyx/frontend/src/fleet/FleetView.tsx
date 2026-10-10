import { useEffect, useState, type ReactNode } from "react";
import { IconLayoutGrid } from "@tabler/icons-react";
import { FleetPromotion } from "./FleetPromotion";
import { FleetExpiry } from "./FleetExpiry";
import { btn } from "../chrome/primitives";
import { useFleet } from "../store/fleet";
import { MetersStrip } from "../chrome/primitives";
import { EmptyState } from "../chrome/EmptyState";
import { ClusterCard, clusterDiagnosticScore } from "./ClusterCard";
import { MeshStrip } from "./MeshStrip";
import { getMeshGraph } from "../bridge/mesh";
import { fetchFleetBoard } from "../bridge/fleetboard";

export function FleetView() {
  const clusters = useFleet((s) => s.clusters);
  const mesh = useFleet((s) => s.mesh);
  const fleetBoard = useFleet((s) => s.fleetBoard);
  const [showPromotion, setShowPromotion] = useState(false);
  const [showExpiry, setShowExpiry] = useState(false);
  // Board enrichment fetches once per set of CONNECTED clusters (a cluster
  // coming online re-triggers); per-card fields degrade independently.
  const connectedKey = clusters
    .filter((c) => c.state !== "Failed" && c.state !== "Unconnected")
    .map((c) => c.name)
    .join(",");
  useEffect(() => {
    getMeshGraph().catch((e) => console.error("getMeshGraph", e));
  }, []);
  useEffect(() => {
    if (connectedKey === "") return;
    void fetchFleetBoard(connectedKey.split(","));
  }, [connectedKey]);

  const orderedClusters = [...clusters].sort((a, b) => {
    const score = clusterDiagnosticScore(b, fleetBoard[b.name]) - clusterDiagnosticScore(a, fleetBoard[a.name]);
    if (score !== 0) return score;
    return a.name.localeCompare(b.name);
  });
  // Connection axis: Synced and Degraded (connected with notes) are
  // connected; Stale is last-good, not live; Connecting is neither.
  const connected = clusters.filter((c) => c.state === "Synced" || c.state === "Degraded").length;
  const unreachable = clusters.filter((c) => c.state === "Failed" || c.state === "Unconnected").length;
  const stale = clusters.filter((c) => c.state === "Stale").length;
  // Workload attention per cluster - explicitly workloads, not a whole-cluster
  // fault total. Clusters whose workload read failed or was capped are
  // "unverified"; buckets may overlap and are labelled as independent facts.
  const workloadAttention = clusters.filter((c) => (fleetBoard[c.name]?.workloads?.counts.attention ?? 0) > 0).length;
  const workloadFault = clusters.some((c) => (fleetBoard[c.name]?.workloads?.counts.fault ?? 0) > 0);
  const unverified = clusters.filter((c) => {
    const w = fleetBoard[c.name]?.workloads;
    return (c.state === "Synced" || c.state === "Degraded") && (!w || w.coverage !== "complete" || w.counts.unknown > 0);
  }).length;
  // ClusterMesh is capability-gated: no patch bay when no cluster runs it.
  const meshPresent = !!mesh && mesh.nodes.some((n) => n.state !== "unavailable");
  const allConnected = connected === clusters.length;

  return (
    <div style={{ padding: "14px 16px", display: "grid", gap: 12, minWidth: 0, overflowX: "hidden" }}>
      {clusters.length === 0 ? (
        <EmptyState
          icon={<IconLayoutGrid size={28} stroke={1.2} />}
          title="No clusters connected yet."
          hint="Klyx reads standard kubeconfig contexts - add or enable them in Settings."
          action={{ label: "Open Settings", onClick: () => useFleet.getState().openSettings() }}
        />
      ) : (
        <>
          <MetersStrip
            note="attention first"
            meters={[
              { label: "clusters", value: clusters.length },
              { label: "connected", value: connected },
              unreachable > 0 && { label: "unreachable", value: unreachable, tone: "danger" as const },
              stale > 0 && { label: "stale", value: stale, tone: "warning" as const },
              workloadAttention > 0 && { label: "workload attention", value: workloadAttention, tone: workloadFault ? ("danger" as const) : ("warning" as const) },
              unverified > 0 && { label: "unverified", value: unverified },
              { label: "nodes", value: clusters.reduce((n, c) => n + c.nodesTotal, 0) },
              { label: "pods", value: clusters.reduce((n, c) => n + c.pods, 0) },
            ]}
          />
          {meshPresent && mesh && (
            <div style={{ background: "var(--bench-well-bg, transparent)", boxShadow: "var(--bench-well-shadow, none)", borderRadius: "var(--radius-sm)", padding: "8px 14px" }}>
              <div style={{ fontSize: 9, letterSpacing: "0.14em", textTransform: "uppercase", fontWeight: 600, color: "var(--color-text-tertiary)", marginBottom: 2 }}>clustermesh · patch bay</div>
              <MeshStrip graph={mesh} />
            </div>
          )}
          <div style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: 12,
            border: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-secondary)",
            padding: "9px 12px",
          }}>
            <div style={{ fontSize: 15, fontWeight: 600 }}>Fleet board</div>
            <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap", justifyContent: "flex-end" }}>
              <button onClick={() => setShowPromotion(true)} title="Track applied GitOps revisions across the fleet (did my change land everywhere?)" style={{ ...btn, fontSize: 11 }}>promotion</button>
              <button onClick={() => setShowExpiry(true)} title="Everything with a certificate lifetime, fleet-wide, soonest first" style={{ ...btn, fontSize: 11 }}>expiry</button>
              <BoardChip strong>diagnostic order</BoardChip>
              <BoardChip>{clusters.length} cluster{clusters.length === 1 ? "" : "s"}</BoardChip>
              <span style={{ display: "inline-flex", alignItems: "center", gap: 6, color: allConnected ? "var(--color-text-secondary)" : "var(--color-text-warning)", fontFamily: "var(--font-mono)", fontSize: 11, whiteSpace: "nowrap" }}>
                <span style={{ width: 7, height: 7, borderRadius: "50%", background: allConnected ? "var(--color-text-tertiary)" : "var(--color-text-warning)" }} />
                {allConnected ? "all informer factories connected" : `${connected}/${clusters.length} informer factories connected`}
              </span>
            </div>
          </div>
          <div style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(min(360px, 100%), 1fr))",
            gap: 8,
            alignItems: "stretch",
            minWidth: 0,
          }}>
            {orderedClusters.map((c) => <ClusterCard key={c.name} c={c} />)}
          </div>
        </>
      )}
      {showPromotion && <FleetPromotion onClose={() => setShowPromotion(false)} />}
      {showExpiry && <FleetExpiry onClose={() => setShowExpiry(false)} />}
    </div>
  );
}

function BoardChip({ children, strong = false }: { children: ReactNode; strong?: boolean }) {
  return (
    <span style={{
      border: "0.5px solid var(--color-border-tertiary)",
      background: strong ? "var(--color-background-primary)" : "transparent",
      color: strong ? "var(--color-text-primary)" : "var(--color-text-secondary)",
      padding: "4px 8px",
      fontSize: 12,
      fontWeight: strong ? 600 : 400,
      whiteSpace: "nowrap",
    }}>
      {children}
    </span>
  );
}
