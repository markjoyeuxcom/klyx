import type { VerdictObservation } from "../health/verdict";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor, screen } from "@testing-library/react";
import { Overview } from "./Overview";
import { useFleet } from "../store/fleet";
import type { AttentionCounts, ClusterDTO, MetricsDTO } from "../store/fleet";

// Default: sparklines unavailable so tests render without them.
import type { SparklinesDTO } from "../bridge/metrics";
const mockGetClusterSparklines = vi.fn<(cluster: string) => Promise<SparklinesDTO>>(() =>
  Promise.resolve({ available: false, message: "metrics unavailable", cpu: [], mem: [] }),
);
vi.mock("../bridge/metrics", () => ({
  getClusterMetrics: vi.fn(),
  getClusterSparklines: (cluster: string) => mockGetClusterSparklines(cluster),
}));
vi.mock("../bridge/attention", () => ({ fetchAttention: vi.fn().mockResolvedValue(undefined), attentionRepollMs: 60000 }));
vi.mock("../bridge/fleetboard", () => ({ fetchFleetBoard: vi.fn().mockResolvedValue(undefined) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn() }));
vi.mock("../bridge/crd", () => ({ listInstancePage: vi.fn().mockResolvedValue({ items: [], nextToken: "" }) }));
import { fetchEventsSnapshot } from "../bridge/events";
import { fetchAttention } from "../bridge/attention";

const dto: ClusterDTO = {
  name: "homelab-nelli", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 58,
  version: "v1.36.1", gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "homelab", region: "", provider: "k3s", group: "", ageSeconds: 3,
};
const blue: ClusterDTO = { ...dto, name: "homelab-blue" };

function setMetrics(d: MetricsDTO | null) {
  useFleet.setState({ metrics: { cluster: "homelab-blue", dto: d, loading: false } });
}

function obs(fault = 0, total = 5, over: Partial<VerdictObservation> = {}): VerdictObservation {
  return { coverage: "complete", counts: { total, fault, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: total - fault, attention: fault }, ...over };
}

function seedAttention(cluster: string, over: Partial<AttentionCounts> = {}) {
  useFleet.setState({
    attention: {
      cluster, loading: false, at: Date.now(),
      counts: { workloads: obs(), pods: obs(), events: 0, nodes: obs(), helm: "absent", flux: "absent", argo: "absent", namespaces: null, ...over },
    },
  });
}

const area = (label: string) => screen.getByRole("button", { name: new RegExp(`^${label}`) });

beforeEach(() => {
  vi.mocked(fetchEventsSnapshot).mockResolvedValue({ namespaces: [], events: [] });
  useFleet.setState({ metrics: { cluster: null, dto: null, loading: false }, fleetBoard: {} });
  useFleet.getState().clearAttention();
});

describe("Overview on the workspace template (redesign step 7e)", () => {
  it("header carries state, version and tags; the mast already names the cluster", () => {
    seedAttention("homelab-nelli");
    render(<Overview c={dto} />);
    expect(screen.getByRole("heading", { name: "Overview" })).toBeTruthy();
    expect(screen.getByText("Connected")).toBeTruthy();
    expect(screen.getByText("v1.36.1")).toBeTruthy();
    expect(screen.getByText("homelab · k3s")).toBeTruthy();
    expect(screen.getByText("1/1 ready")).toBeTruthy();
    expect(screen.getByText("58")).toBeTruthy();
  });

  it("a failed cluster's reason is a notice, shaped", () => {
    render(<Overview c={{ ...dto, state: "Failed", reason: "connect timed out" }} />);
    expect(screen.getByText("▲ connect timed out")).toBeTruthy();
    expect(screen.getByText("▲ Failed")).toBeTruthy();
  });

  it("Refresh re-reads the shared summary", () => {
    seedAttention("homelab-nelli");
    render(<Overview c={dto} />);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(fetchAttention).toHaveBeenCalledWith("homelab-nelli");
  });
});

describe("Overview attention reads the shared summary", () => {
  it("while the first read runs it says so - no zeros", () => {
    useFleet.setState({ attention: { cluster: "homelab-nelli", loading: true, at: null, counts: useFleet.getState().attention.counts } });
    render(<Overview c={dto} />);
    expect(screen.getByText("Reading homelab-nelli…")).toBeTruthy();
    expect(screen.queryByText(/none need attention/)).toBeNull();
  });

  it("one row per area: faults shaped and toned, absent tools omitted, nothing green", () => {
    seedAttention("homelab-nelli", { workloads: obs(3), helm: 1, flux: 2, argo: "absent", events: 9 });
    const { container } = render(<Overview c={dto} />);
    expect(area("Workloads").textContent).toContain("▲ 3 need attention");
    expect(area("Helm releases").textContent).toContain("▲ 1 failed");
    expect(area("Flux").textContent).toContain("▲ 2 not ready");
    expect(area("Events").textContent).toContain("9 warning events");
    expect(screen.queryByRole("button", { name: /^Argo CD/ })).toBeNull();
    expect(container.innerHTML).not.toMatch(/color-text-success/);
  });

  it("unreadable and capped areas are named, and the quiet line is scoped to what was read", () => {
    seedAttention("homelab-nelli", { pods: obs(0, 5, { coverage: "partial" }), nodes: "unreadable" });
    render(<Overview c={dto} />);
    expect(area("Pods").textContent).toContain("unverified (capped)");
    expect(area("Nodes").textContent).toContain("could not be read");
    expect(screen.getByText("No attention found in the 2 areas read; 2 could not be established.")).toBeTruthy();
  });

  it("each area opens its lens with that lens's attention filter", () => {
    seedAttention("homelab-nelli", { workloads: obs(1) });
    useFleet.getState().openCluster("homelab-nelli");
    render(<Overview c={dto} />);
    fireEvent.click(area("Workloads"));
    expect(useFleet.getState().route).toMatchObject({ section: "workloads" });
    expect(useFleet.getState().workloads.needsAttention).toBe(true);
    fireEvent.click(area("Events"));
    expect(useFleet.getState().route).toMatchObject({ section: "events" });
    expect(useFleet.getState().events.warningsOnly).toBe(true);
  });

  it("namespaces come from the summary; unknown is a dash, never zero", () => {
    seedAttention("homelab-nelli", { namespaces: 7 });
    const { unmount } = render(<Overview c={dto} />);
    expect(screen.getByText("7")).toBeTruthy();
    unmount();
    seedAttention("homelab-nelli", { namespaces: null });
    render(<Overview c={dto} />);
    expect(screen.getByText("namespaces").nextElementSibling?.textContent).toBe("—");
  });

  it("another cluster's summary is never shown", () => {
    seedAttention("homelab-blue", { workloads: obs(4) });
    render(<Overview c={dto} />);
    expect(screen.getByText("Reading homelab-nelli…")).toBeTruthy();
  });
});

describe("Overview warning events", () => {
  it("shows warning events and opens the filtered Events page", async () => {
    vi.mocked(fetchEventsSnapshot).mockResolvedValueOnce({
      namespaces: ["apps"],
      events: [{ type: "Warning", reason: "BackOff", message: "Back-off restarting failed container", count: 3, namespace: "apps", kind: "Pod", name: "api-1", lastSeenUnix: 100, firstSeenUnix: 10 }],
    });
    useFleet.getState().openCluster("homelab-nelli");
    render(<Overview c={dto} />);
    await waitFor(() => expect(screen.getByText("BackOff")).toBeTruthy());
    fireEvent.click(screen.getByText("BackOff"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "events" });
    expect(state.events.warningsOnly).toBe(true);
    expect(state.events.search).toBe("api-1");
  });

  it("a failed events read is a failure, never 'no warning events'", async () => {
    vi.mocked(fetchEventsSnapshot).mockRejectedValueOnce(new Error("events is forbidden"));
    render(<Overview c={dto} />);
    expect(await screen.findByText(/Events could not be read: events is forbidden/)).toBeTruthy();
    expect(screen.queryByText(/No warning events/)).toBeNull();
  });

  it("an empty read is neutral and scoped", async () => {
    render(<Overview c={dto} />);
    const line = await screen.findByText("No warning events in the current read.");
    expect(line.className).toContain("t-secondary");
  });
});

describe("Overview metrics", () => {
  it("renders cpu/mem percents and the discovered monitoring line; used is never green", () => {
    setMetrics({ available: true, mode: "discovered-service", source: "monitoring/prometheus-operated:9090", warning: "", reason: "", cpuFraction: 0.38, memFraction: 0.92 });
    const { container } = render(<Overview c={blue} />);
    expect(screen.getByText("38%")).toBeTruthy();
    expect(screen.getByText("▲ 92%")).toBeTruthy();
    expect(screen.getByText(/monitoring: discovered · svc monitoring\/prometheus-operated:9090/)).toBeTruthy();
    expect(container.innerHTML).not.toMatch(/color-text-success/);
  });

  it("renders — for null fractions", () => {
    setMetrics({ available: true, mode: "explicit-endpoint", source: "https://h", warning: "", reason: "", cpuFraction: null, memFraction: null });
    render(<Overview c={blue} />);
    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(2);
  });

  it("renders the unavailable reason", () => {
    setMetrics({ available: false, mode: "unavailable", source: "", warning: "", reason: "multiple candidate Services found, set metrics.serviceRef", cpuFraction: null, memFraction: null });
    render(<Overview c={blue} />);
    expect(screen.getByText(/monitoring unavailable: multiple candidate Services found/)).toBeTruthy();
  });

  it("renders cpu/mem sparklines when the range series are available", async () => {
    mockGetClusterSparklines.mockResolvedValueOnce({ available: true, cpu: [{ t: 100, v: 0.4 }, { t: 160, v: 0.5 }], mem: [{ t: 100, v: 0.6 }, { t: 160, v: 0.7 }] });
    render(<Overview c={dto} />);
    const sparks = await screen.findAllByRole("img", { name: /metric sparkline/i });
    expect(sparks.length).toBe(2);
  });
});
