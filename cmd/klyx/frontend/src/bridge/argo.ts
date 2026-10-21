import { resultStatus, type ActionResultLike } from "../admission/finding";
import { useFleet } from "../store/fleet";
import type { ArgoProjectsResultDTO, ArgoResourceDTO } from "../store/fleet";
import { ArgoService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = ActionResultLike;
export type ArgoSyncOptions = {
  revision: string;
  strategy: "hook" | "apply";
  prune: boolean;
  dryRun: boolean;
  force: boolean;
  replace: boolean;
  serverSideApply: boolean;
  resources: ArgoResourceDTO[];
};

// Each list read carries a generation per kind: only the newest read for the
// lens's current cluster may land (a slow older read - e.g. the list that
// follows a refresh racing a manual reload - never overwrites a newer one).
// Transport failures are read failures (error), never "Argo not installed".
const gen = { projects: 0 };

// The Applications and ApplicationSets are the health Observer's shared rows
// (health/useArgoRows); AppProjects stay a fetch.
export async function listArgoProjects(cluster: string): Promise<void> {
  const seq = ++gen.projects;
  useFleet.getState().setArgoProjectsLoading(cluster);
  const current = () => seq === gen.projects && useFleet.getState().argo.cluster === cluster;
  try {
    const r = (await ArgoService.ListProjects(cluster)) as ArgoProjectsResultDTO;
    if (!current()) return;
    useFleet.getState().setArgoProjects(cluster, r ?? { available: false, error: "no response", projects: [] });
  } catch (e) {
    if (current()) useFleet.getState().setArgoProjects(cluster, { available: false, error: String(e), projects: [] });
  }
}

// refreshArgoApp triggers a re-compare against the source (Argo's reconcile)
// for the armed Application (uid; design §5b): the backend writes only that
// object. A completion names the cluster it was raised on; the rows update
// on their own (no re-list).
export async function refreshArgoApp(cluster: string, namespace: string, name: string, uid: string): Promise<void> {
  const r = (await ArgoService.RefreshApp(cluster, namespace, name, uid)) as ActionResultDTO;
  useFleet.getState().setActionStatus(resultStatus(r, `refresh requested for ${namespace}/${name} on ${cluster}`, "Refresh failed", `${cluster}: `));
}

// syncArgoApp starts an Argo sync operation on the armed Application with the
// options selected in the sync modal.
export async function syncArgoApp(cluster: string, namespace: string, name: string, uid: string, options: ArgoSyncOptions): Promise<void> {
  const r = (await ArgoService.SyncApp(cluster, namespace, name, uid, options)) as ActionResultDTO;
  useFleet.getState().setActionStatus(resultStatus(r, `sync started for ${namespace}/${name} on ${cluster}`, "Sync failed", `${cluster}: `));
}
