import { AISettingsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

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
  diverged: boolean;
};

export type AIModel = { id: string; displayName?: string };
export type AIModels = { models: AIModel[]; error?: string };
export type AITest = { ok: boolean; model?: string; origin?: string; reply?: string; error?: string };

// The phrase the backend's one-shot egress grant expects.
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

export async function saveAISettings(s: AISettings): Promise<ActionResult> {
  try {
    return (await AISettingsService.SaveAISettings(s)) as ActionResult;
  } catch (e) {
    return failed(e);
  }
}

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

export async function armAIEgress(typed: string): Promise<ActionResult> {
  try {
    return (await AISettingsService.ArmAIEgress(typed)) as ActionResult;
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
