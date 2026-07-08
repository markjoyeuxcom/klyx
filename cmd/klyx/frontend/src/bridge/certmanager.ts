import { useFleet, CertificateDTO, IssuerDTO, CertChainDTO } from "../store/fleet";
import { Events } from "@wailsio/runtime";
import { CertManagerService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

const CERTMANAGER_UPDATED = "certmanager:updated";

export async function getCertificateChain(cluster: string, namespace: string, name: string): Promise<void> {
  try {
    const chain = (await CertManagerService.GetCertificateChain(cluster, namespace, name)) as CertChainDTO;
    // Only apply if the same cert is still selected (selection is "<ns>/<name>").
    if (useFleet.getState().certmanager.selectedKey === `${namespace}/${name}`) {
      useFleet.getState().setCertChain(chain);
    }
  } catch (e) {
    // Clear chainLoading so the inspector doesn't get stuck on "Loading…".
    if (useFleet.getState().certmanager.selectedKey === `${namespace}/${name}`) {
      useFleet.setState((s) => ({ certmanager: { ...s.certmanager, chainLoading: false } }));
    }
    throw e; // caller .catches and logs
  }
}

export async function openCertManager(cluster: string): Promise<() => void> {
  useFleet.getState().setCertManagerLoading(cluster);
  await CertManagerService.Open(cluster);
  const off = Events.On(CERTMANAGER_UPDATED, (ev: { data: { cluster: string; certificates: CertificateDTO[]; issuers: IssuerDTO[]; watchError?: string } }) => {
    const d = ev.data;
    if (d && d.cluster === cluster) {
      useFleet.getState().setCertManager(cluster, d.certificates ?? [], d.issuers ?? [], d.watchError);
    }
  });
  return typeof off === "function" ? off : () => {};
}

export async function closeCertManager(cluster: string): Promise<void> {
  try {
    await CertManagerService.Close(cluster);
  } finally {
    // A delayed close must not clobber a freshly-opened cluster's slice: only
    // clear when the slice still names the cluster being closed.
    if (useFleet.getState().certmanager.cluster === cluster) {
      useFleet.getState().clearCertManager();
    }
  }
}
