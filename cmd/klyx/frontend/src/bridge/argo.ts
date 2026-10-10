import { useFleet } from "../store/fleet";
import type { ArgoApplicationSetsResultDTO, ArgoProjectsResultDTO, ArgoResourceDTO, ArgoResultDTO } from "../store/fleet";
import { ArgoService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };
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
const gen = { apps: 0, projects: 0, appSets: 0 };

// listArgoApps fetches every Application on the cluster (broken-first sort is
// backend-side).
export async function listArgoApps(cluster: string): Promise<void> {
  const seq = ++gen.apps;
  useFleet.getState().setArgoLoading(cluster);
  const current = () => seq === gen.apps && useFleet.getState().argo.cluster === cluster;
  try {
    const r = (await ArgoService.ListApplications(cluster)) as ArgoResultDTO;
    if (!current()) return;
    useFleet.getState().setArgo(cluster, r ?? { available: false, error: "no response", apps: [] });
  } catch (e) {
    if (current()) useFleet.getState().setArgo(cluster, { available: false, error: String(e), apps: [] });
  }
}

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

export async function listArgoApplicationSets(cluster: string): Promise<void> {
  const seq = ++gen.appSets;
  useFleet.getState().setArgoApplicationSetsLoading(cluster);
  const current = () => seq === gen.appSets && useFleet.getState().argo.cluster === cluster;
  try {
    const r = (await ArgoService.ListApplicationSets(cluster)) as ArgoApplicationSetsResultDTO;
    if (!current()) return;
    useFleet.getState().setArgoApplicationSets(cluster, r ?? { available: false, error: "no response", applicationSets: [] });
  } catch (e) {
    if (current()) useFleet.getState().setArgoApplicationSets(cluster, { available: false, error: String(e), applicationSets: [] });
  }
}

// refreshArgoApp triggers a re-compare against the source (Argo's reconcile).
// A completion belongs to the cluster it was raised on: the status names that
// cluster, and the follow-up re-list runs only while the lens still shows it -
// a late completion must never relabel or empty another cluster's state.
function relistIfCurrent(cluster: string) {
  if (useFleet.getState().argo.cluster === cluster) void listArgoApps(cluster);
}

export async function refreshArgoApp(cluster: string, namespace: string, name: string): Promise<void> {
  const r = (await ArgoService.RefreshApp(cluster, namespace, name)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `refresh requested for ${namespace}/${name} on ${cluster}` }
      : { kind: "error", message: `${cluster}: ${r.error || "Refresh failed"}` },
  );
  if (r.ok) relistIfCurrent(cluster);
}

// syncArgoApp starts an Argo sync operation with the options selected in the
// sync modal.
export async function syncArgoApp(cluster: string, namespace: string, name: string, options: ArgoSyncOptions): Promise<void> {
  const r = (await ArgoService.SyncApp(cluster, namespace, name, options)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `sync started for ${namespace}/${name} on ${cluster}` }
      : { kind: "error", message: `${cluster}: ${r.error || "Sync failed"}` },
  );
  if (r.ok) relistIfCurrent(cluster);
}
