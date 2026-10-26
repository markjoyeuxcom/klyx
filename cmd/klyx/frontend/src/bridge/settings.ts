import { IndexService, SettingsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { CancellablePromise } from "@wailsio/runtime";

// Settings S1 §5: Diagnostics, read-only. Nothing here changes Klyx's
// configuration or state; checkVersions runs the local tools' client-only
// version commands, contained, and only when asked.

export type DiagTool = { name: string; path?: string; source: "override" | "path" | "fallback" | "not found"; override: string; overrideMissing?: string };
export type DiagBound = { id: string; label: string; value: number; unit: string };
export type Diagnostics = {
  fleet: { path: string; fromEnv: boolean; state: "loaded" | "startup-error" | "not-loading-now"; loadError?: string; fileError?: string; warnings: string[]; drifted: number; fileOnly: string[] };
  tools: DiagTool[];
  proxies: string[];
  envSet: string[];
  bounds: DiagBound[];
  canCheck: boolean;
  cannotCheck?: string;
};
export type ToolVersion = { name: string; version?: string; failure?: string; exit?: number; reused?: boolean };
export type IndexSettings = { kinds: string[]; entriesPerCluster: number; mibPerCluster: number; entriesFleet: number; mibFleet: number; podPercent: number };

export async function getDiagnostics(): Promise<Diagnostics | null> {
  try {
    return (await SettingsService.Diagnostics()) as unknown as Diagnostics;
  } catch {
    return null;
  }
}

// checkVersions is cancellable: cancelling kills what is running.
export function checkVersions(): CancellablePromise<ToolVersion[]> {
  return SettingsService.CheckVersions() as unknown as CancellablePromise<ToolVersion[]>;
}

export async function getIndexSettings(): Promise<IndexSettings | null> {
  try {
    return (await IndexService.Settings()) as unknown as IndexSettings;
  } catch {
    return null;
  }
}
