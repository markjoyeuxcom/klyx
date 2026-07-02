import { useFleet } from "../store/fleet";
import type { TapeCounts } from "../store/fleet";
import { unwrapSources, summariseSources } from "./overview";
import {
  WorkloadsService,
  PodsService,
  EventsService,
  NodesService,
  HelmService,
  GitOpsService,
  ArgoService,
} from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// fetchTape populates the persistent triage tape for a cluster: one fetch per
// cluster ENTRY (not per section switch - the tape survives navigation). Count
// derivations go through the same unwrapSources + summarise() pipeline as
// fetchOverviewSummary (bridge/overview.ts) so the tape and the Overview tiles
// never disagree. Each count degrades independently to "unreadable" (the tape
// skips what it could not read - it never claims quiet from missing data);
// helm/flux/argo additionally distinguish a definitive "absent".
export async function fetchTape(cluster: string): Promise<void> {
  useFleet.getState().setTapeLoading(cluster);

  const [workloads, pods, events, nodes, helm, flux, argo] = await Promise.allSettled([
    WorkloadsService.ListWorkloads(cluster, ""),
    PodsService.ListPods(cluster, ""),
    EventsService.ListEvents(cluster, ""),
    NodesService.ListNodes(cluster),
    HelmService.ListHelmReleases(cluster),
    GitOpsService.GetGitOpsSummary(cluster),
    ArgoService.ListApplications(cluster),
  ]);

  if (useFleet.getState().tape.cluster !== cluster) return; // stale guard

  const src = unwrapSources([workloads, pods, events, nodes, helm, flux]);
  const out = summariseSources(src, new Set());

  const counts: TapeCounts = {
    workloads: src.workloads ? out.unhealthyWorkloads : "unreadable",
    pods: src.pods ? out.podsNotReady : "unreadable",
    events: src.events ? out.warningEvents : "unreadable",
    nodes: src.nodes ? out.nodeProblems : "unreadable",
    // available=false is a definitive "helm not detected" (failedReleases is
    // null exactly when helm is unavailable), not a failure.
    helm: src.helm ? out.failedReleases ?? "absent" : "unreadable",
    flux: src.flux ? (out.flux ? out.flux.notReady : "absent") : "unreadable",
    argo: "unreadable",
  };

  if (argo.status === "fulfilled" && argo.value) {
    const a = argo.value as { available: boolean; apps: { broken: boolean }[]; message?: string };
    if (a.available) counts.argo = (a.apps ?? []).filter((x) => x.broken).length;
    // "not detected" is absence; any other unavailable message is a real failure.
    else counts.argo = (a.message ?? "").includes("not detected") ? "absent" : "unreadable";
  }

  useFleet.getState().setTape(cluster, counts);
}
