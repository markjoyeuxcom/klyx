import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  WorkloadsService: { ListWorkloads: vi.fn() },
  PodsService: { ListPods: vi.fn() },
  EventsService: { ListEvents: vi.fn() },
  NodesService: { ListNodes: vi.fn() },
  HelmService: { ListHelmReleases: vi.fn() },
  GitOpsService: { GetGitOpsSummary: vi.fn() },
  ArgoService: { ListApplications: vi.fn() },
}));

import {
  WorkloadsService, PodsService, EventsService, NodesService, HelmService, GitOpsService, ArgoService,
} from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { fetchTape } from "./tape";

const ok = (fn: unknown, v: unknown) => (fn as ReturnType<typeof vi.fn>).mockResolvedValue(v);

function seedHealthy() {
  ok(WorkloadsService.ListWorkloads, { fluxPresent: false, namespaces: [], workloads: [] });
  ok(PodsService.ListPods, { namespaces: [], pods: [] });
  ok(EventsService.ListEvents, { namespaces: [], events: [] });
  ok(NodesService.ListNodes, { nodes: [] });
  ok(HelmService.ListHelmReleases, { available: true, releases: [] });
  ok(GitOpsService.GetGitOpsSummary, { fluxPresent: true, total: 2, notReady: 0, suspended: 0 });
  ok(ArgoService.ListApplications, { available: false, apps: [], message: "Argo CD not detected" });
}

// Review 2026-10-03 #10: the tape promises it "never claims quiet from missing
// data". A read that failed resolves with `error` set and empty rows - that
// must surface as unreadable, never as 0 or "absent".
describe("fetchTape error honesty", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    seedHealthy();
  });

  it("a successful empty read is quiet (0)", async () => {
    await fetchTape("nelli");
    const c = useFleet.getState().tape.counts;
    expect(c.pods).toMatchObject({ coverage: "complete", counts: { attention: 0, total: 0 } });
    expect(c.workloads).toMatchObject({ coverage: "complete", counts: { attention: 0, total: 0 } });
    expect(c.helm).toBe(0);
    expect(c.flux).toBe(0);
  });

  it("a read carrying error is unreadable, not zero", async () => {
    ok(PodsService.ListPods, { namespaces: [], pods: [], error: "pods is forbidden" });
    ok(NodesService.ListNodes, { nodes: [], error: "cluster not connected: nelli" });
    await fetchTape("nelli");
    const c = useFleet.getState().tape.counts;
    expect(c.pods).toBe("unreadable");
    expect(c.nodes).toBe("unreadable");
    expect(c.workloads).toMatchObject({ counts: { attention: 0 } }); // the others still read
  });

  it("a failed helm list is unreadable; only 'helm not found' is absent", async () => {
    ok(HelmService.ListHelmReleases, { available: false, message: "helm list: unreachable", releases: [], error: "helm list: unreachable" });
    await fetchTape("nelli");
    expect(useFleet.getState().tape.counts.helm).toBe("unreadable");

    ok(HelmService.ListHelmReleases, { available: false, message: "helm not found", releases: [] });
    await fetchTape("nelli");
    expect(useFleet.getState().tape.counts.helm).toBe("absent");
  });

  it("a failed flux summary is unreadable, not absent", async () => {
    ok(GitOpsService.GetGitOpsSummary, { fluxPresent: false, total: 0, notReady: 0, suspended: 0, error: "kustomizations is forbidden" });
    await fetchTape("nelli");
    expect(useFleet.getState().tape.counts.flux).toBe("unreadable");
  });
});
