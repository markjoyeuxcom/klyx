import type { ReactNode } from "react";
import { InspectorLayout } from "../layout/InspectorLayout";
import type { Gone } from "./usePinnedSubject";

// PinnedGone is the inspector for an object opened by UID (the console, The
// Line slice 9 note §4.2) when no row with that UID has been seen: it says
// what became of it - removed, replaced, or not yet verified - and never
// shows the current same-name object in its place. Replaced offers "Open
// the new object", which re-pins the selection to it.

const shortUID = (uid: string) => uid.slice(0, 8);

export function expectsOther(row: { uid?: string } | undefined, uid: string | undefined): boolean {
  return !!uid && (!row || row.uid !== uid);
}

export function PinnedGone({ cluster, kind, namespace, name, uid, gone, removedWords, onAdopt, onClose }: {
  cluster: string;
  kind: string;
  namespace?: string;
  name: string;
  uid: string;
  gone: Gone;
  // what "removed" means where absence is not deletion (an HTTPRoute
  // missing from the attached set is detached)
  removedWords?: ReactNode;
  onAdopt?: () => void;
  onClose: () => void;
}) {
  const words = gone === "removed" && removedWords ? removedWords
    : gone === "removed"
    ? <><span className="t-status">Removed.</span> The {kind} you opened (UID {shortUID(uid)}) is not in a complete read of its kind.</>
    : gone === "replaced"
      ? <><span className="t-status">Replaced.</span> A different {kind} now has this name; the one you opened (UID {shortUID(uid)}) is gone.</>
      : <><span className="t-status">Not verified.</span> The read of its kind is not complete, so the {kind} you opened (UID {shortUID(uid)}) is neither confirmed nor ruled out.</>;
  return (
    <div data-pinned-gone={gone} style={{ display: "contents" }}>
      <InspectorLayout pin={{ cluster, uid }} kind={kind} namespace={namespace} name={name} onClose={onClose}
        verdict={<div className="t-body" style={{ overflowWrap: "anywhere" }}>{words}</div>}
        primary={gone === "replaced" && onAdopt ? { label: "Open the new object", onSelect: onAdopt } : undefined}>
        <p className="t-secondary" style={{ fontSize: 12 }}>
          Nothing of another object is shown here, and no action runs on this one.
        </p>
      </InspectorLayout>
    </div>
  );
}
