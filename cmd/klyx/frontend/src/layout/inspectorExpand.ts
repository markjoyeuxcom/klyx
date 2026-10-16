import { createContext, useContext } from "react";

// The inspector's expand control (The Line slice 16). LensLayout owns the
// geometry, so it provides whether its inspector is expanded over the whole
// workspace and how to toggle it; InspectorLayout draws the button only when
// a layout provides it.
export type InspectorExpandValue = { expanded: boolean; toggle: () => void };
export const InspectorExpand = createContext<InspectorExpandValue | null>(null);
export const useInspectorExpand = () => useContext(InspectorExpand);

// listHidden: a lens's inspector is expanded over its list. A hidden list
// takes no keys - a selection nobody can see must never move or act
// (slice 16 review 1).
export function listHidden(): boolean {
  return typeof document !== "undefined" && document.querySelector('[data-testid="lens-layout"][data-expanded]') !== null;
}
