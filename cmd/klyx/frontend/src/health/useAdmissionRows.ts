import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { AdmissionConfigDTO, AdmissionPolicyDTO } from "../bridge/admission";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Admission lens's rows (coverage S3a §3) from the Observer's shared
// rows: the admission task's webhooks and policies parts.

export const ADMISSION_KIND_NAMES = {
  validatingwebhookconfigurations: "ValidatingWebhookConfiguration", mutatingwebhookconfigurations: "MutatingWebhookConfiguration",
  validatingadmissionpolicies: "ValidatingAdmissionPolicy", mutatingadmissionpolicies: "MutatingAdmissionPolicy",
  validatingadmissionpolicybindings: "ValidatingAdmissionPolicyBinding", mutatingadmissionpolicybindings: "MutatingAdmissionPolicyBinding",
  services: "Service", endpointslices: "EndpointSlice",
};

export type AdmissionPartRows<T> = {
  rows: T[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
};

function usePart<T>(cluster: string | null, part: string, kinds: string[]): AdmissionPartRows<T> {
  const r = useRows<T>(cluster, "admission", part);
  return useMemo(() => {
    const coverage = partCoverage(r, kinds, ADMISSION_KIND_NAMES);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r) };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

export const useAdmissionWebhooks = (cluster: string | null) =>
  usePart<AdmissionConfigDTO>(cluster, "webhooks", ["ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "Service", "EndpointSlice"]);
export const useAdmissionPolicies = (cluster: string | null) =>
  usePart<AdmissionPolicyDTO>(cluster, "policies", ["ValidatingAdmissionPolicy", "MutatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "MutatingAdmissionPolicyBinding"]);
