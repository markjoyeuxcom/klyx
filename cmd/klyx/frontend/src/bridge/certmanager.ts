import { useFleet, CertChainDTO } from "../store/fleet";
import { CertManagerService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The certificate and issuer lists are the health Observer's shared rows
// (health/useCertManagerRows); this bridge reads only the on-demand chain.

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
