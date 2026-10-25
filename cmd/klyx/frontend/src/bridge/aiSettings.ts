import { AISettingsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { refusedPlan, type ApplyResult, type Plan } from "./plan";

// Settings > AI (spec 2026-10-13 §2). A key goes in through setAPIKey and
// never comes back: the state says only whether one is stored, and where.

export type ActionResult = { ok: boolean; error: string };

export type AISettings = {
  provider: string;
  model: string;
  keySource: string;
  apiKeyEnv: string;
  baseUrl: string;
  allowProtectedEgress: boolean;
  tlsSkipVerify: boolean;
  refusalFallback: boolean;
  cliPath: string;
  // AI A3 §6: investigations may propose actions (default on); absent in a
  // draft, the backend keeps the current value
  proposeActions?: boolean;
};

export type AISettingsState = {
  path: string;
  persisted: AISettings;
  persistedError?: string;
  legacyKeyInFile: boolean;
  keysStored: Record<string, boolean>;
  keychainError?: string;
  active: { backend: string; ready: boolean; model?: string; origin?: string; problem?: string };
  activeRev: number;
  // fleet.yaml's block is not what runs: "Use the file's values" plans it
  diverged: boolean;
  // saved, and its activation failed, with why: Retry and Revert
  savedNotActive?: string;
};

export type AIModel = { id: string; displayName?: string };
export type AIModels = { models: AIModel[]; error?: string };
export type AITest = { ok: boolean; model?: string; origin?: string; reply?: string; error?: string };
// What Settings knows about an installed CLI without running it.
export type AICLIStatus = { path: string; version: string; sha256: string; tested: boolean; admitted: boolean; checkedAt?: string; problem?: string };

// The phrase the AI plan's egress interlock expects.
export const AI_EGRESS_PHRASE = "allow protected egress";

function failed(e: unknown): ActionResult {
  return { ok: false, error: e instanceof Error ? e.message : String(e) };
}

export async function getAISettings(): Promise<AISettingsState | null> {
  try {
    return (await AISettingsService.GetAISettings()) as AISettingsState;
  } catch (e) {
    console.error("ai settings", e);
    return null;
  }
}

// Settings slice F §5: a change is a plan from what runs, applied by its
// digest.
async function plan(call: () => Promise<unknown>, kind: Plan["kind"]): Promise<Plan> {
  try {
    return (await call()) as Plan;
  } catch (e) {
    return refusedPlan("ai", kind, failed(e).error);
  }
}

async function applied(call: () => Promise<unknown>): Promise<ApplyResult> {
  try {
    return (await call()) as ApplyResult;
  } catch (e) {
    return { ok: false, error: failed(e).error };
  }
}

export const planAI = (d: AISettings) => plan(() => AISettingsService.PlanAI(d), "save");
// "Use the file's values": the file's block, from what runs, writing nothing
export const planAIFromFile = () => plan(() => AISettingsService.PlanAIFromFile(), "file");
export const planAIRevert = () => plan(() => AISettingsService.PlanAIRevert(), "revert");
export const applyAI = (digest: string, typed: string) => applied(() => AISettingsService.ApplyAI(digest, typed));
// Save and leave: applied only if it ends nothing and needs no interlock;
// otherwise the plan comes back
export const applyAIQuiet = (d: AISettings) => applied(() => AISettingsService.ApplyAIQuiet(d));
export const retryAI = () => applied(() => AISettingsService.RetryAI());

export async function setAPIKey(provider: string, key: string): Promise<ActionResult> {
  try {
    return (await AISettingsService.SetAPIKey(provider, key)) as ActionResult;
  } catch (e) {
    return failed(e);
  }
}

export async function clearAPIKey(provider: string): Promise<ActionResult> {
  try {
    return (await AISettingsService.ClearAPIKey(provider)) as ActionResult;
  } catch (e) {
    return failed(e);
  }
}

export async function moveKeyToKeychain(): Promise<ActionResult> {
  try {
    return (await AISettingsService.MoveKeyToKeychain()) as ActionResult;
  } catch (e) {
    return failed(e);
  }
}

export async function listAIModels(draft: AISettings): Promise<AIModels> {
  try {
    const r = (await AISettingsService.ListModels(draft)) as AIModels;
    return { models: r.models ?? [], error: r.error };
  } catch (e) {
    return { models: [], error: failed(e).error };
  }
}

export async function testAIProvider(draft: AISettings): Promise<AITest> {
  try {
    return (await AISettingsService.TestProvider(draft)) as AITest;
  } catch (e) {
    return { ok: false, error: failed(e).error };
  }
}

export async function getCLIStatus(draft: AISettings): Promise<AICLIStatus> {
  try {
    return (await AISettingsService.CLIStatus(draft)) as AICLIStatus;
  } catch (e) {
    return { path: "", version: "", sha256: "", tested: false, admitted: false, problem: failed(e).error };
  }
}
