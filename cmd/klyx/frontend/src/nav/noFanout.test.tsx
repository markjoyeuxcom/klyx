import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

// Health 1c slice 4 §7-§8: the open cluster's attention surfaces read the
// summary; entering a cluster fans out to no list endpoint for them. (The
// fleet board's per-visit reads are slice 5's and are stubbed here.)
const { calls, spy } = vi.hoisted(() => {
  const calls: string[] = [];
  return { calls, spy: (name: string) => vi.fn(async () => { calls.push(name); return {}; }) };
});
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  WorkloadsService: { ListWorkloads: spy("ListWorkloads") },
  PodsService: { ListPods: spy("ListPods") },
  EventsService: { ListEvents: spy("ListEvents") },
  NodesService: { ListNodes: spy("ListNodes") },
  HelmService: { ListHelmReleases: spy("ListHelmReleases") },
  GitOpsService: { GetGitOpsSummary: spy("GetGitOpsSummary") },
  ArgoService: { ListApplications: spy("ListApplications") },
  GatewayService: { ListGateways: spy("ListGateways"), GetGatewayTopology: spy("GetGatewayTopology") },
  MetricsService: { GetClusterMetrics: spy("GetClusterMetrics") },
}));
vi.mock("../bridge/mesh", () => ({ getMeshGraph: vi.fn(async () => {}) }));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../bridge/metrics", () => ({ getClusterMetrics: vi.fn(), getClusterSparklines: vi.fn(async () => ({ available: false, cpu: [], mem: [] })) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn(async () => ({ namespaces: [], events: [] })) }));
vi.mock("../bridge/crd", () => ({ listInstancePage: vi.fn(async () => ({ items: [], nextToken: "" })) }));
vi.mock("../bridge/forwards", () => ({ stopForward: vi.fn(), stopAllForwards: vi.fn() }));
vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));
vi.mock("../chrome/ThemeToggle", () => ({ ThemeToggle: () => null }));
vi.mock("../chrome/CommandPalette", () => ({ openCommandPalette: vi.fn() }));
import { useFleet, type ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { core, summary } from "../test/healthFixtures";
import { TopBar } from "../chrome/TopBar";
import { NavBand } from "../chrome/NavBand";
import { Overview } from "../cluster/Overview";
import { FleetView } from "../fleet/FleetView";
import { FleetSpine } from "../chrome/FleetSpine";

const c = { name: "dev", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1", gitopsTier: "Absent", gitopsReason: "",
  networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 0 } as ClusterDTO;

beforeEach(() => {
  calls.length = 0;
  resetHealthStore();
  useFleet.setState({ clusters: [c], route: { name: "cluster", cluster: "dev", section: "overview" }, forwards: [] });
});

describe("no attention fan-out on cluster entry", () => {
  it("the mast, the tree and Overview read the summary and call no list endpoint", async () => {
    applySummary(summary(core(), {}, "dev"));
    render(<><TopBar /><NavBand /><Overview c={c} /></>);
    await new Promise((r) => setTimeout(r, 20));
    expect(calls).toEqual([]);
  });
});

describe("no health fan-out on the fleet board (slice 5)", () => {
  it("the board, its cards and the spine read summaries and call no list endpoint", async () => {
    useFleet.setState({ clusters: [c, { ...c, name: "prd" }], route: { name: "fleet" }, mesh: null });
    applySummary(summary(core(), {}, "dev"));
    render(<><FleetSpine /><FleetView /></>);
    await new Promise((r) => setTimeout(r, 20));
    expect(calls).toEqual([]);
  });
});

