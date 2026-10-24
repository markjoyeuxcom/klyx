import { resultStatus, type ActionResultLike } from "../admission/finding";
import { useFleet, WorkloadCompareDTO, RolloutHistoryResultDTO } from "../store/fleet";
import { WorkloadsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = ActionResultLike;

// The workload verbs are armed against one workload OBJECT (its uid, design
// §5b): the backend confirms it and the write carries it, so a same-name
// replacement is never written. The list is the Observer's live rows - no
// re-read follows a verb.

function report(r: ActionResultDTO, ok: string, failed: string): void {
  useFleet.getState().setActionStatus(resultStatus(r, ok, failed));
}

export async function scaleWorkload(cluster: string, kind: string, namespace: string, name: string, uid: string, replicas: number): Promise<void> {
  const r = (await WorkloadsService.ScaleWorkload(cluster, kind, namespace, name, uid, replicas)) as ActionResultDTO;
  report(r, `scaled ${kind.toLowerCase()} ${namespace}/${name} to ${replicas}`, "Scale failed");
}

export async function rolloutRestart(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<void> {
  const r = (await WorkloadsService.RolloutRestart(cluster, kind, namespace, name, uid)) as ActionResultDTO;
  report(r, `restart triggered for ${kind.toLowerCase()} ${namespace}/${name}`, "Restart failed");
}

// workloadCompare fetches one cluster's drift-relevant view of a workload for
// the fleet compare. Errors fold into the DTO (degraded, never "absent").
export async function workloadCompare(cluster: string, kind: string, namespace: string, name: string): Promise<WorkloadCompareDTO> {
  return (await WorkloadsService.WorkloadCompare(cluster, kind, namespace, name)) as WorkloadCompareDTO;
}

// Rollout history + undo + pause/resume (the last ranked day-2 verbs).
// History is read-only; undo/pause report through the same toast as
// restart/scale. Undo is destructive: the caller's ConfirmDialog armed the
// protected grant before this fires - the backend re-checks regardless.
export async function rolloutHistory(cluster: string, kind: string, namespace: string, name: string): Promise<RolloutHistoryResultDTO> {
  try {
    return (await WorkloadsService.RolloutHistory(cluster, kind, namespace, name)) as RolloutHistoryResultDTO;
  } catch (e) {
    return { revisions: [], error: e instanceof Error ? e.message : "rollout history failed" };
  }
}

// objectUID/templateDigest are the revision the history showed: the backend
// refuses before writing when the revision at that number changed (A3 §4.5).
export async function rolloutUndo(cluster: string, kind: string, namespace: string, name: string, uid: string, revision: number, objectUID: string, templateDigest: string): Promise<void> {
  const r = (await WorkloadsService.RolloutUndo(cluster, kind, namespace, name, uid, revision, objectUID, templateDigest)) as ActionResultDTO;
  report(r, `rolled ${kind.toLowerCase()} ${namespace}/${name} back to revision ${revision}`, "Rollback failed");
}

export async function rolloutPause(cluster: string, namespace: string, name: string, uid: string, pause: boolean): Promise<void> {
  const r = (await WorkloadsService.RolloutPause(cluster, namespace, name, uid, pause)) as ActionResultDTO;
  report(r, `${pause ? "paused" : "resumed"} rollouts for deployment ${namespace}/${name}`, pause ? "Pause failed" : "Resume failed");
}
