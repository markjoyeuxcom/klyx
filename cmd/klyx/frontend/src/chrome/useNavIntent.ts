import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import type { NavIntent } from "../store/fleet";

export type NavIntentOutcome = "consumed" | "missing" | "wait";

// useNavIntent consumes one-shot NavIntents addressed to this view (see the
// NavIntent comment in store/fleet.ts). The handler returns:
//   "consumed" - handled; the intent is cleared
//   "missing"  - the target is gone; cleared + an error toast names it
//   "wait"     - data still loading; the intent stays parked for the next run
// Intents for other clusters or kinds are ignored (not cleared) so they stay
// parked for their owning view.
export function useNavIntent(
  cluster: string,
  kinds: readonly NavIntent["kind"][],
  handler: (intent: NavIntent) => NavIntentOutcome,
  deps: readonly unknown[],
): void {
  const intent = useFleet((s) => s.navIntent);
  useEffect(() => {
    if (!intent || intent.cluster !== cluster || !kinds.includes(intent.kind)) return;
    const outcome = handler(intent);
    if (outcome === "wait") return;
    useFleet.getState().clearNavIntent();
    if (outcome === "missing") {
      const target = "node" in intent ? intent.node
        : "ref" in intent ? [intent.ref.namespace, intent.ref.name].filter(Boolean).join("/")
        : "name" in intent ? `${intent.namespace}/${intent.name}`
        : intent.cluster;
      useFleet.getState().setActionStatus({
        kind: "error",
        message: `${intent.kind.replace(/-/g, " ")}: ${target} is no longer present`,
      });
    }
    // The handler closes over view state; callers pass the data it reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent, cluster, ...deps]);
}
