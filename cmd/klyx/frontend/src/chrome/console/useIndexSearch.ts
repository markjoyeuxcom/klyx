import { useEffect, useRef, useState } from "react";
import type { CancellablePromise } from "@wailsio/runtime";
import { searchIndex, type IndexSearch } from "../../bridge/fleetIndex";

// useIndexSearch runs the console's object search (The Line slice 9 note
// §4.1): debounced, each request cancelling the previous one in Go, and a
// reply applied only when it answers the latest request of the same open
// session - so A -> B -> A never shows the first A's reply. A short query
// asks for the coverage alone.

export const SEARCH_DEBOUNCE_MS = 150;
export const SEARCH_LIMIT = 50;

export function useIndexSearch(session: number, query: string): { data: IndexSearch | null; pending: boolean; error: string | null } {
  const [state, setState] = useState<{ session: number; seq: number; data: IndexSearch | null; error: string | null }>({ session: 0, seq: 0, data: null, error: null });
  const seqRef = useRef(0);
  const inflight = useRef<CancellablePromise<IndexSearch> | null>(null);
  const q = query.trim();

  useEffect(() => {
    if (session === 0) return;
    const seq = ++seqRef.current;
    const timer = setTimeout(() => {
      inflight.current?.cancel();
      let p: CancellablePromise<IndexSearch>;
      try {
        p = searchIndex(q.length >= 2 ? q : "", SEARCH_LIMIT);
      } catch (e) {
        setState((s) => ({ ...s, session, seq, error: e instanceof Error ? e.message : String(e) }));
        return;
      }
      inflight.current = p;
      p.then(
        (data) => { if (seqRef.current === seq) setState({ session, seq, data, error: null }); },
        (e: unknown) => {
          if (seqRef.current !== seq) return;
          const msg = e instanceof Error ? e.message : String(e);
          if (/cancel/i.test(msg)) return; // superseded, never an error
          setState((s) => ({ ...s, session, seq, error: msg }));
        },
      );
    }, q === "" ? 0 : SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [session, q]);

  // the session ends: nothing in flight is applied any more
  useEffect(() => () => { seqRef.current++; inflight.current?.cancel(); }, [session]);

  const current = state.session === session;
  return {
    data: current ? state.data : null,
    pending: !current || state.seq !== seqRef.current,
    error: current ? state.error : null,
  };
}
