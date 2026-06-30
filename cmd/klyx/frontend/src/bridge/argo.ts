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

// listArgoApps fetches every Application on the cluster (broken-first sort is
// backend-side). Unavailable (no Argo, list failure) carries a message.
export async function listArgoApps(cluster: string): Promise<void> {
  useFleet.getState().setArgoLoading(cluster);
  try {
    const r = (await ArgoService.ListApplications(cluster)) as ArgoResultDTO;
    if (useFleet.getState().argo.cluster !== cluster) return;
    useFleet.getState().setArgo(cluster, r ?? { available: false, message: "no response", apps: [] });
  } catch (e) {
    if (useFleet.getState().argo.cluster === cluster) {
      useFleet.getState().setArgo(cluster, { available: false, message: String(e), apps: [] });
    }
  }
}

export async function listArgoProjects(cluster: string): Promise<void> {
  useFleet.getState().setArgoProjectsLoading(cluster);
  const listProjects = (ArgoService as unknown as { ListProjects: (cluster: string) => Promise<unknown> }).ListProjects;
  try {
    const r = (await listProjects(cluster)) as ArgoProjectsResultDTO;
    if (useFleet.getState().argo.cluster !== cluster) return;
    useFleet.getState().setArgoProjects(cluster, r ?? { available: false, message: "no response", projects: [] });
  } catch (e) {
    if (useFleet.getState().argo.cluster === cluster) {
      useFleet.getState().setArgoProjects(cluster, { available: false, message: String(e), projects: [] });
    }
  }
}

export async function listArgoApplicationSets(cluster: string): Promise<void> {
  useFleet.getState().setArgoApplicationSetsLoading(cluster);
  const listApplicationSets = (ArgoService as unknown as { ListApplicationSets: (cluster: string) => Promise<unknown> }).ListApplicationSets;
  try {
    const r = (await listApplicationSets(cluster)) as ArgoApplicationSetsResultDTO;
    if (useFleet.getState().argo.cluster !== cluster) return;
    useFleet.getState().setArgoApplicationSets(cluster, r ?? { available: false, message: "no response", applicationSets: [] });
  } catch (e) {
    if (useFleet.getState().argo.cluster === cluster) {
      useFleet.getState().setArgoApplicationSets(cluster, { available: false, message: String(e), applicationSets: [] });
    }
  }
}

// refreshArgoApp triggers a re-compare against the source (Argo's reconcile).
export async function refreshArgoApp(cluster: string, namespace: string, name: string): Promise<void> {
  const r = (await ArgoService.RefreshApp(cluster, namespace, name)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `refresh requested for ${name}` }
      : { kind: "error", message: r.error || "Refresh failed" },
  );
  if (r.ok) void listArgoApps(cluster);
}

// syncArgoApp starts an Argo sync operation with the options selected in the
// sync modal.
export async function syncArgoApp(cluster: string, namespace: string, name: string, options: ArgoSyncOptions): Promise<void> {
  const syncApp = ArgoService.SyncApp as unknown as (cluster: string, namespace: string, name: string, options: ArgoSyncOptions) => Promise<unknown>;
  const r = (await syncApp(cluster, namespace, name, options)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok
      ? { kind: "success", message: `sync started for ${name}` }
      : { kind: "error", message: r.error || "Sync failed" },
  );
  if (r.ok) void listArgoApps(cluster);
}
