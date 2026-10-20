import { KarpenterService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { KarpenterSnapshotDTO } from "../store/fleet";

// One cluster's Karpenter disruption feed (read-only); the NodePools,
// NodeClaims and NodeClasses are rows (health/useKarpenterRows), and the
// controllers are the task's reading (coverage S10 §5).
export async function getKarpenterState(cluster: string): Promise<KarpenterSnapshotDTO> {
  try {
    return (await KarpenterService.GetKarpenterState(cluster)) as KarpenterSnapshotDTO;
  } catch (e) {
    return { detected: false, events: [], error: e instanceof Error ? e.message : "karpenter call failed" };
  }
}
