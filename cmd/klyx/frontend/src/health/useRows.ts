import { useEffect } from "react";
import { useShallow } from "zustand/react/shallow";
import { openRows } from "../bridge/healthRows";
import { bestSub, taskCounts, useHealth, type HealthPart, type SelectedCounts } from "../store/health";

export type LensRows<T> = {
  rows: T[] | null; // null until the first rows arrive (or after an incarnation reset)
  part: HealthPart | null; // the rows' own part (coverage, kinds), from their capture
  evaluatedAt: string | null;
  counts: SelectedCounts; // the one count selector (design §4a)
  error: string | null; // the open failed (retrying with backoff): no rows of its own
  refreshError: string | null; // the last failed re-bootstrap while the counts are on the summary
};

// useRows opens a part's shared rows for the mounted lens and closes them on
// unmount or a cluster change (design §2b). A cluster of null opens nothing.
export function useRows<T>(cluster: string | null, task: string, part: string): LensRows<T> {
  useEffect(() => (cluster ? openRows(cluster, task, part) : undefined), [cluster, task, part]);
  // Flat, reference-stable picks (useShallow compares one level deep).
  const v = useHealth(useShallow((st) => {
    const best = cluster ? bestSub(st, cluster, task, part) : null;
    // an open's or a re-bootstrap's failure stays visible, never an empty list
    let error: string | null = null;
    for (const s of Object.values(st.subs)) {
      if (s.cluster === cluster && s.task === task && s.part === part && s.error) error = s.error;
    }
    const refreshError = best?.refreshError ?? null;
    const c = cluster ? taskCounts(st, cluster, task, part) : null;
    return {
      rows: (best?.rows as T[] | null) ?? null,
      part: best?.partData ?? null,
      evaluatedAt: best?.evaluatedAt ?? null,
      error,
      refreshError,
      countsPart: c?.part ?? null,
      countsSource: c?.source ?? ("none" as const),
      updating: c?.updating ?? false,
      refreshing: c?.refreshing ?? false,
    };
  }));
  return {
    rows: v.rows, part: v.part, evaluatedAt: v.evaluatedAt, error: v.error, refreshError: v.refreshError,
    counts: { part: v.countsPart, source: v.countsSource, updating: v.updating, refreshing: v.refreshing },
  };
}
