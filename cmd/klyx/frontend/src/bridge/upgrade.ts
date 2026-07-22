import { UpgradeService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { UpgradeReportDTO } from "../store/fleet";

// One cluster's upgrade-readiness report (read-only).
export async function getUpgradeReadiness(cluster: string): Promise<UpgradeReportDTO> {
  try {
    return (await UpgradeService.GetUpgradeReadiness(cluster)) as UpgradeReportDTO;
  } catch (e) {
    return { serverVersion: "", serverMinor: 0, eolDate: "", skew: [], deprecated: [], error: e instanceof Error ? e.message : "upgrade check failed" };
  }
}
