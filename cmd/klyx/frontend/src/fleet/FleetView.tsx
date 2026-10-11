import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { IconLayoutGrid } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { EmptyState } from "../chrome/EmptyState";
import { LensLayout } from "../layout/LensLayout";
import { ClusterCard, clusterDiagnosticScore } from "./ClusterCard";
import { MeshStrip } from "./MeshStrip";
import { getMeshGraph } from "../bridge/mesh";
import { fetchFleetBoard } from "../bridge/fleetboard";

export function FleetView() {
  const clusters = useFleet((s) => s.clusters);
  const mesh = useFleet((s) => s.mesh);
  const fleetBoard = useFleet((s) => s.fleetBoard);
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
  const nodes = clusters.reduce((n, c) => n + c.nodesTotal, 0);
  const pods = clusters.reduce((n, c) => n + c.pods, 0);

  // The board on the workspace template (redesign step 7d): the old meter
  // strip becomes the header summary - independent facts, zero neutral,
  // nothing green - and the cards sit in a bounded grid so one cluster never
  // stretches across the desktop.
  return (
    <LensLayout
      label="Fleet board"
      title="Fleet board"
      summary={clusters.length === 0 ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{clusters.length}</span>
          <span className="t-secondary">cluster{clusters.length === 1 ? "" : "s"} · {connected} connected</span>
          {unreachable > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {unreachable} unreachable</span>}
          {stale > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{stale} stale</span>}
          {workloadAttention > 0 && (
            <span className="t-status" style={{ color: workloadFault ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
              {workloadFault ? "▲ " : ""}{workloadAttention} with workload attention
            </span>
          )}
          {unverified > 0 && <span className="t-secondary" title="Connected clusters whose workload read failed, was capped or holds unclassified rows">{unverified} unverified</span>}
        </>
      )}
      headerEnd={clusters.length === 0 ? undefined : <span className="t-secondary">{nodes} node{nodes === 1 ? "" : "s"} · {pods} pod{pods === 1 ? "" : "s"}</span>}
      state={clusters.length === 0 ? "empty" : "ready"}
      stateView={
        <EmptyState
          icon={<IconLayoutGrid size={28} stroke={1.2} />}
          title="No clusters connected yet."
          hint="Klyx reads standard kubeconfig contexts - add or enable them in Settings."
          action={{ label: "Open Settings", onClick: () => useFleet.getState().openSettings() }}
        />
      }
      body={
        <div className="lens-canvas" data-testid="fleet-board">
          {meshPresent && mesh && (
            <section aria-label="ClusterMesh" className="fleet-mesh">
              <h3 className="t-section-label">ClusterMesh</h3>
              <MeshStrip graph={mesh} />
            </section>
          )}
          <FleetGrid>
            {orderedClusters.map((c) => <ClusterCard key={c.name} c={c} />)}
          </FleetGrid>
        </div>
      }
      footerEnd={clusters.length === 0 ? undefined : <span className="t-secondary">{clusters.length} cluster{clusters.length === 1 ? "" : "s"} · attention first</span>}
    />
  );
}

// fleetColumns (spec §1): as many columns as fit at the 360px preferred
// minimum, each at most 480px, left aligned; below 360px one full-width card.
// CSS auto-fill sizes repetitions by the 480px maximum and would leave a
// third of a wide workspace empty, so the count is computed from the width.
export function fleetColumns(width: number, gap = 8): { count: number; width: number } {
  if (width <= 0) return { count: 1, width: 0 };
  if (width < 360) return { count: 1, width };
  const count = Math.max(1, Math.floor((width + gap) / (360 + gap)));
  return { count, width: Math.min(480, Math.floor((width - gap * (count - 1)) / count)) };
}

function FleetGrid({ children }: { children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [cols, setCols] = useState<{ count: number; width: number } | null>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setCols(el.clientWidth > 0 ? fleetColumns(el.clientWidth) : null);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div ref={ref} className="fleet-grid" style={cols ? { gridTemplateColumns: `repeat(${cols.count}, ${cols.width}px)` } : undefined}>
      {children}
    </div>
  );
}
