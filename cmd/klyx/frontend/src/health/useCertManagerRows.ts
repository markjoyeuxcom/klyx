import { useEffect } from "react";
import { useFleet, type CertificateDTO, type IssuerDTO, type KindCoverageDTO, type PartCountsDTO } from "../store/fleet";
import { coverageError, coverageFromHealth } from "../layout/coverage";
import { useRows, type LensRows } from "./useRows";

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

// consumers counts mounted users per cluster: the first resets the slice
// (a new visit's loading state), the last clears it.
const consumers = new Map<string, number>();

const kindsAs = (kinds: string[], coverage: "checking" | "unreadable", error?: string): KindCoverageDTO[] =>
  kinds.map((kind) => ({ kind, served: true, synced: false, error, coverage }));

// partCoverage: the part's own coverage; a failed open (retrying) is
// unreadable with its error; no answer yet - or fenced rows - is pending.
function partCoverage<T>(r: LensRows<T>, kinds: string[]): KindCoverageDTO[] {
  if (r.part) return coverageFromHealth(r.part.kinds, KIND_NAMES);
  if (r.error) return kindsAs(kinds, "unreadable", r.error);
  return kindsAs(kinds, "checking");
}

// refreshingAsOf: the rows' capture time a refreshing view names - the
// oldest among refreshing parts, else the certificates part's.
function refreshingAsOf(parts: LensRows<unknown>[]): string | null {
  const times = parts.filter((r) => r.counts.refreshing && r.evaluatedAt).map((r) => r.evaluatedAt as string).sort();
  return times[0] ?? parts.find((r) => r.evaluatedAt)?.evaluatedAt ?? null;
}

// globalCounts: the one selector's counts for the part (rows', or the
// summary's past the A4 deadline); none while nothing current is known.
function globalCounts<T>(r: LensRows<T>): PartCountsDTO | null {
  const n = r.counts.part?.native;
  return n ? { total: n.total, included: n.included } : null;
}

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
    const st = useFleet.getState();
    if (st.certmanager.cluster !== cluster) return; // another cluster's visit owns the slice
    const coverage = [...partCoverage(certs, PART_KINDS.certificates), ...partCoverage(issuers, PART_KINDS.issuers)];
    const known = (r: LensRows<unknown>) => r.part !== null || r.error !== null;
    st.setCertManagerFromRows(cluster, {
      certificates: certs.part ? certs.rows ?? [] : [],
      issuers: issuers.part ? issuers.rows ?? [] : [],
      coverage,
      watchError: coverageError(coverage),
      loading: !known(certs) && !known(issuers),
      rowsState: {
        updating: certs.counts.updating || issuers.counts.updating,
        refreshing: certs.counts.refreshing || issuers.counts.refreshing,
        evaluatedAt: refreshingAsOf([certs, issuers]),
        refreshError: certs.refreshError ?? issuers.refreshError,
      },
      partCounts: { certificates: globalCounts(certs), issuers: globalCounts(issuers) },
    });
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, certs.rows, certs.part, certs.error, certs.refreshError, certs.evaluatedAt, certs.counts.part, certs.counts.updating, certs.counts.refreshing,
    issuers.rows, issuers.part, issuers.error, issuers.refreshError, issuers.evaluatedAt, issuers.counts.part, issuers.counts.updating, issuers.counts.refreshing]);
}

// test seam
export function _resetCertManagerRowsForTest(): void {
  consumers.clear();
}
