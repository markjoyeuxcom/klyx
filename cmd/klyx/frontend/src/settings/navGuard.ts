import { create } from "zustand";

// Leaving a Settings section with a draft asks first (Settings S1 §1): to
// save, discard or stay. A typed credential is its own draft, named
// separately, and Save never stores it. Every way of leaving goes through
// here - a section switch, a search result, history, a deep link, leaving
// Settings - because the store refuses a route change that leaves a section
// with a draft (store/fleet.ts guardRoute) and history asks before it moves.

export type SectionDraft = {
  // unsaved settings, which save() stores
  settings: boolean;
  // typed credentials, by what the person would call them ("an unsaved API key")
  credentials: string[];
  // other drafts Save does not apply, because an explicit action does (an
  // alias not yet added, an approval not yet confirmed)
  unsaved?: string[];
  // saves the settings draft; resolves true once it is saved and read back
  save?: () => Promise<boolean>;
  // drops the settings draft and the typed credentials
  discard: () => void;
  // a save still being read back, which finishes before anything is asked
  settling?: () => Promise<unknown> | null;
};

const drafts = new Map<string, SectionDraft>();
let bypass = 0;
// The open question: its navigation, and its number. A Save and leave
// resolves only the request it answered: Stay, Escape or the scrim while it
// saves ends that request, and the save then neither discards nor leaves
// (Codex's S1 implementation review, R1).
let pending: { action: () => void; seq: number } | null = null;
let seq = 0;

export type LeaveRequest = { settings: boolean; credentials: string[]; unsaved: string[]; canSave: boolean };
export const useLeaveRequest = create<{ request: LeaveRequest | null; saving: boolean }>(() => ({ request: null, saving: false }));

// setSectionDraft registers (or, with null, clears) a mounted panel's draft.
export function setSectionDraft(owner: string, d: SectionDraft | null): void {
  if (d && (d.settings || d.credentials.length > 0 || (d.unsaved?.length ?? 0) > 0 || d.settling?.())) drafts.set(owner, d);
  else drafts.delete(owner);
}

function current(): SectionDraft[] {
  return [...drafts.values()];
}

function settlingNow(): Promise<unknown> | null {
  for (const d of current()) {
    const s = d.settling?.();
    if (s) return s;
  }
  return null;
}

// mustAsk reports whether leaving now would lose a draft.
export function mustAsk(): boolean {
  return bypass === 0 && current().some((d) => d.settings || d.credentials.length > 0 || (d.unsaved?.length ?? 0) > 0 || !!d.settling?.());
}

// runLeaving runs an approved navigation without asking again.
export function runLeaving(action: () => void): void {
  bypass++;
  try {
    action();
  } finally {
    bypass--;
  }
}

// requestLeave runs the navigation now when nothing would be lost, or asks.
export function requestLeave(action: () => void): void {
  if (!mustAsk()) {
    runLeaving(action);
    return;
  }
  const settling = settlingNow();
  if (settling) {
    // a save being read back finishes first; then the question is asked of
    // what is left
    void settling.finally(() => requestLeave(action));
    return;
  }
  const ds = current();
  pending = { action, seq: ++seq };
  useLeaveRequest.setState({
    request: {
      settings: ds.some((d) => d.settings),
      credentials: ds.flatMap((d) => d.credentials),
      unsaved: ds.flatMap((d) => d.unsaved ?? []),
      canSave: ds.some((d) => d.settings && !!d.save),
    },
  });
}

// resolveLeave answers the question. Save stores the settings draft (never a
// typed credential) and leaves once it is read back; a failed save stays.
export async function resolveLeave(choice: "save" | "discard" | "stay"): Promise<void> {
  const req = pending;
  if (choice === "stay" || !req) {
    pending = null;
    useLeaveRequest.setState({ request: null, saving: false });
    return;
  }
  if (choice === "save") {
    if (useLeaveRequest.getState().saving) return; // one Save per question
    useLeaveRequest.setState({ saving: true });
    for (const d of current()) {
      if (d.settings && d.save && !(await d.save())) {
        // not saved, or saved but not read back: the panel says which, and
        // the person stays to see it (R7)
        if (pending === req) {
          pending = null;
          useLeaveRequest.setState({ request: null, saving: false });
        }
        return;
      }
    }
    if (pending !== req) return; // the person chose to stay while it saved
  }
  for (const d of current()) d.discard();
  drafts.clear();
  pending = null;
  useLeaveRequest.setState({ request: null, saving: false });
  runLeaving(req.action);
}

/** Tests only. */
export function _resetNavGuardForTest(): void {
  drafts.clear();
  bypass = 0;
  pending = null;
  useLeaveRequest.setState({ request: null, saving: false });
}
