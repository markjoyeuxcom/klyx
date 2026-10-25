import { create } from "zustand";
import { readStored, writeStored } from "../settings/storage";
import type { StorageKey } from "../settings/storageKeys";

// The lens index's collapse state (The Line slice 4 §4.3): mod+\ and the
// location bar's button toggle it; collapsed means hidden, not a rail. It
// persists per viewer under klyx.layout.v1.lensIndex.
const KEY: StorageKey = "klyx.layout.v1.lensIndex";
export const LENS_INDEX_ID = "lens-index";
export const LENS_INDEX_TOGGLE_ID = "lens-index-toggle";

function initialOpen(): boolean {
  return readStored(KEY) !== "0";
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
  writeStored(KEY, open ? "1" : "0"); // unavailable storage: the choice lasts this session
}
