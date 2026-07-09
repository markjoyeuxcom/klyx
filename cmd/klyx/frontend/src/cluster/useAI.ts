import { useEffect, useState } from "react";
import { aiAvailability, aiEgressAllowed } from "../bridge/ai";

export type AIState = { ready: boolean; egress: boolean };

// useAIAvailability resolves whether an AI backend is configured (ready) and
// whether an Explain call is permitted for this cluster (egress — false when a
// hosted backend would send a protected cluster's topology off the machine
// without opt-in). Drives whether the Explain affordance renders / is enabled.
export function useAIAvailability(cluster: string, enabled: boolean): AIState {
  const [ai, setAi] = useState<AIState>({ ready: false, egress: false });
  useEffect(() => {
    if (!enabled) { setAi({ ready: false, egress: false }); return; }
    let live = true;
    aiAvailability()
      .then(async (a) => {
        if (!live) return;
        if (!a.ready) { setAi({ ready: false, egress: false }); return; }
        const egress = await aiEgressAllowed(cluster).catch(() => false);
        if (live) setAi({ ready: true, egress });
      })
      .catch(() => { if (live) setAi({ ready: false, egress: false }); });
    return () => { live = false; };
  }, [cluster, enabled]);
  return ai;
}
