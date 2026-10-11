import { useMemo } from "react";
import { useFleet } from "../store/fleet";
import type { Tone } from "../chrome/primitives/styles";
import { attentionTone, coverageOf, summarize } from "../health/verdict";
import { attentionAreas, type Area } from "./attention";

// A navigation count: a number with the tone of its worst verdict (danger for
// any fault, warning for degraded only). Zero renders nothing - it is never a
// green "0" and never a claim that an unread area is quiet. Evidence (warning
// events) carries no tone.
export type NavBadge = { n: number; tone: Tone | undefined; title: string };
const NONE: NavBadge = { n: 0, tone: undefined, title: "" };

export type NavCounts = {
  group: Record<string, NavBadge>;
  destination: Record<string, NavBadge>;
  // Per group: what the summary could not establish ("unverified",
  // "unreadable", "checking…") - shown beside the count, never as zero.
  groupNote: Record<string, string>;
};

const badgeOf = (a: Area | undefined): NavBadge =>
  a && (a.state === "attention" || a.state === "evidence") ? { n: a.n, tone: a.tone, title: `${a.label}: ${a.text}` } : NONE;

// useNavCounts reads the shared cluster attention summary (step 7e). A lens
// that is open and holds this cluster's complete all-namespaces read is
// fresher than the poll, so its count wins (Pods). Flux sources come from the
// Flux lens when it is loaded. Groups show their strongest member, never a
// sum of two representations (workloads and the pods they own) or two
// vocabularies.
export function useNavCounts(cluster: string | null): NavCounts {
  const attention = useFleet((s) => s.attention);
  const pods = useFleet((s) => s.pods);
  const gitops = useFleet((s) => s.gitops);
  return useMemo((): NavCounts => {
    if (!cluster) return { group: {}, destination: {}, groupNote: {} };
    const areas = attentionAreas(attention, cluster);
    const by = Object.fromEntries(areas.map((a) => [a.id, a])) as Partial<Record<Area["id"], Area>>;

    let podsBadge = badgeOf(by.pods);
    // The REQUESTED scope must be all-namespaces too: during a switch to a
    // namespace the store still holds the old all-namespaces rows.
    if (pods.namespace === "" && coverageOf(pods, cluster, "") === "complete") {
      const c = summarize(pods.items, (p) => p.health);
      podsBadge = c.attention > 0 ? { n: c.attention, tone: attentionTone(c), title: `Pods: ${c.attention} need attention (${c.fault} fault, ${c.degraded} degraded)` } : NONE;
    }
    const fluxMine = gitops.cluster === cluster && !gitops.loading;
    const srcNotReady = fluxMine ? (gitops.sources ?? []).filter((s) => !s.suspended && s.ready !== "Ready").length : 0;
    const sources: NavBadge = srcNotReady > 0 ? { n: srcNotReady, tone: "warning", title: `${srcNotReady} Flux sources not ready` } : NONE;

    const workloads = badgeOf(by.workloads);
    const strongest = (...b: NavBadge[]) => b.reduce((acc, x) => (x.n === 0 ? acc : acc.n === 0 || (x.tone === "danger" && acc.tone !== "danger") ? x : acc), NONE);
    const flux = badgeOf(by.flux);

    const groupNote: Record<string, string> = {};
    for (const a of areas) {
      if (a.state !== "unreadable" && a.state !== "unverified" && a.state !== "loading") continue;
      const word = a.state === "loading" ? "checking…" : a.state;
      if (!groupNote[a.groupId] || a.state === "unreadable") groupNote[a.groupId] = word;
    }
    return {
      group: {
        workloads: strongest(workloads, podsBadge),
        cluster: badgeOf(by.nodes),
        flux: flux.n > 0 ? flux : sources,
        argo: badgeOf(by.argo),
        helm: badgeOf(by.helm),
      },
      destination: {
        workloads, pods: podsBadge, nodes: badgeOf(by.nodes), events: badgeOf(by.events),
        flux, "flux-sources": sources, argo: badgeOf(by.argo), helm: badgeOf(by.helm),
      },
      groupNote,
    };
  }, [cluster, attention, pods, gitops]);
}
