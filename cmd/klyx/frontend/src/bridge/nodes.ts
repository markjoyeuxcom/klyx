import { useFleet, NodeDetailDTO, HeadroomReportDTO } from "../store/fleet";
import { NodesService, NodeOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };
type OpenLogStreamResultDTO = { streamId: string; error?: string };

// cordonNode cordons or uncordons the node OBJECT the verb was armed against
// (uid, design §5b): the backend confirms it and the write carries it, so a
// same-name replacement is never written. The list is the Observer's live
// rows - no re-read follows.
export async function cordonNode(cluster: string, node: string, uid: string, cordon: boolean): Promise<void> {
  const r = (await NodeOpsService.Cordon(cluster, node, uid, cordon)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `node ${node} ${cordon ? "cordoned" : "uncordoned"}` }
      : { kind: "error", message: r.error || (cordon ? "Cordon failed" : "Uncordon failed") },
  );
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

// startDrain starts the drain on streamId, which the caller minted
// (newStreamId) and has ALREADY subscribed to as `nodedrain:<streamId>` - the
// backend emits kubectl output from the moment it starts (review 2026-10-03 #8).
// uid is the node object the drain was armed against: a replaced or missing
// node starts nothing.
export async function startDrain(cluster: string, node: string, uid: string, opts: DrainOptions, streamId: string): Promise<OpenLogStreamResultDTO> {
  return (await NodeOpsService.StartDrain(cluster, node, uid, opts, streamId)) as OpenLogStreamResultDTO;
}

export async function cancelDrain(streamId: string): Promise<void> {
  await NodeOpsService.CancelDrain(streamId);
}

// openNodeDetail selects the node OBJECT (name + uid) and reads its detail
// directly. Re-opening the same object keeps the shown detail while the
// re-read is in flight. Only the latest read lands, and only on its own
// cluster (review 2026-10-03 #5); setNodeDetail drops it unless the selection
// is still that object - another node or another object with the name never
// takes it (design §5b). A failed read is the read's outcome, never an empty
// detail.
let detailSeq = 0;
export async function openNodeDetail(cluster: string, name: string, uid: string): Promise<void> {
  const ref = { name, uid };
  const seq = ++detailSeq;
  useFleet.getState().selectNode(ref);
  const current = () => seq === detailSeq && useFleet.getState().nodes.cluster === cluster;
  let d: NodeDetailDTO;
  try {
    d = (await NodesService.GetNodeDetail(cluster, name)) as NodeDetailDTO;
  } catch (e) {
    d = { ...EMPTY_DETAIL, error: e instanceof Error ? e.message : "node detail read failed" };
  }
  if (current()) useFleet.getState().setNodeDetail(ref, d);
}

const EMPTY_DETAIL: NodeDetailDTO = {
  summary: { name: "", roles: [], ready: false, unschedulable: false, problems: [], version: "", os: "", arch: "", taintCount: 0, cpuCapacity: 0, cpuAllocatable: 0, memCapacity: 0, memAllocatable: 0, podCapacity: 0, ageSeconds: 0 },
  labels: {}, annotations: {}, taints: [], conditions: [], events: [], yaml: "", podsOnNode: [],
};

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
