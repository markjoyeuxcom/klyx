import { createContext, useContext } from "react";

// The inspector's expand control (The Line slice 16). LensLayout owns the
// geometry, so it provides whether its inspector is expanded over the whole
// workspace and how to toggle it; InspectorLayout draws the button only when
// a layout provides it.
export type InspectorExpandValue = { expanded: boolean; toggle: () => void };
export const InspectorExpand = createContext<InspectorExpandValue | null>(null);
export const useInspectorExpand = () => useContext(InspectorExpand);
