import { useFleet, FluxResourceDTO, FluxSourceDTO, ResourceDetailDTO } from "../store/fleet";
import { Events, Browser, Clipboard } from "@wailsio/runtime";
import { GitOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

const GITOPS_UPDATED = "gitops:updated";

export async function openGitOps(cluster: string): Promise<() => void> {
  useFleet.getState().setGitOpsLoading(cluster);
  await GitOpsService.Open(cluster);
  const off = Events.On(GITOPS_UPDATED, (ev: { data: { cluster: string; resources: FluxResourceDTO[]; sources: FluxSourceDTO[]; watchError?: string } }) => {
    const d = ev.data;
    if (d && d.cluster === cluster) {
      useFleet.getState().setGitOps(cluster, d.resources ?? [], d.sources ?? [], d.watchError);
    }
  });
  return typeof off === "function" ? off : () => {};
}

export async function closeGitOps(cluster: string): Promise<void> {
  try {
    await GitOpsService.Close(cluster);
  } finally {
    useFleet.getState().clearGitOps();
  }
}

export async function getResourceDetail(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  try {
    const d = (await GitOpsService.GetResourceDetail(cluster, kind, namespace, name)) as ResourceDetailDTO;
    if (d && d.name) {
      useFleet.getState().setDetail(d);
    }
  } catch (e) {
    console.error("getResourceDetail", e);
    // The expanded panel shows "Loading detail…" until a detail lands (there is
    // no separate loading flag) — collapse it and surface the failure so it
    // doesn't spin forever.
    if (useFleet.getState().gitops.expandedKey === `${kind}/${namespace}/${name}`) {
      useFleet.getState().collapse();
      useFleet.getState().setActionStatus({ kind: "error", message: `Failed to load detail for ${namespace}/${name}` });
    }
  }
}

type ActionResultDTO = { ok: boolean; error: string };

export async function reconcile(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  const r = (await GitOpsService.Reconcile(cluster, kind, namespace, name)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok ? { kind: "success", message: `Reconcile requested for ${namespace}/${name}` }
         : { kind: "error", message: r.error || "Reconcile failed" },
  );
}

export async function reconcileWithSource(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  const r = (await GitOpsService.ReconcileWithSource(cluster, kind, namespace, name)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok ? { kind: "success", message: `Reconcile (with source) requested for ${namespace}/${name}` }
         : { kind: "error", message: r.error || "Reconcile failed" },
  );
}

export async function setSuspend(cluster: string, kind: string, namespace: string, name: string, suspend: boolean): Promise<void> {
  const r = (await GitOpsService.SetSuspend(cluster, kind, namespace, name, suspend)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok ? { kind: "success", message: `${suspend ? "Suspended" : "Resumed"} ${namespace}/${name}` }
         : { kind: "error", message: r.error || "Action failed" },
  );
}

export type FluxDiffDTO = { available: boolean; hasChanges: boolean; output: string; error: string };

export async function fluxDiff(cluster: string, namespace: string, name: string, path: string): Promise<FluxDiffDTO> {
  return (await GitOpsService.FluxDiff(cluster, namespace, name, path)) as FluxDiffDTO;
}

type GitLinkDTO = { url: string; isDeepLink: boolean; copyText: string };

export async function resolveGitLink(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  const link = (await GitOpsService.ResolveGitLink(cluster, kind, namespace, name)) as GitLinkDTO;
  if (link.isDeepLink && link.url) {
    await Browser.OpenURL(link.url);
    useFleet.getState().setActionStatus({ kind: "success", message: `Opened ${link.url}` });
  } else if (link.copyText) {
    await Clipboard.SetText(link.copyText);
    useFleet.getState().setActionStatus({ kind: "success", message: "Copied source reference to clipboard" });
  } else {
    useFleet.getState().setActionStatus({ kind: "error", message: "No Git source to open for this resource" });
  }
}
