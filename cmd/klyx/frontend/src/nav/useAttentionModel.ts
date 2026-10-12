import { useEffect, useMemo } from "react";
import { useFleet } from "../store/fleet";
import { useHealth } from "../store/health";
import { startSummaries } from "../bridge/healthRows";
import { clusterAttention, type ClusterAttention } from "./clusterAttention";

// useAttentionModel is the open cluster's attention (health 1c slice 4),
// live from the health store - the summaries and any lens rows - and the
// cluster's capabilities. It starts the summary subscription (idempotent).
export function useAttentionModel(cluster: string | null): ClusterAttention | null {
  useEffect(() => { startSummaries(); }, []);
  const st = useHealth((s) => s);
  const c = useFleet((s) => (cluster ? s.clusters.find((x) => x.name === cluster) : undefined));
  return useMemo(() => (cluster ? clusterAttention(st, cluster, c) : null), [st, cluster, c]);
}
