import { beforeEach, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { Overview } from "./Overview";
import { useFleet, type ClusterDTO, type MetricsDTO } from "../store/fleet";
import { resetHealthStore } from "../store/health";
import { getClusterSparklines } from "../bridge/metrics";

// Settings S3b §3.2, §3.4 on the Overview (Codex's S3b-2c review, R3): the
// 30-minute charts keep their own read's source, and say their own no data
// under its scope, rather than borrow the instant readout's.

vi.mock("../bridge/metrics", () => ({
  getClusterMetrics: vi.fn(),
  getClusterSparklines: vi.fn(),
}));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../bridge/helm", () => ({ openHelmRelease: vi.fn(async () => {}) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn().mockResolvedValue({ namespaces: [], events: [] }) }));
vi.mock("../bridge/crd", () => ({ listInstancePage: vi.fn().mockResolvedValue({ items: [], nextToken: "" }) }));

const c: ClusterDTO = { name: "dev", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.36.1", gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 3 };
const readout = (key: string): MetricsDTO => ({ available: true, mode: "subscribed", source: key, warning: "", reason: "", cpuFraction: 0.5, memFraction: 0.6, sentence: "from source-" + key, lineage: key });
const charts = { available: true, cpu: [{ t: 1, v: 0.1 }, { t: 61, v: 0.2 }], mem: [{ t: 1, v: 0.3 }, { t: 61, v: 0.4 }], sentence: "from source-A", lineage: "A" };

beforeEach(() => {
  vi.mocked(getClusterSparklines).mockResolvedValue(charts);
  useFleet.setState({ clusters: [c], metrics: { cluster: "dev", dto: readout("A"), loading: false } });
  resetHealthStore();
});

it("says nothing more where the charts and the readout read one source", async () => {
  render(<Overview c={c} />);
  await waitFor(() => expect(screen.getAllByRole("img", { name: "metric sparkline" })).toHaveLength(2));
  expect(screen.queryByTestId("sparkline-source")).toBeNull();
});

it("says that the range read found no data under its scope", async () => {
  vi.mocked(getClusterSparklines).mockResolvedValueOnce({ ...charts, cpu: [], mem: [], noData: "no data for dev under cluster=dev" });
  render(<Overview c={c} />);
  await act(async () => {});
  expect(screen.getByTestId("sparkline-source").textContent).toBe("30m charts: no data for dev under cluster=dev");
});

it("keeps a sparkline's own source after the instant readout moves", async () => {
  render(<Overview c={c} />);
  await waitFor(() => expect(screen.getAllByRole("img", { name: "metric sparkline" })).toHaveLength(2));
  act(() => useFleet.getState().setMetrics("dev", readout("B")));
  expect(screen.getByTestId("monitoring-source").textContent).toContain("from source-B");
  expect(screen.getByTestId("sparkline-source").textContent).toBe("30m charts: Earlier read from source-A, not current");
});
