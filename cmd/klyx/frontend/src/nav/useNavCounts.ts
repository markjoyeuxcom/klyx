import { useMemo } from "react";
import { useFleet } from "../store/fleet";
import type { Tone } from "../chrome/primitives/styles";
import { attentionTone } from "../health/verdict";
import { kindEvidence } from "../layout/coverage";
import { attentionAreas, uncoveredDomains, type Area } from "./attention";

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
  // "unreadable", "checking…", "last good", "not summarized") - shown beside
  // the count, never as zero.
  groupNote: Record<string, string>;
  // Per destination: the same, for destinations the summary does not cover.
  destinationNote: Record<string, string>;
};

const clock = (t: number | null) => { if (!t) return ""; const d = new Date(t); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`; };

// Each count names its observation: the shared summary (with its as-of time)
// or a live lens read, so two surfaces that disagree say why.
const badgeOf = (a: Area | undefined): NavBadge =>
  a && (a.state === "attention" || a.state === "evidence") ? { n: a.n, tone: a.tone, title: `${a.label}: ${a.text} - cluster summary${a.at ? ` as of ${clock(a.at)}` : ""}` } : NONE;

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
  const clusters = useFleet((s) => s.clusters);
  return useMemo((): NavCounts => {
    if (!cluster) return { group: {}, destination: {}, groupNote: {}, destinationNote: {} };
    const areas = attentionAreas(attention, cluster);
    const by = Object.fromEntries(areas.map((a) => [a.id, a])) as Partial<Record<Area["id"], Area>>;

    let podsBadge = badgeOf(by.pods);
    // The open Pods lens holds the Observer's rows with the one count
    // selector's global verdict counts (1c slice 3 §4a) - fresher than the
    // poll, so they win while the Pod kind's read is complete. The namespace
    // filter never scopes them: the rows are all namespaces.
    if (pods.cluster === cluster && pods.counts && kindEvidence(!pods.loading, pods.coverage, "Pod", pods.error) === "complete") {
      const g = pods.counts;
      const c = { ...g, attention: g.fault + g.degraded };
      podsBadge = c.attention > 0 ? { n: c.attention, tone: attentionTone(c), title: `Pods: ${c.attention} need attention (${c.fault} fault, ${c.degraded} degraded) - Pods lens rows` } : NONE;
    }
    const fluxMine = gitops.cluster === cluster && !gitops.loading;
    const srcNotReady = fluxMine ? (gitops.sources ?? []).filter((s) => !s.suspended && s.ready !== "Ready").length : 0;
    const sources: NavBadge = srcNotReady > 0 ? { n: srcNotReady, tone: "warning", title: `${srcNotReady} Flux sources not ready` } : NONE;

    const workloads = badgeOf(by.workloads);
    const strongest = (...b: NavBadge[]) => b.reduce((acc, x) => (x.n === 0 ? acc : acc.n === 0 || (x.tone === "danger" && acc.tone !== "danger") ? x : acc), NONE);
    const flux = badgeOf(by.flux);

    // Coverage notes, independent of the counts: a lit area can also be a gap.
    const groupNote: Record<string, string> = {};
    const weight = { unreadable: 4, "last good": 3, unverified: 2, "checking…": 1 } as Record<string, number>;
    for (const a of areas) {
      const word = a.state === "unreadable" ? "unreadable" : a.stale ? "last good" : a.state === "loading" ? "checking…" : a.gap || a.state === "unverified" ? "unverified" : "";
      if (!word) continue;
      if (!groupNote[a.groupId] || weight[word] > weight[groupNote[a.groupId]]) groupNote[a.groupId] = word;
    }
    const destinationNote: Record<string, string> = {};
    for (const u of uncoveredDomains(clusters.find((x) => x.name === cluster))) {
      if (u.capability !== "present") continue; // the tree already says checking/unknown
      destinationNote[u.destinationId] = "not summarized";
      if (u.groupId === "certs" || u.groupId === "eso") groupNote[u.groupId] = groupNote[u.groupId] ?? "not summarized";
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
      destinationNote,
    };
  }, [cluster, attention, pods, gitops, clusters]);
}
