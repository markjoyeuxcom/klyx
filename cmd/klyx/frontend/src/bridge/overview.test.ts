import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";

// Mock all six services before importing the bridge module.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  WorkloadsService: {
    ListWorkloads: vi.fn(),
  },
  PodsService: {
    ListPods: vi.fn(),
  },
  EventsService: {
    ListEvents: vi.fn(),
  },
  NodesService: {
    ListNodes: vi.fn(),
  },
  HelmService: {
    ListHelmReleases: vi.fn(),
  },
  GitOpsService: {
    GetGitOpsSummary: vi.fn(),
  },
}));

import {
  WorkloadsService,
  PodsService,
  EventsService,
  NodesService,
  HelmService,
  GitOpsService,
} from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

import { summarise, fetchOverviewSummary, SummariseInput } from "./overview";

// ---- summarise() pure-function tests -------------------------------------------

describe("summarise", () => {
  it("counts only unhealthy+degraded workloads (not restarts)", () => {
    const input: SummariseInput = {
      workloads: [
        { rank: "unhealthy" },
        { rank: "degraded" },
        { rank: "restarts" }, // info only — should NOT count
        { rank: "healthy" },
      ],
      pods: [],
      events: [],
      nodes: [],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.unhealthyWorkloads).toBe(2);
  });

  it("counts only unhealthy+degraded pods (not restarts)", () => {
    const input: SummariseInput = {
      workloads: [],
      pods: [
        { rank: "unhealthy" },
        { rank: "restarts" },
        { rank: "healthy" },
      ],
      events: [],
      nodes: [],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.podsNotReady).toBe(1);
  });

  it("counts Warning events only (not Normal)", () => {
    const input: SummariseInput = {
      workloads: [],
      pods: [],
      events: [
        { type: "Warning" },
        { type: "Warning" },
        { type: "Normal" },
      ],
      nodes: [],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.warningEvents).toBe(2);
  });

  it("counts node problems: !ready, problems.length>0, unschedulable", () => {
    const input: SummariseInput = {
      workloads: [],
      pods: [],
      events: [],
      nodes: [
        { ready: true,  problems: [],      unschedulable: false }, // healthy
        { ready: false, problems: [],      unschedulable: false }, // not ready
        { ready: true,  problems: ["DiskPressure"], unschedulable: false }, // has problems
        { ready: true,  problems: [],      unschedulable: true  }, // unschedulable
      ],
      helmAvailable: false,
      releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.nodeProblems).toBe(3);
  });

  it("returns null failedReleases when helmAvailable=false", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false,
      releases: [{ status: "failed" }],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.failedReleases).toBeNull();
  });

  it("counts failed releases when helmAvailable=true", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: true,
      releases: [{ status: "failed" }, { status: "deployed" }, { status: "failed" }],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.failedReleases).toBe(2);
  });

  it("returns zero counts for empty datasets", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: true, releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.unhealthyWorkloads).toBe(0);
    expect(out.podsNotReady).toBe(0);
    expect(out.warningEvents).toBe(0);
    expect(out.nodeProblems).toBe(0);
    expect(out.failedReleases).toBe(0);
  });

  it("derives namespace count from the provided set", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: null,
    };
    const ns = new Set(["kube-system", "monitoring", "default"]);
    const out = summarise(input, ns);
    expect(out.namespaces).toBe(3);
  });

  it("passes flux through when present=true", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: { present: true, notReady: 2, suspended: 1 },
    };
    const out = summarise(input, new Set());
    expect(out.flux).toEqual({ present: true, notReady: 2, suspended: 1 });
  });

  it("returns null flux when input flux is null", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: null,
    };
    const out = summarise(input, new Set());
    expect(out.flux).toBeNull();
  });

  it("returns null flux when present=false", () => {
    const input: SummariseInput = {
      workloads: [], pods: [], events: [], nodes: [],
      helmAvailable: false, releases: [],
      flux: { present: false, notReady: 0, suspended: 0 },
    };
    const out = summarise(input, new Set());
    expect(out.flux).toBeNull();
  });
});

// ---- fetchOverviewSummary stale-guard test ------------------------------------

describe("fetchOverviewSummary stale guard", () => {
  beforeEach(() => {
    useFleet.getState().clearOverviewSummary();
    vi.clearAllMocks();
  });

  it("drops result when cluster changes mid-flight", async () => {
    // First call in-flight for cluster-a.
    let resolveWorkloads!: (v: unknown) => void;
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise((res) => { resolveWorkloads = res; }),
    );
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], events: [] });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({ nodes: [] });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({ available: false, releases: [] });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, total: 0, notReady: 0, suspended: 0 });

    // Start fetch for cluster-a — don't await yet.
    const fetchA = fetchOverviewSummary("cluster-a");

    // Simulate cluster change: fetch for cluster-b resets the slice.
    useFleet.getState().setOverviewSummaryLoading("cluster-b");

    // Now resolve the in-flight workloads call for cluster-a.
    resolveWorkloads({ workloads: [] });
    await fetchA;

    // The store should still be in loading state for cluster-b, not have cluster-a data.
    const state = useFleet.getState().overviewSummary;
    expect(state.cluster).toBe("cluster-b");
    expect(state.loading).toBe(true); // cluster-b fetch never completed
  });
});

// ---- fetchOverviewSummary happy-path test ------------------------------------

describe("fetchOverviewSummary integration", () => {
  beforeEach(() => {
    useFleet.getState().clearOverviewSummary();
    vi.clearAllMocks();
  });

  it("writes expected counts into the store on success", async () => {
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockResolvedValue({
      fluxPresent: false, namespaces: [], workloads: [
        { rank: "unhealthy" }, { rank: "degraded" }, { rank: "healthy" },
      ],
    });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({
      namespaces: ["default", "kube-system"],
      pods: [
        { rank: "unhealthy", namespace: "default" },
        { rank: "healthy",   namespace: "kube-system" },
      ],
    });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({
      namespaces: [], events: [{ type: "Warning" }, { type: "Normal" }],
    });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({
      nodes: [{ ready: false, problems: [], unschedulable: false }],
    });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({
      available: true, releases: [{ status: "failed" }, { status: "deployed" }],
    });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockResolvedValue({
      fluxPresent: true, total: 3, notReady: 1, suspended: 1,
    });

    await fetchOverviewSummary("homelab");

    const s = useFleet.getState().overviewSummary;
    expect(s.cluster).toBe("homelab");
    expect(s.loading).toBe(false);
    expect(s.unhealthyWorkloads).toBe(2);
    expect(s.podsNotReady).toBe(1);
    expect(s.warningEvents).toBe(1);
    expect(s.nodeProblems).toBe(1);
    expect(s.helmAvailable).toBe(true);
    expect(s.failedReleases).toBe(1);
    expect(s.namespaces).toBe(2); // "default" + "kube-system"
    expect(s.flux).toEqual({ present: true, notReady: 1, suspended: 1 });
  });

  it("shows null counts for tiles that fail to load", async () => {
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockRejectedValue(new Error("rpc error"));
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], events: [] });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({ nodes: [] });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({ available: false, releases: [] });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, total: 0, notReady: 0, suspended: 0 });

    await fetchOverviewSummary("homelab");

    const s = useFleet.getState().overviewSummary;
    expect(s.unhealthyWorkloads).toBeNull(); // workloads fetch rejected
    expect(s.podsNotReady).toBe(0);         // pods fetch succeeded
  });

  // Review 2026-10-03 #10: the list bindings resolve (not reject) on failure,
  // with `error` set and empty rows. That must read as unreadable, never as
  // zero problems.
  it("a fulfilled read carrying error is unreadable, not zero", async () => {
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, namespaces: [], workloads: [], error: "deployments.apps is forbidden" });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [], error: "cluster not connected: homelab" });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], events: [], error: "context deadline exceeded" });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({ nodes: [], error: "nodes is forbidden" });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({ available: false, releases: [], error: "helm list: Kubernetes cluster unreachable" });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, total: 0, notReady: 0, suspended: 0 });

    await fetchOverviewSummary("homelab");

    const s = useFleet.getState().overviewSummary;
    expect(s.unhealthyWorkloads).toBeNull();
    expect(s.podsNotReady).toBeNull();
    expect(s.warningEvents).toBeNull();
    expect(s.nodeProblems).toBeNull();
  });

  it("flux tile is null when fluxPresent is false", async () => {
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, namespaces: [], workloads: [] });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], events: [] });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({ nodes: [] });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({ available: false, releases: [] });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, total: 0, notReady: 0, suspended: 0 });

    await fetchOverviewSummary("homelab");
    expect(useFleet.getState().overviewSummary.flux).toBeNull();
  });

  it("flux tile is null when GetGitOpsSummary rejects", async () => {
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, namespaces: [], workloads: [] });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], events: [] });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({ nodes: [] });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({ available: false, releases: [] });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockRejectedValue(new Error("rpc"));

    await fetchOverviewSummary("homelab");
    expect(useFleet.getState().overviewSummary.flux).toBeNull();
  });

  // A summary that carries a backend error (transient RBAC/network) is NOT a
  // Flux-free cluster: flux stays null but fluxError is set so the tile shows
  // "—" instead of hiding - error-honesty.
  it("sets fluxError when GetGitOpsSummary returns an error", async () => {
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, namespaces: [], workloads: [] });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    (EventsService.ListEvents as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], events: [] });
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValue({ nodes: [] });
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValue({ available: false, releases: [] });
    (GitOpsService.GetGitOpsSummary as ReturnType<typeof vi.fn>).mockResolvedValue({ fluxPresent: false, total: 0, notReady: 0, suspended: 0, error: "forbidden" });

    await fetchOverviewSummary("homelab");
    const s = useFleet.getState().overviewSummary;
    expect(s.flux).toBeNull();
    expect(s.fluxError).toBe(true);
  });
});
