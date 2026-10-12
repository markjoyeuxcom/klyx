import { useLayoutEffect } from "react";
import type { RowsDTO, RowsOutcome } from "../store/health";
import type { RowsVer } from "../store/fleet";

// perfHooks is the slice 3 §6a lab probe's seam (app/perfProbe, compiled in
// only with VITE_KLYX_PERF=1): unset in every normal build, so a rows push
// or a lens commit costs one property read.
export const perfHooks: {
  // a rows message's handler: its outcome, and the handler's own span
  rows?: (key: string, start: number, end: number, dto: RowsDTO | null, receivedAtMs: number, outcome: RowsOutcome) => void;
  // a lens's committed render shows these rows (addendum §3.4)
  commit?: (key: string, incarnation: number, ver: number) => void;
  // a bootstrap reply (the open's, or a re-bootstrap's): the call's span,
  // decode included, and its outcome
  bootstrap?: (key: string, kind: "open" | "rebootstrap", start: number, received: number, dto: RowsDTO | null, outcome: RowsOutcome) => void;
  // drop: the lab forces a lost message (and so a real wrong-base resync)
  drop?: (key: string) => boolean;
} = {};

// useCommitProbe reports, from a layout effect, the rows (incarnation, Ver)
// the lens's committed render shows - the end of a push's timing.
export function useCommitProbe(key: string, ver: RowsVer | null | undefined): void {
  const inc = ver?.incarnation;
  const v = ver?.ver;
  useLayoutEffect(() => {
    if (inc !== undefined && v !== undefined) perfHooks.commit?.(key, inc, v);
  }, [key, inc, v]);
}
