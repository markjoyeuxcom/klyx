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

// explainCertificate asks the model to explain a stuck certificate, grounded in
// the resolved issuance chain. Read-only; the result carries either an
// explanation or an error string.
export async function explainCertificate(cluster: string, namespace: string, name: string): Promise<AIResultDTO> {
  return (await AIService.ExplainCertificate(cluster, namespace, name)) as AIResultDTO;
}
