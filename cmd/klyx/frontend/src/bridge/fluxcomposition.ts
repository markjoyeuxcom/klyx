import { GitOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { EventRowDTO, RuleDTO } from "../store/fleet";
import type { FluxOpOwnerDTO } from "./fluxop";

// Coverage S1c: source composition (ArtifactGenerators and ExternalArtifacts)
// and Flux notifications. The rows are the health Observer's shared
// "composition" and "receivers" rows (health/useCompositionRows,
// useReceiverRows); Providers and Alerts are read on demand; the
// inspectors read their detail fenced by UID. The verbs are a generator's
// annotation verbs (bridge/fluxop fluxOpAct) and the shared Flux verbs.

export type CompositionCommonDTO = {
  uid: string; gvr: string; kind: string; namespace: string; name: string;
  generation: number; readyGeneration?: number; created?: string;
  ready: "Ready" | "Failed" | "Reconciling" | "Unknown" | "";
  reason?: string; message?: string; readySince?: string; stalled?: boolean;
  clipped: string[]; incomplete: string[];
  rule: RuleDTO;
};

// A joined object; state: found | not found | incomplete | not readable.
export type CompositionRefDTO = { kind?: string; alias?: string; namespace: string; name: string; state?: string; found: boolean; label?: string; role?: string };

export type GeneratorDTO = CompositionCommonDTO & {
  suspended: boolean; resume?: "awaiting" | "evaluating" | "unproven";
  requestedAt?: string; lastHandled?: string;
  sources: CompositionRefDTO[]; sourcesIncomplete?: boolean;
  outputs: { name: string; revision?: string; copy: string[] }[];
  generated: CompositionRefDTO[]; generatedState: string;
};

// A producer: Label and Role only when its identity is established; never
// the artifact's own verdict (S1c §2).
export type SourceProducerDTO = {
  state: string; kind?: string; namespace?: string; name?: string; label?: string; role?: string; failing?: boolean;
  recordedNamespace?: string; recordedName?: string; recordedGone?: boolean; recordedLookup?: string;
};

export type ArtifactDTO = CompositionCommonDTO & {
  hasArtifact: boolean; revision?: string; digest?: string;
  origin?: string; // the URL's scheme and host only
  originRevision?: string; lastUpdate?: string; size?: number;
  sourceRef?: { apiVersion?: string; kind: string; namespace: string; name: string };
  managedBy?: string;
  producer?: SourceProducerDTO;
};

export type CompositionRowDTO = { uid: string; kind: "ArtifactGenerator" | "ExternalArtifact"; rule: RuleDTO; generator?: GeneratorDTO; artifact?: ArtifactDTO };

// A Receiver row: never its webhook path or its Ready message.
export type ReceiverDTO = {
  uid: string; gvr: string; kind: "Receiver"; namespace: string; name: string;
  generation: number; observedGeneration?: number; created?: string;
  type?: string; interval?: string; secretRef?: string; events: string[];
  resources: { kind: string; name: string; namespace?: string; matchLabels?: number }[];
  resourceCount: number; filterCount: number; resourceFilter?: boolean; oidcProviders?: number;
  suspended: boolean; ready: string; reason?: string; readySince?: string; readyGeneration?: number; stalled?: boolean;
  requestedAt?: string; owner?: FluxOpOwnerDTO; meaning?: string; clipped: string[];
  rule: RuleDTO;
};

export type ProviderDTO = {
  uid: string; namespace: string; name: string; type?: string; channel?: string;
  hasAddress: boolean; hasProxy: boolean; secretRef?: string; certSecretRef?: string; proxySecretRef?: string;
  serviceAccountName?: string; interval?: string; timeout?: string; suspended: boolean; owner?: FluxOpOwnerDTO;
  alerts: CompositionRefDTO[]; alertsState: string;
};

export type AlertDTO = {
  uid: string; namespace: string; name: string; providerName: string;
  providerState: string; providerSuspended?: boolean; eventSeverity?: string;
  eventSources: { kind: string; name: string; namespace?: string; matchLabels?: number }[];
  inclusionList: string[]; exclusionList: string[]; metadataKeys: string[];
  suspended: boolean; owner?: FluxOpOwnerDTO;
  dispatch: Record<string, number>; dispatchSummary: string;
};

// More: past the cap; -1 when the server does not say how many.
export type NotificationListDTO = { absent?: boolean; error?: string; more?: number };
export type NotificationsDTO = {
  providers: ProviderDTO[]; alerts: AlertDTO[]; providersList: NotificationListDTO; alertsList: NotificationListDTO;
  eventsError?: string; eventsTruncated?: boolean; asOfUnix: number; error?: string;
};

export type CompositionDetailDTO = {
  uid?: string; replaced?: boolean; error?: string; asOfUnix?: number;
  sources: CompositionRefDTO[]; sourcesState?: string;
  consumers: CompositionRefDTO[]; consumersState?: string;
  events: EventRowDTO[]; eventsError?: string;
};

const emptyNotifications = (error: string): NotificationsDTO =>
  ({ providers: [], alerts: [], providersList: {}, alertsList: {}, asOfUnix: 0, error });

// getNotifications reads Providers and Alerts on demand; a failed call is
// an error of its own, never an empty list.
export async function getNotifications(cluster: string): Promise<NotificationsDTO> {
  try {
    return (await GitOpsService.GetNotifications(cluster)) as unknown as NotificationsDTO;
  } catch (e) {
    return emptyNotifications(e instanceof Error ? e.message : String(e));
  }
}

// getCompositionDetail reads the shown object's detail, fenced by its UID:
// a reply about another object is refused.
export async function getCompositionDetail(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<CompositionDetailDTO> {
  const failed = (error: string): CompositionDetailDTO => ({ uid, sources: [], consumers: [], events: [], error });
  try {
    const d = (await GitOpsService.GetCompositionDetail(cluster, kind, namespace, name, uid)) as unknown as CompositionDetailDTO;
    if (d.replaced) return { ...failed(`${kind} ${namespace}/${name} was replaced (its UID changed); nothing about the new object is shown here`), replaced: true };
    if (d.uid !== uid && !d.error) return failed(`${kind} ${namespace}/${name} was replaced (its UID changed); nothing about the new object is shown here`);
    return d;
  } catch (e) {
    return failed(e instanceof Error ? e.message : String(e));
  }
}
