import { useFleet, NodesResultDTO, NodeDetailDTO, HeadroomReportDTO } from "../store/fleet";
import { NodesService, NodeOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };
type OpenLogStreamResultDTO = { streamId: string; error?: string };

export async function listNodes(cluster: string): Promise<void> {
  useFleet.getState().setNodesLoading(cluster);
  try {
    const r = (await NodesService.ListNodes(cluster)) as NodesResultDTO;
    // Drop a stale response if the user changed cluster while in flight.
    const cur = useFleet.getState().nodes;
    if (cur.cluster !== cluster) return;
    useFleet.getState().setNodes(cluster, r ?? { nodes: [] });
  } catch {
    // Clear the loading flag so the view doesn't get stuck on "Loading…".
    if (useFleet.getState().nodes.cluster === cluster) {
      useFleet.setState((s) => ({ nodes: { ...s.nodes, loading: false } }));
    }
  }
}

export async function cordonNode(cluster: string, node: string, cordon: boolean): Promise<void> {
  const r = (await NodeOpsService.Cordon(cluster, node, cordon)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `node ${node} ${cordon ? "cordoned" : "uncordoned"}` }
      : { kind: "error", message: r.error || (cordon ? "Cordon failed" : "Uncordon failed") },
  );
  if (r.ok) {
    void listNodes(cluster);
  }
}

// DrainOptions are the user-composed kubectl drain flags from the drain
// composer. The zero-ish default is the SAFE drain: emptyDir data preserved
// (an explicit opt-in since 2026-07-03 - it was silently always-on before),
// no force, daemonsets not tolerated unless chosen.
export type DrainOptions = {
  ignoreDaemonSets: boolean;
  deleteEmptyDirData: boolean;
  force: boolean;
  timeoutSeconds: number;
};

export async function startDrain(cluster: string, node: string, opts: DrainOptions): Promise<OpenLogStreamResultDTO> {
  return (await NodeOpsService.StartDrain(cluster, node, opts)) as OpenLogStreamResultDTO;
}

export async function cancelDrain(streamId: string): Promise<void> {
  await NodeOpsService.CancelDrain(streamId);
}

export async function openNodeDetail(cluster: string, name: string): Promise<void> {
  const ref = { name };
  useFleet.getState().selectNode(ref);
  try {
    const d = (await NodesService.GetNodeDetail(cluster, name)) as NodeDetailDTO;
    const sel = useFleet.getState().nodes.selected;
    if (!sel || sel.name !== name) return;
    useFleet.getState().setNodeDetail(ref, d);
  } catch {
    // On failure, clear detailLoading so the panel doesn't spin forever.
    const sel = useFleet.getState().nodes.selected;
    if (sel && sel.name === name) {
      useFleet.setState((s) => ({ nodes: { ...s.nodes, detailLoading: false } }));
    }
  }
}

// Survivability simulation: lose a node/zone/pool, do the displaced pods fit?
export async function getNodeHeadroom(cluster: string, loseKind: string, loseName: string): Promise<HeadroomReportDTO> {
  try {
    return (await NodesService.GetNodeHeadroom(cluster, loseKind, loseName)) as HeadroomReportDTO;
  } catch (e) {
    return {
      result: { lostNodes: 0, survivorNodes: 0, displaced: 0, rescheduled: 0, unplaced: [], cpuShortfall: 0, memShortfall: 0 },
      pdbBlockers: [], podListCapped: false,
      error: e instanceof Error ? e.message : "headroom simulation failed",
    };
  }
}
