import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { DRAClaimDTO, DRAClassDTO, DRAPoolDTO } from "../bridge/dra";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The DRA lens's rows (coverage S11b §5) from the Observer's shared rows:
// the dra task's claims, pools and classes parts.

export const DRA_KIND_NAMES = { resourceclaims: "ResourceClaim", resourceslices: "ResourceSlice", deviceclasses: "DeviceClass", pods: "Pod" };

export type DRAPartRows<T> = {
  rows: T[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
  // the part read is current and whole: absence in it is proof
  complete: boolean;
};

function usePart<T>(cluster: string | null, part: string, kinds: string[]): DRAPartRows<T> {
  const r = useRows<T>(cluster, "dra", part);
  return useMemo(() => {
    const coverage = partCoverage(r, kinds, DRA_KIND_NAMES);
    const complete = !!r.part && coverage.length > 0 && coverage.every((k) => k.coverage === "live" && !k.truncated);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r), complete };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

export const useDRAClaims = (cluster: string | null) => usePart<DRAClaimDTO>(cluster, "claims", ["ResourceClaim", "Pod"]);
export const useDRAPools = (cluster: string | null) => usePart<DRAPoolDTO>(cluster, "pools", ["ResourceSlice"]);
export const useDRAClasses = (cluster: string | null) => usePart<DRAClassDTO>(cluster, "classes", ["DeviceClass"]);

// allocatedIn counts a pool's allocated devices from the claims rows
// (S11b §3.2): distinct by driver/pool/device, admin-access results not
// counted, shared results (shareId) counted once with their shares.
export function allocatedIn(claims: DRAClaimDTO[], driver: string, pool: string): { devices: number; shares: number } {
  const seen = new Set<string>();
  let shares = 0;
  for (const c of claims) {
    for (const r of c.results) {
      if (r.driver !== driver || r.pool !== pool || r.adminAccess) continue;
      if (r.shareId) shares++;
      seen.add(r.device);
    }
  }
  return { devices: seen.size, shares };
}

// claimsOfClass counts the claims whose requests name a class.
export function claimsOfClass(claims: DRAClaimDTO[], cls: string): number {
  return claims.filter((c) => c.requests.some((r) => r.class === cls || (r.subclasses ?? []).includes(cls))).length;
}
