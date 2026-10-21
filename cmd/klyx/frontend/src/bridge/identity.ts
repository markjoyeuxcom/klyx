import { IdentityService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Coverage S5: workload identity, read on demand (mirrors internal/identity
// and appbridge/identity_service.go). Values arrive already shaped: a value
// outside its documented form reads "unexpected value (not shown)".

export type IdValue = { shown: string; unexpected?: boolean };
export type IdDeclared = {
  namespace: string; name: string; uid: string;
  roleArn?: IdValue; audience?: IdValue; stsRegional?: IdValue; tokenExpiry?: IdValue;
  azureClient?: IdValue; azureTenant?: IdValue; azureExpiry?: IdValue; azureExpiryOutOfRange?: boolean;
  gkeServiceAccount?: IdValue; automount?: boolean;
};
export type IdEnvValue = { value: IdValue; fromRef?: boolean; expansion?: boolean; oversized?: boolean };
export type IdMechanism = { kind: "irsa" | "pod-identity" | "azure"; state: "complete" | "partial" | "unresolved"; missing?: string; audience?: IdValue; role?: IdValue; client?: IdValue; tenant?: IdValue; uri?: IdValue };
export type IdContainer = { name: string; init?: boolean; env?: Record<string, IdEnvValue>; envFrom?: boolean; mechanisms?: IdMechanism[]; skippedAws?: boolean; skippedAzure?: boolean };
export type IdPod = {
  namespace: string; name: string; uid: string; serviceAccount: string; node?: string; created: string; hostNetwork?: boolean;
  awsOptOut?: boolean; podIdentityLabel?: boolean; azureOptIn: boolean; containers: IdContainer[]; containersCut?: number; volumesCut?: boolean;
};
export type IdReading = { adapter: "aws" | "azure" | "gke"; container?: string; state: string; text: string; compared?: string };
export type IdManagement = { manager: "self-hosted" | "platform" | "unknown"; managerEvidence?: string; observed: "observed" | "not-observed"; observedEvidence?: string };
export type IdSide = { component: string; management: IdManagement; health: string; fault?: boolean; note?: string };
// compared: read against its ServiceAccount; without it the readings are
// absent (the ServiceAccount was not read), never "nothing configured".
export type IdPodReport = { pod: IdPod; compared: boolean; readings: IdReading[] | null; nodes?: IdSide[] };
export type IdStoreLink = { kind: string; namespace?: string; name: string; uid: string; provider: string; mode: string; via: "serviceAccountRef" | "controller"; referent?: boolean };
export type IdSAReport = {
  readAt: string; state: string; error?: string; declared?: IdDeclared; pods: IdPodReport[]; podsRead: string;
  webhooks?: IdSide[]; stores: IdStoreLink[]; storesRead: string; storesGap?: string; limits: string[];
  // coverage S9 §6.3: the KEDA authentications that authenticate as it
  keda?: { kind: string; namespace?: string; name: string; via: string }[]; kedaRead?: string;
};
export type IdPodInspectorReport = { readAt: string; state: string; error?: string; declared?: IdDeclared; saState: string; report?: IdPodReport; webhooks?: IdSide[]; limits: string[] };
export type IdSubject = {
  provider: string; mode: string; base: string; serviceAccount?: { name: string; namespace?: string; referent?: boolean };
  controller?: boolean; controllerEnv?: boolean; requested?: string[]; refusals?: string[]; unresolved?: string[]; notes?: string[];
};
export type IdGroup = { namespace: string; serviceAccount: string; candidate: string; pods: IdPod[] };
export type IdStoreReport = { readAt: string; state: string; error?: string; subject: IdSubject; handlers?: { groups?: IdGroup[]; unresolved?: string; notRunning?: string[] }; envGaps?: string[]; limits: string[] };

export type SAIdentityDTO = { cluster: string; namespace: string; name: string; uid: string; error?: string; report: IdSAReport; podsDropped?: number };
export type PodIdentityDTO = { cluster: string; namespace: string; name: string; uid: string; error?: string; report: IdPodInspectorReport; containersDropped?: number };
export type StoreIdentityDTO = { cluster: string; kind: string; namespace?: string; name: string; uid: string; error?: string; report: IdStoreReport; podsDropped?: number };

const failed = (e: unknown) => (e instanceof Error ? e.message : "the read failed");

export async function readServiceAccountIdentity(cluster: string, namespace: string, name: string, uid: string): Promise<SAIdentityDTO> {
  try {
    return (await IdentityService.ServiceAccount(cluster, namespace, name, uid)) as unknown as SAIdentityDTO;
  } catch (e) {
    return { cluster, namespace, name, uid, error: failed(e), report: { readAt: "", state: "unreadable", pods: [], podsRead: "unread", stores: [], storesRead: "unread", limits: [] } };
  }
}

export async function readPodIdentity(cluster: string, namespace: string, name: string, uid: string): Promise<PodIdentityDTO> {
  try {
    return (await IdentityService.Pod(cluster, namespace, name, uid)) as unknown as PodIdentityDTO;
  } catch (e) {
    return { cluster, namespace, name, uid, error: failed(e), report: { readAt: "", state: "unreadable", saState: "unread", limits: [] } };
  }
}

export async function readStoreIdentity(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<StoreIdentityDTO> {
  try {
    return (await IdentityService.Store(cluster, kind, namespace, name, uid)) as unknown as StoreIdentityDTO;
  } catch (e) {
    return { cluster, kind, namespace, name, uid, error: failed(e), report: { readAt: "", state: "unreadable", subject: { provider: "", mode: "", base: "" }, limits: [] } };
  }
}
