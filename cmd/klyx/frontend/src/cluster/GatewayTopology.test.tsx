import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, CondDTO, GatewayRef, RouteNodeDTO, TopologyDTO } from "../store/fleet";

vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), listGateways: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}), abandonRouteMetrics: vi.fn() }));
import { GatewayTopology } from "./GatewayTopology";
import { certStatus, routeHealth, routeIssues, sortWarnings } from "./gatewayModel";

const gateway: GatewayRef = { namespace: "infra", name: "eg" };

const T = (reason = "Accepted"): CondDTO => ({ status: "True", reason, message: "", observedGeneration: 1 });
const F = (reason: string, observedGeneration = 1): CondDTO => ({ status: "False", reason, message: "", observedGeneration });

function route(namespace: string, name: string, over: Partial<RouteNodeDTO> = {}, ep: { ready: number; total: number; nonPod?: number } = { ready: 2, total: 2 }): RouteNodeDTO {
  const svc = `${name}-svc`;
  return {
    namespace, name, hostnames: [`${name}.example.com`], matches: [{ pathType: "PathPrefix", pathValue: `/${name}`, method: "" }], parentSectionName: "https",
    accepted: true, resolvedRefs: true, generation: 1, parentFound: true, acceptedCond: T(), resolvedRefsCond: T("ResolvedRefs"),
    backends: [{ kind: "Service", name: svc, namespace, port: 80, weight: 0, state: "resolved", endpoints: { ready: ep.ready, total: ep.total, nonPod: ep.nonPod ?? 0 } }],
    services: [{ namespace, name: svc, type: "ClusterIP", port: 80, resolved: true, global: false, meshClusters: [], meshUnconfirmed: false, policies: [], cnps: [] }],
    pods: { ready: ep.ready, total: ep.total, unknown: false }, policies: [], ...over,
  };
}

const topo = (routes: RouteNodeDTO[], over: Partial<TopologyDTO> = {}): TopologyDTO => ({
  gateway: { namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true, generation: 1, acceptedCond: T(), programmedCond: T("Programmed"), addresses: [{ type: "IPAddress", value: "192.0.2.10" }],
    listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443, certificateRefs: [{ group: "", kind: "Secret", namespace: "infra", name: "eg-tls" }] }], policies: [] },
  routes, routesComplete: true, ...over,
});

function seed(t: TopologyDTO | null, over: Partial<ReturnType<typeof useFleet.getState>["network"]> = {}) {
  useFleet.setState({
    route: { name: "cluster", cluster: "x", section: "gatewayApi", gateway },
    network: { cluster: "x", served: true, gateways: [], listLoading: false, listError: null, selected: gateway, topology: t, topologyLoading: false, topologyError: null, topologyAt: t ? Date.now() : null, routesAt: t?.routesComplete ? Date.now() : null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false, ...over },
    certmanager: { cluster: null, certificates: [], issuers: [], loading: false, selectedKey: null, chain: null, chainLoading: false },
  });
}

beforeEach(() => vi.clearAllMocks());

describe("routeHealth (evidence, Codex review)", () => {
  const unknown: CondDTO = { status: "Unknown", reason: "Pending", message: "", observedGeneration: 1 };
  it("endpoints: zero ready is a fault, partial is degraded, unreadable is unknown", () => {
    expect(routeHealth(route("a", "r", {}, { ready: 0, total: 3 }))).toEqual({ kind: "fault", label: "no ready endpoints" });
    expect(routeHealth(route("a", "r", {}, { ready: 1, total: 3 }))).toEqual({ kind: "degraded", label: "1/3 endpoints ready" });
    const r = route("a", "r");
    r.backends[0].endpoints = { ready: 0, total: 0, nonPod: 0, error: "forbidden" };
    expect(routeHealth(r)).toEqual({ kind: "unknown", label: "endpoints unreadable" });
  });
  it("absent, Unknown, explicit False and stale False are four different claims", () => {
    expect(routeHealth(route("a", "r", { parentFound: false, acceptedCond: undefined }))).toEqual({ kind: "unknown", label: "no status for this Gateway" });
    expect(routeHealth(route("a", "r", { acceptedCond: unknown }))).toEqual({ kind: "progressing", label: "acceptance pending" });
    expect(routeHealth(route("a", "r", { acceptedCond: F("NotAllowedByListeners") }))).toEqual({ kind: "fault", label: "not accepted" });
    expect(routeHealth(route("a", "r", { generation: 2, acceptedCond: F("NotAllowedByListeners", 1) }))).toEqual({ kind: "degraded", label: "not accepted (last observed)" });
  });
  it("fails closed: absent ResolvedRefs, stale positives and a zero observedGeneration are not current proof", () => {
    expect(routeHealth(route("a", "r", { resolvedRefsCond: undefined, resolvedRefs: false }))).toEqual({ kind: "unknown", label: "refs not reported" });
    expect(routeHealth(route("a", "r", { generation: 2 }))).toEqual({ kind: "progressing", label: "status syncing" });
    expect(routeHealth(route("a", "r", { generation: 3, acceptedCond: F("NotAllowedByListeners", 0) }))).toEqual({ kind: "degraded", label: "not accepted (last observed)" });
    // an independent, confirmed backend fault still wins over a syncing status
    expect(routeHealth(route("a", "r", { generation: 2 }, { ready: 0, total: 2 }))).toEqual({ kind: "fault", label: "no ready endpoints" });
  });
  it("each backend carries its own evidence: a missing primary among several is degraded, never another backend's numbers", () => {
    const r = route("a", "r");
    r.backends = [
      { kind: "Service", name: "gone", namespace: "a", port: 80, weight: 50, state: "not-found" },
      { kind: "Service", name: "ok", namespace: "a", port: 80, weight: 50, state: "resolved", endpoints: { ready: 3, total: 3, nonPod: 0 } },
    ];
    expect(routeHealth(r)).toEqual({ kind: "degraded", label: "primary backend not found" });
    expect(routeIssues(r)[0].text).toMatch(/1 other backend is not diagnosed/);
    r.backends = [r.backends[0]];
    expect(routeHealth(r)).toEqual({ kind: "fault", label: "backend not found" });
  });
  it("a forbidden Service read and a non-Service backend are unverified, not missing", () => {
    const r = route("a", "r");
    r.backends = [{ kind: "Service", name: "locked", namespace: "a", port: 80, weight: 0, state: "unreadable", error: "services is forbidden" }];
    expect(routeHealth(r)).toEqual({ kind: "unknown", label: "backend unreadable" });
    r.backends = [{ kind: "Backend", name: "raw", namespace: "a", port: 80, weight: 0, state: "unsupported" }];
    expect(routeHealth(r)).toEqual({ kind: "unknown", label: "Backend backend not evaluated" });
  });
  it("endpoints are endpoints: non-pod endpoints are named", () => {
    const r = route("a", "r", {}, { ready: 1, total: 2, nonPod: 1 });
    expect(routeIssues(r)[0].text).toBe("1/2 endpoints ready (1 not backed by a pod) - reduced capacity.");
  });
});

describe("certStatus (cert-manager coverage rules)", () => {
  const cert = (over: Partial<CertificateDTO> = {}): CertificateDTO => ({ namespace: "infra", name: "eg", ready: "Ready", reason: "", message: "", issuerKind: "ClusterIssuer", issuerName: "le", secretName: "eg-tls", dnsNames: [], notAfterUnix: 2_000, expiresInSeconds: 1_000, ...over } as CertificateDTO);
  it("an unread or failed watch is unknown, never 'not issued'", () => {
    expect(certStatus(undefined, "pending", 1_000).text).toMatch(/not read/);
    expect(certStatus(undefined, "failed", 1_000).text).toMatch(/not read/);
    expect(certStatus(undefined, "complete", 1_000).text).toBe("no matching Certificate in this read");
  });
  it("expiry beats Ready; Unknown stays neutral; stale is last observed", () => {
    expect(certStatus(cert({ notAfterUnix: 900 }), "complete", 1_000)).toEqual({ text: "▲ certificate expired", color: "var(--color-text-danger)" });
    expect(certStatus(cert({ ready: "Unknown" }), "complete", 1_000).color).toBe("var(--color-text-tertiary)");
    expect(certStatus(cert(), "stale", 1_000).text).toBe("certificate ready (last observed)");
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
  it("absent policy kinds are one collapsed neutral fact; route notes go to that route; gaps stay in the notice", () => {
    seed(topo([route("apps", "share")], { warnings: ["SecurityPolicy CRD not installed", "BackendTLSPolicy CRD not installed", "route apps/share has 2 backends; the lane shows the primary", "could not list ClientTrafficPolicy: forbidden"] }));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    const strip = screen.getByTestId("gateway-strip");
    fireEvent.click(within(strip).getByRole("button", { name: "2 optional kinds not installed" }));
    expect(within(strip).getByText(/SecurityPolicy, BackendTLSPolicy; no such policies can apply here/)).toBeTruthy();
    expect(screen.queryByText("SecurityPolicy CRD not installed")).toBeNull();
    expect(screen.getByText(/could not list ClientTrafficPolicy: forbidden/)).toBeTruthy();
    expect(screen.queryByText(/the lane shows the primary/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /HTTPRoute apps\/share/ }));
    expect(within(screen.getByTestId("route-panel")).getByText("Has 2 backends; the lane shows the primary.")).toBeTruthy();
  });
  it("draws the gateway node and one lane per route, broken first, and auto-selects the first broken lane", () => {
    seed(topo([route("apps", "share"), route("apps", "cart", { resolvedRefs: false, resolvedRefsCond: F("BackendNotFound") })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(within(screen.getByTestId("gateway-strip")).getByText("192.0.2.10")).toBeTruthy();
    const lanes = screen.getAllByRole("listitem");
    expect(lanes[0].textContent).toContain("cart");
    expect(lanes[1].textContent).toContain("share");
    expect(useFleet.getState().network.selectedRoute).toBe("apps/cart");
    expect(within(screen.getByTestId("route-panel")).getByText(/References not resolved - BackendNotFound/)).toBeTruthy();
    expect(screen.getByText("▲ 1 broken")).toBeTruthy();
  });
  it("with nothing broken nothing is auto-selected and nothing claims the path works end to end", () => {
    seed(topo([route("apps", "share")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.queryByTestId("route-panel")).toBeNull();
    expect(screen.queryByText(/usable/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /HTTPRoute apps\/share/ }));
    const panel = screen.getByTestId("route-panel");
    expect(within(panel).getByText(/does not test the path end to end/)).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: "Open Service" }));
    const s = useFleet.getState();
    expect(s.route.name === "cluster" && s.route.resource?.kind).toBe("Service");
  });

  it("unknown endpoints are counted as unverified; a not-programmed Gateway qualifies otherwise-resolved routes", () => {
    const r = route("apps", "share");
    r.backends[0].endpoints = { ready: 0, total: 0, nonPod: 0, error: "forbidden" };
    const t = topo([r, route("apps", "cart")]);
    t.gateway.programmed = false; t.gateway.programmedCond = F("AddressNotAssigned");
    seed(t);
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("1 unverified")).toBeTruthy();
    expect(screen.getByText("Gateway not programmed.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /HTTPRoute apps\/cart/ }));
    expect(within(screen.getByTestId("route-panel")).getByText("The Gateway itself is not programmed.")).toBeTruthy();
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

  it("a first read whose HTTPRoute list failed is a coverage failure, never 'no routes'", () => {
    seed(topo([], { routesComplete: false, routesError: "could not list HTTPRoutes: forbidden" }));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("Could not list HTTPRoutes.")).toBeTruthy();
    expect(screen.queryByText(/No HTTPRoutes are attached/)).toBeNull();
  });

  it("a forbidden HTTPRoute list after a complete read keeps the lanes and claims no detachment", () => {
    seed(topo([route("apps", "share"), route("apps", "cart", { acceptedCond: F("NotAllowedByListeners") })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(useFleet.getState().network.selectedRoute).toBe("apps/cart");
    act(() => { useFleet.getState().setTopology(topo([], { routesComplete: false, routesError: "could not list HTTPRoutes: forbidden" })); });
    expect(screen.getByText(/HTTPRoute list failed - the lanes are from the complete read/)).toBeTruthy();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByText("No longer attached.")).toBeNull();
    expect(screen.getByText(/\(last complete read\)/)).toBeTruthy();
  });
  it("a route gone from a complete re-read is named as gone, with its last evidence", () => {
    seed(topo([route("apps", "share"), route("apps", "cart", { acceptedCond: F("NotAllowedByListeners") })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(useFleet.getState().network.selectedRoute).toBe("apps/cart");
    act(() => { useFleet.getState().setTopology(topo([route("apps", "share")])); });
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

  it("Gateway status: explicit False is a fault notice; absent status is unverified, never a rejection", () => {
    const t = topo([route("apps", "share")]);
    t.gateway.accepted = false; t.gateway.programmed = false; t.gateway.acceptedCond = F("InvalidParameters"); t.gateway.programmedCond = undefined;
    seed(t);
    const first = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("▲ Gateway not accepted.")).toBeTruthy();
    first.unmount();
    const t2 = topo([route("apps", "share")]);
    t2.gateway.accepted = false; t2.gateway.programmed = false; t2.gateway.acceptedCond = { status: "", reason: "", message: "", observedGeneration: 0 };
    seed(t2);
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(within(screen.getByTestId("gateway-strip")).getByText("no status reported")).toBeTruthy();
    expect(screen.queryByText(/not accepted/)).toBeNull();
  });

  it("the drawer's traffic block carries the metrics provenance", () => {
    seed(topo([route("apps", "share")]), { selectedRoute: "apps/share", routeMetrics: { "apps/share": { rps: 3, p50: 12, p99: 80, errRate: 0 } }, routeMetricsStatus: { available: false, message: "timeout", updatedAt: "then" }, routeMetricsStale: true, routeMetricsAt: 1_700_000_000_000 });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(within(screen.getByTestId("route-panel")).getByText(/Last good read from .* - the latest poll failed \(timeout\)/)).toBeTruthy();
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
