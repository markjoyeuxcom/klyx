import { useMemo } from "react";
import type { KarpenterClaimDTO, KarpenterClassDTO, KarpenterPoolDTO, KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partDependency, partRows, rowsState } from "./lensRows";

// The Karpenter lens's NodePools and NodeClaims from the Observer's shared
// rows (slice 3 §5.5). The lens is their only reader, so the hook hands them
// to it directly - no store slice. Coverage as for every rows lens
// (health/lensRows): each part's evidence is its own, per kind.

const KIND_NAMES = { nodepools: "NodePool", nodeclaims: "NodeClaim", ec2nodeclasses: "EC2NodeClass", aksnodeclasses: "AKSNodeClass", nodeclasses: "NodeClass" };
// coverage S10: the class kinds, inventory (never counted)
export const CLASS_KINDS = ["EC2NodeClass", "AKSNodeClass", "NodeClass"];

export type KarpenterRows = {
  pools: KarpenterPoolDTO[];
  claims: KarpenterClaimDTO[];
  classes: KarpenterClassDTO[]; // coverage S10: fact rows, with their readings
  coverage: KindCoverageDTO[]; // each part's OWN (counted) kinds
  // the NodeClaim coverage the NodePools capture read its node counts from
  // (a dependency of that part - never the NodeClaims list's evidence)
  poolNodesDep: KindCoverageDTO | null;
  watchError: string | null;
  loading: boolean; // no part has answered yet
  rowsState: RowsStateDTO; // A4 across every part, the NodeClasses' included
  // coverage S10: the NodeClasses part's own A4 state, so the classes view
  // and inspector qualify themselves from their own evidence
  classesState: RowsStateDTO;
  partCounts: { nodepools: PartCountsDTO | null; nodeclaims: PartCountsDTO | null };
};

export function useKarpenterRows(cluster: string | null): KarpenterRows {
  const pools = useRows<KarpenterPoolDTO>(cluster, "karpenter", "nodepools");
  const claims = useRows<KarpenterClaimDTO>(cluster, "karpenter", "nodeclaims");
  const classes = useRows<KarpenterClassDTO>(cluster, "karpenter", "nodeclasses");
  return useMemo(() => {
    const coverage = [...partCoverage(pools, ["NodePool"], KIND_NAMES), ...partCoverage(claims, ["NodeClaim"], KIND_NAMES), ...partCoverage(classes, CLASS_KINDS, KIND_NAMES)];
    return {
      pools: partRows(pools),
      claims: partRows(claims),
      classes: partRows(classes),
      coverage,
      poolNodesDep: partDependency(pools, "NodeClaim", KIND_NAMES),
      watchError: coverageError(coverage),
      loading: !answered(pools) && !answered(claims) && !answered(classes),
      rowsState: rowsState([pools, claims, classes]),
      classesState: rowsState([classes]),
      partCounts: { nodepools: partCounts(pools), nodeclaims: partCounts(claims) },
    };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pools.rows, pools.part, pools.error, pools.refreshError, pools.evaluatedAt, pools.counts.part, pools.counts.updating, pools.counts.refreshing,
    claims.rows, claims.part, claims.error, claims.refreshError, claims.evaluatedAt, claims.counts.part, claims.counts.updating, claims.counts.refreshing,
    classes.rows, classes.part, classes.error, classes.refreshError, classes.evaluatedAt, classes.counts.updating, classes.counts.refreshing]);
}
