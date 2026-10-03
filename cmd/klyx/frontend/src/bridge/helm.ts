import { useFleet, HelmReleaseDTO, HelmHistoryEntryDTO, HelmRef } from "../store/fleet";
import { HelmService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type HelmReleasesResultDTO = { available: boolean; message: string; releases: HelmReleaseDTO[] };
type HelmHistoryResultDTO = { history: HelmHistoryEntryDTO[]; error?: string };
type HelmValuesResultDTO = { values: string; error?: string };
type ActionResultDTO = { ok: boolean; error: string };

export async function listHelmReleases(cluster: string): Promise<void> {
  useFleet.getState().setHelmLoading(cluster);
  try {
    const r = (await HelmService.ListHelmReleases(cluster)) as HelmReleasesResultDTO;
    // Drop stale response if cluster changed while in flight.
    const cur = useFleet.getState().helm;
    if (cur.cluster !== cluster) return;
    useFleet.getState().setHelm(
      cluster,
      r.available,
      r.message ?? "",
      r.releases ?? [],
    );
  } catch {
    if (useFleet.getState().helm.cluster === cluster) {
      useFleet.setState((s) => ({ helm: { ...s.helm, loading: false } }));
    }
  }
}

export async function openHelmRelease(cluster: string, namespace: string, name: string): Promise<void> {
  const ref: HelmRef = { namespace, name };
  useFleet.getState().selectHelmRelease(ref);
  try {
    const [historyResult, valuesResult] = await Promise.all([
      HelmService.GetHelmHistory(cluster, namespace, name) as Promise<HelmHistoryResultDTO>,
      HelmService.GetHelmValues(cluster, namespace, name) as Promise<HelmValuesResultDTO>,
    ]);
    // Stale guard: a different release OR a different cluster (release names
    // repeat across DEV/STG/PRD - review 2026-10-03 #5).
    const cur = useFleet.getState().helm;
    const sel = cur.selected;
    if (cur.cluster !== cluster || !sel || sel.namespace !== namespace || sel.name !== name) return;
    useFleet.getState().setHelmDetail(
      ref,
      historyResult.history ?? [],
      valuesResult.values ?? "",
    );
  } catch {
    const cur = useFleet.getState().helm;
    const sel = cur.selected;
    if (cur.cluster === cluster && sel && sel.namespace === namespace && sel.name === name) {
      useFleet.setState((s) => ({ helm: { ...s.helm, detailLoading: false } }));
    }
  }
}

// RollbackOptions are the user-composed helm rollback flags from the rollback
// composer (previously hardcoded to --wait --timeout 120s).
export type RollbackOptions = {
  wait: boolean;
  noHooks: boolean;
  timeoutSeconds: number;
};

export async function helmRollback(cluster: string, namespace: string, name: string, revision: number, opts: RollbackOptions): Promise<void> {
  try {
    const r = (await HelmService.HelmRollback(cluster, namespace, name, revision, opts)) as ActionResultDTO;
    useFleet.getState().setActionStatus(
      r.ok
        ? { kind: "success", message: `rolled back ${namespace}/${name} to revision ${revision}` }
        : { kind: "error", message: r.error || "rollback failed" },
    );
    if (r.ok) {
      await listHelmReleases(cluster);
      await openHelmRelease(cluster, namespace, name);
    }
  } catch (e: unknown) {
    useFleet.getState().setActionStatus({
      kind: "error",
      message: e instanceof Error ? e.message : "rollback failed",
    });
  }
}
