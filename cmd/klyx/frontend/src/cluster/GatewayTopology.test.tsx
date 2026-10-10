import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { GatewayRef, RouteNodeDTO, TopologyDTO } from "../store/fleet";

vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), listGateways: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}) }));
import { GatewayTopology } from "./GatewayTopology";
import { routeHealth, sortWarnings } from "./gatewayModel";

const gateway: GatewayRef = { namespace: "infra", name: "eg" };

function route(namespace: string, name: string, over: Partial<RouteNodeDTO> = {}): RouteNodeDTO {
  const svc = `${name}-svc`;
  return {
    namespace, name, hostnames: [`${name}.example.com`], matches: [{ pathType: "PathPrefix", pathValue: `/${name}`, method: "" }], parentSectionName: "https",
    accepted: true, resolvedRefs: true, backends: [{ kind: "Service", name: svc, namespace, port: 80, weight: 0 }],
    services: [{ namespace, name: svc, type: "ClusterIP", port: 80, resolved: true, global: false, meshClusters: [], meshUnconfirmed: false, policies: [], cnps: [] }],
    pods: { ready: 2, total: 2, unknown: false }, policies: [], ...over,
  };
}

const topo = (routes: RouteNodeDTO[], over: Partial<TopologyDTO> = {}): TopologyDTO => ({
  gateway: { namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true, addresses: [{ type: "IPAddress", value: "192.0.2.10" }],
    listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443, certificateRefs: [{ group: "", kind: "Secret", namespace: "infra", name: "eg-tls" }] }], policies: [] },
  routes, ...over,
});

function seed(t: TopologyDTO | null, over: Partial<ReturnType<typeof useFleet.getState>["network"]> = {}) {
  useFleet.setState({
    route: { name: "cluster", cluster: "x", section: "gatewayApi", gateway },
    network: { cluster: "x", served: true, gateways: [], listLoading: false, listError: null, selected: gateway, topology: t, topologyLoading: false, topologyError: null, topologyAt: t ? Date.now() : null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false, ...over },
    certmanager: { cluster: null, certificates: [], issuers: [], loading: false, selectedKey: null, chain: null, chainLoading: false },
  });
}

beforeEach(() => vi.clearAllMocks());

describe("routeHealth", () => {
  it("zero ready of several pods is a fault, not a partial", () => {
    expect(routeHealth(route("a", "r", { pods: { ready: 0, total: 3, unknown: false } }))).toEqual({ kind: "fault", label: "no ready endpoints" });
    expect(routeHealth(route("a", "r", { pods: { ready: 1, total: 3, unknown: false } }))).toEqual({ kind: "degraded", label: "1/3 ready" });
    expect(routeHealth(route("a", "r", { pods: { ready: 0, total: 0, unknown: true } }))).toEqual({ kind: "unknown", label: "endpoints unverified" });
  });
});

describe("sortWarnings", () => {
  it("splits capability facts, per-route notes and evidence gaps", () => {
    const w = sortWarnings([
      "SecurityPolicy CRD not installed",
      "route apps/share has 2 backends; the lane shows the primary",
      "route apps/cart: backend Service apps/cart not found",
      "could not list BackendTrafficPolicy: forbidden",
      "CiliumNetworkPolicy apps/x: matchExpressions-only selector not evaluated",
    ]);
    expect(w.absentKinds).toEqual(["SecurityPolicy"]);
    expect(w.byRoute.get("apps/share")).toEqual(["has 2 backends; the lane shows the primary"]);
    expect(w.byRoute.get("apps/cart")).toEqual(["backend Service apps/cart not found"]);
    expect(w.gaps).toEqual(["could not list BackendTrafficPolicy: forbidden", "CiliumNetworkPolicy apps/x: matchExpressions-only selector not evaluated"]);
  });
});

describe("GatewayTopology (redesign step 6, canvas-detail)", () => {
  it("absent policy kinds are one neutral fact on the gateway node; route notes go to that route; gaps stay in the notice", () => {
    seed(topo([route("apps", "share")], { warnings: ["SecurityPolicy CRD not installed", "BackendTLSPolicy CRD not installed", "route apps/share has 2 backends; the lane shows the primary", "could not list ClientTrafficPolicy: forbidden"] }));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(within(screen.getByTestId("gateway-strip")).getByText(/SecurityPolicy, BackendTLSPolicy - no such policies can apply here/)).toBeTruthy();
    expect(screen.queryByText("SecurityPolicy CRD not installed")).toBeNull();
    expect(screen.getByText(/could not list ClientTrafficPolicy: forbidden/)).toBeTruthy();
    expect(screen.queryByText(/the lane shows the primary/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /HTTPRoute apps\/share/ }));
    expect(within(screen.getByTestId("route-panel")).getByText("Has 2 backends; the lane shows the primary.")).toBeTruthy();
  });

  it("draws the gateway node and one lane per route, broken first, and auto-selects the first broken lane", () => {
    seed(topo([route("apps", "share"), route("apps", "cart", { resolvedRefs: false })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    const strip = screen.getByTestId("gateway-strip");
    expect(within(strip).getByText("192.0.2.10")).toBeTruthy();
    const lanes = screen.getAllByRole("listitem");
    expect(lanes[0].textContent).toContain("cart");
    expect(lanes[1].textContent).toContain("share");
    expect(useFleet.getState().network.selectedRoute).toBe("apps/cart");
    const panel = screen.getByTestId("route-panel");
    expect(within(panel).getByText(/ResolvedRefs is not True/)).toBeTruthy();
    expect(screen.getByText("▲ 1 broken")).toBeTruthy();
  });

  it("with nothing broken nothing is auto-selected; a click inspects and opens objects", () => {
    seed(topo([route("apps", "share")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.queryByTestId("route-panel")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /HTTPRoute apps\/share/ }));
    const panel = screen.getByTestId("route-panel");
    expect(within(panel).getByText("Attachment, references, backend Service and endpoints are usable.")).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: "Open Service" }));
    const s = useFleet.getState();
    expect(s.route.name === "cluster" && s.route.resource?.kind).toBe("Service");
  });

  it("a failed first read is unreadable with the error and Retry, never 'no routes'", () => {
    seed(null, { topologyError: "gateways is forbidden" });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("Could not read this Gateway's topology.")).toBeTruthy();
    expect(screen.getByText("gateways is forbidden")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });

  it("a failed refresh keeps the lanes and says they are the last good read", () => {
    seed(topo([route("apps", "share")]), { topologyError: "timeout" });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText(/share\.example\.com/)).toBeTruthy();
    expect(screen.getByText(/Refresh failed - showing the read from/)).toBeTruthy();
  });

  it("a partial read is named in the notice and the summary", () => {
    seed(topo([route("apps", "share")], { error: "httproutes in team-b: forbidden" }), { selectedRoute: "apps/gone" });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText(/httproutes in team-b: forbidden/)).toBeTruthy();
    expect(screen.getByText("(partial read)", { exact: false })).toBeTruthy();
  });

  it("a route gone from a complete re-read is named as gone, with its last evidence", () => {
    seed(topo([route("apps", "share"), route("apps", "cart", { accepted: false })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(useFleet.getState().network.selectedRoute).toBe("apps/cart");
    act(() => { useFleet.setState((s) => ({ network: { ...s.network, topology: topo([route("apps", "share")]) } })); });
    expect(within(screen.getByTestId("route-panel")).getByText("No longer attached.")).toBeTruthy();
  });

  it("a palette deep link reveals its route once by clearing filters; a later filter edit closes it", () => {
    seed(topo([route("apps", "share"), route("monitoring", "grafana", { pods: { ready: 1, total: 2, unknown: false } })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(screen.getByRole("button", { name: /^Issues/ }));
    expect(screen.queryByRole("button", { name: /HTTPRoute apps\/share/ })).toBeNull();
    act(() => { useFleet.getState().selectRoute("apps/share"); });
    expect(screen.getByRole("button", { name: /HTTPRoute apps\/share/ })).toBeTruthy();
    expect(screen.getByTestId("route-panel")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Issues/ }));
    expect(useFleet.getState().network.selectedRoute).toBeNull();
  });

  it("another cluster's topology is never drawn under this one", () => {
    seed(topo([route("apps", "foreign")]), { cluster: "other" });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.queryByText(/foreign/)).toBeNull();
  });

  it("a Gateway that is not accepted is a notice over the canvas", () => {
    const t = topo([route("apps", "share")]);
    t.gateway.accepted = false; t.gateway.programmed = false;
    seed(t);
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("▲ Gateway not accepted.")).toBeTruthy();
  });

  it("a healthy route's error rate stays neutral", () => {
    seed(topo([route("apps", "share")]), { routeMetrics: { "apps/share": { rps: 3, p50: 12, p99: 80, errRate: 0 } }, routeMetricsStatus: { available: true, message: "", updatedAt: "now" } });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText(/^5xx 0\.0%$/).style.color).toBe("var(--color-text-secondary)");
  });

  it("no metrics source is said on the lane and in the footer, never drawn as dashes", () => {
    seed(topo([route("apps", "share")]), { routeMetricsStatus: { available: false, message: "Envoy Gateway not detected", updatedAt: "" } });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("no metrics source")).toBeTruthy();
    expect(screen.getByText(/route traffic unavailable: Envoy Gateway not detected/)).toBeTruthy();
  });

  it("a failed metrics refresh keeps the last numbers, labelled", () => {
    seed(topo([route("apps", "share")]), { routeMetrics: { "apps/share": { rps: 3, p50: 12, p99: 80, errRate: 0 } }, routeMetricsStatus: { available: false, message: "timeout", updatedAt: "then" }, routeMetricsStale: true });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText(/3 rps/)).toBeTruthy();
    expect(screen.getByText(/route traffic: last good read - the latest failed \(timeout\)/)).toBeTruthy();
  });

  it("Back to Gateways closes the topology", () => {
    seed(topo([route("apps", "share")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(screen.getByRole("button", { name: "Back to Gateways" }));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.gateway).toBeUndefined();
  });
});
