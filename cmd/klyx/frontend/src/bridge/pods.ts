import { useFleet, PodDetailDTO, AdhocResultDTO } from "../store/fleet";
import { PodsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };

// deletePod deletes the pod OBJECT the verb was armed against (uid, design
// §5b): the API server's UID precondition refuses a same-name replacement.
// The list is the Observer's live rows - no re-read follows.
export async function deletePod(cluster: string, namespace: string, name: string, uid: string): Promise<void> {
  const r = (await PodsService.DeletePod(cluster, namespace, name, uid)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `pod ${namespace}/${name} deleted` }
      : { kind: "error", message: r.error || "Delete failed" },
  );
}

// openPodDetail selects the pod OBJECT (namespace, name and uid) and reads its
// detail directly. Re-opening the same object keeps the shown detail while
// the re-read is in flight. Only the latest read lands, and only on its own
// cluster (DEV/STG/PRD share namespace/name - review 2026-10-03 #5);
// setPodDetail drops it unless the selection is still that object (design
// §5b). A failed read is the read's outcome, never an empty detail. Links
// that know only a name go through the "select-pod" intent, which the Pods
// lens resolves against its rows.
let detailSeq = 0;
export async function openPodDetail(cluster: string, namespace: string, name: string, uid: string): Promise<void> {
  const ref = { namespace, name, uid };
  const seq = ++detailSeq;
  useFleet.getState().selectPod(ref);
  const current = () => seq === detailSeq && useFleet.getState().pods.cluster === cluster;
  let d: PodDetailDTO;
  try {
    d = (await PodsService.GetPodDetail(cluster, namespace, name)) as PodDetailDTO;
  } catch (e) {
    d = { ...EMPTY_DETAIL, error: e instanceof Error ? e.message : "pod detail read failed" };
  }
  if (current()) useFleet.getState().setPodDetail(ref, d);
}

const EMPTY_DETAIL: PodDetailDTO = {
  summary: { namespace: "", name: "", ready: false, phase: "", reason: "", rank: "healthy", restarts: 0, node: "", ip: "", ownerKind: "", ownerName: "", ageSeconds: 0, containers: [] },
  labels: {}, annotations: {}, conditions: [], events: [], yaml: "", qosClass: "", serviceAccount: "",
};

// launchDiagnosticPod creates a TTL-bounded scratch debugging pod in the
// klyx-scratch namespace. Returns the per-object result (ok or an honest error).
export async function launchDiagnosticPod(cluster: string, name: string, image: string, ttlSeconds: number): Promise<AdhocResultDTO> {
  return (await PodsService.LaunchDiagnosticPod(cluster, name, image, ttlSeconds)) as AdhocResultDTO;
}
