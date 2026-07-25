import { KarpenterService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { KarpenterSnapshotDTO } from "../store/fleet";

// One cluster's Karpenter lens snapshot (read-only).
export async function getKarpenterState(cluster: string): Promise<KarpenterSnapshotDTO> {
  try {
    return (await KarpenterService.GetKarpenterState(cluster)) as KarpenterSnapshotDTO;
  } catch (e) {
    return { detected: false, controllerHealthy: false, pools: [], claims: [], events: [], error: e instanceof Error ? e.message : "karpenter call failed" };
  }
}
