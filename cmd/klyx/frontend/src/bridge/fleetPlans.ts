import { ConfigService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { askApply, askPlan } from "./plan";

// Settings S2 §4-§5: a cluster's plans. Each is from what runs to a
// candidate, confirmed by its digest; Apply re-plans, and comes back with
// the new plan when anything moved.

// The editor's draft: a cluster's S2 fields as fleet.yaml would say them.
// Region is tags.region.
export type ClusterDraft = { context: string; kubeconfig: string; group: string; environment: string; tags: Record<string, string>; protected: boolean };

// One cluster's entry as the editor opens it: the raw fields, what decides
// its protection and why it cannot be loosened here, and its kubeconfig's
// contexts.
export type ClusterEntry = {
  name: string;
  draft: ClusterDraft;
  inFile: boolean;
  protectedBy?: string;
  locked?: string;
  contexts: string[];
  contextsError?: string;
  error?: string;
};

export async function getClusterEntry(name: string): Promise<ClusterEntry> {
  try {
    return (await ConfigService.GetClusterEntry(name)) as ClusterEntry;
  } catch (e) {
    return { name, draft: { context: "", kubeconfig: "", group: "", environment: "", tags: {}, protected: false }, inFile: false, contexts: [], error: e instanceof Error ? e.message : String(e) };
  }
}

export const planClusterEdit = (name: string, d: ClusterDraft) => askPlan("fleet", "save", () => ConfigService.PlanClusterEdit(name, d));
// "Use the file's values": a running cluster's entry as the file says it,
// a cluster in the file and not running connected, or one gone from the
// file removed - writing nothing
export const planClusterFromFile = (name: string) => askPlan("fleet", "file", () => ConfigService.PlanClusterFromFile(name));
export const planClusterRemove = (name: string, records: string[]) => askPlan("fleet", "remove", () => ConfigService.PlanClusterRemove(name, records));
export const planClusterRevert = (name: string) => askPlan("fleet", "revert", () => ConfigService.PlanClusterRevert(name));
export const planClusterRetry = (name: string) => askPlan("fleet", "retry", () => ConfigService.PlanClusterRetry(name));
export const applyClusterPlan = (digest: string, typed: string) => askApply(() => ConfigService.ApplyClusterPlan(digest, typed));
