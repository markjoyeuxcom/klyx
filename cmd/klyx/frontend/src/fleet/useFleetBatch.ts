import { useCallback, useEffect, useRef, useState } from "react";
import { useFleet } from "../store/fleet";

// useFleetBatch: one fleet-wide read = one pinned cluster membership (the
// Fleet compare pattern, Codex 7d review). Every answer belongs to the batch
// that asked for it - a late reply from an earlier batch is dropped - and a
// cluster that joins or leaves the fleet meanwhile is named rather than
// silently added (never scanned) or dropped. Refresh starts a new batch over
// the current fleet. A batch started before the fleet seeded (an empty
// membership, e.g. a restored route at launch) starts over once clusters
// appear.

export type BatchMember = { name: string; env: string; region: string };

const envRank: Record<string, number> = { dev: 0, development: 0, stg: 1, staging: 1, prd: 2, prod: 2, production: 2 };
const rankOfEnv = (env: string): number => envRank[(env ?? "").toLowerCase()] ?? 3;
const byEnv = (a: BatchMember, b: BatchMember) => rankOfEnv(a.env) - rankOfEnv(b.env) || (a.env ?? "").localeCompare(b.env ?? "") || a.name.localeCompare(b.name);

export type FleetBatch<T> = {
  members: BatchMember[];          // the pinned membership of the current read, dev-first
  results: Record<string, T>;      // replies so far, by cluster
  pending: number;                 // members with no reply yet
  readAt: number | null;           // when the latest reply of this batch landed
  joined: string[];                // in the fleet now, not in this read
  left: string[];                  // in this read, no longer in the fleet
  refresh: () => void;             // start a new batch over the current fleet
};

export function useFleetBatch<T>(fetch: (cluster: string) => Promise<T>, failed: (e: unknown) => T): FleetBatch<T> {
  const liveNames = useFleet((s) => s.clusters.map((c) => c.name).sort().join("\n"));
  const [batch, setBatch] = useState<{ gen: number; members: BatchMember[] }>({ gen: 0, members: [] });
  const [results, setResults] = useState<Record<string, T>>({});
  const [readAt, setReadAt] = useState<number | null>(null);
  const gen = useRef(0);
  const fetchRef = useRef(fetch);
  const failedRef = useRef(failed);
  fetchRef.current = fetch;
  failedRef.current = failed;

  const refresh = useCallback(() => {
    const g = ++gen.current;
    const members = useFleet.getState().clusters.map((c) => ({ name: c.name, env: c.env, region: c.region })).sort(byEnv);
    setBatch({ gen: g, members });
    setResults({});
    setReadAt(null);
    for (const m of members) {
      void fetchRef.current(m.name)
        .catch((e) => failedRef.current(e))
        .then((r) => {
          if (g !== gen.current) return;
          setResults((prev) => ({ ...prev, [m.name]: r }));
          setReadAt(Date.now());
        });
    }
  }, []);

  useEffect(refresh, [refresh]);
  // An empty first batch (the fleet had not seeded) starts over when clusters
  // appear; any other membership change waits for an explicit Refresh.
  const live = liveNames ? liveNames.split("\n") : [];
  useEffect(() => {
    if (batch.gen > 0 && batch.members.length === 0 && live.length > 0) refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [liveNames]);

  const readNames = batch.members.map((m) => m.name);
  return {
    members: batch.members,
    results,
    pending: batch.members.filter((m) => !(m.name in results)).length,
    readAt,
    joined: live.filter((n) => !readNames.includes(n)),
    left: readNames.filter((n) => !live.includes(n)),
    refresh,
  };
}
