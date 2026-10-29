import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";
import type { MetricsDTO, WorkloadMetricsResultDTO } from "../store/fleet";

// Settings S3b §3.2: an earlier read's late reply never replaces a newer one.
type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): Deferred<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r; }); return { promise, resolve }; };
const clusterMetrics = vi.fn();
const workloadMetrics = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  MetricsService: { GetClusterMetrics: (...a: unknown[]) => clusterMetrics(...a) },
  WorkloadsService: { GetWorkloadMetrics: (...a: unknown[]) => workloadMetrics(...a) },
}));
import { getClusterMetrics } from "./metrics";
import { getWorkloadMetrics } from "./workload-metrics";

const readout = (lineage: string): MetricsDTO => ({ available: true, mode: "subscribed", source: "s", warning: "", reason: "", cpuFraction: 0.1, memFraction: 0.2, sentence: "from " + lineage, lineage });
const usage = (lineage: string): WorkloadMetricsResultDTO => ({ status: { available: true, message: "", updatedAt: "t", sentence: "from " + lineage, lineage }, usage: {} });

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.getState().clearMetrics();
  useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: "dev", namespace: "", usage: {}, metricsAvailable: false, metricsStatus: null, metricsStale: false } }));
});

describe("late metrics replies", () => {
  it("the cluster readout keeps the newer read", async () => {
    const old = deferred<MetricsDTO>();
    clusterMetrics.mockReturnValueOnce(old.promise).mockResolvedValueOnce(readout("new"));
    const first = getClusterMetrics("dev", false);
    await getClusterMetrics("dev", true);
    old.resolve(readout("old"));
    await first;
    expect(useFleet.getState().metrics.dto?.lineage).toBe("new");
    expect(useFleet.getState().metrics.loading).toBe(false);
  });
  it("workload usage keeps the newer read", async () => {
    const old = deferred<WorkloadMetricsResultDTO>();
    workloadMetrics.mockReturnValueOnce(old.promise).mockResolvedValueOnce(usage("new"));
    const first = getWorkloadMetrics("dev", "");
    await getWorkloadMetrics("dev", "");
    old.resolve(usage("old"));
    await first;
    expect(useFleet.getState().workloads.metricsStatus?.lineage).toBe("new");
  });
  it("usage kept after a failed read keeps its own sentence, stale", () => {
    const f = useFleet.getState();
    f.setWorkloadUsage("dev", "", usage("first"));
    f.setWorkloadUsage("dev", "", { status: { available: false, message: "metrics unavailable: the read was dropped", updatedAt: "" }, usage: {} });
    const w = useFleet.getState().workloads;
    expect(w.metricsStale).toBe(true);
    expect(w.metricsStatus?.sentence).toBe("from first");
    expect(w.metricsStatus?.lineage).toBe("first");
    expect(w.metricsStatus?.message).toBe("metrics unavailable: the read was dropped");
  });
  it("route numbers kept after a failed read keep their own sentence", () => {
    const f = useFleet.getState();
    f.setRouteMetrics({ status: { available: true, message: "", updatedAt: "t", sentence: "from first", lineage: "first" }, routes: { "ns/r": { rps: 1, p50: null, p99: null, errRate: null } } });
    f.setRouteMetrics({ status: { available: false, message: "failed", updatedAt: "" }, routes: {} });
    const n = useFleet.getState().network;
    expect(n.routeMetricsStale).toBe(true);
    expect(n.routeMetricsStatus?.sentence).toBe("from first");
  });
});
