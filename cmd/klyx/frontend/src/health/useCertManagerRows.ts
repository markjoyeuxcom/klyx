import { useEffect } from "react";
import { useFleet, type CertificateDTO, type IssuerDTO } from "../store/fleet";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState, sliceOwner } from "./lensRows";

// The cert-manager lens's data from the Observer's shared rows (slice 3 §5,
// Certificates): the certs/certificates and certs/issuers parts fill the
// existing certmanager slice, whose selection and chain stay the lens's own.
// CertsView and GatewayTopology each call it, so the topology's certificate
// lookup no longer depends on the Certificates lens being mounted.
//
// Each part's evidence is its own and is recomputed on every change: a part
// whose rows the health store fenced (a Final, a newer incarnation) is
// pending again - its rows empty, its kinds checking - and never joins the
// other part's newer evidence or keeps its own old rows (Codex review #2).

const KIND_NAMES = { certificates: "Certificate", issuers: "Issuer", clusterissuers: "ClusterIssuer" };
const PART_KINDS = { certificates: ["Certificate"], issuers: ["Issuer", "ClusterIssuer"] };

const owner = sliceOwner(
  () => useFleet.getState().certmanager.cluster,
  (cluster) => useFleet.getState().setCertManagerLoading(cluster),
  () => useFleet.getState().clearCertManager(),
);

export function useCertManagerRows(cluster: string | null): void {
  const certs = useRows<CertificateDTO>(cluster, "certs", "certificates");
  const issuers = useRows<IssuerDTO>(cluster, "certs", "issuers");
  owner.useOwner(cluster);

  useEffect(() => {
    if (!cluster) return;
    const st = useFleet.getState();
    if (st.certmanager.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = [...partCoverage(certs, PART_KINDS.certificates, KIND_NAMES), ...partCoverage(issuers, PART_KINDS.issuers, KIND_NAMES)];
    st.setCertManagerFromRows(cluster, {
      certificates: partRows(certs),
      issuers: partRows(issuers),
      coverage,
      watchError: coverageError(coverage),
      loading: !answered(certs) && !answered(issuers),
      rowsState: rowsState([certs, issuers]),
      partCounts: { certificates: partCounts(certs), issuers: partCounts(issuers) },
    });
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, certs.rows, certs.part, certs.error, certs.refreshError, certs.evaluatedAt, certs.counts.part, certs.counts.updating, certs.counts.refreshing,
    issuers.rows, issuers.part, issuers.error, issuers.refreshError, issuers.evaluatedAt, issuers.counts.part, issuers.counts.updating, issuers.counts.refreshing]);
}

// test seam
export function _resetCertManagerRowsForTest(): void {
  owner.resetForTest();
}
