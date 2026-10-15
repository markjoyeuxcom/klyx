import { ObjectWritesService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The last recorded managed-field update (The Line slice 7a, parent §8.3):
// read for the dependency graph's nodes and their supply objects (slice 8).
// Not an audit log: a manager is a workflow identifier, not an authenticated
// writer, and nothing says which field changed. A status update is
// labelled as one and never used for change proximity.

// uid is required: the read is pinned to it, never by name alone.
export type ObjectRef = { group: string; version: string; kind: string; namespace?: string; name: string; uid: string };
export type ManagedWrite = { time: string; manager: string; operation: string; subresource?: string; statusUpdate: boolean };
export type ObjectWrite = { ref: ObjectRef; state: "ok" | "no record" | "replaced" | "gone" | "error"; last?: ManagedWrite; lastSpec?: ManagedWrite; error?: string };
export type ObjectWrites = { results: ObjectWrite[]; error?: string };

// objectWrites reads at most 200 objects per call; a failed read is that
// object's error, never "no record".
export async function objectWrites(cluster: string, refs: ObjectRef[]): Promise<ObjectWrites> {
  try {
    return (await ObjectWritesService.ObjectWrites(cluster, refs as never)) as ObjectWrites;
  } catch (e) {
    return { results: [], error: e instanceof Error ? e.message : String(e) };
  }
}
