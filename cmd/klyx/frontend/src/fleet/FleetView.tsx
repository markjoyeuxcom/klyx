import { IconLayoutGrid } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { useLine } from "../store/line";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LensLayout } from "../layout/LensLayout";
import { useFleetAttention } from "../nav/fleetAttention";
import { LineBoard, LineVerdictView } from "./LineBoard";
import { NeedsYou } from "./NeedsYou";

// The fleet screen (The Line slice 5): the line's verdict and legend, then
// the board, from LineDTO and each cluster's summary, then "Needs you" from
// the same summaries - the screen fetches nothing of its own. The aside
// (5c) follows.
export function FleetView() {
  const clusters = useFleet((s) => s.clusters);
  const dto = useLine((s) => s.dto);
  const models = useFleetAttention();
  const nodes = clusters.reduce((n, c) => n + c.nodesTotal, 0);
  // Unknown counts are never summed as zero: the total says how many are out.
  const pods = clusters.reduce((n, c) => n + (c.podsKnown === false ? 0 : c.pods), 0);
  const podsUnknown = clusters.filter((c) => c.podsKnown === false).length;

  return (
    <LensLayout
      label="Fleet board"
      title="Fleet board"
      summary={clusters.length === 0 ? null : <span className="t-secondary">{clusters.length} cluster{clusters.length === 1 ? "" : "s"}</span>}
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
        <div className="lens-canvas fleet-screen" data-testid="fleet-board">
          {dto ? (
            <>
              <LineVerdictView dto={dto} />
              <LineBoard dto={dto} clusters={clusters} models={models} />
            </>
          ) : <SkeletonRows rows={3} label="reading the line" />}
          <NeedsYou clusters={clusters} models={models} />
        </div>
      }
      footerEnd={clusters.length === 0 ? undefined : <span className="t-secondary">{clusters.length} cluster{clusters.length === 1 ? "" : "s"}</span>}
    />
  );
}
