import { create } from "zustand";

// The open inspector's pin (The Line slice 4 §6.3-6.4): the cluster, and the
// UID where the object has one, that every verb, follow-up and Explain from
// the inspector uses. While an inspector is mounted it publishes its pin
// here, which gives the location bar its object crumb.
export type InspectorPinValue = { cluster: string; uid?: string; kind: string; namespace?: string; name: string };

type Held = { token: number; pin: InspectorPinValue } | null;
export const useInspectorPin = create<{ held: Held }>(() => ({ held: null }));

let seq = 0;

// publishPin holds a pin until its returned release runs; a later inspector's
// pin is never cleared by an earlier one's release.
export function publishPin(pin: InspectorPinValue): () => void {
  const token = ++seq;
  useInspectorPin.setState({ held: { token, pin } });
  return () => { if (useInspectorPin.getState().held?.token === token) useInspectorPin.setState({ held: null }); };
}

export const INSPECTOR_IDENTITY_ID = "inspector-identity";
