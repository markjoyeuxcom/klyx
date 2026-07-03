import { useFleet, ExternalSecretDTO, ExternalSecretStoreDTO } from "../store/fleet";
import { Events } from "@wailsio/runtime";
import { ExternalSecretsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

const EXTERNALSECRETS_UPDATED = "externalsecrets:updated";

export async function openExternalSecrets(cluster: string): Promise<() => void> {
  useFleet.getState().setExternalSecretsLoading(cluster);
  await ExternalSecretsService.Open(cluster);
  const off = Events.On(EXTERNALSECRETS_UPDATED, (ev: { data: { cluster: string; secrets: ExternalSecretDTO[]; stores: ExternalSecretStoreDTO[]; watchError?: string } }) => {
    const d = ev.data;
    if (d && d.cluster === cluster) {
      useFleet.getState().setExternalSecrets(cluster, d.secrets ?? [], d.stores ?? [], d.watchError);
    }
  });
  return typeof off === "function" ? off : () => {};
}

export async function closeExternalSecrets(cluster: string): Promise<void> {
  try {
    await ExternalSecretsService.Close(cluster);
  } finally {
    useFleet.getState().clearExternalSecrets();
  }
}
