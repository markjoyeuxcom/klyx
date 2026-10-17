import { GitOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { EventRowDTO, RuleDTO } from "../store/fleet";

// The Flux Operator (coverage S1a): the Operator view's rows are the health
// Observer's shared rows (health/useFluxOperatorRows); this bridge keeps the
// inspector's on-demand detail and the operator's verbs.

export type FluxOpOwnerDTO = { kind: string; namespace?: string; name: string };

export type FluxOpCommonDTO = {
  uid: string; gvr: string; generation: number;
  kind: string; namespace: string; name: string;
  created?: string;
  suspended: boolean;
  ready: "Ready" | "Failed" | "Reconciling" | "Unknown" | "";
  reason?: string; message?: string;
  readySince?: string; readyGeneration?: number;
  stalled?: boolean; progress?: string;
  resume?: "" | "awaiting" | "evaluating" | "unproven";
  owner?: FluxOpOwnerDTO;
  clipped: string[];
  rule: RuleDTO;
};

export type FluxInstanceDTO = FluxOpCommonDTO & {
  binding: "bound" | "orphan" | "ambiguous" | "unverified" | "";
  version?: string; registry?: string; variant?: string; artifact?: string;
  components: string[];
  running: { name: string; repository?: string; tag?: string; digest?: string }[];
  clusterType?: string; multitenant?: boolean; networkPolicy?: boolean; clusterSize?: string;
  shardKey?: string; shards: string[];
  sync?: { kind: string; url: string; ref?: string; path?: string; interval?: string; provider?: string };
  syncName?: string; wait?: boolean; patchCount?: number;
  lastApplied?: string; lastAttempted?: string; lastArtifact?: string;
};

export type FluxSetDTO = FluxOpCommonDTO & {
  inputStrategy?: string;
  inputsFrom: { kind?: string; name?: string; selector?: string }[];
  dependsOn: { apiVersion: string; kind: string; name: string; namespace?: string; ready?: boolean; hasReadyExpr?: boolean }[];
  steps: string[]; stepCount?: number; inputCount?: number; resourceCount?: number; hasTemplate?: boolean;
  serviceAccountName?: string; wait?: boolean; lastAppliedRevision?: string; inventoryCount?: number;
};

export type FluxScheduleDTO = { cron: string; timeZone?: string; window?: string };

export type FluxProviderDTO = FluxOpCommonDTO & {
  type: string; url?: string; serviceAccountName?: string; secretRef?: string; certSecretRef?: string; insecure?: boolean;
  schedule: FluxScheduleDTO[];
  nextSchedule?: FluxScheduleDTO & { when?: string };
  lastExportedRevision?: string; exportedInputCount: number;
};

export type FluxSetRowDTO = { uid: string; kind: "ResourceSet" | "ResourceSetInputProvider"; rule: RuleDTO; set?: FluxSetDTO; provider?: FluxProviderDTO };

export type FluxReportDTO = {
  uid: string; namespace: string; name: string;
  reportingSucceeded: boolean; conditionChanged?: string;
  entitlement?: string; distributionStatus?: string; distributionVersion?: string; managedBy?: string;
  serverVersion?: string; platform?: string; nodes?: number; operatorVersion?: string; operatorPlatform?: string;
  components: { name: string; ready: boolean; status?: string; image?: string }[];
  reconcilers: { kind: string; running: number; failing: number; suspended: number; totalSize?: string }[];
  sync?: { id?: string; path?: string; status?: string; source?: string; ready: boolean };
};

export type FluxOpDetailDTO = {
  // the object this detail describes; replaced: the name now holds another
  uid: string;
  replaced?: boolean;
  history: { firstReconciled?: string; lastReconciled?: string; duration?: string; status?: string; totalReconciliations: number; metadata?: Record<string, string> }[];
  inventory: { namespace?: string; name: string; group?: string; kind: string; version?: string }[];
  inventoryTotal: number;
  readyExprs?: Record<string, string>;
  events: EventRowDTO[];
  eventsError?: string;
  error?: string;
};

export type FluxOpVerb = "reconcile" | "fetch" | "suspend" | "resume" | "refresh";

export async function fluxOpAct(cluster: string, kind: string, namespace: string, name: string, uid: string, verb: FluxOpVerb): Promise<{ ok: boolean; error: string }> {
  try {
    return (await GitOpsService.FluxOpAct(cluster, kind, namespace, name, uid, verb)) as { ok: boolean; error: string };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) };
  }
}

// getFluxOpDetail reads the object the inspector shows, fenced by its UID:
// a reply about another object is refused (Codex's S1a implementation
// review, finding 9).
export async function getFluxOpDetail(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<FluxOpDetailDTO> {
  const failed = (error: string): FluxOpDetailDTO => ({ uid, history: [], inventory: [], inventoryTotal: 0, events: [], error });
  try {
    const d = (await GitOpsService.GetFluxOpDetail(cluster, kind, namespace, name, uid)) as unknown as FluxOpDetailDTO;
    if (d.uid !== uid && !d.error) return failed(`${kind} ${namespace}/${name} was replaced (its UID changed); nothing about the new object is shown here`);
    return d;
  } catch (e) {
    return failed(e instanceof Error ? e.message : String(e));
  }
}
