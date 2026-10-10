import { useFleet, ExternalSecretDTO, ExternalSecretStoreDTO, KindCoverageDTO } from "../store/fleet";
import { Events } from "@wailsio/runtime";
import { ExternalSecretsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

const EXTERNALSECRETS_UPDATED = "externalsecrets:updated";

export async function openExternalSecrets(cluster: string): Promise<() => void> {
  useFleet.getState().setExternalSecretsLoading(cluster);
  await ExternalSecretsService.Open(cluster);
  const off = Events.On(EXTERNALSECRETS_UPDATED, (ev: { data: { cluster: string; secrets: ExternalSecretDTO[]; stores: ExternalSecretStoreDTO[]; watchError?: string; coverage?: KindCoverageDTO[] } }) => {
    const d = ev.data;
    // a late callback for a cluster the lens has left must not overwrite it
    if (d && d.cluster === cluster && useFleet.getState().externalsecrets.cluster === cluster) {
      useFleet.getState().setExternalSecrets(cluster, d.secrets ?? [], d.stores ?? [], d.watchError, d.coverage);
    }
  });
  return typeof off === "function" ? off : () => {};
}

export async function closeExternalSecrets(cluster: string): Promise<void> {
  try {
    await ExternalSecretsService.Close(cluster);
  } finally {
    // A delayed close must not clobber a freshly-opened cluster's slice: only
    // clear when the slice still names the cluster being closed.
    if (useFleet.getState().externalsecrets.cluster === cluster) {
      useFleet.getState().clearExternalSecrets();
    }
  }
}
