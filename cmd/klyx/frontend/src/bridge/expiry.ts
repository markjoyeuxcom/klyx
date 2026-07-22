import { ExpiryService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { ExpiryScanResultDTO } from "../store/fleet";

// One cluster's expiring-material inventory (read-only, public cert material).
export async function getExpiryScan(cluster: string): Promise<ExpiryScanResultDTO> {
  try {
    return (await ExpiryService.GetExpiryScan(cluster)) as ExpiryScanResultDTO;
  } catch (e) {
    return { items: [], warnings: [], error: e instanceof Error ? e.message : "expiry scan failed" };
  }
}
