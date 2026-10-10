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
import { getGatewayTopology, getRouteMetrics, listGateways } from "./gateway";

const eg = { namespace: "envoy-gateway-system", name: "eg" };
const t = (marker: string): TopologyDTO => ({
  gateway: { ...eg, className: marker, listeners: [], accepted: true, programmed: true, policies: [] },
  routes: [],
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
});
