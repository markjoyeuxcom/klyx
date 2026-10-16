import type { AspectKind } from "./primitives/Aspect";

// The one condition polarity registry (The Line slice 15, design review 1).
// A mark describes a recorded condition, never an object's health, and only
// for a type whose polarity is known: positive types are healthy when True,
// negative types when False. Every other type is neutral - it carries no
// mark, rather than being assumed positive (before this, Stalled=True read
// healthy and Stalled=False read as a fault).

export const POSITIVE_CONDITIONS = new Set([
  "Ready", "Available", "Accepted", "Programmed", "ResolvedRefs", "Synced", "Established", "NamesAccepted",
  "Initialized", "ContainersReady", "PodScheduled", "PodReadyToStartContainers", "Progressing", "Complete",
]);
export const NEGATIVE_CONDITIONS = new Set([
  "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable", "ScalingLimited", "Stalled", "ReplicaFailure", "Failed",
]);

export type Polarity = "positive" | "negative" | "neutral";
export function polarityOf(type: string): Polarity {
  return POSITIVE_CONDITIONS.has(type) ? "positive" : NEGATIVE_CONDITIONS.has(type) ? "negative" : "neutral";
}

// conditionState reads one recorded condition: "bad" (a known type in its
// bad state), "good", "unknown" (Unknown, for any type) or "neutral".
export function conditionState(type: string, status: string): "bad" | "good" | "unknown" | "neutral" {
  if (status === "Unknown") return "unknown";
  const p = polarityOf(type);
  if (p === "neutral" || (status !== "True" && status !== "False")) return "neutral";
  const healthy = p === "positive" ? status === "True" : status === "False";
  return healthy ? "good" : "bad";
}

export function conditionFault(type: string, status: string): boolean {
  return conditionState(type, status) === "bad";
}

// conditionAspect maps a recorded condition onto its mark: a fault, checking
// for Unknown, and no mark for a healthy or neutral one.
export function conditionAspect(type: string, status: string): AspectKind {
  const s = conditionState(type, status);
  return s === "bad" ? "fault" : s === "unknown" ? "checking" : "none";
}
