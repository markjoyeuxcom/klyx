import { PromotionService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { PromotionSnapshotDTO } from "../store/fleet";

// One cluster's GitOps promotion snapshot (read-only). A thrown bridge error
// degrades to the DTO's error channel so a column renders its failure, never
// an empty cluster.
export async function getPromotion(cluster: string): Promise<PromotionSnapshotDTO> {
  try {
    return (await PromotionService.GetPromotion(cluster)) as PromotionSnapshotDTO;
  } catch (e) {
    return { fluxPresent: false, argoPresent: false, units: [], sources: [], error: e instanceof Error ? e.message : "promotion call failed" };
  }
}
