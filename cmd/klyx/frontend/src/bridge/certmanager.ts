import { useFleet, CertificateDTO, IssuerDTO, CertChainDTO } from "../store/fleet";
import { Events } from "@wailsio/runtime";
import { CertManagerService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

const CERTMANAGER_UPDATED = "certmanager:updated";

// Chain reads are fenced by request generation, cluster and selected key: only
// the newest read for the CURRENT cluster's CURRENT selection may land, on
// success or failure (DEV/STG/PRD share namespace/name).
let chainSeq = 0;

export async function getCertificateChain(cluster: string, namespace: string, name: string): Promise<void> {
  const seq = ++chainSeq;
  const current = () => {
    const s = useFleet.getState().certmanager;
    return seq === chainSeq && s.cluster === cluster && s.selectedKey === `${namespace}/${name}`;
  };
  try {
    const chain = (await CertManagerService.GetCertificateChain(cluster, namespace, name)) as CertChainDTO;
    if (current()) useFleet.getState().setCertChain(chain, cluster);
  } catch (e) {
    // Clear chainLoading so the inspector doesn't get stuck on "Loading…".
    if (current()) useFleet.setState((s) => ({ certmanager: { ...s.certmanager, chainLoading: false } }));
    throw e; // caller .catches and logs
  }
}

export async function openCertManager(cluster: string): Promise<() => void> {
  useFleet.getState().setCertManagerLoading(cluster);
  await CertManagerService.Open(cluster);
  const off = Events.On(CERTMANAGER_UPDATED, (ev: { data: { cluster: string; certificates: CertificateDTO[]; issuers: IssuerDTO[]; watchError?: string } }) => {
    const d = ev.data;
    // a late callback for a cluster the lens has left must not overwrite it
    if (d && d.cluster === cluster && useFleet.getState().certmanager.cluster === cluster) {
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
