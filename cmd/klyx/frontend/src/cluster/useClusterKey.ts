import { useCallback, useRef, useState } from "react";

// useClusterKey is a lens's local selection, held with the cluster it was
// made on and read only on that cluster (The Line slice 4 §6.3). A cluster
// change never shows an old key for even one render, and a key is never
// re-targeted to another cluster. The setter is stable: it records the
// cluster current when it runs, so a memoised handler stays correct.
export function useClusterKey(cluster: string): [string | null, (key: string | null) => void] {
  const [held, setHeld] = useState<{ cluster: string; key: string } | null>(null);
  const current = useRef(cluster);
  current.current = cluster;
  const set = useCallback((key: string | null) => setHeld(key === null ? null : { cluster: current.current, key }), []);
  return [held && held.cluster === cluster ? held.key : null, set];
}
