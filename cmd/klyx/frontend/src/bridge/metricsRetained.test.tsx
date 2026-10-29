import { beforeEach, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { useFleet, type ClusterDTO, type MetricsDTO } from "../store/fleet";
import { Overview } from "../cluster/Overview";
import { resetHealthStore } from "../store/health";

// Settings S3b §3.4 (Codex's S3b-2c review, R4): a readout kept after its
// refresh failed is said as an earlier read, and only the latest request's
// failure marks it.

const call = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({ MetricsService: { GetClusterMetrics: (...a: unknown[]) => call(...a), GetClusterSparklines: vi.fn().mockResolvedValue({ available: false, cpu: [], mem: [] }) } }));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../bridge/helm", () => ({ openHelmRelease: vi.fn(async () => {}) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn().mockResolvedValue({ namespaces: [], events: [] }) }));
vi.mock("../bridge/crd", () => ({ listInstancePage: vi.fn().mockResolvedValue({ items: [], nextToken: "" }) }));
import { getClusterMetrics } from "./metrics";

const c: ClusterDTO = { name: "dev", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.36.1", gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 3 };
const d: MetricsDTO = { available: true, mode: "subscribed", source: "s", warning: "", reason: "", cpuFraction: 0.5, memFraction: 0.6, sentence: "from source-A", lineage: "A" };

beforeEach(() => {
  call.mockReset();
  useFleet.setState({ clusters: [c], metrics: { cluster: "dev", dto: d, loading: false } });
  resetHealthStore();
  vi.spyOn(console, "error").mockImplementation(() => {});
});

it("qualifies retained instant numbers after a lost refresh, until a read answers", async () => {
  call.mockResolvedValueOnce(d);
  render(<Overview c={c} />);
  await act(async () => {});
  call.mockRejectedValueOnce(new Error("answer was lost"));
  await act(async () => { await getClusterMetrics("dev", true); });
  expect(screen.getByText("50%")).toBeTruthy();
  expect(screen.getByTestId("monitoring-source").textContent).toBe("Earlier read from source-A, not current");
  call.mockResolvedValueOnce(d);
  await act(async () => { await getClusterMetrics("dev", true); });
  expect(screen.getByTestId("monitoring-source").textContent).toBe("Read from source-A");
});

it("never marks a newer answer by an older request's failure", async () => {
  let fail!: (e: Error) => void;
  call.mockReturnValueOnce(new Promise((_, reject) => { fail = reject; }));
  const older = getClusterMetrics("dev", true);
  call.mockResolvedValueOnce({ ...d, cpuFraction: 0.7 });
  await getClusterMetrics("dev", true);
  fail(new Error("late"));
  await older;
  expect(useFleet.getState().metrics.stale).toBe(false);
  expect(useFleet.getState().metrics.dto?.cpuFraction).toBe(0.7);
});

it("a visit to another cluster starts unmarked", () => {
  useFleet.setState({ metrics: { cluster: "dev", dto: d, loading: false, stale: true } });
  useFleet.getState().setMetricsLoading("prd");
  expect(useFleet.getState().metrics).toEqual({ cluster: "prd", dto: null, stale: false, loading: true });
});
