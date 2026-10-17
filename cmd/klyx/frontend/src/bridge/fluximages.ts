import { GitOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { EventRowDTO, RuleDTO } from "../store/fleet";
import type { FluxOpOwnerDTO } from "./fluxop";

// Flux image automation (coverage S1b): the Images view's rows are the
// health Observer's shared "images" rows (health/useImageRows); this bridge
// keeps the inspector's on-demand detail. The verbs are the shared Flux
// verbs (bridge/gitops reconcile and setSuspend).

export type ImageCommonDTO = {
  uid: string; gvr: string; kind: string; namespace: string; name: string;
  generation: number; observedGeneration?: number; readyGeneration?: number;
  created?: string; suspended: boolean;
  ready: "Ready" | "Failed" | "Reconciling" | "Unknown";
  reason?: string; message?: string; readySince?: string; stalled?: boolean;
  requestedAt?: string; lastHandled?: string;
  owner?: FluxOpOwnerDTO;
  clipped: string[]; incomplete: string[];
  rule: RuleDTO;
};

export type ImageRefDTO = { name: string; tag?: string; digest?: string; text: string };

export type ImageRepositoryDTO = ImageCommonDTO & {
  image: string; canonicalImage?: string; provider?: string; serviceAccountName?: string;
  secretRef?: string; certSecretRef?: string; proxySecretRef?: string;
  interval?: string; timeout?: string; insecure?: boolean;
  exclusionList: string[]; accessFromSelectors?: number;
  scanned: boolean; tagCount: number; scanTime?: string; latestTags: string[];
  policies: { namespace: string; name: string; label: string; role: string }[];
  // the policies read's state: found, incomplete or not readable
  policiesState: string;
};

export type ImagePolicyDTO = ImageCommonDTO & {
  repositoryNamespace: string; repositoryName: string;
  policyType?: string; policyValue?: string; filterPattern?: string; filterExtract?: string;
  digestReflection?: string; interval?: string;
  latest?: ImageRefDTO; previous?: ImageRefDTO; blockedBy?: string;
  repository?: { state: string; namespace: string; name: string; label?: string; role?: string; scanTime?: string };
  automations: { namespace: string; name: string; state: string }[];
  automationsState: string;
};

export type ImageCheckoutDTO = { branch?: string; tag?: string; semver?: string; name?: string; commit?: string };

export type ImageAutomationDTO = ImageCommonDTO & {
  sourceKind?: string; sourceName?: string; sourceNamespace?: string; interval?: string;
  checkout?: ImageCheckoutDTO; pushBranch?: string; pushRefspec?: string; pushOptionCount?: number;
  authorName?: string; authorEmail?: string; signingKeySecret?: string;
  updateStrategy?: string; updatePath?: string; selector?: string; selectorIncomplete?: boolean;
  lastRun?: string; lastPushTime?: string; lastPushCommit?: string; observedSourceRevision?: string;
  observedComplete: boolean;
  uses: { namespace: string; name: string; resolved: ImageRefDTO; observed?: ImageRefDTO; state: string }[];
  usesState: string;
};

export type ImageRowDTO = {
  uid: string; kind: "ImageRepository" | "ImagePolicy" | "ImageUpdateAutomation"; rule: RuleDTO;
  repository?: ImageRepositoryDTO; policy?: ImagePolicyDTO; automation?: ImageAutomationDTO;
};

export type ImagePushDTO = {
  // what this evidence describes: shown only beside a row with the same
  lastPushCommit?: string; sourceNamespace?: string; sourceName?: string;
  branchState: string; branch?: string;
  target: { repositoryURL?: string; checkoutFrom: string; checkout?: ImageCheckoutDTO; branch?: string; error?: string; refspec?: string; optionCount?: number };
  sourceState: string; sourceLine: string; sourceRevision?: string; sourceBranchNote?: string;
  applied: { namespace: string; name: string; revision?: string; applied: boolean }[];
  appliedState: string;
};

export type ImageDetailDTO = { uid: string; generation?: number; replaced?: boolean; error?: string; events: EventRowDTO[]; eventsError?: string; push?: ImagePushDTO };

// getImageDetail reads the object the inspector shows, fenced by its UID:
// a reply about another object is refused.
export async function getImageDetail(cluster: string, kind: string, namespace: string, name: string, uid: string): Promise<ImageDetailDTO> {
  const failed = (error: string): ImageDetailDTO => ({ uid, events: [], error });
  try {
    const d = (await GitOpsService.GetImageDetail(cluster, kind, namespace, name, uid)) as unknown as ImageDetailDTO;
    if (d.uid !== uid && !d.error) return failed(`${kind} ${namespace}/${name} was replaced (its UID changed); nothing about the new object is shown here`);
    return d;
  } catch (e) {
    return failed(e instanceof Error ? e.message : String(e));
  }
}
