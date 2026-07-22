import { ConnectivityService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { ConnectivityReportDTO } from "../store/fleet";

// Connectivity can-i (read-only NetworkPolicy simulation).
export async function canReach(cluster: string, srcNS: string, srcPod: string, dstNS: string, dstPod: string, port: number, protocol: string): Promise<ConnectivityReportDTO> {
  try {
    return (await ConnectivityService.CanReach(cluster, srcNS, srcPod, dstNS, dstPod, port, protocol)) as ConnectivityReportDTO;
  } catch (e) {
    const empty = { isolated: false, allowed: false, policies: [], explain: "" };
    return { allowed: false, ingress: empty, egress: empty, srcLabels: {}, dstLabels: {}, ciliumPolicies: 0, error: e instanceof Error ? e.message : "connectivity check failed" };
  }
}
