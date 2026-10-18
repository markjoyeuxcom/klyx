import { useMemo } from "react";
import type { KindCoverageDTO, PartCountsDTO, RowsStateDTO } from "../store/fleet";
import type { LBPoolDTO, LBServiceDTO, LBStateDTO } from "../bridge/loadbalancers";
import { coverageError } from "../layout/coverage";
import { useRows } from "./useRows";
import { answered, partCounts, partCoverage, partRows, rowsState } from "./lensRows";

// The Load balancers lens's rows (coverage S4 §6) from the Observer's shared
// rows: the lb task's services, pools and metallb parts.

export const LB_KIND_NAMES = {
  services: "Service", daemonsets: "DaemonSet", pods: "Pod", nodes: "Node", events: "Events",
  ipaddresspools: "IPAddressPool", l2advertisements: "L2Advertisement", bgpadvertisements: "BGPAdvertisement",
  configurationstates: "ConfigurationState", ciliumloadbalancerippools: "CiliumLoadBalancerIPPool",
};

export type LBPartRows<T> = {
  rows: T[];
  coverage: KindCoverageDTO[];
  watchError: string | null;
  loading: boolean;
  rowsState: RowsStateDTO;
  counts: PartCountsDTO | null;
};

function usePart<T>(cluster: string | null, part: string, kinds: string[]): LBPartRows<T> {
  const r = useRows<T>(cluster, "lb", part);
  return useMemo(() => {
    const coverage = partCoverage(r, kinds, LB_KIND_NAMES);
    return { rows: partRows(r), coverage, watchError: coverageError(coverage), loading: !answered(r), rowsState: rowsState([r]), counts: partCounts(r) };
    // reference-stable picks only: the hook's result objects are rebuilt per render
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [r.rows, r.part, r.error, r.refreshError, r.evaluatedAt, r.counts.part, r.counts.updating, r.counts.refreshing]);
}

// The counted kinds name the lens's own read; dependencies leave their
// evidence "unverified" in the inspector instead.
export const useLBServices = (cluster: string | null) => usePart<LBServiceDTO>(cluster, "services", ["Service"]);
export const useLBPools = (cluster: string | null) => usePart<LBPoolDTO>(cluster, "pools", ["IPAddressPool", "CiliumLoadBalancerIPPool"]);
export const useLBStates = (cluster: string | null) => usePart<LBStateDTO>(cluster, "metallb", ["ConfigurationState"]);
