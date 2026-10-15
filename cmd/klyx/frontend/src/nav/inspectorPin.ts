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
// An expectation is for the opening that parked it: it expires, so a later
// plain selection of the same key never inherits it.
const expectations = new Map<string, { uid: string; at: number }>();
const EXPECT_MAX = 32;
export const EXPECT_TTL_MS = 10_000;
// An expectation is scoped to the pin it is for (its kind): two pins of one
// lens can share a key (Argo's Application and ApplicationSet), and neither
// may take the other's (review of efbfb31).
const expectKey = (cluster: string, scope: string, key: string) => `${cluster}\u0000${scope}\u0000${key}`;

export function expectPin(cluster: string, scope: string, key: string, uid: string): void {
  const k = expectKey(cluster, scope, key);
  expectations.delete(k);
  expectations.set(k, { uid, at: Date.now() });
  while (expectations.size > EXPECT_MAX) expectations.delete(expectations.keys().next().value as string);
}

/** takeExpectedPin returns and forgets the UID parked for a pin's key. */
export function takeExpectedPin(cluster: string, scope: string, key: string): string | undefined {
  const k = expectKey(cluster, scope, key);
  const e = expectations.get(k);
  expectations.delete(k);
  return e && Date.now() - e.at <= EXPECT_TTL_MS ? e.uid : undefined;
}
