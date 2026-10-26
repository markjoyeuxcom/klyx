import { useFleet } from "../store/fleet";
import { ConfigService, ExecService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };

export type FleetClusterConfigDTO = {
  name: string;
  context: string;
  env: string;
  region: string;
  provider: string;
  account: string;
  group: string;
  // Effective protection (what the backend guard enforces); inferred marks a
  // prd/prod/production token rather than an explicit `protected: true`.
  protected: boolean;
  protectedInferred?: boolean;
  hasMetrics: boolean;
  // drift: how fleet.yaml now differs from this running entry, applied at a
  // restart (Settings review slice 0)
  drift?: string;
};
export type KubeContextDTO = { name: string; inFleet: boolean };
export type FleetConfigDTO = {
  path: string;
  kubeconfigPath: string;
  warnings: string[];
  scanError?: string;
  clusters: FleetClusterConfigDTO[];
  contexts: KubeContextDTO[];
  // loadError: fleet.yaml did not load at startup, so no cluster connected;
  // fileError: it does not load now; fileOnly: in the file, not running
  loadError?: string;
  fileError?: string;
  fileOnly?: string[];
};

// getFleetConfig fetches the fleet file state plus a FRESH kubeconfig scan —
// the backend re-reads ~/.kube/config on every call, so opening Settings picks
// up newly added contexts without restarting Klyx.
export async function getFleetConfig(): Promise<FleetConfigDTO | null> {
  try {
    return (await ConfigService.GetFleetConfig()) as FleetConfigDTO;
  } catch (e) {
    console.error("fleet config", e);
    return null;
  }
}

// configLoadError is why fleet.yaml did not load when Klyx started ("" when
// it did, or there was none yet): the fleet's empty state says it.
export async function configLoadError(): Promise<string> {
  try {
    return await ConfigService.LoadError();
  } catch {
    return "";
  }
}

// addClusters appends contexts to fleet.yaml (validated before write). The
// running fleet gains the new conns immediately (no restart needed).
export async function addClusters(contexts: string[]): Promise<ActionResultDTO> {
  try {
    return (await ConfigService.AddClusters(contexts)) as ActionResultDTO;
  } catch (e) {
    return { ok: false, error: String(e) };
  }
}

// armProtected records the typed confirmation for a protected cluster with the
// backend, unlocking exactly one destructive verb for the next 30s. Confirm
// dialogs call this before dispatching the action; without it the backend
// refuses destructive verbs on protected clusters (defense in depth - the
// frontend gate alone must never be the only thing standing before a PRD
// cluster). Arming an unprotected cluster is a no-op success.
export async function armProtected(cluster: string, typed: string): Promise<ActionResultDTO> {
  try {
    return (await ConfigService.ArmProtected(cluster, typed)) as ActionResultDTO;
  } catch (e) {
    return { ok: false, error: String(e) };
  }
}

// refreshNewContextCount updates the sidebar's Settings badge (kubeconfig
// contexts not in the fleet). Called once at startup; Settings refreshes it on
// every add. Errors leave the badge at 0 — it never invents a count.
export async function refreshNewContextCount(): Promise<void> {
  try {
    const n = (await ConfigService.NewContextCount()) as number;
    useFleet.getState().setNewContexts(n ?? 0);
  } catch {
    useFleet.getState().setNewContexts(0);
  }
}

// openTerminal opens a plain external terminal window (macOS Terminal.app).
export async function openTerminal(): Promise<void> {
  try {
    const r = (await ExecService.OpenTerminal()) as ActionResultDTO;
    if (!r?.ok) {
      useFleet.getState().setActionStatus({ kind: "error", message: r?.error || "could not open terminal" });
    }
  } catch (e) {
    useFleet.getState().setActionStatus({ kind: "error", message: String(e) });
  }
}
