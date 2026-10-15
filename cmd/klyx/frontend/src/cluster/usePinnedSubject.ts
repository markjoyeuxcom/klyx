import { useReducer, useRef } from "react";

// usePinnedSubject is a selection's object, pinned to the UID it had when
// first seen (The Line slice 4 §6.3, ruled in design review 37f00bc3). The
// lens finds `live` at the selected key; this keeps the pinned object's last
// evidence and says what became of it:
//   - "removed": absent from a complete read of its kind;
//   - "unverified": absent, or a different UID at the key, on a read that is
//     not complete - nothing is claimed either way;
//   - "replaced": a complete read holds a different UID at the key. The
//     subject stays the pinned object; `next` is the new one, and `adopt`
//     re-pins the selection to it ("Open the new object").
// An object with no UID never pins and is never replaced.
export type Gone = "removed" | "unverified" | "replaced";
type Held<T> = { cluster: string; key: string; uid: string; row: T };

export function usePinnedSubject<T extends { uid?: string }>(cluster: string, key: string | null, live: T | undefined, complete: boolean): {
  subject: T | null;
  gone: Gone | null;
  next: T | null;
  adopt: () => void;
} {
  const held = useRef<Held<T> | null>(null);
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  if (!key) {
    held.current = null;
    return { subject: null, gone: null, next: null, adopt: () => {} };
  }
  const h = held.current && held.current.cluster === cluster && held.current.key === key ? held.current : null;
  if (live) {
    const differs = !!h?.uid && !!live.uid && live.uid !== h.uid;
    if (!differs) {
      // the same object (or one with no UID): follow it, pinning its UID
      held.current = { cluster, key, uid: h?.uid || live.uid || "", row: live };
      return { subject: live, gone: null, next: null, adopt: () => {} };
    }
    const pinned = h as Held<T>;
    const adopt = () => { held.current = { cluster, key, uid: live.uid ?? "", row: live }; rerender(); };
    return complete
      ? { subject: pinned.row, gone: "replaced", next: live, adopt }
      : { subject: pinned.row, gone: "unverified", next: null, adopt: () => {} };
  }
  if (!h) return { subject: null, gone: null, next: null, adopt: () => {} };
  return { subject: h.row, gone: complete ? "removed" : "unverified", next: null, adopt: () => {} };
}
