import { useFleet } from "../store/fleet";
import type { AttentionCounts } from "../store/fleet";
import { unwrapSources, summariseSources, observations } from "./overview";
import {
  WorkloadsService,
  PodsService,
  EventsService,
  NodesService,
  HelmService,
  GitOpsService,
  ArgoService,
} from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// fetchAttention reads the shared cluster attention summary (redesign step
// 7e): one fetch per cluster ENTRY plus a gentle re-poll, never per section
// switch. Counts go through the shared unwrapSources + summarise() pipeline
// (bridge/overview.ts). Each area degrades independently to "unreadable" -
// nothing claims quiet from missing data - and helm/flux/argo additionally
// distinguish a definitive "absent".
export async function fetchAttention(cluster: string): Promise<void> {
  useFleet.getState().setAttentionLoading(cluster);

  const [workloads, pods, events, nodes, helm, flux, argo] = await Promise.allSettled([
    WorkloadsService.ListWorkloads(cluster, ""),
    PodsService.ListPods(cluster, ""),
    EventsService.ListEvents(cluster, ""),
    NodesService.ListNodes(cluster),
    HelmService.ListHelmReleases(cluster),
    GitOpsService.GetGitOpsSummary(cluster),
    ArgoService.ListApplications(cluster),
  ]);

  if (useFleet.getState().attention.cluster !== cluster) return; // stale guard

  const src = unwrapSources([workloads, pods, events, nodes, helm, flux]);
  // Distinct namespaces come from the all-namespaces pods list.
  const nsSet = new Set<string>();
  for (const p of src.pods ?? []) if (p.namespace) nsSet.add(p.namespace);
  const out = summariseSources(src, nsSet);
  const obs = observations(src, out);

  const counts: AttentionCounts = {
    workloads: obs.workloads ?? "unreadable",
    pods: obs.pods ?? "unreadable",
    events: src.events ? out.warningEvents : "unreadable",
    nodes: obs.nodes ?? "unreadable",
    // available=false is a definitive "helm not detected" (failedReleases is
    // null exactly when helm is unavailable), not a failure.
    helm: src.helm ? out.failedReleases ?? "absent" : "unreadable",
    // A summary read that failed carries `error`: unreadable, never "absent".
    flux: src.flux && !src.flux.error ? (out.flux ? out.flux.notReady : "absent") : "unreadable",
    argo: "unreadable",
    // Only a complete pods read can count namespaces; a capped one is a lower
    // bound and an unread one is unknown.
    namespaces: src.pods && !src.podsPartial ? out.namespaces : null,
  };

  if (argo.status === "fulfilled" && argo.value) {
    const a = argo.value as { available: boolean; apps: { broken: boolean }[]; message?: string };
    if (a.available) counts.argo = (a.apps ?? []).filter((x) => x.broken).length;
    // "not detected" is absence; any other unavailable message is a real failure.
    else counts.argo = (a.message ?? "").includes("not detected") ? "absent" : "unreadable";
  }

  useFleet.getState().setAttention(cluster, counts);
}

// A gentle re-poll keeps the summary honest about a cluster that breaks while
// you work elsewhere in it, without hammering seven list endpoints.
export const attentionRepollMs = 60_000;
