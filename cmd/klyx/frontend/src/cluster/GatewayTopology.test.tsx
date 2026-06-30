import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet, TopologyDTO, GatewayRef, RouteMetricDTO, RouteMetricsStatusDTO } from "../store/fleet";
import { GatewayTopology } from "./GatewayTopology";

vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), listGateways: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}) }));

const gateway: GatewayRef = { namespace: "infra", name: "eg" };
const topo: TopologyDTO = {
  gateway: { namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true, addresses: [{ type: "IPAddress", value: "192.0.2.10" }], listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443, certificateRefs: [{ group: "", kind: "Secret", namespace: "infra", name: "eg-tls" }] }], policies: [] },
  routes: [
    { namespace: "apps", name: "share", hostnames: ["share.example.com"], matches: [{ pathType: "PathPrefix", pathValue: "/api/share", method: "GET" }], parentSectionName: "https", accepted: true, resolvedRefs: true, backends: [{ kind: "Service", name: "share-api", namespace: "apps", port: 8080, weight: 100 }], services: [{ namespace: "apps", name: "share-api", type: "ClusterIP", port: 8080, resolved: true, global: false, meshClusters: [], meshUnconfirmed: false, policies: [], cnps: [] }], pods: { ready: 3, total: 3, unknown: false }, policies: [] },
  ],
  warnings: ["route apps/share has 2 backends; the lane shows the primary"],
};

function route(namespace: string, name: string, path: string): TopologyDTO["routes"][number] {
  const svc = `${name}-svc`;
  return { namespace, name, hostnames: [], matches: [{ pathType: "PathPrefix", pathValue: path, method: "" }], accepted: true, resolvedRefs: true, backends: [{ kind: "Service", name: svc, namespace, port: 80, weight: 0 }], services: [{ namespace, name: svc, type: "ClusterIP", port: 80, resolved: true, global: false, meshClusters: [], meshUnconfirmed: false, policies: [], cnps: [] }], pods: { ready: 1, total: 1, unknown: false }, policies: [] };
}

const multiTopo: TopologyDTO = {
  gateway: topo.gateway,
  routes: [route("apps", "share", "/share"), route("monitoring", "grafana", "/grafana")],
  warnings: [],
};

function seed(t: TopologyDTO | null, loading = false, metrics?: { routeMetrics?: Record<string, RouteMetricDTO>; routeMetricsStatus?: RouteMetricsStatusDTO | null; routeMetricsStale?: boolean }) {
  useFleet.setState({
    route: { name: "cluster", cluster: "x", section: "gatewayApi", gateway },
    network: { served: true, gateways: [], listLoading: false, selected: gateway, topology: t, topologyLoading: loading, selectedRoute: null, routeMetrics: metrics?.routeMetrics ?? {}, routeMetricsStatus: metrics?.routeMetricsStatus ?? null, routeMetricsStale: metrics?.routeMetricsStale ?? false },
    certmanager: { cluster: null, certificates: [], issuers: [], loading: false, selectedKey: null, chain: null, chainLoading: false },
  });
}

beforeEach(() => { vi.clearAllMocks(); seed(topo); });

describe("GatewayTopology", () => {
  it("renders the route + service(+pods) + traffic lane (gateway lives in the header)", () => {
    const { getByText, getAllByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText("eg")).toBeTruthy(); // header
    expect(getAllByText("192.0.2.10").length).toBeGreaterThanOrEqual(1);
    expect(getByText("share")).toBeTruthy();
    expect(getByText("share-api")).toBeTruthy();
    expect(getByText(/pods 3\/3/)).toBeTruthy(); // folded into the service box
    expect(getByText("traffic")).toBeTruthy(); // the lane's own traffic node
  });

  it("surfaces warnings", () => {
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/shows the primary/i)).toBeTruthy();
  });

  it("clicking a route selects it (detail panel)", () => {
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    expect(useFleet.getState().network.selectedRoute).toBe("apps/share");
    expect(getByText(/PathPrefix/)).toBeTruthy();
    expect(getByText(/listener: https/)).toBeTruthy();
  });

  it("opens the selected HTTPRoute raw resource from the inspector", () => {
    const { getByText, getByLabelText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    fireEvent.click(getByLabelText("open HTTPRoute"));
    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "gateway.networking.k8s.io", version: "v1", plural: "httproutes", kind: "HTTPRoute", scope: "Namespaced" },
      instance: { namespace: "apps", name: "share" },
    });
  });

  it("opens a backend Service raw resource from the inspector", () => {
    const { getByText, getByLabelText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    fireEvent.click(getByLabelText("open Service"));
    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" },
      instance: { namespace: "apps", name: "share-api" },
    });
  });

  it("keeps route lanes and the inspector as independent scroll owners", () => {
    const { getByTestId } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByTestId("gateway-route-scroll").style.overflowY).toBe("auto");
    expect(getByTestId("gateway-route-inspector-scroll").style.overflowY).toBe("auto");
  });

  it("shows a persistent empty route inspector before selection", () => {
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/Select a route to inspect/i)).toBeTruthy();
  });

  it("correlates listener TLS Secret refs with loaded cert-manager certificate state", () => {
    seed(topo);
    useFleet.setState({
      certmanager: {
        cluster: "x",
        certificates: [{
          namespace: "infra", name: "wildcard-example", ready: "Ready", reason: "", message: "",
          issuerKind: "ClusterIssuer", issuerName: "letsencrypt", secretName: "eg-tls",
          dnsNames: ["*.example.com"], notBeforeUnix: 0, notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0,
        }],
        issuers: [], loading: false, selectedKey: null, chain: null, chainLoading: false,
      },
    });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    expect(getByText(/listener tls/i)).toBeTruthy();
    expect(getByText(/Secret infra\/eg-tls/)).toBeTruthy();
    expect(getByText(/cert ready/)).toBeTruthy();
  });

  it("opens the matched cert-manager Certificate from listener TLS context", () => {
    seed(topo);
    useFleet.setState({
      certmanager: {
        cluster: "x",
        certificates: [{
          namespace: "infra", name: "wildcard-example", ready: "Ready", reason: "", message: "",
          issuerKind: "ClusterIssuer", issuerName: "letsencrypt", secretName: "eg-tls",
          dnsNames: ["*.example.com"], notBeforeUnix: 0, notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0,
        }],
        issuers: [], loading: false, selectedKey: null, chain: null, chainLoading: false,
      },
    });
    const { getByText, getByLabelText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    fireEvent.click(getByLabelText("open Certificate"));
    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" },
      instance: { namespace: "infra", name: "wildcard-example" },
    });
  });

  it("opens the listener TLS Secret raw resource from the inspector", () => {
    const { getByText, getByLabelText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    fireEvent.click(getByLabelText("open Secret"));
    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" },
      instance: { namespace: "infra", name: "eg-tls" },
    });
  });

  it("flags a route pinned to a missing listener section", () => {
    const bad = { ...topo.routes[0], name: "bad-listener", parentSectionName: "missing" };
    seed({ ...topo, routes: [bad] });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/listener missing not found/)).toBeTruthy();
    fireEvent.click(getByText("bad-listener"));
    expect(getByText(/No Gateway listener matches this route section/)).toBeTruthy();
  });

  it("shows the error block when topology.error is set", () => {
    seed({ gateway: topo.gateway, routes: [], error: "get gateway failed" });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/get gateway failed/i)).toBeTruthy();
  });

  it("shows no-routes empty state", () => {
    seed({ gateway: topo.gateway, routes: [], warnings: [] });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/No HTTPRoutes attached/i)).toBeTruthy();
  });

  it("shows no namespace filter when routes span a single namespace", () => {
    seed(topo); // one route in "apps"
    const { queryByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(queryByText("All")).toBeNull();
  });

  it("shows a namespace filter when routes span multiple namespaces", () => {
    seed(multiTopo);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText("All")).toBeTruthy();
    expect(getByText("apps")).toBeTruthy();
    expect(getByText("monitoring")).toBeTruthy();
    // both routes visible by default
    expect(getByText("share")).toBeTruthy();
    expect(getByText("grafana")).toBeTruthy();
  });

  it("filtering by namespace narrows the visible routes", () => {
    seed(multiTopo);
    const { getByText, queryByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("monitoring"));
    expect(getByText("grafana")).toBeTruthy();
    expect(queryByText("share")).toBeNull();
    // returning to All restores everything
    fireEvent.click(getByText("All"));
    expect(queryByText("share")).toBeTruthy();
  });

  it("falls back to a dropdown (not chips) above the namespace threshold", () => {
    const routes = Array.from({ length: 12 }, (_, i) => route(`ns-${i}`, `r-${i}`, `/r${i}`));
    seed({ gateway: topo.gateway, routes, warnings: [] });
    const { getByRole, queryByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    const select = getByRole("combobox") as HTMLSelectElement;
    expect(select).toBeTruthy();
    expect(queryByText("All")).toBeNull(); // no chip wall
    // selecting a namespace narrows the lanes
    fireEvent.change(select, { target: { value: "ns-3" } });
    expect(queryByText("r-3")).toBeTruthy();
    expect(queryByText("r-0")).toBeNull();
  });

  it("uses chips (no dropdown) at or below the threshold", () => {
    seed(multiTopo); // 2 namespaces
    const { queryByRole, getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(queryByRole("combobox")).toBeNull();
    expect(getByText("All")).toBeTruthy();
  });

  it("hides the detail panel for a selected route filtered out of view", () => {
    seed(multiTopo);
    const { getByText, queryByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share")); // select apps/share -> detail panel shows
    expect(queryByText("HTTPRoute")).toBeTruthy(); // detail-only badge
    fireEvent.click(getByText("monitoring")); // filter away apps
    expect(queryByText("share")).toBeNull(); // lane gone
    expect(queryByText("HTTPRoute")).toBeNull(); // detail panel gone too
  });

  it("renders policy chips on the gateway header, route, and service", () => {
    const withPolicies: TopologyDTO = {
      gateway: { ...topo.gateway, policies: [{ kind: "ClientTrafficPolicy", namespace: "infra", name: "ctp", targetKind: "Gateway", targetNamespace: "infra", targetName: "eg", targetSectionName: "", summary: "http2", details: [], inferred: false, match: "" }] },
      routes: [{
        ...topo.routes[0],
        policies: [{ kind: "BackendTrafficPolicy", namespace: "apps", name: "btp", targetKind: "HTTPRoute", targetNamespace: "apps", targetName: "share", targetSectionName: "", summary: "retries", details: [{ key: "retries", value: "3" }], inferred: false, match: "" }],
        services: [{ ...topo.routes[0].services[0], policies: [{ kind: "BackendTLSPolicy", namespace: "apps", name: "btls", targetKind: "Service", targetNamespace: "apps", targetName: "share-api", targetSectionName: "", summary: "hostname", details: [], inferred: false, match: "" }] }],
      }],
      warnings: [],
    };
    seed(withPolicies);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/CTP/)).toBeTruthy();   // gateway header
    expect(getByText(/BTP/)).toBeTruthy();   // route box
    expect(getByText(/BTLS/)).toBeTruthy();  // service box
  });

  it("shows attached policies (with target + detail rows) in the route detail panel", () => {
    const withPolicies: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{
        ...topo.routes[0],
        policies: [{ kind: "BackendTrafficPolicy", namespace: "apps", name: "backend-retries", targetKind: "HTTPRoute", targetNamespace: "apps", targetName: "share", targetSectionName: "", summary: "retries + timeout", details: [{ key: "retries", value: "3" }, { key: "request timeout", value: "30s" }], inferred: false, match: "" }],
        services: [topo.routes[0].services[0]],
      }],
      warnings: [],
    };
    seed(withPolicies);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share")); // open the detail panel
    expect(getByText(/attached policies/i)).toBeTruthy();
    expect(getByText((_, el) => el?.tagName === "SPAN" && el.textContent === "BackendTrafficPolicy apps/backend-retries")).toBeTruthy();
    expect(getByText((_, el) => el?.tagName === "DIV" && /^Target: HTTPRoute apps\/share/.test(el.textContent ?? ""))).toBeTruthy();
    expect(getByText(/request timeout/)).toBeTruthy();
    expect(getByText(/Gateway policies are shown in the topology header/i)).toBeTruthy();
  });

  it("opens an attached policy raw resource from the route detail panel", () => {
    const withPolicies: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{
        ...topo.routes[0],
        policies: [{ kind: "BackendTrafficPolicy", namespace: "apps", name: "backend-retries", targetKind: "HTTPRoute", targetNamespace: "apps", targetName: "share", targetSectionName: "", summary: "retries + timeout", details: [], inferred: false, match: "" }],
        services: [topo.routes[0].services[0]],
      }],
      warnings: [],
    };
    seed(withPolicies);
    const { getByText, getByLabelText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    fireEvent.click(getByLabelText("open BackendTrafficPolicy"));
    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "backendtrafficpolicies", kind: "BackendTrafficPolicy", scope: "Namespaced" },
      instance: { namespace: "apps", name: "backend-retries" },
    });
  });

  it("renders inferred CNP chips on the pods box and cluster-wide CCNPs in the header", () => {
    const cnp = { kind: "CiliumNetworkPolicy", namespace: "apps", name: "share-allow", targetKind: "Pods", targetNamespace: "apps", targetName: "share-api", targetSectionName: "", summary: "ingress", details: [], inferred: true, match: "selector" };
    const ccnp = { kind: "CiliumClusterwideNetworkPolicy", namespace: "", name: "cluster-deny", targetKind: "Pods", targetNamespace: "", targetName: "", targetSectionName: "", summary: "ingress default-deny", details: [], inferred: true, match: "cluster-wide" };
    const withCilium: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{ ...topo.routes[0], services: [{ ...topo.routes[0].services[0], cnps: [cnp] }] }],
      clusterPolicies: [ccnp],
      warnings: [],
    };
    seed(withCilium);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText("CNP")).toBeTruthy();          // pods box (exact - avoids matching "CCNP")
    expect(getByText(/cluster-wide policies/i)).toBeTruthy(); // header group label
    expect(getByText("CCNP")).toBeTruthy();         // header chip (exact)
  });

  it("route detail shows inferred CNPs with honest pod-target wording", () => {
    const cnp = { kind: "CiliumNetworkPolicy", namespace: "apps", name: "share-allow", targetKind: "Pods", targetNamespace: "apps", targetName: "share-api", targetSectionName: "", summary: "ingress", details: [{ key: "L7", value: "http" }], inferred: true, match: "selector" };
    const withCilium: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{ ...topo.routes[0], services: [{ ...topo.routes[0].services[0], cnps: [cnp] }] }],
      warnings: [],
    };
    seed(withCilium);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    expect(getByText(/inferred network policies/i)).toBeTruthy();
    expect(getByText(/Pods selected via Service apps\/share-api/)).toBeTruthy();
    expect(getByText(/Inferred via: selector/)).toBeTruthy();
  });

  it("opens an inferred CiliumNetworkPolicy raw resource from the route detail panel", () => {
    const cnp = { kind: "CiliumNetworkPolicy", namespace: "apps", name: "share-allow", targetKind: "Pods", targetNamespace: "apps", targetName: "share-api", targetSectionName: "", summary: "ingress", details: [], inferred: true, match: "selector" };
    const withCilium: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{ ...topo.routes[0], services: [{ ...topo.routes[0].services[0], cnps: [cnp] }] }],
      warnings: [],
    };
    seed(withCilium);
    const { getByText, getByLabelText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    fireEvent.click(getByText("share"));
    fireEvent.click(getByLabelText("open CiliumNetworkPolicy"));
    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "cilium.io", version: "v2", plural: "ciliumnetworkpolicies", kind: "CiliumNetworkPolicy", scope: "Namespaced" },
      instance: { namespace: "apps", name: "share-allow" },
    });
  });

  it("renders a ⇄ global cross-cluster edge on the pods box for a global service", () => {
    const withGlobal: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{ ...topo.routes[0], services: [{ ...topo.routes[0].services[0], global: true, meshClusters: ["homelab-orange"], meshUnconfirmed: false }] }],
      warnings: [],
    };
    seed(withGlobal);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    // Tightened from /global/i: the retired-placeholder caption also contains
    // "⇄ global …", so match the edge's distinctive peer arrow instead.
    expect(getByText(/⇄ global → homelab-orange/)).toBeTruthy();
    expect(getByText(/homelab-orange/)).toBeTruthy();
  });

  it("shows '(peers unverified)' when meshUnconfirmed and no confirmed peers", () => {
    const withGlobal: TopologyDTO = {
      gateway: topo.gateway,
      routes: [{ ...topo.routes[0], services: [{ ...topo.routes[0].services[0], global: true, meshClusters: [], meshUnconfirmed: true }] }],
      warnings: [],
    };
    seed(withGlobal);
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/peers unverified/i)).toBeTruthy();
  });

  it("renders populated route metrics on the lane", () => {
    seed(topo, false, {
      routeMetrics: { "apps/share": { rps: 12.4, p50: 8, p99: 42, errRate: 0.003 } },
      routeMetricsStatus: { available: true, message: "", updatedAt: new Date().toISOString() },
    });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/12 rps/)).toBeTruthy(); // fmtRps rounds >=10
    expect(getByText(/p99 42ms/)).toBeTruthy();
    expect(getByText(/err 0.3%/)).toBeTruthy();
  });

  it("shows labels with — for an idle route (no fake zeros)", () => {
    seed(topo, false, {
      routeMetrics: { "apps/share": { rps: 0, p50: null, p99: null, errRate: null } },
      routeMetricsStatus: { available: true, message: "", updatedAt: new Date().toISOString() },
    });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/0 rps/)).toBeTruthy();
    expect(getByText(/p50 —/)).toBeTruthy(); // label shown even when nil
  });

  it("shows the unavailable caption when route metrics are unavailable", () => {
    seed(topo, false, {
      routeMetricsStatus: { available: false, message: "Envoy Gateway not detected", updatedAt: "" },
    });
    const { getByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    expect(getByText(/route metrics unavailable: Envoy Gateway not detected/)).toBeTruthy();
  });

  it("no global edge for a non-global service", () => {
    seed(topo); // share-api global:false
    const { queryByText } = render(<GatewayTopology cluster="x" gateway={gateway} />);
    // Tightened from /⇄ global/: the caption legitimately contains "⇄ global
    // services …"; match only the edge forms ("⇄ global →" / "⇄ global (").
    expect(queryByText(/⇄ global(?: →| \()/)).toBeNull();
  });

  // --- Route filter + worst-first ordering ---

  describe("route filter", () => {
    function seedMany() {
      const t = { ...topo, routes: [route("apps", "share", "/share"), route("apps", "billing", "/billing"), route("apps", "grafana", "/")] };
      t.routes[2].hostnames = ["grafana.example.com"];
      seed(t);
    }

    it("filters lanes by route name and shows the narrowed count", () => {
      seedMany();
      const { getByLabelText, queryByText, getByText } = render(<GatewayTopology cluster="c" gateway={gateway} />);
      fireEvent.change(getByLabelText("filter routes"), { target: { value: "billing" } });
      expect(getByText("billing")).toBeTruthy();
      expect(queryByText("share")).toBeNull();
      expect(getByText("1 of 3 routes")).toBeTruthy();
    });

    it("matches hostnames and backend service names too", () => {
      seedMany();
      const { getByLabelText, getByText, queryByText } = render(<GatewayTopology cluster="c" gateway={gateway} />);
      const input = getByLabelText("filter routes");
      fireEvent.change(input, { target: { value: "grafana.example" } });
      expect(getByText("grafana")).toBeTruthy();
      fireEvent.change(input, { target: { value: "billing-svc" } });
      expect(getByText("billing")).toBeTruthy();
      expect(queryByText("grafana")).toBeNull();
    });

    it("shows an honest empty state when nothing matches", () => {
      seedMany();
      const { getByLabelText, getByText } = render(<GatewayTopology cluster="c" gateway={gateway} />);
      fireEvent.change(getByLabelText("filter routes"), { target: { value: "zzz-nope" } });
      expect(getByText("No routes match the current filter.")).toBeTruthy();
    });

    it("broken routes sort above healthy ones", () => {
      const broken = route("apps", "zz-broken", "/zz");
      broken.accepted = false;
      const t = { ...topo, routes: [route("apps", "aa-healthy", "/aa"), broken] };
      seed(t);
      const { getAllByText } = render(<GatewayTopology cluster="c" gateway={gateway} />);
      // Lane order in the DOM: the rejected route's name renders before the healthy one's.
      const names = getAllByText(/aa-healthy|zz-broken/).map((el) => el.textContent);
      expect(names[0]).toBe("zz-broken");
    });

    it("a broken lane states 'no data path' in the traffic node", () => {
      const broken = route("apps", "rejected-app", "/x");
      broken.accepted = false;
      seed({ ...topo, routes: [broken] });
      const { getByText } = render(<GatewayTopology cluster="c" gateway={gateway} />);
      expect(getByText("— no data path")).toBeTruthy();
    });

    it("treats a resolved backend with no ready endpoints as an issue", () => {
      const noEndpoints = route("apps", "empty-backend", "/empty");
      noEndpoints.pods = { ready: 0, total: 0, unknown: false };
      seed({ ...topo, routes: [noEndpoints] });
      const { getByText } = render(<GatewayTopology cluster="c" gateway={gateway} />);
      expect(getByText(/backend has no ready endpoints/)).toBeTruthy();
      expect(getByText("— no data path")).toBeTruthy();
    });
  });
});
