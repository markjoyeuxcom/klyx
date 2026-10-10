import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";
import type { TopologyDTO } from "../store/fleet";

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void };
const deferred = <T,>(): Deferred<T> => { let resolve!: (v: T) => void; let reject!: (e: unknown) => void; const promise = new Promise<T>((r, j) => { resolve = r; reject = j; }); return { promise, resolve, reject }; };
const list = vi.fn();
const topo = vi.fn();
const metrics = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  GatewayService: {
    ListGateways: (...a: unknown[]) => list(...a),
    GetGatewayTopology: (...a: unknown[]) => topo(...a),
    GetRouteMetrics: (...a: unknown[]) => metrics(...a),
  },
}));
import { abandonRouteMetrics, getGatewayTopology, getRouteMetrics, listGateways } from "./gateway";

const eg = { namespace: "envoy-gateway-system", name: "eg" };
const t = (marker: string): TopologyDTO => ({
  gateway: { ...eg, className: marker, listeners: [], accepted: true, programmed: true, policies: [] },
  routes: [], routesComplete: true,
});

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.setState((s) => ({ route: { name: "cluster", cluster: "A", section: "gatewayApi" }, network: { ...s.network, cluster: null, selected: null, topology: null, topologyLoading: false, topologyError: null, topologyAt: null, gateways: [], served: false, listError: null } }));
});

describe("Gateway bridge (redesign step 6)", () => {
  it("a late topology from cluster A never lands on cluster B's same-named Gateway", async () => {
    const a = deferred<TopologyDTO>();
    topo.mockReturnValueOnce(a.promise).mockResolvedValueOnce(t("B"));
    const first = getGatewayTopology("A", eg);
    await getGatewayTopology("B", eg);
    a.resolve(t("A"));
    await first;
    const n = useFleet.getState().network;
    expect(n.cluster).toBe("B");
    expect(n.topology?.gateway.className).toBe("B");
  });

  it("a failed refresh keeps the last good topology and records the error", async () => {
    topo.mockResolvedValueOnce(t("one"));
    await getGatewayTopology("A", eg);
    const at = useFleet.getState().network.topologyAt;
    expect(at).not.toBeNull();
    topo.mockRejectedValueOnce(new Error("gateways.gateway.networking.k8s.io is forbidden"));
    await expect(getGatewayTopology("A", eg)).rejects.toThrow();
    const n = useFleet.getState().network;
    expect(n.topology?.gateway.className).toBe("one");
    expect(n.topologyError).toMatch(/forbidden/);
    expect(n.topologyLoading).toBe(false);
    expect(n.topologyAt).toBe(at);
  });

  it("route metrics for another cluster's same-named Gateway are dropped", async () => {
    topo.mockResolvedValueOnce(t("B"));
    await getGatewayTopology("B", eg);
    metrics.mockResolvedValueOnce({ status: { available: true, message: "", updatedAt: "now" }, routes: { "apps/x": { rps: 9, p50: 1, p99: 2, errRate: 0 } } });
    await getRouteMetrics("A", eg.namespace, eg.name, ["apps/x"]);
    expect(useFleet.getState().network.routeMetrics).toEqual({});
  });

  it("a failed list call is an error, never an empty list or a stuck loader", async () => {
    list.mockRejectedValueOnce(new Error("connection refused"));
    await expect(listGateways("A")).rejects.toThrow();
    const n = useFleet.getState().network;
    expect(n.listLoading).toBe(false);
    expect(n.listError).toMatch(/connection refused/);
  });

  it("a superseded list read is dropped", async () => {
    const a = deferred<unknown>();
    list.mockReturnValueOnce(a.promise).mockResolvedValueOnce({ gatewayAPIServed: true, gateways: [{ ...eg, className: "new", accepted: true, programmed: true }] });
    const first = listGateways("A");
    await listGateways("A");
    a.resolve({ gatewayAPIServed: true, gateways: [] });
    await first;
    expect(useFleet.getState().network.gateways.map((g) => g.className)).toEqual(["new"]);
  });

  it("re-opening the Gateway already shown keeps its topology (the fetch would not re-run)", async () => {
    topo.mockResolvedValueOnce(t("kept"));
    await getGatewayTopology("A", eg);
    useFleet.setState((s) => ({ network: { ...s.network, cluster: "A" } }));
    useFleet.getState().openGateway(eg.namespace, eg.name);
    useFleet.getState().selectRoute("apps/share");
    const n = useFleet.getState().network;
    expect(n.topology?.gateway.className).toBe("kept");
    expect(n.topologyLoading).toBe(false);
    expect(n.selectedRoute).toBe("apps/share");
  });

  it("an in-band DTO error is a failed read: the last good topology stays, its receipt time unchanged", async () => {
    topo.mockResolvedValueOnce(t("good"));
    await getGatewayTopology("A", eg);
    const at = useFleet.getState().network.topologyAt;
    topo.mockResolvedValueOnce({ error: "get gateway infra/eg: forbidden" });
    await getGatewayTopology("A", eg);
    const n = useFleet.getState().network;
    expect(n.topology?.gateway.className).toBe("good");
    expect(n.topologyError).toMatch(/forbidden/);
    expect(n.topologyAt).toBe(at);
  });

  it("an in-band list error keeps the last good Gateways", async () => {
    list.mockResolvedValueOnce({ gatewayAPIServed: true, gateways: [{ ...eg, className: "kept", accepted: true, programmed: true }] });
    await listGateways("A");
    list.mockResolvedValueOnce({ gatewayAPIServed: true, gateways: [], error: "list gateways: forbidden" });
    await listGateways("A");
    const n = useFleet.getState().network;
    expect(n.gateways.map((g) => g.className)).toEqual(["kept"]);
    expect(n.listError).toMatch(/forbidden/);
  });

  it("route metrics: the latest poll wins over an older one resolving late", async () => {
    topo.mockResolvedValueOnce(t("A"));
    await getGatewayTopology("A", eg);
    const old = deferred<unknown>();
    metrics.mockReturnValueOnce(old.promise).mockResolvedValueOnce({ status: { available: true, message: "", updatedAt: "new" }, routes: { "apps/x": { rps: 2, p50: 1, p99: 1, errRate: 0 } } });
    const first = getRouteMetrics("A", eg.namespace, eg.name, ["apps/x"]);
    await getRouteMetrics("A", eg.namespace, eg.name, ["apps/x"]);
    old.resolve({ status: { available: true, message: "", updatedAt: "old" }, routes: { "apps/x": { rps: 99, p50: 1, p99: 1, errRate: 0 } } });
    await first;
    expect(useFleet.getState().network.routeMetrics["apps/x"].rps).toBe(2);
  });

  it("route metrics: leaving and returning to the same Gateway drops the earlier poll", async () => {
    topo.mockResolvedValueOnce(t("A"));
    await getGatewayTopology("A", eg);
    const old = deferred<unknown>();
    metrics.mockReturnValueOnce(old.promise);
    const first = getRouteMetrics("A", eg.namespace, eg.name, ["apps/x"]);
    abandonRouteMetrics(); // left the topology; returning re-reads it
    old.resolve({ status: { available: true, message: "", updatedAt: "old" }, routes: { "apps/x": { rps: 99, p50: 1, p99: 1, errRate: 0 } } });
    await first;
    expect(useFleet.getState().network.routeMetrics).toEqual({});
  });

  it("route metrics: a rejected poll after good numbers keeps them, marked stale", async () => {
    topo.mockResolvedValueOnce(t("A"));
    await getGatewayTopology("A", eg);
    metrics.mockResolvedValueOnce({ status: { available: true, message: "", updatedAt: "t1" }, routes: { "apps/x": { rps: 2, p50: 1, p99: 1, errRate: 0 } } });
    await getRouteMetrics("A", eg.namespace, eg.name, ["apps/x"]);
    metrics.mockRejectedValueOnce(new Error("prometheus timeout"));
    await getRouteMetrics("A", eg.namespace, eg.name, ["apps/x"]);
    const n = useFleet.getState().network;
    expect(n.routeMetrics["apps/x"].rps).toBe(2);
    expect(n.routeMetricsStale).toBe(true);
    expect(n.routeMetricsStatus?.message).toMatch(/prometheus timeout/);
  });
});
