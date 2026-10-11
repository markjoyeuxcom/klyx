import { summarize, type HealthLike, type VerdictCounts, type VerdictObservation } from "../health/verdict";
// ---- pure summary math (exported for unit tests) --------------------------------

// Workloads, pods and nodes are counted from the shared backend verdict
// (internal/health) - attention = fault + degraded. A row without a verdict
// is unknown, never counted healthy or broken from legacy fields.
type RankedRow = { health?: HealthLike };
type EventRow = { type: string };
type NodeRow = { health?: HealthLike };
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
  // Verdict counts for the migrated domains - attention, AND unknown, so a
  // surface can tell "quiet" from "could not classify".
  workloads: VerdictCounts;
  pods: VerdictCounts;
  warningEvents: number;
  nodes: VerdictCounts;
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
  const workloads = summarize(input.workloads, (w) => w.health);
  const pods = summarize(input.pods, (p) => p.health);

  const warningEvents = input.events.filter((e) => e.type === "Warning").length;

  // Cordoned is an intended operator state (paused), not a node problem.
  const nodes = summarize(input.nodes, (n) => n.health);

  const failedReleases = input.helmAvailable
    ? input.releases.filter((r) => r.status === "failed").length
    : null;

  const flux = input.flux && input.flux.present
    ? { present: true, notReady: input.flux.notReady, suspended: input.flux.suspended }
    : null;

  return {
    workloads,
    pods,
    warningEvents,
    nodes,
    helmAvailable: input.helmAvailable,
    failedReleases,
    namespaces: distinctNamespaces.size,
    flux,
  };
}

// observations pairs each migrated domain's counts with its coverage; a
// failed source is null (unreadable), never a zero.
export function observations(src: UnwrappedSources, out: SummariseOutput): {
  workloads: VerdictObservation | null;
  pods: VerdictObservation | null;
  nodes: VerdictObservation | null;
} {
  const asOf = Date.now();
  return {
    workloads: src.workloads ? { counts: out.workloads, coverage: src.workloadsPartial ? "partial" : "complete", asOf } : null,
    pods: src.pods ? { counts: out.pods, coverage: src.podsPartial ? "partial" : "complete", asOf } : null,
    nodes: src.nodes ? { counts: out.nodes, coverage: "complete", asOf } : null,
  };
}

// ---- fetch orchestration -------------------------------------------------------

// unwrapSources turns the six settled binding results into summarise() inputs:
// the row array (or raw DTO) on success, null when the fetch failed. Shared
// with fetchTape (bridge/tape.ts) so both surfaces derive from one place.
export type UnwrappedSources = {
  workloads: RankedRow[] | null;
  pods: Array<RankedRow & { namespace?: string }> | null;
  // The list hit the server cap: rows are a lower bound.
  workloadsPartial: boolean;
  podsPartial: boolean;
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

// readable: value() for a read that reports failure IN the DTO. The list
// bindings resolve with `error` set (cluster not connected, forbidden,
// timeout) and empty rows rather than rejecting, so a fulfilled promise is not
// proof of data: a DTO carrying `error` is unreadable, never "zero problems"
// (review 2026-10-03 #10 - the tape promises it never claims quiet from
// missing data).
function readable<T>(r: PromiseSettledResult<unknown>): T | null {
  const v = value<T & { error?: string }>(r);
  return v && !v.error ? v : null;
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
  const workloads = readable<{ workloads?: RankedRow[]; truncated?: boolean }>(workloadsResult);
  const pods = readable<{ pods?: Array<RankedRow & { namespace?: string }>; truncated?: boolean }>(podsResult);
  const events = readable<{ events?: EventRow[] }>(eventsResult);
  const nodes = readable<{ nodes?: NodeRow[] }>(nodesResult);
  return {
    workloads: workloads ? workloads.workloads ?? [] : null,
    pods: pods ? pods.pods ?? [] : null,
    workloadsPartial: !!workloads?.truncated,
    podsPartial: !!pods?.truncated,
    events: events ? events.events ?? [] : null,
    nodes: nodes ? nodes.nodes ?? [] : null,
    helm: readable<{ available: boolean; releases: HelmRow[] }>(helmResult),
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
