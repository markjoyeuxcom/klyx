import { useEffect } from "react";
import { useFleet, type CertificateDTO, type IssuerDTO, type KindCoverageDTO } from "../store/fleet";
import { coverageError, coverageFromHealth } from "../layout/coverage";
import { useRows } from "./useRows";

// The cert-manager lens's data from the Observer's shared rows (slice 3 §5,
// Certificates): the certs/certificates and certs/issuers parts fill the
// existing certmanager slice, whose selection and chain stay the lens's own.
// CertsView and GatewayTopology each call it, so the topology's certificate
// lookup no longer depends on the Certificates lens being mounted.

const KIND_NAMES = { certificates: "Certificate", issuers: "Issuer", clusterissuers: "ClusterIssuer" };

// consumers counts mounted users per cluster: the first resets the slice
// (a new visit's loading state), the last clears it.
const consumers = new Map<string, number>();

// openFailure is a part whose open failed (retrying with backoff): its
// kinds are unreadable, never empty.
const openFailure = (kinds: string[], error: string): KindCoverageDTO[] =>
  kinds.map((kind) => ({ kind, served: true, synced: false, error, coverage: "unreadable" }));

export function useCertManagerRows(cluster: string | null): void {
  const certs = useRows<CertificateDTO>(cluster, "certs", "certificates");
  const issuers = useRows<IssuerDTO>(cluster, "certs", "issuers");

  useEffect(() => {
    if (!cluster) return;
    const n = (consumers.get(cluster) ?? 0) + 1;
    consumers.set(cluster, n);
    if (n === 1 || useFleet.getState().certmanager.cluster !== cluster) useFleet.getState().setCertManagerLoading(cluster);
    return () => {
      const left = (consumers.get(cluster) ?? 1) - 1;
      if (left > 0) consumers.set(cluster, left);
      else {
        consumers.delete(cluster);
        if (useFleet.getState().certmanager.cluster === cluster) useFleet.getState().clearCertManager();
      }
    };
  }, [cluster]);

  useEffect(() => {
    if (!cluster) return;
    const certsKnown = certs.part !== null || certs.error !== null;
    const issuersKnown = issuers.part !== null || issuers.error !== null;
    if (!certsKnown || !issuersKnown) return; // still loading: no claim either way
    const coverage = [
      ...(certs.part ? coverageFromHealth(certs.part.kinds, KIND_NAMES) : openFailure(["Certificate"], certs.error as string)),
      ...(issuers.part ? coverageFromHealth(issuers.part.kinds, KIND_NAMES) : openFailure(["Issuer", "ClusterIssuer"], issuers.error as string)),
    ];
    const st = useFleet.getState();
    if (st.certmanager.cluster !== cluster) return; // another cluster's visit owns the slice
    st.setCertManager(cluster, certs.rows ?? [], issuers.rows ?? [], coverageError(coverage) ?? undefined, coverage, {
      updating: certs.counts.updating || issuers.counts.updating,
      refreshing: certs.counts.refreshing || issuers.counts.refreshing,
      evaluatedAt: certs.evaluatedAt,
    });
  }, [cluster, certs.rows, certs.part, certs.error, certs.evaluatedAt, certs.counts.updating, certs.counts.refreshing,
    issuers.rows, issuers.part, issuers.error, issuers.counts.updating, issuers.counts.refreshing]);
}

// test seam
export function _resetCertManagerRowsForTest(): void {
  consumers.clear();
}
