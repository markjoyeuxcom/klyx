import { create } from "zustand";

// The lens index's collapse state (The Line slice 4 §4.3): mod+\ and the
// location bar's button toggle it; collapsed means hidden, not a rail. It
// persists per viewer under klyx.layout.v1.lensIndex.
const KEY = "klyx.layout.v1.lensIndex";

function initialOpen(): boolean {
  try { return localStorage.getItem(KEY) !== "0"; } catch { return true; }
}

export const useLensIndexOpen = create<{ open: boolean }>(() => ({ open: initialOpen() }));

export function toggleLensIndex(): void {
  const open = !useLensIndexOpen.getState().open;
  useLensIndexOpen.setState({ open });
  try { localStorage.setItem(KEY, open ? "1" : "0"); } catch { /* the choice lasts this session */ }
}
