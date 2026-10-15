import type { CancellablePromise } from "@wailsio/runtime";
import { GitService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Git's "not here yet" for the dependency graph (The Line slice 8b-2,
// parent §5.1): for each node behind T, the commits between its applied sha
// and T that touch its spec.path, and the files they change. T is always
// Go's own; a node's facts only name its repository, ref, sha and path, and
// "behind" is proved before anything is listed. Returned as generated, so
// cancel() stops the Git reads.

export type NotHereYetNode = { id: string; url: string; refKind: string; refName: string; applied: string; path: string };
export type NotHereYetCommit = { sha: string; subject: string; author: string; time: string; has?: string[] | null };
export type NotHereYetResult = {
  id: string;
  state: "behind" | "at" | "unavailable";
  reason?: string; // why unavailable: another key, ahead, diverged, unrelated, missing history, not approved, invalid facts
  commits: NotHereYetCommit[];
  more: boolean; // more commits touch the path than are listed
  files: { status: string; path: string }[];
};
export type NotHereYet = {
  target?: { ref: string; name: string; sha?: string; fetchedAt?: string; error?: string };
  results: NotHereYetResult[];
  error?: string;
};

export function notHereYet(nodes: NotHereYetNode[]): CancellablePromise<NotHereYet> {
  return GitService.NotHereYet(nodes as never) as unknown as CancellablePromise<NotHereYet>;
}
