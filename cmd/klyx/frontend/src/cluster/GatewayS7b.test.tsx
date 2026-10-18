import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";

// Coverage S7b in the Gateway lens: routes of every kind, one lane per
// attachment, kind-qualified identity, L4 data paths, HTTP metrics for
// HTTPRoutes only, the attachment's evidence, and the experimental group's
// one line. The rows hook runs on the real health store; only the transport
// is stubbed.
vi.mock("../health/useCertManagerRows", () => ({ useCertManagerRows: () => {} }));
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
const getRouteMetrics = vi.fn(async () => {});
vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), getRouteMetrics: (...a: unknown[]) => getRouteMetrics(...(a as [])), abandonRouteMetrics: vi.fn() }));
vi.mock("../bridge/ruleMetrics", () => ({ ruleMetrics: () => Object.assign(Promise.resolve({ status: { available: true, message: "", updatedAt: "" }, scope: "owning-gateway", start: "", end: "", stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "", notes: [] }), { cancel: () => {} }) }));
import { useFleet } from "../store/fleet";
import type { CondDTO, GatewayRef, GatewayRouteRowDTO, RouteNodeDTO, TopologyDTO } from "../store/fleet";
import { GatewayTopology } from "./GatewayTopology";
import { GatewayApiView } from "./GatewayApiView";
import { DataPath } from "./DataPath";
import { backendIssue, experimentalText, laneIssues, laneKey, resolveLane, routeRef, ruleMatchText, sortWarnings } from "./gatewayModel";
import { gatewayRule, routeRule } from "../test/backendRules";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, useHealth, type HealthPart, type KindCoverage } from "../store/health";

const T = (reason = "Accepted"): CondDTO => ({ status: "True", reason, message: "", observedGeneration: 1 });
const gateway: GatewayRef = { namespace: "infra", name: "eg" };
const egRef = (section = "") => ({ group: "gateway.networking.k8s.io", kind: "Gateway", namespace: "infra", name: "eg", sectionName: section || undefined });
const key = (r: { group: string; kind: string; namespace: string; name: string; sectionName?: string; port?: number }) => `${r.group}/${r.kind}/${r.namespace}/${r.name}#${r.sectionName ?? ""}:${r.port ?? 0}`;

type Lane = Omit<RouteNodeDTO, "rule"> & { rule?: RouteNodeDTO["rule"] };
function lane(kind: string, name: string, section: string, over: Partial<RouteNodeDTO> = {}): Lane {
  const parentRef = over.parentRef ?? egRef(section);
  const port = kind === "UDPRoute" ? 53 : kind === "TCPRoute" ? 5432 : 80;
  return {
    kind, apiVersion: "gateway.networking.k8s.io/v1", namespace: "apps", name, uid: `uid-${kind}-${name}`,
    attachment: key(parentRef), attachmentLabel: `infra/eg${section ? ` · ${section}` : ""}`, parentRef, controller: "example.com/gw",
    hostnames: kind === "TCPRoute" || kind === "UDPRoute" ? [] : [`${name}.example.com`], matches: [], parentSectionName: section,
    accepted: true, resolvedRefs: true, generation: 1, parentFound: true, acceptedCond: T(), resolvedRefsCond: T("ResolvedRefs"),
    backends: [{ kind: "Service", name: `${name}-svc`, namespace: "apps", port, weight: 0, transport: kind === "UDPRoute" ? "UDP" : "TCP", state: "resolved", endpoints: { ready: 1, total: 1, nonPod: 0 } }],
    rules: [{ index: 0, fingerprint: "fp", rule: { included: false, role: "ok", label: "resolved" }, matches: [], backends: [{ kind: "Service", namespace: "apps", name: `${name}-svc`, port, weight: 1, weightSet: false, rule: { included: false, role: "ok", label: "resolved" } }] }],
    services: [{ namespace: "apps", name: `${name}-svc`, type: "ClusterIP", port, resolved: true, global: false, meshClusters: [], meshUnconfirmed: false, policies: [], cnps: [] }],
    pods: { ready: 1, total: 1, unknown: false }, policies: [], ruleNames: [""], hostnameCount: 0, ...over,
  };
}
const topo = (routes: Lane[]): TopologyDTO => {
  const g: TopologyDTO["gateway"] = { namespace: "infra", name: "eg", className: "eg", accepted: true, programmed: true, generation: 1, acceptedCond: T(), programmedCond: T("Programmed"),
    listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443 }, { name: "pg", protocol: "TCP", hostname: "", port: 5432 }, { name: "dns", protocol: "UDP", hostname: "", port: 53 }], policies: [], rule: { included: false, role: "ok", label: "programmed" } };
  const stamped = { ...g, rule: gatewayRule(g) };
  return { routesComplete: true, gateway: stamped, routes: routes.map((r) => ({ ...r, rule: r.rule ?? routeRule(r, stamped.listeners) })) };
};
function seed(t: TopologyDTO, selectedRoute: string | null = null) {
  useFleet.setState({
    route: { name: "cluster", cluster: "x", section: "gatewayApi", gateway },
    network: { cluster: "x", selected: gateway, topology: t, topologyLoading: false, topologyError: null, topologyAt: Date.now(), routesAt: Date.now(), selectedRoute, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
    certmanager: { cluster: null, certificates: [], issuers: [], loading: false, selectedKey: null, chain: null, chainLoading: false },
  });
}

beforeEach(() => { vi.clearAllMocks(); resetHealthStore(); });

describe("the S7b model", () => {
  it("a lane key is the route and its attachment; a route identity names its worst lane", () => {
    const ok = { ...lane("HTTPRoute", "web", "https"), rule: { included: false, role: "ok", label: "resolved" } } as RouteNodeDTO;
    const bad = { ...lane("HTTPRoute", "web", "pg"), rule: { included: true, role: "fault", label: "not accepted" } } as RouteNodeDTO;
    const tcp = { ...lane("TCPRoute", "web", "pg"), rule: { included: false, role: "ok", label: "resolved" } } as RouteNodeDTO;
    const lanes = [ok, bad, tcp];
    expect(laneKey(ok)).not.toBe(laneKey(bad));
    expect(resolveLane(laneKey(ok), lanes)).toBe(ok);
    expect(resolveLane("HTTPRoute/apps/web", lanes)).toBe(bad);
    expect(resolveLane("apps/web", lanes)).toBe(bad); // the older namespace/name names an HTTPRoute
    expect(resolveLane("TCPRoute/apps/web", lanes)).toBe(tcp);
    expect(resolveLane("UDPRoute/apps/web", lanes)).toBeUndefined();
  });
  it("route notes are keyed by kind; one without a kind is an HTTPRoute's", () => {
    const w = sortWarnings(["route TCPRoute apps/db: Service has no TCP port 5432 apps/db", "route apps/web: backend Service apps/x not found", "could not list ReferenceGrants: forbidden"]);
    expect(w.byRoute.get("TCPRoute/apps/db")).toEqual(["Service has no TCP port 5432 apps/db"]);
    expect(w.byRoute.get("HTTPRoute/apps/web")).toEqual(["backend Service apps/x not found"]);
    expect(w.gaps).toEqual(["could not list ReferenceGrants: forbidden"]);
  });
  it("a backend without its transport's port is a fault naming what the Service publishes", () => {
    expect(backendIssue({ kind: "Service", name: "dns", namespace: "apps", port: 53, weight: 0, transport: "UDP", state: "no-port", error: "Service has no UDP port 53 (port 53 exists for TCP only)" }))
      .toEqual({ short: "no UDP port 53", text: "Backend Service apps/dns has no UDP port 53 - port 53 exists for TCP only.", severity: "fault" });
  });
  it("no endpoints names the port whose slices were counted", () => {
    expect(backendIssue({ kind: "Service", name: "dns", namespace: "apps", port: 53, weight: 0, transport: "UDP", state: "resolved", endpoints: { ready: 0, total: 0, nonPod: 0 } })?.text)
      .toBe("The backend Service has no endpoints for its UDP port 53.");
  });
  it("the attachment's verdict leads when it decided before the status (controller not established)", () => {
    const r = { ...lane("GRPCRoute", "api", "https", { controller: "", controllerWhy: "the GatewayClass eg does not exist", parentFound: false }), rule: { included: false, role: "unknown", label: "the controller judging this parent is not established" } } as RouteNodeDTO;
    const issues = laneIssues(r, []);
    expect(issues[0].short).toBe("the controller judging this parent is not established");
    expect(issues[0].text).toContain("the GatewayClass eg does not exist");
    expect(issues.some((i) => i.short === "no status for this Gateway")).toBe(false);
  });
  it("each kind's rule says what it matches", () => {
    const pg = [{ name: "pg", protocol: "TCP", hostname: "", port: 5432 }];
    expect(ruleMatchText("TCPRoute", { hostnames: [] }, [], pg)).toBe("every connection on pg :5432");
    expect(ruleMatchText("TLSRoute", { hostnames: [] }, [], [])).toBe("any SNI");
    expect(ruleMatchText("TLSRoute", { hostnames: ["a.example"] }, [], [])).toBe("SNI a.example");
    expect(ruleMatchText("GRPCRoute", { hostnames: [] }, [{ pathType: "", pathValue: "", method: "", grpcService: "pkg.Svc" }], [])).toBe("pkg.Svc/any method");
    expect(ruleMatchText("GRPCRoute", { hostnames: [] }, [], [])).toBe("every call");
    expect(ruleMatchText("HTTPRoute", { hostnames: [] }, [], [])).toBe("every request");
  });
  it("a route opens at the version it was read", () => {
    expect(routeRef("TLSRoute", "gateway.networking.k8s.io/v1alpha2")).toEqual({ group: "gateway.networking.k8s.io", version: "v1alpha2", plural: "tlsroutes", kind: "TLSRoute", scope: "Namespaced" });
    expect(routeRef("UDPRoute").version).toBe("v1");
  });
  it("the experimental group is one neutral line, and an XListenerSet names its promotion", () => {
    expect(experimentalText(null, "")).toBe("");
    expect(experimentalText(["XListenerSet", "XMesh"], "")).toBe("Experimental Gateway API kinds served: XListenerSet, XMesh; Klyx does not read them. Gateway API 1.5 promoted XListenerSet to ListenerSet (gateway.networking.k8s.io/v1).");
    expect(experimentalText([], "the gateway.networking.x-k8s.io resources at v1alpha1 could not be read")).toContain("is served, but the gateway.networking.x-k8s.io resources at v1alpha1 could not be read");
  });
});

describe("the topology's lanes (S7b §9)", () => {
  it("two attachments of one route are two lanes; a TCPRoute lane says every connection and no HTTP metrics", () => {
    seed(topo([lane("HTTPRoute", "web", "https"), lane("HTTPRoute", "web", "pg"), lane("TCPRoute", "db", "pg")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    const lanes = within(screen.getByRole("list", { name: "Route lanes" })).getAllByRole("listitem");
    expect(lanes).toHaveLength(3);
    expect(screen.getAllByRole("button", { name: /^HTTPRoute apps\/web via/ })).toHaveLength(2);
    const db = screen.getByRole("button", { name: /^TCPRoute apps\/db via infra\/eg · pg/ });
    expect(db.textContent).toContain("listener pg · every connection");
    expect(db.closest("[role=listitem]")!.textContent).toContain("HTTP request metrics do not apply to TCPRoute");
    // the header counts routes, and says how many attachments draw them
    expect(document.querySelector(".lens-header .t-number")!.textContent).toBe("2");
    expect(document.querySelector(".lens-header")!.textContent).toContain("routes · 3 attachments");
  });
  it("the kind filter shows one kind's lanes", () => {
    seed(topo([lane("HTTPRoute", "web", "https"), lane("TCPRoute", "db", "pg"), lane("UDPRoute", "dns", "dns")]));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.change(screen.getByLabelText("Route kind"), { target: { value: "UDPRoute" } });
    const lanes = within(screen.getByRole("list", { name: "Route lanes" })).getAllByRole("listitem");
    expect(lanes).toHaveLength(1);
    expect(lanes[0].textContent).toContain("dns");
  });
  it("a TCPRoute's inspector: no HTTP request read, its own kind opened at its version, the attachment's evidence", () => {
    const openResource = vi.fn();
    const db = lane("TCPRoute", "db", "pg", { apiVersion: "gateway.networking.k8s.io/v1alpha2", others: [{ controller: "retired.example/gw", accepted: "True", resolvedRefs: "True" }],
      evidence: { controller: "example.com/gw", allowance: { from: "", allowed: "yes", text: "listener pg allows TCPRoute from Same", disagrees: false }, features: [] } });
    seed(topo([db]), laneKey(db));
    useFleet.setState({ openResource } as never);
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    const panel = within(screen.getByTestId("route-panel"));
    expect(getRouteMetrics).not.toHaveBeenCalled();
    expect(panel.getByTestId("no-http-metrics").textContent).toBe("HTTP request metrics do not apply to TCPRoute.");
    expect(panel.getByText("Every connection on the listener: a TCPRoute matches no fields.")).toBeTruthy();
    const ev = panel.getByTestId("route-attachment-evidence");
    expect(ev.textContent).toContain("example.com/gw");
    expect(ev.textContent).toContain("an entry from another controller (retired.example/gw), not judged");
    expect(ev.textContent).toContain("listener pg allows TCPRoute from Same");
    expect(panel.queryByText("Listener TLS")).toBeNull();
    fireEvent.click(panel.getByRole("button", { name: "Open TCPRoute" }));
    expect(openResource).toHaveBeenCalledWith(expect.objectContaining({ plural: "tcproutes", version: "v1alpha2", kind: "TCPRoute" }));
  });
  it("an HTTPRoute's inspector still reads its request metrics, by namespace/name", () => {
    const web = lane("HTTPRoute", "web", "https");
    seed(topo([web]), laneKey(web));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getRouteMetrics).toHaveBeenCalledWith("x", "infra", "eg", ["apps/web"]);
  });
  it("a route identity from the Routes view or the palette becomes its worst lane", () => {
    const ok = lane("HTTPRoute", "web", "https");
    const bad = lane("HTTPRoute", "web", "pg", { rule: { included: true, role: "fault", label: "not accepted" } });
    seed(topo([ok, bad]), "HTTPRoute/apps/web");
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(useFleet.getState().network.selectedRoute).toBe(laneKey(bad));
    expect(within(screen.getByTestId("route-panel")).getAllByText("not accepted").length).toBeGreaterThan(0);
  });
  it("through a ListenerSet, the Gateway's listeners are not the section's evidence", () => {
    const via = lane("HTTPRoute", "web", "extra", { parentRef: { group: "gateway.networking.k8s.io", kind: "ListenerSet", namespace: "infra", name: "extra", sectionName: "extra" }, attachmentLabel: "ListenerSet infra/extra · extra" });
    seed(topo([via]), laneKey(via));
    render(<GatewayTopology cluster="x" gateway={gateway} />);
    const panel = within(screen.getByTestId("route-panel"));
    expect(panel.queryByText(/Listener extra does not exist on this Gateway/)).toBeNull();
    expect(panel.getByText(/its listeners, and their certificates, are the ListenerSet's/)).toBeTruthy();
  });
});

describe("the data path for every kind (S7b §9)", () => {
  it("an L4 rule matches no fields and reads no HTTP metrics", () => {
    const t = topo([lane("UDPRoute", "dns", "dns")]);
    render(<DataPath gateway={t.gateway} route={t.routes[0]} listeners={t.gateway.listeners} metrics={null} />);
    const path = screen.getByTestId("data-path");
    expect(path.textContent).toContain("UDPRoute");
    expect(path.textContent).toContain("every connection on dns :53");
    expect(path.textContent).toContain("measured: HTTP request metrics do not apply to UDPRoute");
    expect(path.textContent).not.toContain("every request");
  });
  it("an L4 route shows none of the HTTP read's notes, even when that read is available", () => {
    const t = topo([lane("TCPRoute", "db", "pg")]);
    const read = { status: { available: true, message: "", updatedAt: "" }, scope: "owning-gateway" as const, start: "", end: "", stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "by weight", notes: ["Direct responses are not in these series."] };
    render(<DataPath gateway={t.gateway} route={t.routes[0]} listeners={t.gateway.listeners} metrics={read} />);
    expect(screen.getByTestId("data-path").textContent).not.toContain("Direct responses");
    expect(screen.getByTestId("data-path").textContent).not.toContain("Per-backend split");
  });
  it("a GRPCRoute rule names its service and method; a rule name is shown", () => {
    const t = topo([lane("GRPCRoute", "api", "https", { ruleNames: ["get"], matches: [{ pathType: "", pathValue: "", method: "", rule: 0, grpcService: "pkg.Svc", grpcMethod: "Get" }],
      rules: [{ index: 0, fingerprint: "fp", rule: { included: false, role: "ok", label: "resolved" }, matches: [{ pathType: "", pathValue: "", method: "", rule: 0, grpcService: "pkg.Svc", grpcMethod: "Get" }], backends: [] }] })]);
    render(<DataPath gateway={t.gateway} route={t.routes[0]} listeners={t.gateway.listeners} metrics={null} />);
    expect(screen.getByTestId("data-path").textContent).toContain("rule 1 get");
    expect(screen.getByTestId("data-path").textContent).toContain("pkg.Svc/Get");
  });
});

// The Routes view on the real rows hook: identity is kind/namespace/name.
describe("the Routes view's kinds (S7b §9)", () => {
  const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
  const part = (k: KindCoverage[], total: number, included: number): HealthPart => ({
    id: "routes", entity: "routes", scope: "", classifier: "native", coverage: "live", kinds: k, obs: 1, ver: 1,
    native: { total, included, word: "", roles: { total, fault: included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: total - included } },
  });
  const parent = (section: string) => key(egRef(section));
  const row = (k: string, name: string, section: string, role = "ok", label = "resolved"): GatewayRouteRowDTO => ({
    uid: `uid-${k}-${name}`, gvr: `gateway.networking.k8s.io/v1/${k.toLowerCase()}s`, kind: k, namespace: "apps", name,
    parent: parent(section), parentLabel: `infra/eg · ${section}`, gateway: "infra/eg", hostnames: [], hostnameCount: 0,
    rule: { included: role !== "ok", role, label },
    attachments: [{ parent: parent(section), parentLabel: `infra/eg · ${section}`, gateway: "infra/eg", rule: { included: role !== "ok", role, label },
      evidence: { controller: "example.com/gw", allowance: { from: "", allowed: "", text: "", disagrees: false } } }],
  });
  const mount = (rows: GatewayRouteRowDTO[]) => {
    addSub("r", "x", "gateway", "routes");
    openStarted("r", "tok-r"); openSucceeded("r", "tok-r", 1);
    const kinds = [kind("httproutes"), kind("tcproutes"), kind("udproutes", { coverage: "absent", objects: 0 }), ...["gateways", "services", "endpointslices"].map((k) => kind(k, { role: "dependency" }))];
    applyRows("r", { cluster: "x", incarnation: 1, task: "gateway", evaluatedAt: "2026-10-18T12:00:00Z", rows, part: part(kinds, rows.length, rows.filter((r) => r.rule.included).length) });
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gatewayApi" }, clusters: [] });
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: /^Routes/ }));
  };
  const names = () => [...document.querySelectorAll(".lens-row")].map((n) => `${n.querySelector("[data-col=kind]")?.textContent} ${n.querySelector(".lens-cell-name")?.textContent}`);

  it("an HTTPRoute and a TCPRoute of one name are two rows, each with its kind; the filter keeps one", () => {
    mount([row("HTTPRoute", "db", "https"), row("TCPRoute", "db", "pg", "fault", "no TCP port 5432")]);
    expect(names()).toEqual(["TCPRoute apps / db", "HTTPRoute apps / db"]);
    expect(document.querySelector(".lens-header")!.textContent).toContain("2 routes");
    fireEvent.change(screen.getByTestId("route-kind-filter"), { target: { value: "TCPRoute" } });
    expect(names()).toEqual(["TCPRoute apps / db"]);
  });
  it("a reveal names its kind by resource; Open topology selects that attachment's lane", () => {
    mount([row("HTTPRoute", "db", "https"), row("TCPRoute", "db", "pg", "fault", "no TCP port 5432")]);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "gateway", part: "routes", ref: { gvr: "gateway.networking.k8s.io/v1/tcproutes", namespace: "apps", name: "db", uid: "uid-TCPRoute-db" } }); });
    const panel = within(screen.getByTestId("route-row-panel"));
    expect(document.querySelector(".lens-row[data-selected=true] [data-col=kind]")?.textContent).toBe("TCPRoute");
    expect(panel.getByTestId("route-attachment").textContent).toContain("judged by example.com/gw");
    fireEvent.click(panel.getByRole("button", { name: "Open topology (infra/eg)" }));
    expect(useFleet.getState().network.selectedRoute).toBe(`TCPRoute/apps/db@${parent("pg")}`);
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.gateway).toEqual({ namespace: "infra", name: "eg" });
  });
  it("the Gateways view says which experimental kinds are served, neutrally", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gatewayApi" }, clusters: [{ name: "x", gatewayExperimentalKinds: ["XMesh"] } as never] });
    expect(useHealth.getState()).toBeTruthy();
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("gateway-experimental").textContent).toBe("Experimental Gateway API kinds served: XMesh; Klyx does not read them.");
  });
});
