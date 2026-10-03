import { Events } from "@wailsio/runtime";
import { useFleet, PodsResultDTO, PodDetailDTO, AdhocResultDTO } from "../store/fleet";
import { PodsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };

export async function listPods(cluster: string, namespace: string): Promise<void> {
  useFleet.getState().setPodsLoading(cluster, namespace);
  try {
    const r = (await PodsService.ListPods(cluster, namespace)) as PodsResultDTO;
    // Drop a stale response if the user changed cluster OR namespace while in flight.
    const cur = useFleet.getState().pods;
    if (cur.cluster !== cluster || cur.namespace !== namespace) return;
    useFleet.getState().setPods(cluster, namespace, r ?? { namespaces: [], pods: [] });
  } catch {
    // Clear the loading flag so the view doesn't get stuck on "Loading…".
    if (useFleet.getState().pods.cluster === cluster) {
      useFleet.setState((s) => ({ pods: { ...s.pods, loading: false } }));
    }
  }
}

// liveOpenRetryMs paces re-attempts when OpenLive* reports the cluster is not
// connected yet (app launch race: the view can mount before the registry
// finishes connecting). Shared by the pods/workloads/events live bridges.
export const liveOpenRetryMs = 3000;

// openLivePods subscribes to live pod updates for a cluster+namespace.
// The backend fires an immediate emit so the view receives data without calling
// listPods first. Returns a cleanup function to be called on unmount or ns change.
export function openLivePods(cluster: string, namespace: string): () => void {
  // Claim the store slice BEFORE anything can emit: the data handler and
  // setPodsLive both guard on store cluster+namespace, which the previous
  // unmount's clearPods left null - without this the first emit after mount is
  // silently dropped (empty list + "manual" forever). Also flips the empty
  // state to "Loading…" until real data lands.
  useFleet.getState().setPodsLoading(cluster, namespace);

  const dataEvent = "livePods:" + cluster + ":" + namespace;
  const statusEvent = "livePodsStatus:" + cluster + ":" + namespace;

  const offData = Events.On(dataEvent, (ev: { data: PodsResultDTO }) => {
    const cur = useFleet.getState().pods;
    if (cur.cluster !== cluster || cur.namespace !== namespace) return;
    useFleet.getState().setPods(cluster, namespace, ev.data ?? { namespaces: [], pods: [] });
  });

  const offStatus = Events.On(statusEvent, (ev: { data: { live: boolean } }) => {
    useFleet.getState().setPodsLive(cluster, namespace, ev.data?.live ?? false);
  });

  // Open the live sub. "cluster not connected" comes back as ok:false (a value,
  // not a throw). Degrade honestly - manual indicator plus a one-shot list
  // attempt - and keep retrying until the cluster connects or the view unmounts.
  let closed = false;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  function degrade() {
    useFleet.getState().setPodsLive(cluster, namespace, false);
    void listPods(cluster, namespace);
    retryTimer = setTimeout(tryOpen, liveOpenRetryMs);
  }
  function tryOpen() {
    PodsService.OpenLivePods(cluster, namespace)
      .then((r) => {
        if (closed || (r as ActionResultDTO | undefined)?.ok) return;
        degrade();
      })
      .catch(() => {
        if (!closed) degrade();
      });
  }
  tryOpen();

  return () => {
    closed = true;
    if (retryTimer !== undefined) clearTimeout(retryTimer);
    if (typeof offData === "function") offData();
    if (typeof offStatus === "function") offStatus();
    PodsService.CloseLivePods(cluster, namespace).catch(() => undefined);
  };
}

export async function deletePod(cluster: string, namespace: string, name: string): Promise<void> {
  const r = (await PodsService.DeletePod(cluster, namespace, name)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `pod ${namespace}/${name} deleted` }
      : { kind: "error", message: r.error || "Delete failed" },
  );
  if (r.ok) {
    const cur = useFleet.getState().pods;
    void listPods(cluster, cur.namespace);
  }
}

export async function openPodDetail(cluster: string, namespace: string, name: string): Promise<void> {
  const ref = { namespace, name };
  useFleet.getState().selectPod(ref);
  try {
    const d = (await PodsService.GetPodDetail(cluster, namespace, name)) as PodDetailDTO;
    // Drop a late detail for another pod OR another cluster: DEV/STG/PRD share
    // namespace/name (review 2026-10-03 #5).
    const cur = useFleet.getState().pods;
    const sel = cur.selected;
    if (cur.cluster !== cluster || !sel || sel.namespace !== namespace || sel.name !== name) return;
    useFleet.getState().setPodDetail(ref, d);
  } catch {
    // On failure, clear detailLoading so the panel doesn't spin forever - but
    // only for THIS cluster's selection (another cluster's late rejection must
    // not touch the new cluster's panel).
    const cur = useFleet.getState().pods;
    const sel = cur.selected;
    if (cur.cluster === cluster && sel && sel.namespace === namespace && sel.name === name) {
      useFleet.setState((s) => ({ pods: { ...s.pods, detailLoading: false } }));
    }
  }
}

// launchDiagnosticPod creates a TTL-bounded scratch debugging pod in the
// klyx-scratch namespace. Returns the per-object result (ok or an honest error).
export async function launchDiagnosticPod(cluster: string, name: string, image: string, ttlSeconds: number): Promise<AdhocResultDTO> {
  return (await PodsService.LaunchDiagnosticPod(cluster, name, image, ttlSeconds)) as AdhocResultDTO;
}
