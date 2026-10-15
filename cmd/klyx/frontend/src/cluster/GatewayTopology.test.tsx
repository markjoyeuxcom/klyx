import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, CondDTO, GatewayRef, RouteNodeDTO, TopologyDTO } from "../store/fleet";

// The rows hook is the transport; these tests seed the certmanager slice it fills.
vi.mock("../health/useCertManagerRows", () => ({ useCertManagerRows: () => {} }));
vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}), abandonRouteMetrics: vi.fn() }));
// The per-rule read (slice 8d): a cancellable call these tests answer.
const ruleMetricsCall = vi.fn();
vi.mock("../bridge/ruleMetrics", () => ({ ruleMetrics: (...a: unknown[]) => ruleMetricsCall(...a) }));
const cancellable = <T,>(v: T) => Object.assign(Promise.resolve(v), { cancel: vi.fn() });
const ruleRead = (over: object = {}) => ({ status: { available: true, message: "", updatedAt: "" }, scope: "owning-gateway", start: "2026-10-15T20:00:00Z", end: "2026-10-15T20:30:00Z",
  stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "", notes: [], ...over });
ruleMetricsCall.mockImplementation(() => cancellable(ruleRead()));
import { GatewayTopology } from "./GatewayTopology";
import { certStatus, routeIssues, sortWarnings } from "./gatewayModel";
import { gatewayRule, routeRule } from "../test/backendRules";

const gateway: GatewayRef = { namespace: "infra", name: "eg" };

const T = (reason = "Accepted"): CondDTO => ({ status: "True", reason, message: "", observedGeneration: 1 });
const F = (reason: string, observedGeneration = 1): CondDTO => ({ status: "False", reason, message: "", observedGeneration });

// The route verdict as the backend computes it (test/backendRules.ts, held to
// the Go golden fixture); lens lanes read it from rule.
type Route = Omit<RouteNodeDTO, "rule"> & { rule?: RouteNodeDTO["rule"] };
const routeHealth = (r: Route) => { const x = routeRule(r); return { kind: x.role, label: x.label }; };

function route(namespace: string, name: string, over: Partial<RouteNodeDTO> = {}, ep: { ready: number; total: number; nonPod?: number } = { ready: 2, total: 2 }): Route {
  const svc = `${name}-svc`;
  return {
    namespace, name, hostnames: [`${name}.example.com`], matches: [{ pathType: "PathPrefix", pathValue: `/${name}`, method: "" }], parentSectionName: "https",
    accepted: true, resolvedRefs: true, generation: 1, parentFound: true, acceptedCond: T(), resolvedRefsCond: T("ResolvedRefs"),
    backends: [{ kind: "Service", name: svc, namespace, port: 80, weight: 0, state: "resolved", endpoints: { ready: ep.ready, total: ep.total, nonPod: ep.nonPod ?? 0 } }],
    services: [{ namespace, name: svc, type: "ClusterIP", port: 80, resolved: true, global: false, meshClusters: [], meshUnconfirmed: false, policies: [], cnps: [] }],
    pods: { ready: ep.ready, total: ep.total, unknown: false }, policies: [], ...over,
  };
}

// The topology stamps each route and its Gateway with the backend's rule, the
// routes against this Gateway's listeners (a fixture that set its own rule on
// purpose keeps it).
const topo = (routes: Route[], over: Partial<Omit<TopologyDTO, "routes" | "gateway">> & { gateway?: Partial<TopologyDTO["gateway"]> } = {}): TopologyDTO => {
  const gateway: TopologyDTO["gateway"] = { namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true, generation: 1, acceptedCond: T(), programmedCond: T("Programmed"), addresses: [{ type: "IPAddress", value: "192.0.2.10" }],
    listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443, certificateRefs: [{ group: "", kind: "Secret", namespace: "infra", name: "eg-tls" }] }], policies: [], rule: { included: false, role: "ok", label: "programmed" } };
  const g = { ...gateway, ...over.gateway };
  const stamped = { ...g, rule: over.gateway?.rule ?? gatewayRule(g) };
  return { routesComplete: true, ...over, gateway: stamped, routes: routes.map((r) => ({ ...r, rule: r.rule ?? routeRule(r, stamped.listeners) })) };
};

function seed(t: TopologyDTO | null, over: Partial<ReturnType<typeof useFleet.getState>["network"]> = {}) {
  useFleet.setState({
    route: { name: "cluster", cluster: "x", section: "gatewayApi", gateway },
    network: { cluster: "x", selected: gateway, topology: t, topologyLoading: false, topologyError: null, topologyAt: t ? Date.now() : null, routesAt: t?.routesComplete ? Date.now() : null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false, ...over },
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
    expect(routeHealth(r)).toEqual({ kind: "degraded", label: "gone: backend not found" });
    expect(routeIssues(r)[0].text).toBe("a/gone: Backend Service a/gone not found. Other backends in its rule may still carry the traffic.");
    r.backends = [r.backends[0]];
    expect(routeHealth(r)).toEqual({ kind: "fault", label: "backend not found" });
  });
  it("diagnoses every backend that carries traffic, not only the lane's (Codex review 2026-10-13)", () => {
    const r = route("a", "r");
    const svc = (name: string, ready: number, total: number, weight?: number) =>
      ({ kind: "Service", name, namespace: "a", port: 80, weight: weight ?? 0, weightSet: weight !== undefined, state: "resolved" as const, endpoints: { ready, total, nonPod: 0 } });
    // An equal-weight second backend with no endpoints is never "resolved".
    r.backends = [svc("first", 2, 2), svc("second", 0, 0)];
    expect(routeHealth(r)).toEqual({ kind: "degraded", label: "second: no endpoints" });
    // Every carrying backend faulting is a fault.
    r.backends = [svc("first", 0, 0), svc("second", 0, 2)];
    expect(routeHealth(r).kind).toBe("fault");
    // A weight-0 backend carries no traffic and is not diagnosed.
    r.backends = [svc("first", 2, 2), svc("canary", 0, 0, 0)];
    expect(routeHealth(r)).toEqual({ kind: "ok", label: "resolved" });
    r.backends = [svc("first", 2, 2, 0), svc("second", 2, 2, 0)];
    expect(routeHealth(r)).toEqual({ kind: "fault", label: "every backend has weight 0" });
  });
  it("judges weights per rule: a dead zero-weight rule is never hidden by a healthy one (Codex review 2026-10-13)", () => {
    const r = route("a", "r");
    const svc = (name: string, rule: number, ready: number, total: number, weight?: number) =>
      ({ kind: "Service", name, namespace: "a", port: 80, rule, weight: weight ?? 0, weightSet: weight !== undefined, state: "resolved" as const, endpoints: { ready, total, nonPod: 0 } });
    r.backends = [svc("dead", 0, 0, 0, 0), svc("healthy", 1, 2, 2)];
    expect(routeHealth(r)).toEqual({ kind: "fault", label: "rule 1: every backend has weight 0" });
    r.backends = [svc("healthy", 0, 2, 2), svc("dead", 1, 0, 0, 0)];
    expect(routeHealth(r)).toEqual({ kind: "fault", label: "rule 2: every backend has weight 0" });
    // A sole backend failing in one rule is a fault for that rule's traffic,
    // whatever another rule's backends do.
    r.backends = [svc("healthy", 0, 2, 2), svc("empty", 1, 0, 0)];
    expect(routeHealth(r)).toEqual({ kind: "fault", label: "empty: no endpoints" });
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
  it("lanes read the Go-computed route rule, never their own (1c §2)", () => {
    seed(topo([route("apps", "share", { rule: { included: true, role: "fault", label: "Flagged by backend" } }), route("apps", "cart")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByRole("button", { name: /HTTPRoute apps\/share/ }).textContent).toContain("Flagged by backend");
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
    const t = topo([r, route("apps", "cart")], { gateway: { programmed: false, programmedCond: F("AddressNotAssigned") } });
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
    expect(within(screen.getByRole("list", { name: "HTTPRoute lanes" })).getAllByRole("listitem")).toHaveLength(2);
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
    const t = topo([route("apps", "share")], { gateway: { accepted: false, programmed: false, acceptedCond: F("InvalidParameters"), programmedCond: undefined } });
    seed(t);
    const first = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(screen.getByText("▲ Gateway not accepted.")).toBeTruthy();
    first.unmount();
    const t2 = topo([route("apps", "share")], { gateway: { accepted: false, programmed: false, acceptedCond: { status: "", reason: "", message: "", observedGeneration: 0 } } });
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
  // The lane reads its rules (slice 8d): one rule's latest attributed 5xx
  // share, never a sum, in the neutral colour when healthy.
  it("a healthy route's error rate stays neutral", async () => {
    const pts = [{ t: 1792079970, v: 0 }, { t: 1792080000, v: 0 }];
    ruleMetricsCall.mockImplementationOnce(() => cancellable(ruleRead({ rules: [{ namespace: "apps", name: "share", uid: "r", rule: 0, fingerprint: "f", attribution: { proven: true },
      requests: { availability: "available", points: pts }, classes: { availability: "available", classes: { "2xx": pts, "5xx": pts } }, share503: { availability: "absent", points: [] },
      retries: { availability: "available", points: pts }, backends: [] }] })));
    seed(topo([route("apps", "share", { rules: [{ index: 0, rule: { included: false, role: "ok", label: "resolved" }, matches: [], backends: [] }] })]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect((await screen.findByText("0%")).style.color).toBe("var(--color-text-secondary)");
    expect(screen.getByText(/1 rule · rule 1 5xx/)).toBeTruthy();
  });

  it("no metrics source is said on the lane and in the footer, never drawn as dashes", async () => {
    ruleMetricsCall.mockImplementationOnce(() => cancellable(ruleRead({ status: { available: false, message: "Envoy Gateway not detected", updatedAt: "" } })));
    seed(topo([route("apps", "share")]), { routeMetricsStatus: { available: false, message: "Envoy Gateway not detected", updatedAt: "" } });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(await screen.findByText(/no route metrics/)).toBeTruthy();
    expect(screen.getByText(/route traffic unavailable: Envoy Gateway not detected/)).toBeTruthy();
  });

  // The route-level numbers are the selected route's, in its inspector.
  it("a failed metrics refresh keeps the last numbers, labelled", () => {
    seed(topo([route("apps", "share")]), { selectedRoute: "apps/share", routeMetrics: { "apps/share": { rps: 3, p50: 12, p99: 80, errRate: 0 } }, routeMetricsStatus: { available: false, message: "timeout", updatedAt: "then" }, routeMetricsStale: true });
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(within(screen.getByTestId("route-panel")).getByText(/3 rps/)).toBeTruthy();
    expect(screen.getByText(/route traffic: last good read - the latest failed \(timeout\)/)).toBeTruthy();
  });

  // The per-rule poll (slice 8d): single-flight - a read still running is not
  // doubled - and cancelled when the view leaves.
  it("polls the per-rule read single-flight and cancels it on leaving", () => {
    vi.useFakeTimers();
    try {
      const pending = Object.assign(new Promise(() => {}), { cancel: vi.fn() });
      ruleMetricsCall.mockReset();
      ruleMetricsCall.mockImplementation(() => pending);
      seed(topo([route("apps", "share")]));
      const { unmount } = render(<GatewayTopology cluster="x" gateway={gateway} />);
      expect(ruleMetricsCall).toHaveBeenCalledTimes(1);
      expect(ruleMetricsCall).toHaveBeenCalledWith("x", gateway.namespace, gateway.name);
      vi.advanceTimersByTime(90000);
      expect(ruleMetricsCall).toHaveBeenCalledTimes(1); // still in flight: no second read
      unmount();
      expect(pending.cancel).toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
      ruleMetricsCall.mockReset();
      ruleMetricsCall.mockImplementation(() => cancellable(ruleRead()));
    }
  });

  it("Back to Gateways closes the topology", () => {
    seed(topo([route("apps", "share")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(screen.getByRole("button", { name: "Back to Gateways" }));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.gateway).toBeUndefined();
  });
});
