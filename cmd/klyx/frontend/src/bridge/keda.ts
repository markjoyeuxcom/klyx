import { KEDAService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { RuleDTO } from "../store/fleet";
import type { FluxOpOwnerDTO } from "./fluxop";

// Coverage S9: the Autoscaling lens's rows (HPAs, KEDA's ScaledObjects,
// ScaledJobs and authentications) and KEDA's on-demand reads and verb.
// Mirrors internal/appbridge/keda_dto.go and keda_service.go.

export type HPAConditionDTO = { type: string; status: string; reason?: string; message?: string; at?: string };

export type HPARowDTO = {
  uid: string; gvr: string; namespace: string; name: string; created?: string;
  target: { apiVersion: string; kind: string; name: string };
  min?: number; max: number; current: number; desired: number; lastScale?: string;
  metrics: number; metricTypes: string[]; conditions: HPAConditionDTO[]; conditionsCut?: boolean;
  generation: number; observedGeneration?: number; generationReported: boolean;
  // a KEDA-owned HPA: a fact row, judged under its ScaledObject; no rule
  kedaOwner?: string; others: string[]; reason?: string; rule?: RuleDTO;
};

export type KEDAConditionDTO = { type: string; status: string; reason?: string; message?: string };
export type KEDATriggerDTO = { type: string; name?: string; metricType?: string; authName?: string; authKind?: string; cached?: boolean };
export type KEDAMetricDTO = { metric: string; failures?: number; status?: string; active?: boolean };
export type KEDAPauseDTO = {
  paused?: string; pausedReplicas?: string; scaleIn?: string; scaleOut?: string;
  kind: "full" | "directional" | ""; annotation?: string; requested?: number; malformed?: boolean; directions: string[];
  notApplied?: string; unverified?: string; applied?: number; completed: boolean;
};
export type KEDAFindingDTO = { role: string; label: string; text?: string; note?: boolean };

export type ScaledObjectDTO = {
  uid: string; gvr: string; namespace: string; name: string; created?: string;
  targetGroup: string; targetKind: string; targetName: string; hpaNameSpec?: string; hpaName?: string;
  min?: number; max?: number; idle?: number; fallback?: { failureThreshold: number; replicas: number; behavior: string };
  scalingModifiers?: boolean; transfer?: boolean;
  triggers: KEDATriggerDTO[]; triggerCount: number; triggersCut?: boolean;
  conditions: KEDAConditionDTO[]; conditionsCut?: boolean;
  metrics: KEDAMetricDTO[]; metricsCut?: boolean; healthTracked: boolean; lastActive?: string;
  pause: KEDAPauseDTO;
  hpa?: { name: string; uid: string; min?: number; max: number; current: number; desired: number; conditions: HPAConditionDTO[] };
  hpaCurrent: boolean; hpaComplete: boolean; others: string[]; version: string; authsRead: string;
  findings: KEDAFindingDTO[]; owner?: FluxOpOwnerDTO; reason?: string; rule: RuleDTO;
};

export type ScaledJobDTO = {
  uid: string; gvr: string; namespace: string; name: string; created?: string;
  triggers: KEDATriggerDTO[]; triggerCount: number; triggersCut?: boolean;
  conditions: KEDAConditionDTO[]; conditionsCut?: boolean; lastActive?: string;
  successfulLimit?: number; failedLimit?: number; pause: KEDAPauseDTO; version: string;
  findings: KEDAFindingDTO[]; owner?: FluxOpOwnerDTO; reason?: string; rule: RuleDTO;
};

export type KEDARefDTO = { kind: string; name: string; key?: string; parameter?: string };
export type KEDASourceDTO = { kind: string; refs: KEDARefDTO[]; refCount: number; cut?: boolean; detail?: string; method?: string };
export type KEDAAuthDTO = {
  uid: string; gvr: string; namespace?: string; name: string; cluster: boolean; created?: string;
  sources: KEDASourceDTO[]; podIdentity?: { provider: string; identityOwner?: string; roleArn?: string; identityId?: string };
  users: string[]; usersComplete: boolean;
};

export type KEDARefCheckDTO = { source: string; kind: string; name: string; key?: string; namespace?: string; state: string; why?: string };
export type KEDASubjectDTO = {
  provider: string; mode: string; base: string; serviceAccount?: string; roleFrom?: string;
  requested: string[]; refusals: string[]; unresolved: string[]; notes: string[];
};
export type KEDAAuthCheckDTO = {
  kind: string; name: string; state: string; why?: string; visible: Record<string, string>;
  refs: KEDARefCheckDTO[]; refsCut?: boolean; subject?: KEDASubjectDTO;
};
export type KEDAInspectionDTO = {
  uid: string; handler?: string; none?: boolean; candidates: string[]; candidatesCut?: boolean;
  version?: string; disagree: string[]; auths: KEDAAuthCheckDTO[]; error?: string;
};
export type KEDAJobDTO = { name: string; created?: string; succeeded: number; failed: number; active: number; complete: boolean; failedOut: boolean };
export type KEDAJobsDTO = { jobs: KEDAJobDTO[]; complete: boolean; read: number; owned: number; remaining?: number; error?: string };
export type ReplicaOwnerDTO = {
  autoscaler: "owned" | "unowned" | "unverified"; autoscalerWhy?: string; owners: string[];
  gitops: "owned" | "unowned" | "unverified"; gitopsOwner?: string; gitopsWhy?: string; error?: string;
};

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export async function inspectScaler(cluster: string, resource: "scaledobjects" | "scaledjobs", namespace: string, name: string, uid: string): Promise<KEDAInspectionDTO> {
  try {
    return (await KEDAService.Inspect(cluster, resource, namespace, name, uid)) as unknown as KEDAInspectionDTO;
  } catch (e) {
    return { uid, candidates: [], disagree: [], auths: [], error: errText(e) };
  }
}

export async function scaledJobJobs(cluster: string, namespace: string, name: string, uid: string): Promise<KEDAJobsDTO> {
  try {
    return (await KEDAService.Jobs(cluster, namespace, name, uid)) as unknown as KEDAJobsDTO;
  } catch (e) {
    return { jobs: [], complete: false, read: 0, owned: 0, error: errText(e) };
  }
}

export async function replicaOwner(cluster: string, kind: string, namespace: string, name: string): Promise<ReplicaOwnerDTO> {
  try {
    return (await KEDAService.ReplicaOwner(cluster, kind, namespace, name)) as unknown as ReplicaOwnerDTO;
  } catch (e) {
    return { autoscaler: "unverified", gitops: "unverified", owners: [], error: errText(e) };
  }
}

export async function setScalerPaused(cluster: string, resource: "scaledobjects" | "scaledjobs", namespace: string, name: string, uid: string, pause: boolean): Promise<{ ok: boolean; error: string }> {
  try {
    return (await KEDAService.SetPaused(cluster, resource, namespace, name, uid, pause)) as { ok: boolean; error: string };
  } catch (e) {
    return { ok: false, error: errText(e) };
  }
}
