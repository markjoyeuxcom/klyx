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
import { abandonAttention, fetchAttention } from "./attention";
import { helmRelease } from "../test/backendRules";

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
describe("fetchAttention error honesty", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearAttention();
    seedHealthy();
  });

  it("a successful empty read is quiet (0)", async () => {
    await fetchAttention("nelli");
    const c = useFleet.getState().attention.counts;
    expect(c.pods).toMatchObject({ coverage: "complete", counts: { attention: 0, total: 0 } });
    expect(c.workloads).toMatchObject({ coverage: "complete", counts: { attention: 0, total: 0 } });
    expect(c.helm).toBe(0);
    expect(c.flux).toBe(0);
  });

  it("a read carrying error is unreadable, not zero", async () => {
    ok(PodsService.ListPods, { namespaces: [], pods: [], error: "pods is forbidden" });
    ok(NodesService.ListNodes, { nodes: [], error: "cluster not connected: nelli" });
    await fetchAttention("nelli");
    const c = useFleet.getState().attention.counts;
    expect(c.pods).toBe("unreadable");
    expect(c.nodes).toBe("unreadable");
    expect(c.workloads).toMatchObject({ counts: { attention: 0 } }); // the others still read
  });

  it("a failed helm list is unreadable; only 'helm not found' is absent", async () => {
    ok(HelmService.ListHelmReleases, { available: false, message: "helm list: unreachable", releases: [], error: "helm list: unreachable" });
    await fetchAttention("nelli");
    expect(useFleet.getState().attention.counts.helm).toBe("unreadable");

    ok(HelmService.ListHelmReleases, { available: false, message: "helm not found", releases: [] });
    await fetchAttention("nelli");
    expect(useFleet.getState().attention.counts.helm).toBe("absent");
  });

  it("a failed flux summary is unreadable, not absent", async () => {
    ok(GitOpsService.GetGitOpsSummary, { fluxPresent: false, total: 0, notReady: 0, suspended: 0, error: "kustomizations is forbidden" });
    await fetchAttention("nelli");
    expect(useFleet.getState().attention.counts.flux).toBe("unreadable");
  });
});

// Coverage and scope moved here with fetchOverviewSummary (step 7e): the
// attention summary is now the only cluster summary read.
describe("fetchAttention coverage and scope", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearAttention();
    seedHealthy();
  });

  it("a capped list is partial even with zero attention, and namespaces are not counted from it", async () => {
    ok(WorkloadsService.ListWorkloads, { fluxPresent: false, namespaces: [], workloads: [{ health: { kind: "ok" } }], truncated: true });
    ok(PodsService.ListPods, { namespaces: [], pods: [{ namespace: "a", health: { kind: "ok" } }], truncated: true });
    await fetchAttention("homelab");
    const c = useFleet.getState().attention.counts;
    expect(typeof c.workloads === "object" && c.workloads.coverage).toBe("partial");
    expect(typeof c.pods === "object" && c.pods.coverage).toBe("partial");
    expect(c.namespaces).toBeNull();
  });

  it("namespaces are counted from a complete pods read", async () => {
    ok(PodsService.ListPods, { namespaces: [], pods: [{ namespace: "a" }, { namespace: "b" }, { namespace: "a" }] });
    await fetchAttention("homelab");
    expect(useFleet.getState().attention.counts.namespaces).toBe(2);
    expect(useFleet.getState().attention.at).not.toBeNull();
  });

  it("a read for a cluster left mid-flight is dropped", async () => {
    let release!: (v: unknown) => void;
    (WorkloadsService.ListWorkloads as ReturnType<typeof vi.fn>).mockReturnValue(new Promise((r) => { release = r; }));
    const first = fetchAttention("a");
    void fetchAttention("b");
    release({ fluxPresent: false, namespaces: [], workloads: [] });
    await first;
    expect(useFleet.getState().attention.cluster).toBe("b");
  });
});

describe("fetchAttention ordering and last-good (Codex 7e review)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearAttention();
    seedHealthy();
  });

  it("the latest request wins: an older reply landing later changes nothing", async () => {
    let releaseOld!: (v: unknown) => void;
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>)
      .mockReturnValueOnce(new Promise((r) => { releaseOld = r; }))
      .mockResolvedValueOnce({ available: true, releases: [] });
    const older = fetchAttention("a");
    await fetchAttention("a");
    const at = useFleet.getState().attention.at;
    expect(useFleet.getState().attention.counts.helm).toBe(0);
    releaseOld({ available: true, releases: [{ status: "failed", updatedUnix: 1 }] });
    await older;
    const s = useFleet.getState().attention;
    expect(s.counts.helm).toBe(0);
    expect(s.at).toBe(at);
    expect(s.loading).toBe(false);
  });

  it("leaving and re-entering a cluster drops the reply that was in flight", async () => {
    let release!: (v: unknown) => void;
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockReturnValueOnce(new Promise((r) => { release = r; }));
    const first = fetchAttention("a");
    abandonAttention();
    useFleet.getState().clearAttention();
    await fetchAttention("b");
    await fetchAttention("a");
    release({ available: true, releases: [{ status: "failed", updatedUnix: 1 }] });
    await first;
    expect(useFleet.getState().attention.counts.helm).toBe(0);
  });

  it("a failed re-poll keeps each area's last good value and time, marked stale", async () => {
    await fetchAttention("a");
    const before = useFleet.getState().attention;
    const nodesAt = before.areaAt?.nodes;
    (NodesService.ListNodes as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ nodes: [], error: "forbidden" });
    await new Promise((r) => setTimeout(r, 5));
    await fetchAttention("a");
    const after = useFleet.getState().attention;
    expect(typeof after.counts.nodes).toBe("object");
    expect(after.stale).toContain("nodes");
    expect(after.areaAt?.nodes).toBe(nodesAt);
    expect(after.areaAt?.workloads).toBeGreaterThan(nodesAt!);
  });

  it("stuck Helm releases are counted with the Go rule the lens reads", async () => {
    const now = Math.floor(Date.now() / 1000);
    const base = { name: "r", namespace: "apps", chart: "c", appVersion: "1", revision: 1 };
    (HelmService.ListHelmReleases as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ available: true, releases: [
      helmRelease({ ...base, name: "stuck", status: "pending-upgrade", updatedUnix: now - 3600 }),
      helmRelease({ ...base, name: "fresh", status: "pending-install", updatedUnix: now }),
      // the backend's verdict wins over the status word
      { ...helmRelease({ ...base, name: "flagged", status: "deployed", updatedUnix: now }), rule: { included: true, role: "degraded", label: "Flagged by backend" } },
    ] });
    await fetchAttention("a");
    expect(useFleet.getState().attention.counts.helmStuck).toBe(2);
  });
});

describe("a previously absent tool after a failed read (Codex 7e re-review)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearAttention();
    seedHealthy(); // Flux summary present, Argo "not detected" (absent)
  });

  it("absent -> failed is unreadable (a gap), never a retained absence; -> recovered present shows the reading", async () => {
    const { attentionAreas, attentionSummary } = await import("../nav/attention");
    await fetchAttention("a");
    expect(useFleet.getState().attention.counts.argo).toBe("absent");
    (ArgoService.ListApplications as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error("timeout"));
    await fetchAttention("a");
    const s = useFleet.getState().attention;
    expect(s.counts.argo).toBe("unreadable");
    expect(s.stale ?? []).not.toContain("argo");
    const areas = attentionAreas(s, "a");
    expect(areas.find((x) => x.id === "argo")?.state).toBe("unreadable");
    expect(attentionSummary(areas).gaps).toBeGreaterThan(0);
    (ArgoService.ListApplications as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ available: true, apps: [{ broken: true }] });
    await fetchAttention("a");
    expect(useFleet.getState().attention.counts.argo).toBe(1);
  });

  it("absent -> failed -> confirmed absent again is omitted with no gap left", async () => {
    const { attentionAreas, attentionSummary } = await import("../nav/attention");
    await fetchAttention("a");
    (ArgoService.ListApplications as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error("timeout"));
    await fetchAttention("a");
    await fetchAttention("a"); // default mock: Argo not detected
    const s = useFleet.getState().attention;
    expect(s.counts.argo).toBe("absent");
    const areas = attentionAreas(s, "a");
    expect(areas.find((x) => x.id === "argo")).toBeUndefined();
    expect(attentionSummary(areas).gaps).toBe(0);
  });
});
