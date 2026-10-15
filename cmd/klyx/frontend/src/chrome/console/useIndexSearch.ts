import { useEffect, useRef, useState } from "react";
import type { CancellablePromise } from "@wailsio/runtime";
import { searchIndex, type IndexSearch } from "../../bridge/fleetIndex";

// useIndexSearch runs the console's object search (The Line slice 9 note
// §4.1): debounced, each request cancelling the previous one in Go, and a
// reply applied only when it answers the latest request of the same open
// session - so A -> B -> A never shows the first A's reply. A short query
// asks for the coverage alone.
//
// A reply is tagged with the query it answers, and hits and errors are
// returned only for the visible query: typing B withholds A's hits at once
// (they can be neither shown nor opened), and B's failure is shown, never
// hidden behind A's result. Coverage describes the index, not the query, so
// the last reply's coverage stays readable while B is pending.

export const SEARCH_DEBOUNCE_MS = 150;
export const SEARCH_LIMIT = 50;

export type IndexSearchState = {
  data: IndexSearch | null; // the reply to the visible query, or null
  coverage: IndexSearch | null; // the last reply of this session, for its coverage line
  pending: boolean;
  error: string | null; // the visible query's failure
};

type Reply = { session: number; seq: number; asked: string; data: IndexSearch | null; error: string | null; coverage: IndexSearch | null };

export function useIndexSearch(session: number, query: string): IndexSearchState {
  const [state, setState] = useState<Reply>({ session: 0, seq: 0, asked: "", data: null, error: null, coverage: null });
  const seqRef = useRef(0);
  const inflight = useRef<CancellablePromise<IndexSearch> | null>(null);
  const q = query.trim();
  const asked = q.length >= 2 ? q : "";

  useEffect(() => {
    if (session === 0) return;
    const seq = ++seqRef.current;
    const fail = (msg: string) => setState((s) => ({ ...s, session, seq, asked, data: null, error: msg }));
    const timer = setTimeout(() => {
      inflight.current?.cancel();
      let p: CancellablePromise<IndexSearch>;
      try {
        p = searchIndex(asked, SEARCH_LIMIT);
      } catch (e) {
        fail(e instanceof Error ? e.message : String(e));
        return;
      }
      inflight.current = p;
      p.then(
        (data) => { if (seqRef.current === seq) setState({ session, seq, asked, data, error: null, coverage: data }); },
        (e: unknown) => {
          if (seqRef.current !== seq) return;
          const msg = e instanceof Error ? e.message : String(e);
          if (/cancel/i.test(msg)) return; // superseded, never an error
          fail(msg);
        },
      );
    }, asked === "" ? 0 : SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [session, asked]);

  // the session ends: nothing in flight is applied any more
  useEffect(() => () => { seqRef.current++; inflight.current?.cancel(); }, [session]);

  const current = state.session === session;
  // the stored reply answers what is typed now - never an earlier query's
  const answers = current && state.asked === asked;
  return {
    data: answers ? state.data : null,
    coverage: current ? state.coverage : null,
    pending: !answers || state.seq !== seqRef.current,
    error: answers ? state.error : null,
  };
}
