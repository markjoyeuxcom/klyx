import { create } from "zustand";

// The lens index's collapse state (The Line slice 4 §4.3): mod+\ and the
// location bar's button toggle it; collapsed means hidden, not a rail. It
// persists per viewer under klyx.layout.v1.lensIndex.
const KEY = "klyx.layout.v1.lensIndex";
export const LENS_INDEX_ID = "lens-index";
export const LENS_INDEX_TOGGLE_ID = "lens-index-toggle";

function initialOpen(): boolean {
  try { return localStorage.getItem(KEY) !== "0"; } catch { return true; }
}

export const useLensIndexOpen = create<{ open: boolean }>(() => ({ open: initialOpen() }));

// toggleLensIndex shows or hides the index. Hiding it while focus is inside
// hands focus to the location bar's disclosure first, so the focused entry's
// unmount never drops focus to the page (Codex review of 4b).
export function toggleLensIndex(): void {
  const open = !useLensIndexOpen.getState().open;
  if (!open && typeof document !== "undefined") {
    const index = document.getElementById(LENS_INDEX_ID);
    if (index && index.contains(document.activeElement)) document.getElementById(LENS_INDEX_TOGGLE_ID)?.focus();
  }
  useLensIndexOpen.setState({ open });
  try { localStorage.setItem(KEY, open ? "1" : "0"); } catch { /* the choice lasts this session */ }
}
