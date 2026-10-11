import { KarpenterService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { KarpenterSnapshotDTO } from "../store/fleet";

// One cluster's Karpenter controller probe and disruption feed (read-only);
// the NodePools and NodeClaims are rows (health/useKarpenterRows).
export async function getKarpenterState(cluster: string): Promise<KarpenterSnapshotDTO> {
  try {
    return (await KarpenterService.GetKarpenterState(cluster)) as KarpenterSnapshotDTO;
  } catch (e) {
    return { detected: false, controllerHealthy: false, events: [], error: e instanceof Error ? e.message : "karpenter call failed" };
  }
}
