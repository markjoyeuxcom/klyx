import { useMemo } from "react";
import { useFleet } from "../store/fleet";
import type { Tone } from "../chrome/primitives/styles";
import { attentionTone, coverageOf, summarize } from "../health/verdict";

// A navigation count: a number with the tone of its worst verdict (danger for
// any fault, warning for degraded only). Zero renders nothing - it is never a
// green "0" and never a claim that an unread lens is quiet.
export type NavBadge = { n: number; tone: Tone | undefined; title: string };
const NONE: NavBadge = { n: 0, tone: undefined, title: "" };

export type NavCounts = { group: Record<string, NavBadge>; destination: Record<string, NavBadge> };

// useNavCounts reads only data that is already loaded for THIS cluster: a lens
// that has not been opened contributes nothing rather than a fake zero.
// Workloads come from the fleet board (fetched fleet-wide) and Pods from the
// pods store, each counted only when it is a complete all-namespaces
// observation. Flux and Argo are not on the shared verdict yet: their legacy
// counts keep their own wording and are never summed with verdict counts.
export function useNavCounts(cluster: string | null): NavCounts {
  const board = useFleet((s) => s.fleetBoard);
  const pods = useFleet((s) => s.pods);
  const gitops = useFleet((s) => s.gitops);
  const argo = useFleet((s) => s.argo);
  return useMemo((): NavCounts => {
    if (!cluster) return { group: {}, destination: {} };
    const wl = board[cluster]?.workloads;
    const workloads: NavBadge = wl && wl.coverage === "complete" && wl.counts.attention > 0
      ? { n: wl.counts.attention, tone: attentionTone(wl.counts), title: `${wl.counts.attention} workloads need attention (${wl.counts.fault} fault, ${wl.counts.degraded} degraded)` }
      : NONE;
    let podsBadge = NONE;
    // The REQUESTED scope must be all-namespaces too: during a switch to a
    // namespace the store still holds the old all-namespaces rows.
    if (pods.namespace === "" && coverageOf(pods, cluster, "") === "complete") {
      const c = summarize(pods.items, (p) => p.health);
      if (c.attention > 0) podsBadge = { n: c.attention, tone: attentionTone(c), title: `${c.attention} pods need attention (${c.fault} fault, ${c.degraded} degraded)` };
    }
    const fluxMine = gitops.cluster === cluster && !gitops.loading;
    const fluxFailed = fluxMine ? gitops.resources.filter((r) => r.ready === "Failed" && !r.suspended).length : 0;
    const flux: NavBadge = fluxFailed > 0 ? { n: fluxFailed, tone: "danger", title: `${fluxFailed} Flux resources failed` } : NONE;
    const srcNotReady = fluxMine ? (gitops.sources ?? []).filter((s) => !s.suspended && s.ready !== "Ready").length : 0;
    const sources: NavBadge = srcNotReady > 0 ? { n: srcNotReady, tone: "warning", title: `${srcNotReady} Flux sources not ready` } : NONE;
    const argoBroken = argo.cluster === cluster ? argo.apps.filter((a) => a.broken).length : 0;
    const argoBadge: NavBadge = argoBroken > 0 ? { n: argoBroken, tone: "danger", title: `${argoBroken} Argo CD applications broken` } : NONE;

    // A group shows its strongest member, never a sum of two representations
    // (workloads and the pods they own) or two vocabularies (Flux and Argo).
    const strongest = (...b: NavBadge[]) => b.reduce((a, x) => (x.n === 0 ? a : a.n === 0 || (x.tone === "danger" && a.tone !== "danger") ? x : a), NONE);
    return {
      group: { workloads: strongest(workloads, podsBadge), flux: flux.n > 0 ? flux : sources, argo: argoBadge },
      destination: { workloads, pods: podsBadge, flux, "flux-sources": sources, argo: argoBadge },
    };
  }, [cluster, board, pods, gitops, argo]);
}
