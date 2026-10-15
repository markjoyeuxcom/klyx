import { useFleet, ResourceDetailDTO } from "../store/fleet";
import { Browser, Clipboard } from "@wailsio/runtime";
import { GitOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The Flux lists are the health Observer's shared rows (health/useFluxRows);
// this bridge keeps the on-demand reads and the day-2 verbs.

// Detail reads are fenced by request generation, cluster and selected key:
// only the newest read for the CURRENT cluster's CURRENT selection may land,
// on success or failure. Overlapping reads (live re-emits, fast j/k) and a
// late read from a cluster the lens has left are dropped.
let detailSeq = 0;

export async function getResourceDetail(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  const seq = ++detailSeq;
  const key = `${kind}/${namespace}/${name}`;
  const current = () => {
    const g = useFleet.getState().gitops;
    return seq === detailSeq && g.cluster === cluster && g.expandedKey === key;
  };
  try {
    const d = (await GitOpsService.GetResourceDetail(cluster, kind, namespace, name)) as ResourceDetailDTO;
    if (d && d.name && current()) {
      useFleet.getState().setDetail(d, cluster);
    }
  } catch (e) {
    console.error("getResourceDetail", e);
    // The expanded panel shows "Loading detail…" until a detail lands (there is
    // no separate loading flag) — collapse it and surface the failure so it
    // doesn't spin forever.
    if (current()) {
      useFleet.getState().collapse();
      useFleet.getState().setActionStatus({ kind: "error", message: `Failed to load detail for ${namespace}/${name}` });
    }
  }
}

type ActionResultDTO = { ok: boolean; error: string };

// The verbs carry the UID of the row they were armed against: the backend
// writes only that object - a same-name replacement is refused (design §5b).
export async function reconcile(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<void> {
  const r = (await GitOpsService.Reconcile(cluster, kind, namespace, name, uid)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok ? { kind: "success", message: `Reconcile requested for ${namespace}/${name}` }
         : { kind: "error", message: r.error || "Reconcile failed" },
  );
}

export async function reconcileWithSource(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<void> {
  const r = (await GitOpsService.ReconcileWithSource(cluster, kind, namespace, name, uid)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok ? { kind: "success", message: `Reconcile (with source) requested for ${namespace}/${name}` }
         : { kind: "error", message: r.error || "Reconcile failed" },
  );
}

export async function setSuspend(cluster: string, kind: string, namespace: string, name: string, uid: string, suspend: boolean): Promise<void> {
  const r = (await GitOpsService.SetSuspend(cluster, kind, namespace, name, uid, suspend)) as ActionResultDTO;
  useFleet.getState().setActionStatus(
    r.ok ? { kind: "success", message: `${suspend ? "Suspended" : "Resumed"} ${namespace}/${name}` }
         : { kind: "error", message: r.error || "Action failed" },
  );
}

// via: "cache" - built from the Git cache at revision, dry-run on the
// station's own connection (The Line slice 6e) - or "flux CLI".
export type FluxDiffChangeDTO = { action: string; kind: string; namespace?: string; name: string; diff?: string; error?: string };
export type FluxDiffDTO = {
  available: boolean; hasChanges: boolean; output: string; error: string;
  via?: string; revision?: string; notes?: string[]; changes?: FluxDiffChangeDTO[];
};

export async function fluxDiff(cluster: string, namespace: string, name: string, path: string): Promise<FluxDiffDTO> {
  return (await GitOpsService.FluxDiff(cluster, namespace, name, path)) as FluxDiffDTO;
}

type GitLinkDTO = { url: string; isDeepLink: boolean; copyText: string; error?: string };

export async function resolveGitLink(cluster: string, kind: string, namespace: string, name: string): Promise<void> {
  const link = (await GitOpsService.ResolveGitLink(cluster, kind, namespace, name)) as GitLinkDTO;
  // a read that failed on the way is named - never "no Git source" (§5b)
  if (link.error) {
    useFleet.getState().setActionStatus({ kind: "error", message: `Could not resolve the Git source: ${link.error}` });
  } else if (link.isDeepLink && link.url) {
    await Browser.OpenURL(link.url);
    useFleet.getState().setActionStatus({ kind: "success", message: `Opened ${link.url}` });
  } else if (link.copyText) {
    await Clipboard.SetText(link.copyText);
    useFleet.getState().setActionStatus({ kind: "success", message: "Copied source reference to clipboard" });
  } else {
    useFleet.getState().setActionStatus({ kind: "error", message: "No Git source to open for this resource" });
  }
}
