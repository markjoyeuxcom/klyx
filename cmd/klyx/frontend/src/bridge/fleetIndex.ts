import type { CancellablePromise } from "@wailsio/runtime";
import { IndexService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The fleet metadata index's search (The Line slice 9 note §3): hits pinned
// to their cluster and UID, each with the Observer's word on it, and the
// coverage the search ran under.

export type IndexRole = {
  state: "current" | "lastObserved" | "unverified" | string;
  role?: string;
  label?: string;
  at?: string;
  reason?: string;
};
export type IndexHit = {
  cluster: string;
  kind: string;
  group: string;
  version: string;
  resource: string;
  namespace: string;
  name: string;
  uid: string;
  kindState: string;
  asOf?: string;
  role: IndexRole;
};
export type IndexKindCoverage = { key: string; kind: string; state: string; entries: number; asOf?: string; err?: string };
export type IndexClusterCoverage = { cluster: string; indexed: boolean; connection: string; kinds: IndexKindCoverage[] };
export type IndexSearch = {
  hits: IndexHit[];
  total: number;
  searchedClusters: number;
  searchedKinds: number;
  coverage: IndexClusterCoverage[];
};

// searchIndex is returned as generated: its cancel() stops the search in Go.
export function searchIndex(query: string, limit: number): CancellablePromise<IndexSearch> {
  return IndexService.Search(query, limit) as unknown as CancellablePromise<IndexSearch>;
}
