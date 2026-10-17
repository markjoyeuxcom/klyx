import type { CancellablePromise } from "@wailsio/runtime";
import { GitOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The GitOps dependency graph (The Line slice 8b, parent §5.1), drawn by the
// Flux inspector (8c). The call is returned as generated, never wrapped in
// an async function: its cancel() cancels the walk in Go, so a changed
// selection stops the reads it no longer needs.

export type DepWrite = { time: string; manager: string; operation: string; subresource?: string; statusUpdate: boolean };
export type DepSupplier = { kind: string; namespace: string; name: string; uid: string; readAt: string; ready: string; reason?: string; message?: string; lastWrite?: DepWrite; lastSpecWrite?: DepWrite };
export type DepSupply = {
  field: string;
  namespace: string;
  name: string;
  uid?: string;
  readAt?: string;
  error?: string;
  notFound?: boolean;
  lastWrite?: DepWrite;
  lastSpecWrite?: DepWrite;
  supplier?: DepSupplier; // named only through a UID-confirmed controller ownerReference
  supplierNote?: string;
};
export type DepNode = {
  id: string;
  group: string;
  kind: string;
  namespace: string;
  name: string;
  uid?: string;
  readAt?: string;
  // where evidence stops (unreadable, replaced, or the walk's one summary of
  // what was not followed: depth, node limit, read limit, cancelled), or
  // "not found" (a read that answered)
  stub?: "depth" | "node limit" | "read limit" | "unreadable" | "replaced" | "cancelled" | "not found";
  note?: string;
  ready: string;
  reason?: string;
  message?: string;
  conditionTime?: string; // "condition time", never a role start
  conditionGeneration: number; // the condition's own observedGeneration; 0 when none
  generation: number;
  observedGeneration: number;
  stale: boolean;
  suspended: boolean;
  failing: boolean;
  upstreamFailure: boolean; // "upstream failure"
  chainEnd: boolean; // "reported chain ends here"
  lastWrite?: DepWrite;
  lastSpecWrite?: DepWrite;
  supply: DepSupply[];
  inventory?: { lastApplied: string; entries: number; workloads: number };
  git?: { url: string; refKind: string; refName: string; applied: string; path: string };
  // An ExternalArtifact's producer evidence (coverage S1c §2): the
  // generator UID it records, and a producer of another kind it declares -
  // shown, never followed.
  generatorUid?: string;
  declaredProducer?: { group?: string; kind: string; namespace: string; name: string };
};
// cycle: the edge leads back to a node on its own path - cut here, and named.
// producer: an ExternalArtifact's generator (S1c §2), qualified - verified,
// named (identity not recorded) or disagree (drawn, never attributed), or
// the target's stub.
export type DepEdge = { from: string; to: string; type: "source" | "dependsOn" | "producer"; reported: boolean; cycle: boolean; producer?: string };
export type DepPair = { failing: string; conditionTime: string; object: string; write: DepWrite };
export type DepGraph = { start: string; nodes: DepNode[]; edges: DepEdge[]; incomplete: string[]; proximity: DepPair[]; reads: number; error?: string };

export function dependencyGraph(cluster: string, kind: string, namespace: string, name: string, uid: string): CancellablePromise<DepGraph> {
  return GitOpsService.DependencyGraph(cluster, kind, namespace, name, uid) as unknown as CancellablePromise<DepGraph>;
}
