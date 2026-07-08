import { useFleet, OverviewSummary } from "../store/fleet";
import {
  WorkloadsService,
  PodsService,
  EventsService,
  NodesService,
  HelmService,
  GitOpsService,
} from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// ---- pure summary math (exported for unit tests) --------------------------------

type RankedRow = { rank: string };
type EventRow = { type: string };
type NodeRow = { ready: boolean; problems: string[]; unschedulable: boolean };
type HelmRow = { status: string };
type FluxSummaryInput = { present: boolean; notReady: number; suspended: number } | null;

export type SummariseInput = {
  workloads: RankedRow[];
  pods: RankedRow[];
  events: EventRow[];
  nodes: NodeRow[];
  helmAvailable: boolean;
  releases: HelmRow[];
  flux: FluxSummaryInput;
};

export type SummariseOutput = {
  unhealthyWorkloads: number;
  podsNotReady: number;
  warningEvents: number;
  nodeProblems: number;
  helmAvailable: boolean;
  failedReleases: number | null; // null when helm unavailable
  namespaces: number;
  flux: { present: boolean; notReady: number; suspended: number } | null;
};

/**
 * summarise derives counts from the raw binding results.
 * Pure function — no side-effects; directly unit-testable.
 */
export function summarise(
  input: SummariseInput,
  distinctNamespaces: Set<string>,
): SummariseOutput {
  const unhealthyWorkloads = input.workloads.filter(
    (w) => w.rank === "unhealthy" || w.rank === "degraded",
  ).length;

  const podsNotReady = input.pods.filter(
    (p) => p.rank === "unhealthy" || p.rank === "degraded",
  ).length;

  const warningEvents = input.events.filter((e) => e.type === "Warning").length;

  const nodeProblems = input.nodes.filter(
    (n) => !n.ready || n.problems.length > 0 || n.unschedulable,
  ).length;

  const failedReleases = input.helmAvailable
    ? input.releases.filter((r) => r.status === "failed").length
    : null;

  const flux = input.flux && input.flux.present
    ? { present: true, notReady: input.flux.notReady, suspended: input.flux.suspended }
    : null;

  return {
    unhealthyWorkloads,
    podsNotReady,
    warningEvents,
    nodeProblems,
    helmAvailable: input.helmAvailable,
    failedReleases,
    namespaces: distinctNamespaces.size,
    flux,
  };
}

// ---- fetch orchestration -------------------------------------------------------

// unwrapSources turns the six settled binding results into summarise() inputs:
// the row array (or raw DTO) on success, null when the fetch failed. Shared
// with fetchTape (bridge/tape.ts) so both surfaces derive from one place.
export type UnwrappedSources = {
  workloads: RankedRow[] | null;
  pods: Array<RankedRow & { namespace?: string }> | null;
  events: EventRow[] | null;
  nodes: NodeRow[] | null;
  helm: { available: boolean; releases: HelmRow[] } | null;
  flux: { fluxPresent: boolean; notReady: number; suspended?: number; error?: string } | null;
};

// value: the fulfilled result cast to its DTO shape, null on rejection (or a
// falsy value — the bindings never resolve to one, but stay defensive).
function value<T>(r: PromiseSettledResult<unknown>): T | null {
  return r.status === "fulfilled" && r.value ? (r.value as T) : null;
}

export function unwrapSources(
  results: readonly [
    PromiseSettledResult<unknown>,
    PromiseSettledResult<unknown>,
    PromiseSettledResult<unknown>,
    PromiseSettledResult<unknown>,
    PromiseSettledResult<unknown>,
    PromiseSettledResult<unknown>,
  ],
): UnwrappedSources {
  const [workloadsResult, podsResult, eventsResult, nodesResult, helmResult, fluxResult] = results;
  const workloads = value<{ workloads?: RankedRow[] }>(workloadsResult);
  const pods = value<{ pods?: Array<RankedRow & { namespace?: string }> }>(podsResult);
  const events = value<{ events?: EventRow[] }>(eventsResult);
  const nodes = value<{ nodes?: NodeRow[] }>(nodesResult);
  return {
    workloads: workloads ? workloads.workloads ?? [] : null,
    pods: pods ? pods.pods ?? [] : null,
    events: events ? events.events ?? [] : null,
    nodes: nodes ? nodes.nodes ?? [] : null,
    helm: value<{ available: boolean; releases: HelmRow[] }>(helmResult),
    flux: value<{ fluxPresent: boolean; notReady: number; suspended?: number; error?: string }>(fluxResult),
  };
}

// summariseSources feeds the unwrapped sources through summarise() — THE
// counting logic, unit-tested in overview.test.ts. Failed sources go in as
// empty arrays; callers null/mark them from the UnwrappedSources fields.
export function summariseSources(src: UnwrappedSources, nsSet: Set<string>): SummariseOutput {
  return summarise(
    {
      workloads: src.workloads ?? [],
      pods: src.pods ?? [],
      events: src.events ?? [],
      nodes: src.nodes ?? [],
      helmAvailable: src.helm?.available ?? false,
      releases: src.helm?.releases ?? [],
      flux: src.flux
        ? { present: src.flux.fluxPresent, notReady: src.flux.notReady ?? 0, suspended: src.flux.suspended ?? 0 }
        : null,
    },
    nsSet,
  );
}

/**
 * fetchOverviewSummary fetches all six data sources in parallel, derives counts
 * via summarise(), and writes the result into overviewSummary in the fleet store.
 * Stale-guarded: if the cluster changes before all promises settle, the result is
 * silently dropped.
 */
export async function fetchOverviewSummary(cluster: string): Promise<void> {
  useFleet.getState().setOverviewSummaryLoading(cluster);

  const results = await Promise.allSettled([
    WorkloadsService.ListWorkloads(cluster, ""),
    PodsService.ListPods(cluster, ""),
    EventsService.ListEvents(cluster, ""),
    NodesService.ListNodes(cluster),
    HelmService.ListHelmReleases(cluster),
    GitOpsService.GetGitOpsSummary(cluster),
  ]);

  // Stale guard — cluster may have changed while requests were in flight.
  if (useFleet.getState().overviewSummary.cluster !== cluster) return;

  const src = unwrapSources(results);

  // Distinct namespaces come from the pods list (the all-namespaces load).
  const nsSet = new Set<string>();
  for (const p of src.pods ?? []) {
    if (p.namespace) nsSet.add(p.namespace);
  }

  const out = summariseSources(src, nsSet);

  // Per-tile degradation: a failed source renders null ("?"), never a fake 0.
  const summary: OverviewSummary = {
    cluster,
    loading: false,
    unhealthyWorkloads: src.workloads ? out.unhealthyWorkloads : null,
    podsNotReady: src.pods ? out.podsNotReady : null,
    warningEvents: src.events ? out.warningEvents : null,
    nodeProblems: src.nodes ? out.nodeProblems : null,
    helmAvailable: out.helmAvailable,
    failedReleases: out.failedReleases,
    namespaces: out.namespaces > 0 ? out.namespaces : null,
    flux: out.flux,
    // fluxError distinguishes a failed summary read (transient RBAC/network,
    // carried as a non-empty error) from a Flux-free cluster - the tile shows
    // "—" rather than hiding, so the failure never reads as "no Flux".
    fluxError: !!(src.flux && src.flux.error),
  };

  useFleet.getState().setOverviewSummary(summary);
}
