import { AIResultDTO, AIAvailabilityDTO } from "../store/fleet";
import { AIService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// aiAvailability reports whether an AI backend is configured (drives whether the
// Explain affordance renders at all).
export async function aiAvailability(): Promise<AIAvailabilityDTO> {
  return (await AIService.Available()) as AIAvailabilityDTO;
}

// aiEgressAllowed reports whether an Explain call is permitted for this cluster
// (false when a hosted backend would send a protected cluster's topology off the
// machine without an explicit opt-in). Lets the UI disable rather than fail.
export async function aiEgressAllowed(cluster: string): Promise<boolean> {
  return (await AIService.EgressAllowed(cluster)) as boolean;
}

// suggestFix drafts a minimal change to desired state that would fix the diagnosed
// problem, as a patch to COMMIT TO GIT. Klyx never applies it. Read-only.
// The uid is the object the inspector shows: the backend refuses a
// same-name replacement before anything is sent.
export async function suggestFix(cluster: string, group: string, version: string, plural: string, namespace: string, name: string, uid: string): Promise<AIResultDTO> {
  return (await AIService.SuggestFix(cluster, group, version, plural, namespace, name, uid)) as AIResultDTO;
}

// ExplainTarget names what Explain reads (spec 2026-10-13 §4.1): a lens row
// (its part and the UID the inspector shows), a generic Resources object, or
// a Helm release. The backend fences every row and object by UID.
export type ExplainTarget =
  | { kind: "row"; task: string; part: string; uid: string }
  | { kind: "object"; group: string; version: string; plural: string; namespace?: string; name: string; uid: string }
  | { kind: "helm"; namespace: string; name: string };

// explain asks for a grounded explanation of one object, anchored on Klyx's
// own verdict where it has one. Read-only; failures fold into the result.
export async function explain(cluster: string, target: ExplainTarget): Promise<AIResultDTO> {
  try {
    return (await AIService.Explain(cluster, target)) as AIResultDTO;
  } catch (e) {
    return { ok: false, explanation: "", model: "", backend: "", groundedOn: "", error: e instanceof Error ? e.message : String(e) };
  }
}
