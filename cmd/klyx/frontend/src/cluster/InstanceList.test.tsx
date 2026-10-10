import { describe, it, expect, beforeEach, beforeAll, afterAll, vi } from "vitest";
import { render, fireEvent, waitFor, act } from "@testing-library/react";
import { useFleet, ResourceRef, InstanceDTO, ClusterDTO } from "../store/fleet";
import { InstanceList } from "./InstanceList";

vi.mock("../bridge/crd", () => ({
  loadInstances: vi.fn(async () => {}),
  listInstancePage: vi.fn(async () => ({ items: [], nextToken: "" })),
  // The open instance now renders as a side panel inside the list, so the
  // detail bridge loads too.
  getInstanceDetail: vi.fn(async () => {}),
  copyText: vi.fn(async () => {}),
  revealSecretKey: vi.fn(async () => null),
  dryRunInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
  applyInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
}));
import { loadInstances, listInstancePage } from "../bridge/crd";

const nsRef: ResourceRef = { group: "cilium.io", version: "v2", plural: "ciliumendpoints", kind: "CiliumEndpoint", scope: "Namespaced" };
const clusterRef: ResourceRef = { group: "cilium.io", version: "v2", plural: "ciliumnodes", kind: "CiliumNode", scope: "Cluster" };
const serviceRef: ResourceRef = { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" };
const endpointSliceRef: ResourceRef = { group: "discovery.k8s.io", version: "v1", plural: "endpointslices", kind: "EndpointSlice", scope: "Namespaced" };
const rows: InstanceDTO[] = [
  { namespace: "kube-system", name: "coredns-abc", created: "" },
  { namespace: "monitoring", name: "prometheus-0", created: "" },
];

function seed(ref: ResourceRef, over: Partial<{ rows: InstanceDTO[]; nextToken: string; loading: boolean; filter: string }> = {}) {
  useFleet.setState({ instances: { cluster: "x", ref, rows, nextToken: "", loading: false, filter: "", riskOnly: false, ...over } });
}

const cluster = (name: string, state = "Synced"): ClusterDTO => ({
  name,
  state,
  reason: "",
  nodesReady: 1,
  nodesTotal: 1,
  pods: 1,
  version: "v1",
  gitopsTier: "Healthy",
  gitopsReason: "",
  networkTier: "Healthy",
  networkReason: "",
  env: "local",
  region: "lab",
  provider: "k3s",
  group: "homelab",
  ageSeconds: 1,
});

// jsdom has no layout: report a wide workspace so priority-fitted columns all
// fit (narrow folding is covered by the template tests).
const widthDesc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
beforeAll(() => { Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 1800 }); });
afterAll(() => { if (widthDesc) Object.defineProperty(HTMLElement.prototype, "clientWidth", widthDesc); });

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.setState({ clusters: [], route: { name: "fleet" } });
  seed(nsRef);
});

describe("InstanceList", () => {
  it("renders rows with namespace for a namespaced kind", () => {
    const { getByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(getByText("coredns-abc")).toBeTruthy();
    expect(getByText("kube-system")).toBeTruthy();
  });

  it("shows service networking columns in the initial list", () => {
    seed(serviceRef, {
      rows: [{
        namespace: "envoy-gateway-system",
        name: "external-gateway",
        created: "",
        fields: { type: "LoadBalancer", clusterIP: "10.43.12.9", externalIP: "192.0.2.10", ports: "https 443/TCP->8443" },
      }],
    });
    const { getByText } = render(<InstanceList cluster="x" resource={serviceRef} />);
    expect(getByText("type")).toBeTruthy();
    expect(getByText("cluster ip")).toBeTruthy();
    expect(getByText("external ip")).toBeTruthy();
    expect(getByText("LoadBalancer")).toBeTruthy();
    expect(getByText("10.43.12.9")).toBeTruthy();
    expect(getByText("192.0.2.10")).toBeTruthy();
    expect(getByText("https 443/TCP->8443")).toBeTruthy();
  });

  it("shows EndpointSlice service, endpoint, address, and port columns", () => {
    seed(endpointSliceRef, {
      rows: [{
        namespace: "apps",
        name: "api-abc",
        created: "",
        fields: { service: "api", addressType: "IPv4", endpoints: "2/3", addresses: "10.0.0.1, 10.0.0.2", ports: "http 8080/TCP" },
      }],
    });
    const { getByText } = render(<InstanceList cluster="x" resource={endpointSliceRef} />);
    expect(getByText("service")).toBeTruthy();
    expect(getByText("addr")).toBeTruthy();
    expect(getByText("ready")).toBeTruthy();
    expect(getByText("api")).toBeTruthy();
    expect(getByText("IPv4")).toBeTruthy();
    expect(getByText("2/3")).toBeTruthy();
    expect(getByText("10.0.0.1, 10.0.0.2")).toBeTruthy();
    expect(getByText("http 8080/TCP")).toBeTruthy();
  });

  it.each([
    {
      ref: { group: "", version: "v1", plural: "persistentvolumeclaims", kind: "PersistentVolumeClaim", scope: "Namespaced" },
      fields: { status: "Bound", class: "fast", size: "20Gi", modes: "RWO", volume: "pvc-123" },
      labels: ["status", "class", "size", "modes", "volume"],
      values: ["Bound", "fast", "20Gi", "RWO", "pvc-123"],
    },
    {
      ref: { group: "networking.k8s.io", version: "v1", plural: "ingresses", kind: "Ingress", scope: "Namespaced" },
      fields: { class: "external", hosts: "api.example.com", address: "192.0.2.20", tls: "api-tls", backends: "api:8080" },
      labels: ["class", "hosts", "address", "tls", "backends"],
      values: ["external", "api.example.com", "192.0.2.20", "api-tls", "api:8080"],
    },
    {
      ref: { group: "autoscaling", version: "v2", plural: "horizontalpodautoscalers", kind: "HorizontalPodAutoscaler", scope: "Namespaced" },
      fields: { target: "Deployment/api", replicas: "2/3/4/5", metrics: "cpu 42%/80%" },
      labels: ["target", "min/current/desired/max", "metrics"],
      values: ["Deployment/api", "2/3/4/5", "cpu 42%/80%"],
    },
    {
      ref: { group: "policy", version: "v1", plural: "poddisruptionbudgets", kind: "PodDisruptionBudget", scope: "Namespaced" },
      fields: { allowed: "1", healthy: "4/3", expected: "5" },
      labels: ["allowed", "healthy", "expected"],
      values: ["4/3", "5"],
    },
    {
      ref: { group: "batch", version: "v1", plural: "jobs", kind: "Job", scope: "Namespaced" },
      fields: { active: "1", succeeded: "2", failed: "1", completions: "2/3" },
      labels: ["active", "succeeded", "failed", "complete"],
      values: ["2/3"],
    },
    {
      ref: { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" },
      fields: { type: "Opaque", keys: "3", immutable: "yes" },
      labels: ["type", "keys", "immutable"],
      values: ["Opaque", "3", "yes"],
    },
    {
      ref: { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" },
      fields: { ready: "ready", issuer: "ClusterIssuer/letsencrypt", expires: "2026-09-01", renew: "2026-08-01", dns: "api.example.com" },
      labels: ["ready", "issuer", "expires", "renew", "dns"],
      values: ["ClusterIssuer/letsencrypt", "2026-09-01", "2026-08-01", "api.example.com"],
    },
    {
      ref: { group: "networking.k8s.io", version: "v1", plural: "networkpolicies", kind: "NetworkPolicy", scope: "Namespaced" },
      fields: { selector: "app=api", policyTypes: "Ingress,Egress", ingress: "1", egress: "1" },
      labels: ["selector", "types", "ingress", "egress"],
      values: ["app=api", "Ingress,Egress"],
    },
    {
      ref: { group: "cilium.io", version: "v2", plural: "ciliumnetworkpolicies", kind: "CiliumNetworkPolicy", scope: "Namespaced" },
      fields: { selector: "app=api", ingress: "1 +1 deny", egress: "2", scope: "namespace" },
      labels: ["selector", "ingress", "egress", "scope"],
      values: ["app=api", "1 +1 deny", "2", "namespace"],
    },
    {
      ref: { group: "external-secrets.io", version: "v1beta1", plural: "externalsecrets", kind: "ExternalSecret", scope: "Namespaced" },
      fields: { ready: "ready", store: "ClusterSecretStore/akv", target: "db-secret", refresh: "1h", synced: "2026-06-14" },
      labels: ["ready", "store", "target secret", "refresh", "synced"],
      values: ["ClusterSecretStore/akv", "db-secret", "1h", "2026-06-14"],
    },
    {
      ref: { group: "helm.toolkit.fluxcd.io", version: "v2", plural: "helmreleases", kind: "HelmRelease", scope: "Namespaced" },
      fields: { ready: "not ready", suspended: "no", chart: "cilium", source: "HelmRepository/cilium", revision: "1.2.3" },
      labels: ["ready", "suspend", "chart", "source", "revision"],
      values: ["not ready", "cilium", "HelmRepository/cilium", "1.2.3"],
    },
  ] satisfies Array<{ ref: ResourceRef; fields: Record<string, string>; labels: string[]; values: string[] }>)("shows useful list columns for $ref.kind", ({ ref, fields, labels, values }) => {
    seed(ref, { rows: [{ namespace: "apps", name: "sample", created: "", fields: fields as unknown as Record<string, string> }] });
    const { getAllByText } = render(<InstanceList cluster="x" resource={ref} />);
    labels.forEach((label) => expect(getAllByText(label).length).toBeGreaterThan(0));
    values.forEach((value) => expect(getAllByText(value).length).toBeGreaterThan(0));
  });

  it("omits the namespace column for a cluster-scoped kind", () => {
    seed(clusterRef, { rows: [{ namespace: "", name: "node-1", created: "" }] });
    const { getByText, queryByText } = render(<InstanceList cluster="x" resource={clusterRef} />);
    expect(getByText("node-1")).toBeTruthy();
    expect(queryByText("namespace")).toBeNull();
  });

  it("shows Load more only when nextToken is set and calls the bridge with it", () => {
    seed(nsRef, { nextToken: "tok-2" });
    const { getByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    fireEvent.click(getByText(/load more/i));
    expect(loadInstances).toHaveBeenCalledWith("x", nsRef, "tok-2");
  });

  it("header and rows share the one LensTable scroll surface", () => {
    const { getByTestId } = render(<InstanceList cluster="x" resource={nsRef} />);
    const head = getByTestId("instance-list-header");
    expect(head.classList.contains("lens-table-head")).toBe(true);
    expect(head.closest(".lens-table")).toBeTruthy();
  });

  it("breadcrumb back returns to the resource catalog", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource: nsRef } });
    const { getByLabelText } = render(<InstanceList cluster="x" resource={nsRef} />);
    fireEvent.click(getByLabelText("back to resources"));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.resource).toBeUndefined();
  });

  it("hides Load more when there is no nextToken", () => {
    const { queryByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(queryByText(/load more/i)).toBeNull();
  });

  it("filters rows by substring", () => {
    seed(nsRef, { filter: "prometheus" });
    const { queryByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(queryByText("prometheus-0")).toBeTruthy();
    expect(queryByText("coredns-abc")).toBeNull();
  });

  it("filters supported resources to rows needing attention", () => {
    seed(serviceRef, {
      rows: [
        { namespace: "infra", name: "ok", created: "", fields: { type: "ClusterIP", externalIP: "-", clusterIP: "10.0.0.1", ports: "80/TCP" } },
        { namespace: "infra", name: "waiting", created: "", fields: { type: "LoadBalancer", externalIP: "pending", clusterIP: "10.0.0.2", ports: "443/TCP" } },
      ],
    });
    const { getByText, queryByText, getByRole } = render(<InstanceList cluster="x" resource={serviceRef} />);
    fireEvent.click(getByRole("button", { name: /^Attention 1/ }));
    expect(getByText("waiting")).toBeTruthy();
    expect(queryByText("ok")).toBeNull();
  });

  it("shows a filtered empty state when loaded rows do not match", () => {
    seed(nsRef, { filter: "missing" });
    const { getByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(getByText(/match the current filter/i)).toBeTruthy();
  });

  it("shows the empty state when there are no rows and not loading", () => {
    seed(nsRef, { rows: [] });
    const { getByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(getByText(/No CiliumEndpoint instances/i)).toBeTruthy();
  });

  it("clicking a row opens the instance detail", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource: nsRef } });
    seed(nsRef);
    const { getByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    fireEvent.click(getByText("coredns-abc"));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.instance).toEqual({ namespace: "kube-system", name: "coredns-abc" });
  });

  it("the open instance renders as a side panel - the list never leaves the screen", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource: nsRef } });
    seed(nsRef);
    const { getByText, getByTestId, queryByTestId, getAllByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(queryByTestId("instance-detail-panel")).toBeNull();
    fireEvent.click(getByText("coredns-abc"));
    // panel and list coexist: the name appears in both the row and the panel header
    expect(getByTestId("instance-detail-panel")).toBeTruthy();
    expect(getByTestId("instance-list-header")).toBeTruthy();
    expect(getAllByText("coredns-abc").length).toBeGreaterThanOrEqual(2);
    // Esc closes the panel, list stays
    fireEvent.keyDown(window, { key: "Escape" });
    expect(queryByTestId("instance-detail-panel")).toBeNull();
    expect(getByTestId("instance-list-header")).toBeTruthy();
  });

  it("can browse a generic resource across connected clusters", async () => {
    useFleet.setState({ clusters: [cluster("blue"), cluster("orange"), cluster("down", "Failed")] });
    vi.mocked(listInstancePage).mockImplementation(async (target) => ({
      items: [{ namespace: "apps", name: `api-${target}`, created: "", fields: { type: "ClusterIP", clusterIP: "10.0.0.1" } }],
      nextToken: "",
    }));

    const { getByText, getAllByText, getByRole } = render(<InstanceList cluster="blue" resource={serviceRef} />);
    fireEvent.click(getByRole("button", { name: "Fleet" }));

    await waitFor(() => expect(getByText("api-blue")).toBeTruthy());
    expect(getByText("api-orange")).toBeTruthy();
    expect(getAllByText("Cluster").length).toBeGreaterThan(0);
    expect(getByRole("option", { name: "blue (1)" })).toBeTruthy();
    expect(getByRole("option", { name: "orange (1)" })).toBeTruthy();
    expect(listInstancePage).toHaveBeenCalledWith("blue", serviceRef);
    expect(listInstancePage).toHaveBeenCalledWith("orange", serviceRef);
    expect(listInstancePage).not.toHaveBeenCalledWith("down", serviceRef);
  });

  it("does not refetch fleet resources when the same connected clusters refresh", async () => {
    useFleet.setState({ clusters: [cluster("blue"), cluster("orange")] });
    vi.mocked(listInstancePage).mockImplementation(async (target) => ({
      items: [{ namespace: "apps", name: `api-${target}`, created: "", fields: { type: "ClusterIP" } }],
      nextToken: "",
    }));

    const { getByRole } = render(<InstanceList cluster="blue" resource={serviceRef} />);
    fireEvent.click(getByRole("button", { name: "Fleet" }));
    await waitFor(() => expect(listInstancePage).toHaveBeenCalledTimes(2));

    vi.mocked(listInstancePage).mockClear();
    await act(async () => {
      useFleet.setState({ clusters: [cluster("blue", "Stale"), cluster("orange", "Degraded")] });
    });

    expect(listInstancePage).not.toHaveBeenCalled();
  });

  it("filters fleet resources locally by cluster tab", async () => {
    useFleet.setState({ clusters: [cluster("blue"), cluster("orange")] });
    vi.mocked(listInstancePage).mockImplementation(async (target) => ({
      items: [{ namespace: "apps", name: `api-${target}`, created: "", fields: { type: "ClusterIP" } }],
      nextToken: "",
    }));

    const { getByText, queryByText, getByRole, getByLabelText } = render(<InstanceList cluster="blue" resource={serviceRef} />);
    fireEvent.click(getByRole("button", { name: "Fleet" }));
    await waitFor(() => expect(getByText("api-orange")).toBeTruthy());
    expect(getByRole("option", { name: "All clusters (2)" })).toBeTruthy();
    expect(getByRole("option", { name: "blue (1)" })).toBeTruthy();
    expect(getByRole("option", { name: "orange (1)" })).toBeTruthy();

    vi.mocked(listInstancePage).mockClear();
    fireEvent.change(getByLabelText("Fleet cluster"), { target: { value: "orange" } });

    expect(queryByText("api-blue")).toBeNull();
    expect(getByText("api-orange")).toBeTruthy();
    expect(listInstancePage).not.toHaveBeenCalled();
  });

  it("windows large fleet-scope lists instead of rendering every row", async () => {
    useFleet.setState({ clusters: [cluster("blue"), cluster("orange")] });
    const many = (target: string) =>
      Array.from({ length: 300 }, (_, i) => ({
        namespace: "apps",
        name: `inst-${String(i).padStart(3, "0")}-${target}`,
        created: "",
        fields: { type: "ClusterIP" },
      }));
    vi.mocked(listInstancePage).mockImplementation(async (target) => ({
      items: many(target),
      nextToken: "",
    }));

    const { getByText, queryByText, getByRole } = render(<InstanceList cluster="blue" resource={serviceRef} />);
    fireEvent.click(getByRole("button", { name: "Fleet" }));
    await waitFor(() => expect(getByText("inst-000-blue")).toBeTruthy());

    // 600 merged rows: the window (jsdom fallback 600px / 28px rows + overscan)
    // renders the head of the list, not the full fleet.
    expect(getByRole("option", { name: "All clusters (600)" })).toBeTruthy();
    expect(queryByText("inst-299-orange")).toBeNull();
  });

  it("windows large cluster-scope lists too", () => {
    seed(nsRef, {
      rows: Array.from({ length: 400 }, (_, i) => ({
        namespace: "apps",
        name: `ep-${String(i).padStart(3, "0")}`,
        created: "",
      })),
    });
    const { getByText, queryByText } = render(<InstanceList cluster="x" resource={nsRef} />);
    expect(getByText("ep-000")).toBeTruthy();
    expect(queryByText("ep-399")).toBeNull();
  });

  it("opens the right cluster when a fleet resource row is selected", async () => {
    useFleet.setState({
      clusters: [cluster("blue"), cluster("orange")],
      route: { name: "cluster", cluster: "blue", section: "resources", resource: serviceRef },
    });
    vi.mocked(listInstancePage).mockImplementation(async (target) => ({
      items: [{ namespace: "apps", name: `api-${target}`, created: "", fields: { type: "ClusterIP" } }],
      nextToken: "",
    }));

    const { getByText, getByRole } = render(<InstanceList cluster="blue" resource={serviceRef} />);
    fireEvent.click(getByRole("button", { name: "Fleet" }));
    await waitFor(() => expect(getByText("api-orange")).toBeTruthy());
    fireEvent.click(getByText("api-orange"));

    const r = useFleet.getState().route;
    expect(r).toMatchObject({
      name: "cluster",
      cluster: "orange",
      section: "resources",
      resource: serviceRef,
      instance: { namespace: "apps", name: "api-orange" },
    });
  });
});

describe("InstanceList workspace template (redesign)", () => {
  const svcRows: InstanceDTO[] = [
    { namespace: "infra", name: "ok", created: "", fields: { type: "ClusterIP", externalIP: "-", clusterIP: "10.0.0.1", ports: "80/TCP" } },
    { namespace: "infra", name: "waiting", created: "", fields: { type: "LoadBalancer", externalIP: "pending", clusterIP: "10.0.0.2", ports: "443/TCP" } },
  ];
  const openRoute = (instance?: { namespace: string; name: string }) =>
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource: serviceRef, ...(instance ? { instance } : {}) } });

  it("auto-selects the first at-risk row once, from a fresh read only", () => {
    openRoute();
    seed(serviceRef, { rows: svcRows });
    render(<InstanceList cluster="x" resource={serviceRef} />);
    const instanceOf = () => { const r = useFleet.getState().route; return r.name === "cluster" ? r.instance : undefined; };
    expect(instanceOf()).toBeUndefined(); // cached rows at entry
    act(() => seed(serviceRef, { rows: [...svcRows] }));
    expect(instanceOf()).toEqual({ namespace: "infra", name: "waiting" });
    act(() => useFleet.getState().closeInstance());
    act(() => seed(serviceRef, { rows: [...svcRows] }));
    expect(instanceOf()).toBeUndefined(); // once only
  });

  it("an instance gone from a complete read is shown as removed, read-only", () => {
    openRoute({ namespace: "infra", name: "waiting" });
    seed(serviceRef, { rows: svcRows });
    const { getByText, queryByRole } = render(<InstanceList cluster="x" resource={serviceRef} />);
    act(() => seed(serviceRef, { rows: [svcRows[0]] }));
    expect(getByText("Service removed.")).toBeTruthy();
    expect(queryByRole("button", { name: "Describe" })).toBeNull();
  });

  it("an absent instance is NOT called removed while more pages exist", () => {
    openRoute({ namespace: "infra", name: "waiting" });
    seed(serviceRef, { rows: [svcRows[0]], nextToken: "tok" });
    const { queryByText } = render(<InstanceList cluster="x" resource={serviceRef} />);
    expect(queryByText("Service removed.")).toBeNull();
  });

  it("the operator's filter excluding the open instance closes it", () => {
    openRoute({ namespace: "infra", name: "ok" });
    seed(serviceRef, { rows: svcRows });
    const { getByLabelText, queryByTestId } = render(<InstanceList cluster="x" resource={serviceRef} />);
    expect(queryByTestId("lens-inspector")).toBeTruthy();
    fireEvent.change(getByLabelText("Search Service"), { target: { value: "waiting" } });
    expect(queryByTestId("lens-inspector")).toBeNull();
  });

  it("a cross-link opened under a stale filter reveals the instance instead of closing it", () => {
    openRoute();
    seed(serviceRef, { rows: svcRows, filter: "waiting" });
    const { queryByTestId } = render(<InstanceList cluster="x" resource={serviceRef} />);
    act(() => useFleet.getState().openInstance("infra", "ok"));
    expect(queryByTestId("lens-inspector")).toBeTruthy();
    expect(useFleet.getState().instances.filter).toBe("");
  });
});

describe("InstanceList column folding", () => {
  it("drops kind-specific columns before the name at a narrow width", () => {
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 560 });
    try {
      seed(serviceRef, { rows: [{ namespace: "infra", name: "a-long-service-name", created: "", fields: { type: "ClusterIP", clusterIP: "10.0.0.1", externalIP: "-", ports: "80/TCP" } }] });
      const { getByTestId, getByText } = render(<InstanceList cluster="x" resource={serviceRef} />);
      const head = getByTestId("instance-list-header").textContent ?? "";
      expect(head).toContain("Name");
      expect(head).not.toContain("ports");
      expect(getByText("a-long-service-name")).toBeTruthy();
    } finally {
      Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 1800 });
    }
  });
});
