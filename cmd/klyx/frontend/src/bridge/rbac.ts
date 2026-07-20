import { RBACService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { AccessQueryDTO, AccessVerdictDTO, AccessWhoCanDTO } from "../store/fleet";

// Access check (RBAC lens). Both calls are read-only: can-i is a virtual
// SubjectAccessReview POST, who-can only lists RBAC objects. A thrown bridge
// error degrades to the DTO's own error channel so the view never distinguishes
// transport failure from backend failure.

export async function canI(cluster: string, q: AccessQueryDTO): Promise<AccessVerdictDTO> {
  try {
    return (await RBACService.CanI(cluster, q)) as AccessVerdictDTO;
  } catch (e) {
    return {
      allowed: false, denied: false, reason: "", evaluationError: "",
      subject: "", error: e instanceof Error ? e.message : "access check failed",
    };
  }
}

export async function whoCan(cluster: string, q: AccessQueryDTO): Promise<AccessWhoCanDTO> {
  try {
    return (await RBACService.WhoCan(cluster, q)) as AccessWhoCanDTO;
  } catch (e) {
    return { grants: [], clusterWideOnly: false, error: e instanceof Error ? e.message : "who-can failed" };
  }
}
