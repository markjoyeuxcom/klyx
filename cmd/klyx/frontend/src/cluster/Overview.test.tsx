import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { Overview } from "./Overview";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, MetricsDTO, OverviewSummary } from "../store/fleet";

// Default: sparklines unavailable so existing tests render without them.
import type { SparklinesDTO } from "../bridge/metrics";
const mockGetClusterSparklines = vi.fn<(cluster: string) => Promise<SparklinesDTO>>(() =>
  Promise.resolve({ available: false, message: "metrics unavailable", cpu: [], mem: [] }),
);
vi.mock("../bridge/metrics", () => ({
  getClusterMetrics: vi.fn(),
  getClusterSparklines: (cluster: string) => mockGetClusterSparklines(cluster),
}));
vi.mock("../bridge/overview", () => ({ fetchOverviewSummary: vi.fn().mockResolvedValue(undefined) }));
vi.mock("../bridge/fleetboard", () => ({ fetchFleetBoard: vi.fn().mockResolvedValue(undefined) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn() }));
import { fetchEventsSnapshot } from "../bridge/events";

const dto: ClusterDTO = {
  name: "homelab-nelli", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 58,
  version: "v1.36.1", gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "homelab", region: "", provider: "k3s", group: "", ageSeconds: 3,
};

const blue: ClusterDTO = { ...dto, name: "homelab-blue" };

function setMetrics(d: MetricsDTO | null) {
  useFleet.setState({ metrics: { cluster: "homelab-blue", dto: d, loading: false } });
}

function seedSummary(cluster: string, partial: Partial<OverviewSummary> = {}) {
  useFleet.setState({
    overviewSummary: {
      cluster,
      loading: false,
      unhealthyWorkloads: 0,
      podsNotReady: 0,
      warningEvents: 0,
      nodeProblems: 0,
      helmAvailable: false,
      failedReleases: null,
      namespaces: null,
      flux: null,
      fluxError: false,
      ...partial,
    },
  });
}

beforeEach(() => {
  vi.mocked(fetchEventsSnapshot).mockResolvedValue({ namespaces: [], events: [] });
});

describe("Overview", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("renders summary fields from the DTO", () => {
    const { getByText } = render(<Overview c={dto} />);
    expect(getByText("homelab-nelli")).toBeTruthy();
    expect(getByText("v1.36.1")).toBeTruthy();
    expect(getByText("1/1")).toBeTruthy();
    expect(getByText("58")).toBeTruthy();
    expect(getByText("homelab")).toBeTruthy();
  });
  it("shows the reason for a failed cluster", () => {
    const { getByText } = render(<Overview c={{ ...dto, state: "Failed", reason: "connect timed out" }} />);
    expect(getByText(/connect timed out/i)).toBeTruthy();
  });
});

describe("Overview critical events", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("shows warning events and opens the filtered Events page", async () => {
    vi.mocked(fetchEventsSnapshot).mockResolvedValueOnce({
      namespaces: ["apps"],
      events: [{
        type: "Warning",
        reason: "BackOff",
        message: "Back-off restarting failed container",
        count: 3,
        namespace: "apps",
        kind: "Pod",
        name: "api-1",
        lastSeenUnix: 100,
        firstSeenUnix: 10,
      }],
    });
    useFleet.getState().openCluster("homelab-nelli");
    const { getByText } = render(<Overview c={dto} />);
    await waitFor(() => expect(getByText("BackOff")).toBeTruthy());
    fireEvent.click(getByText("BackOff"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "events" });
    expect(state.events.warningsOnly).toBe(true);
    expect(state.events.search).toBe("api-1");
  });
});

describe("Overview metrics", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("renders cpu/mem percents and the discovered monitoring line", () => {
    setMetrics({ available: true, mode: "discovered-service", source: "monitoring/prometheus-operated:9090", warning: "", reason: "", cpuFraction: 0.38, memFraction: 0.61 });
    const { getByText } = render(<Overview c={blue} />);
    expect(getByText("38%")).toBeTruthy();
    expect(getByText("61%")).toBeTruthy();
    expect(getByText(/monitoring: discovered · svc monitoring\/prometheus-operated:9090/)).toBeTruthy();
  });

  it("renders — for null fractions", () => {
    setMetrics({ available: true, mode: "explicit-endpoint", source: "https://h", warning: "", reason: "", cpuFraction: null, memFraction: null });
    const { getAllByText } = render(<Overview c={blue} />);
    expect(getAllByText("—").length).toBeGreaterThanOrEqual(2);
  });

  it("renders the unavailable reason", () => {
    setMetrics({ available: false, mode: "unavailable", source: "", warning: "", reason: "multiple candidate Services found, set metrics.serviceRef", cpuFraction: null, memFraction: null });
    const { getByText } = render(<Overview c={blue} />);
    expect(getByText(/monitoring unavailable: multiple candidate Services found/)).toBeTruthy();
  });
});

// ---- Attention strip ---------------------------------------------------------

describe("Overview attention strip", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("shows — (muted) for all tiles while loading", () => {
    useFleet.setState({ overviewSummary: { cluster: "homelab-nelli", loading: true, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, helmAvailable: false, failedReleases: null, namespaces: null, flux: null, fluxError: false } });
    const { getAllByText } = render(<Overview c={dto} />);
    // During loading, all tiles show "—".
    const dashes = getAllByText("—");
    expect(dashes.length).toBeGreaterThanOrEqual(4); // 4 always-visible tiles + usage dashes
  });

  it("renders non-zero counts as numbers", () => {
    seedSummary("homelab-nelli", { unhealthyWorkloads: 3, podsNotReady: 1, warningEvents: 5, nodeProblems: 0 });
    const { getByText } = render(<Overview c={dto} />);
    expect(getByText("3")).toBeTruthy();
    expect(getByText("1")).toBeTruthy();
    expect(getByText("5")).toBeTruthy();
  });

  it("zero counts render as '0' (quiet, not hidden)", () => {
    seedSummary("homelab-nelli", { unhealthyWorkloads: 0, podsNotReady: 0, warningEvents: 0, nodeProblems: 0 });
    const { getAllByText } = render(<Overview c={dto} />);
    const zeros = getAllByText("0");
    expect(zeros.length).toBeGreaterThanOrEqual(4);
  });

  it("navigates to workloads and sets needs-attention on tile click", () => {
    useFleet.getState().openCluster("homelab-nelli");
    seedSummary("homelab-nelli", { unhealthyWorkloads: 2 });
    const { getByText } = render(<Overview c={dto} />);
    fireEvent.click(getByText("unhealthy workloads"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(state.workloads.needsAttention).toBe(true);
  });

  it("navigates to pods and sets needs-attention on tile click", () => {
    useFleet.getState().openCluster("homelab-nelli");
    seedSummary("homelab-nelli", { podsNotReady: 1 });
    const { getByText } = render(<Overview c={dto} />);
    fireEvent.click(getByText("pods not ready"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "pods" });
    expect(state.pods.needsAttention).toBe(true);
  });

  it("navigates to events and enables warnings-only on tile click", () => {
    useFleet.getState().openCluster("homelab-nelli");
    seedSummary("homelab-nelli", { warningEvents: 7 });
    const { getByText } = render(<Overview c={dto} />);
    fireEvent.click(getByText("warning events"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "events" });
    expect(state.events.warningsOnly).toBe(true);
  });

  it("helm tile is hidden when helmAvailable=false", () => {
    seedSummary("homelab-nelli", { helmAvailable: false, failedReleases: null });
    const { queryByText } = render(<Overview c={dto} />);
    expect(queryByText("failed releases")).toBeNull();
  });

  it("helm tile is visible when helmAvailable=true", () => {
    seedSummary("homelab-nelli", { helmAvailable: true, failedReleases: 0 });
    const { getByText } = render(<Overview c={dto} />);
    expect(getByText("failed releases")).toBeTruthy();
  });

  it("shows — with title='failed to load' when a tile has null count after load", () => {
    seedSummary("homelab-nelli", { unhealthyWorkloads: null });
    const { getByTitle } = render(<Overview c={dto} />);
    expect(getByTitle("failed to load")).toBeTruthy();
  });
});


// ---- Flux attention strip tile -------------------------------------------------

describe("Overview flux tile", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("flux tile is hidden when flux is null", () => {
    seedSummary("homelab-nelli", { flux: null });
    const { queryByText } = render(<Overview c={dto} />);
    expect(queryByText("flux not ready")).toBeNull();
  });

  it("flux tile is visible when flux.present=true", () => {
    seedSummary("homelab-nelli", { flux: { present: true, notReady: 0, suspended: 0 } });
    const { getByText } = render(<Overview c={dto} />);
    expect(getByText("flux not ready")).toBeTruthy();
  });

  it("flux tile renders notReady count when >0", () => {
    seedSummary("homelab-nelli", { flux: { present: true, notReady: 3, suspended: 0 } });
    const { getByText } = render(<Overview c={dto} />);
    expect(getByText("3")).toBeTruthy();
  });

  it("flux tile click navigates to gitops section", () => {
    useFleet.getState().openCluster("homelab-nelli");
    seedSummary("homelab-nelli", { flux: { present: true, notReady: 1, suspended: 0 } });
    const { getByText } = render(<Overview c={dto} />);
    fireEvent.click(getByText("flux not ready"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "gitops" });
  });

  it("flux tile shows suspended count in title when >0", () => {
    seedSummary("homelab-nelli", { flux: { present: true, notReady: 1, suspended: 2 } });
    const { getByTitle } = render(<Overview c={dto} />);
    expect(getByTitle("2 suspended")).toBeTruthy();
  });

  it("flux tile has no title when suspended is 0", () => {
    seedSummary("homelab-nelli", { flux: { present: true, notReady: 0, suspended: 0 } });
    const { queryByTitle } = render(<Overview c={dto} />);
    expect(queryByTitle(/suspended/)).toBeNull();
  });
});

// ---- Capacity namespaces row ---------------------------------------------------

describe("Overview capacity namespace row", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("shows namespace count in Capacity when summary is loaded", () => {
    seedSummary("homelab-nelli", { namespaces: 7 });
    const { getByText } = render(<Overview c={dto} />);
    expect(getByText("7")).toBeTruthy();
    expect(getByText("namespaces")).toBeTruthy();
  });

  it("omits namespace row when namespaces is null (not yet loaded)", () => {
    seedSummary("homelab-nelli", { namespaces: null });
    const { queryByText } = render(<Overview c={dto} />);
    expect(queryByText("namespaces")).toBeNull();
  });
});

describe("Overview sparklines", () => {
  beforeEach(() => {
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    useFleet.getState().clearOverviewSummary();
  });

  it("renders cpu/mem sparklines when the range series are available", async () => {
    mockGetClusterSparklines.mockResolvedValueOnce({
      available: true,
      cpu: [{ t: 100, v: 0.4 }, { t: 160, v: 0.5 }],
      mem: [{ t: 100, v: 0.6 }, { t: 160, v: 0.7 }],
    });
    const { findAllByRole } = render(<Overview c={dto} />);
    const sparks = await findAllByRole("img", { name: /metric sparkline/i });
    expect(sparks.length).toBe(2);
  });

  it("renders no sparklines when unavailable (default mock)", async () => {
    const { queryAllByRole, findByText } = render(<Overview c={dto} />);
    await findByText("cpu used"); // settle
    expect(queryAllByRole("img", { name: /metric sparkline/i }).length).toBe(0);
  });
});
