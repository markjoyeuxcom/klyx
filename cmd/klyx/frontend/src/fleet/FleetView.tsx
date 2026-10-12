import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { IconLayoutGrid } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { EmptyState } from "../chrome/EmptyState";
import { LensLayout } from "../layout/LensLayout";
import { ClusterCard, clusterDiagnosticScore } from "./ClusterCard";
import { MeshStrip } from "./MeshStrip";
import { getMeshGraph } from "../bridge/mesh";
import { fleetMeters, useFleetAttention } from "../nav/fleetAttention";

export function FleetView() {
  const clusters = useFleet((s) => s.clusters);
  const mesh = useFleet((s) => s.mesh);
  // Each cluster's health is its summary (health 1c slice 5): the board
  // fetches no health of its own.
  const models = useFleetAttention();
  useEffect(() => {
    getMeshGraph().catch((e) => console.error("getMeshGraph", e));
  }, []);

  const orderedClusters = [...clusters].sort((a, b) => {
    const score = clusterDiagnosticScore(b, models[b.name]) - clusterDiagnosticScore(a, models[a.name]);
    if (score !== 0) return score;
    return a.name.localeCompare(b.name);
  });
  // Connection axis: Synced and Degraded (connected with notes) are
  // connected; Stale is last-good, not live; Connecting is neither.
  const connected = clusters.filter((c) => c.state === "Synced" || c.state === "Degraded").length;
  const unreachable = clusters.filter((c) => c.state === "Failed" || c.state === "Unconnected").length;
  const stale = clusters.filter((c) => c.state === "Stale").length;
  // Attention and unknown coverage from each cluster's summary (health 1c
  // slice 5 §3): counted as clusters - never objects, never areas summed
  // across clusters - and independent facts that may overlap.
  const meters = fleetMeters(clusters, models);
  // ClusterMesh is capability-gated: no patch bay when no cluster runs it.
  const meshPresent = !!mesh && mesh.nodes.some((n) => n.state !== "unavailable");
  const nodes = clusters.reduce((n, c) => n + c.nodesTotal, 0);
  // Unknown counts are never summed as zero: the total says how many are out.
  const pods = clusters.reduce((n, c) => n + (c.podsKnown === false ? 0 : c.pods), 0);
  const podsUnknown = clusters.filter((c) => c.podsKnown === false).length;

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
          {meters.attention > 0 && (
            <span className="t-status" data-testid="fleet-attention" style={{ color: meters.attentionDanger ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
              {meters.attentionDanger ? "▲ " : ""}{meters.attention} need{meters.attention === 1 ? "s" : ""} attention
            </span>
          )}
          {meters.unknown > 0 && <span className="t-secondary" data-testid="fleet-unknown" title="Connected clusters whose summary leaves something unestablished: a gap, a part still checking, or an ended observation - each card names it">{meters.unknown} with unknown coverage</span>}
        </>
      )}
      headerEnd={clusters.length === 0 ? undefined : <span className="t-secondary">{nodes} node{nodes === 1 ? "" : "s"} · {pods} pod{pods === 1 ? "" : "s"}{podsUnknown > 0 ? ` (${podsUnknown} cluster${podsUnknown === 1 ? "" : "s"} unknown)` : ""}</span>}
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
