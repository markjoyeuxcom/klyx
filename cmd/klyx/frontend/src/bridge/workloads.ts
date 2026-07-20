import { Events } from "@wailsio/runtime";
import { useFleet, WorkloadsResultDTO, WorkloadCompareDTO } from "../store/fleet";
import { WorkloadsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { liveOpenRetryMs } from "./pods";

type ActionResultDTO = { ok: boolean; error: string };

export async function scaleWorkload(cluster: string, kind: string, namespace: string, name: string, replicas: number): Promise<void> {
  const r = (await WorkloadsService.ScaleWorkload(cluster, kind, namespace, name, replicas)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `scaled ${kind.toLowerCase()} ${namespace}/${name} to ${replicas}` }
      : { kind: "error", message: r.error || "Scale failed" },
  );
  if (r.ok) {
    const cur = useFleet.getState().workloads;
    void listWorkloads(cluster, cur.namespace);
  }
}

export async function rolloutRestart(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  const r = (await WorkloadsService.RolloutRestart(cluster, kind, namespace, name)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `restart triggered for ${kind.toLowerCase()} ${namespace}/${name}` }
      : { kind: "error", message: r.error || "Restart failed" },
  );
  if (r.ok) {
    const cur = useFleet.getState().workloads;
    void listWorkloads(cluster, cur.namespace);
  }
}

export async function listWorkloads(cluster: string, namespace: string): Promise<void> {
  useFleet.getState().setWorkloadsLoading(cluster, namespace);
  try {
    const r = (await WorkloadsService.ListWorkloads(cluster, namespace)) as WorkloadsResultDTO;
    // Drop a stale response if the user changed cluster OR namespace while in flight.
    const cur = useFleet.getState().workloads;
    if (cur.cluster !== cluster || cur.namespace !== namespace) return;
    useFleet.getState().setWorkloads(cluster, namespace, r ?? { fluxPresent: false, namespaces: [], workloads: [] });
  } catch {
    // On failure, clear the loading flag (keep any existing items) so the view
    // doesn't get stuck on "Loading…". Only if still on this cluster.
    if (useFleet.getState().workloads.cluster === cluster) {
      useFleet.setState((s) => ({ workloads: { ...s.workloads, loading: false } }));
    }
  }
}

// openLiveWorkloads subscribes to live workload updates for a cluster+namespace.
// The backend fires an immediate emit so the view receives data without calling
// listWorkloads first. Returns a cleanup function to be called on unmount.
export function openLiveWorkloads(cluster: string, namespace: string): () => void {
  // Claim the store slice BEFORE anything can emit: the data handler and
  // setWorkloadsLive both guard on store cluster+namespace, which the previous
  // unmount's clearWorkloads left null - without this the first emit after
  // mount is silently dropped. Also flips the empty state to "Loading…".
  useFleet.getState().setWorkloadsLoading(cluster, namespace);

  const dataEvent = "liveWorkloads:" + cluster + ":" + namespace;
  const statusEvent = "liveWorkloadsStatus:" + cluster + ":" + namespace;

  const offData = Events.On(dataEvent, (ev: { data: WorkloadsResultDTO }) => {
    const cur = useFleet.getState().workloads;
    if (cur.cluster !== cluster || cur.namespace !== namespace) return;
    useFleet.getState().setWorkloads(cluster, namespace, ev.data ?? { fluxPresent: false, namespaces: [], workloads: [] });
  });

  const offStatus = Events.On(statusEvent, (ev: { data: { live: boolean } }) => {
    useFleet.getState().setWorkloadsLive(cluster, namespace, ev.data?.live ?? false);
  });

  // Open the live sub. "cluster not connected" comes back as ok:false (a value,
  // not a throw - the app-launch race). Degrade honestly and retry until the
  // cluster connects or the view unmounts.
  let closed = false;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  function degrade() {
    useFleet.getState().setWorkloadsLive(cluster, namespace, false);
    void listWorkloads(cluster, namespace);
    retryTimer = setTimeout(tryOpen, liveOpenRetryMs);
  }
  function tryOpen() {
    WorkloadsService.OpenLiveWorkloads(cluster, namespace)
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
    WorkloadsService.CloseLiveWorkloads(cluster, namespace).catch(() => undefined);
  };
}

// workloadCompare fetches one cluster's drift-relevant view of a workload for
// the fleet compare. Errors fold into the DTO (degraded, never "absent").
export async function workloadCompare(cluster: string, kind: string, namespace: string, name: string): Promise<WorkloadCompareDTO> {
  return (await WorkloadsService.WorkloadCompare(cluster, kind, namespace, name)) as WorkloadCompareDTO;
}
