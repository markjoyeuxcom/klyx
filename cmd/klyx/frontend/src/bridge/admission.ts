import type { RuleDTO } from "../store/fleet";
import { AdmissionService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Coverage S3a: the Admission lens's rows, mirroring internal/appbridge
// admission_dto.go. Counts and presence only: never an expression, a
// type-checking warning's text, a selector value, a caBundle or a URL
// beyond its origin.

export type AdmissionRuleDTO = { operations: string[]; groups: string[]; versions: string[]; resources: string[]; scope?: string };

export type AdmissionBackendDTO = {
  state: "ready" | "unavailable" | "terminating" | "unknown";
  label: string;
  reason?: string;
  endpointRouting: number;
  clusterRouting: number;
  terminating: number;
  selectorless?: boolean;
};

export type AdmissionWebhookDTO = {
  name: string;
  failurePolicy: string; // "" is the v1 default, Fail
  failClosed: boolean;
  sideEffects?: string;
  timeoutSeconds: number;
  matchPolicy?: string;
  reinvocationPolicy?: string;
  serviceNamespace?: string;
  serviceName?: string;
  servicePort?: number;
  pathSet?: boolean;
  urlOrigin?: string;
  url?: boolean;
  caBundle: boolean;
  rules: AdmissionRuleDTO[];
  ruleCount: number;
  namespaceSelector?: boolean;
  namespaceExprs?: number;
  objectSelector?: boolean;
  objectExprs?: number;
  matchConditions?: number;
  backend: AdmissionBackendDTO;
  role: string;
  reason?: string;
};

export type AdmissionConfigDTO = {
  uid: string;
  gvr: string;
  name: string;
  kind: "Validating" | "Mutating";
  generation: number;
  created?: string;
  webhookCount: number;
  cut?: boolean;
  webhooks: AdmissionWebhookDTO[];
  unavailable: number;
  unverified: number;
  reason?: string;
  rule: RuleDTO;
};

export type AdmissionParamRefDTO = { name?: string; namespace?: string; selector?: boolean; notFoundAction?: string };

export type AdmissionBindingDTO = {
  uid: string;
  name: string;
  policyName: string;
  policyState?: "found" | "not found" | "unverified";
  paramRef?: AdmissionParamRefDTO;
  actions: string[];
  matchRules?: number;
};

export type AdmissionParamKindDTO = { apiVersion: string; kind: string; state: "served" | "not-served" | "unknown"; namespaced?: boolean; at?: string };

// A policy with its verdict, or (entity "binding") a binding as a fact.
export type AdmissionPolicyDTO = {
  entity: "policy" | "binding";
  uid: string;
  gvr: string;
  name: string;
  kind: "Validating" | "Mutating";
  generation: number;
  created?: string;
  failurePolicy?: string;
  failClosed: boolean;
  paramKind?: AdmissionParamKindDTO;
  matchRules: AdmissionRuleDTO[];
  matchRuleCount: number;
  validations?: number;
  mutations?: number;
  variables?: number;
  matchConditions?: number;
  auditAnnotations?: number;
  reinvocationPolicy?: string;
  observedGeneration?: number;
  behind?: boolean;
  warningCount?: number;
  warningFields: string[];
  bindings: AdmissionBindingDTO[];
  // only "complete" proves that no binding names the policy
  bindingsRead?: "complete" | "stale" | "unread";
  // what a param check's reading depends on; a reply for another is stale
  paramRevision?: string;
  binding?: AdmissionBindingDTO;
  reason?: string;
  rule?: RuleDTO;
};

export type AdmissionParamCheckDTO = { binding: string; state: string; text: string };

export type AdmissionParamsDTO = {
  cluster: string;
  policyUid: string;
  revision?: string;
  state: "read" | "not-applicable" | "error";
  text?: string;
  checks: AdmissionParamCheckDTO[];
  more?: number;
  readAt?: string;
};

// The policy inspector's param check, read when it opens; the caller fences
// the reply by cluster and policy UID.
export async function checkAdmissionParams(cluster: string, policyUid: string): Promise<AdmissionParamsDTO> {
  try {
    return (await AdmissionService.CheckParams(cluster, policyUid)) as unknown as AdmissionParamsDTO;
  } catch (e) {
    return { cluster, policyUid, state: "error", text: e instanceof Error ? e.message : "the read failed", checks: [] };
  }
}
