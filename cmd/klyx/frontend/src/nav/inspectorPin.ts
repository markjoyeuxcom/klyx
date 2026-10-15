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

// Expected UIDs (The Line slice 9 note §4.2): an opening that names its
// object by UID (the console) parks the UID for the lens's selection key;
// the lens's pin starts from it before any row is seen, so a same-name
// replacement or an absence is said - never the current same-name row
// silently substituted. Consumed by the first pin that reads the key.
const expectations = new Map<string, string>();
const EXPECT_MAX = 32;
const expectKey = (cluster: string, key: string) => `${cluster}\u0000${key}`;

export function expectPin(cluster: string, key: string, uid: string): void {
  const k = expectKey(cluster, key);
  expectations.delete(k);
  expectations.set(k, uid);
  while (expectations.size > EXPECT_MAX) expectations.delete(expectations.keys().next().value as string);
}

/** takeExpectedPin returns and forgets the UID parked for a selection key. */
export function takeExpectedPin(cluster: string, key: string): string | undefined {
  const k = expectKey(cluster, key);
  const uid = expectations.get(k);
  expectations.delete(k);
  return uid;
}
