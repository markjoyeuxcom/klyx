import { useEffect, useState } from "react";
import { IconLayoutGrid } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { useLine } from "../store/line";
import { useLineGitFor } from "../store/lineGit";
import { fetchDue } from "../bridge/lineGit";
import { configLoadError } from "../bridge/configsvc";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LensLayout } from "../layout/LensLayout";
import { useFleetAttention } from "../nav/fleetAttention";
import { LineBoard, LineVerdictView } from "./LineBoard";
import { NeedsYou } from "./NeedsYou";
import { Horizon, Ledger } from "./FleetAside";

// The fleet screen (The Line slice 5): the line's verdict and legend, then
// the board, from LineDTO and each cluster's summary; then "Needs you" from
// the same summaries beside the aside - the Ledger and the Horizon, whose
// expiry scans run once per session, shared with the Expiry view.
export function FleetView() {
  const clusters = useFleet((s) => s.clusters);
  const dto = useLine((s) => s.dto);
  const git = useLineGitFor(dto);
  // The fetch cadence (slice 6 note §6.3): on mount, then every 5 minutes
  // while the fleet screen is open; the backend fetches only what is due.
  useEffect(() => {
    fetchDue();
    const t = setInterval(fetchDue, 5 * 60 * 1000);
    return () => clearInterval(t);
  }, []);
  // an empty fleet because fleet.yaml did not load says so (Settings review
  // slice 0)
  const [loadError, setLoadError] = useState("");
  useEffect(() => {
    if (clusters.length > 0) return;
    let live = true;
    void configLoadError().then((e) => {
      if (live) setLoadError(e);
    });
    return () => {
      live = false;
    };
  }, [clusters.length]);
  const models = useFleetAttention();
  const nodes = clusters.reduce((n, c) => n + c.nodesTotal, 0);
  // Unknown counts are never summed as zero: the total says how many are out.
  const pods = clusters.reduce((n, c) => n + (c.podsKnown === false ? 0 : c.pods), 0);
  const podsUnknown = clusters.filter((c) => c.podsKnown === false).length;
  // The footer counts connections only; it never says the line or the fleet
  // is live (parent §3.6).
  const conn = (states: string[]) => clusters.filter((c) => states.includes(c.state)).length;
  const footer = [
    `${clusters.length} cluster${clusters.length === 1 ? "" : "s"}`,
    `${conn(["Synced", "Degraded"])} connected`,
    conn(["Stale"]) > 0 ? `${conn(["Stale"])} stale` : "",
    conn(["Failed", "Unconnected"]) > 0 ? `${conn(["Failed", "Unconnected"])} failed` : "",
    conn(["Connecting"]) > 0 ? `${conn(["Connecting"])} connecting` : "",
  ].filter(Boolean).join(" · ");

  return (
    <LensLayout
      label="Fleet board"
      title="Fleet board"
      summary={clusters.length === 0 ? null : <span className="t-secondary">{clusters.length} cluster{clusters.length === 1 ? "" : "s"}</span>}
      headerEnd={clusters.length === 0 ? undefined : <span className="t-secondary">{nodes} node{nodes === 1 ? "" : "s"} · {pods} pod{pods === 1 ? "" : "s"}{podsUnknown > 0 ? ` (${podsUnknown} cluster${podsUnknown === 1 ? "" : "s"} unknown)` : ""}</span>}
      state={clusters.length === 0 ? "empty" : "ready"}
      stateView={
        loadError ? (
          <EmptyState
            icon={<IconLayoutGrid size={28} stroke={1.2} />}
            title="fleet.yaml did not load, so no cluster is connected."
            hint={`${loadError} - fix the file in an editor, then restart Klyx.`}
            action={{ label: "Open Settings", onClick: () => useFleet.getState().openSettings() }}
          />
        ) : (
          <EmptyState
            icon={<IconLayoutGrid size={28} stroke={1.2} />}
            title="No clusters connected yet."
            hint="Klyx reads standard kubeconfig contexts - add or enable them in Settings."
            action={{ label: "Open Settings", onClick: () => useFleet.getState().openSettings() }}
          />
        )
      }
      body={
        <div className="ov-page fleet-screen" data-testid="fleet-board">
          {dto ? (
            <>
              <LineVerdictView dto={dto} git={git} />
              <LineBoard dto={dto} git={git} clusters={clusters} models={models} />
            </>
          ) : <SkeletonRows rows={3} label="reading the line" />}
          <div className="fleet-lower">
            <NeedsYou clusters={clusters} models={models} />
            <aside className="fleet-aside" aria-label="Ledger and Horizon">
              <Ledger dto={dto} git={git} />
              <Horizon clusters={clusters} />
            </aside>
          </div>
        </div>
      }
      footerEnd={clusters.length === 0 ? undefined : <span className="t-secondary" data-testid="fleet-footer">{footer}</span>}
    />
  );
}
