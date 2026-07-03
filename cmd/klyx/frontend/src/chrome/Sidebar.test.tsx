import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import { Sidebar } from "./Sidebar";

beforeEach(() => {
  localStorage.clear();
  useFleet.setState({
    clusters: [],
    route: { name: "fleet" },
    fleetBoard: {},
    metrics: { cluster: null, dto: null, loading: false },
    crd: { cluster: null, groups: [], loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null },
  });
  useFleet.getState().setWorkloadKindFilter(["Deployment", "StatefulSet", "DaemonSet"]);
});

// --- legacy nav tests (preserved) ---

describe("Sidebar nav", () => {
  it("Fleet button returns to the grid", () => {
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    getByLabelText("Fleet").click();
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("a section button is disabled at the fleet root", () => {
    const { getByLabelText } = render(<Sidebar />);
    expect((getByLabelText("Flux") as HTMLButtonElement).disabled).toBe(true);
  });

  it("a section button sets the section in cluster scope", () => {
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    getByLabelText("Flux").click();
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "gitops" });
  });

  it("highlights the Overview button after openCluster", () => {
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    const overview = getByLabelText("Overview") as HTMLButtonElement;
    expect(overview.disabled).toBe(false);
    expect(overview.style.background).toContain("--color-background-primary");
  });
});

// --- daily-driver section order ---

describe("Sidebar section order", () => {
  it("renders the full grouped resource catalog when expanded", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getAllByText, getByLabelText } = render(<Sidebar />);

    [
      "Fleet",
      "Cluster",
      "Workloads",
      "Config",
      "Autoscaling",
      "Network",
      "Storage",
      "Access control",
      "Gateway API",
      "cert-manager",
      "External Secrets",
      "Argo CD",
      "Flux CD",
      "Helm",
      "Custom resources",
      "Tools",
    ].forEach((group) => expect(getAllByText(group).length).toBeGreaterThan(0));

    [
      "Fleet",
      "Port-forwards",
      "Overview",
      "Nodes",
      "Namespaces",
      "All workloads",
      "Pods",
      "Deployments",
      "ConfigMaps",
      "Services",
      "PersistentVolumeClaims",
      "Service Accounts",
      "Gateway topology",
      "Gateways",
      "BackendTLSPolicies",
      "ClientTrafficPolicies",
      "BackendTrafficPolicies",
      "SecurityPolicies",
      "Certificates",
      "Applications",
      "Kustomizations",
      "Releases",
      "Browse CRDs",
      "Settings",
    ].forEach((label) => expect(getByLabelText(label)).toBeTruthy());
  });

  it("gitops section button is labelled Flux (design principle 8)", () => {
    const { getByLabelText } = render(<Sidebar />);
    expect(getByLabelText("Flux")).toBeTruthy();
  });

  it("renders 4 dividers between the 5 collapsed section groups", () => {
    const { getAllByRole } = render(<Sidebar />);
    const dividers = getAllByRole("separator");
    expect(dividers).toHaveLength(4);
  });

  it("renders 15 dividers between the 16 expanded groups", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getAllByRole } = render(<Sidebar />);
    const dividers = getAllByRole("separator");
    expect(dividers).toHaveLength(15);
  });

  it("collapses expanded menu groups and persists the state", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getByLabelText, queryByLabelText } = render(<Sidebar />);

    expect(getByLabelText("Pods")).toBeTruthy();
    fireEvent.click(getByLabelText("collapse Workloads"));

    expect(queryByLabelText("Pods")).toBeNull();
    expect(getByLabelText("expand Workloads").getAttribute("aria-expanded")).toBe("false");
    expect(localStorage.getItem("klyx-sidebar-collapsed-groups")).toContain("Workloads");
  });

  it("restores collapsed expanded menu groups from localStorage", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    localStorage.setItem("klyx-sidebar-collapsed-groups", JSON.stringify(["Storage"]));
    const { getByLabelText, queryByLabelText } = render(<Sidebar />);

    expect(getByLabelText("expand Storage")).toBeTruthy();
    expect(queryByLabelText("PersistentVolumeClaims")).toBeNull();
  });
});

// --- expand / collapse toggle ---

describe("Sidebar expand/collapse", () => {
  it("defaults to collapsed when no stored preference", () => {
    const { getByLabelText } = render(<Sidebar />);
    // In collapsed state the toggle button has label "expand sidebar"
    expect(getByLabelText("expand sidebar")).toBeTruthy();
  });

  it("restores expanded state when stored preference is '1'", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getByLabelText } = render(<Sidebar />);
    expect(getByLabelText("collapse sidebar")).toBeTruthy();
  });

  it("clicking the toggle expands and persists '1' to localStorage", () => {
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("expand sidebar"));
    expect(localStorage.getItem("klyx-sidebar-expanded")).toBe("1");
    expect(getByLabelText("collapse sidebar")).toBeTruthy();
  });

  it("clicking the toggle again collapses and persists '0' to localStorage", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("collapse sidebar"));
    expect(localStorage.getItem("klyx-sidebar-expanded")).toBe("0");
    expect(getByLabelText("expand sidebar")).toBeTruthy();
  });
});

// --- active state in expanded mode ---

describe("Sidebar active state", () => {
  it("active section label is medium-weight in expanded mode", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    const overview = getByLabelText("Overview") as HTMLButtonElement;
    expect(overview.style.fontWeight).toBe("500");
  });

  it("inactive section label is normal weight in expanded mode", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    const pods = getByLabelText("Pods") as HTMLButtonElement;
    expect(pods.style.fontWeight).toBe("400");
  });
});

// --- expanded resource navigation ---

describe("Sidebar expanded resource navigation", () => {
  it("clicking All workloads opens the controller health lens with all controller kinds visible", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    useFleet.getState().setWorkloadKindFilter(["Deployment"]);
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("All workloads"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
    expect(useFleet.getState().workloads.kindFilter).toEqual({
      Deployment: true,
      StatefulSet: true,
      DaemonSet: true,
    });
  });

  it("clicking Deployments opens the workload health lens filtered to Deployments", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("Deployments"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
    expect(useFleet.getState().workloads.kindFilter).toEqual({
      Deployment: true,
      StatefulSet: false,
      DaemonSet: false,
    });
  });

  it("clicking StatefulSets opens the workload health lens filtered to StatefulSets", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("StatefulSets"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
    expect(useFleet.getState().workloads.kindFilter).toEqual({
      Deployment: false,
      StatefulSet: true,
      DaemonSet: false,
    });
  });

  it("clicking DaemonSets opens the workload health lens filtered to DaemonSets", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("DaemonSets"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
    expect(useFleet.getState().workloads.kindFilter).toEqual({
      Deployment: false,
      StatefulSet: false,
      DaemonSet: true,
    });
  });

  it("clicking DaemonSets from a ReplicaSet resource page filters on the first click", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    useFleet.getState().openResource({ group: "apps", version: "v1", plural: "replicasets", kind: "ReplicaSet", scope: "Namespaced" });
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("DaemonSets"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
    expect(useFleet.getState().workloads.kindFilter).toEqual({
      Deployment: false,
      StatefulSet: false,
      DaemonSet: true,
    });
  });

  it("clicking a built-in resource opens the generic resource route", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("ConfigMaps"));

    expect(useFleet.getState().route).toMatchObject({
      name: "cluster",
      section: "resources",
      resource: { group: "", version: "v1", plural: "configmaps", kind: "ConfigMap", scope: "Namespaced" },
    });
  });

  it("clicking an integration entry routes to its owned lens", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("Applications"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "argo", argoView: "applications" });
  });

  it("clicking Argo Projects routes to the project lens, not the generic resource browser", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("Projects"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "argo", argoView: "projects" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
  });

  it("clicking Argo ApplicationSets routes to the applicationset lens", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("ApplicationSets"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "argo", argoView: "applicationsets" });
    expect(useFleet.getState().route).not.toHaveProperty("resource");
  });

  it("clicking Browse CRDs routes to the CRD browser", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    fireEvent.click(getByLabelText("Browse CRDs"));

    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "crds" });
  });

  it("keeps the expanded catalog inside a scrollable sidebar nav area", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByTestId } = render(<Sidebar />);
    expect(getByTestId("sidebar-nav-scroll").style.overflowY).toBe("auto");
  });

  it("collapsed mode does not show the full resource catalog", () => {
    useFleet.getState().openCluster("x");
    const { queryByLabelText } = render(<Sidebar />);
    expect(queryByLabelText("ConfigMaps")).toBeNull();
  });

  it("disables cluster-scoped entries at the fleet root", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getByLabelText } = render(<Sidebar />);
    const configMaps = getByLabelText("ConfigMaps") as HTMLButtonElement;
    expect(configMaps.disabled).toBe(true);
  });
});

// --- capability gating (design principle 7: render only what's installed) ---

describe("Sidebar capability gating", () => {
  const cluster = (over: Record<string, unknown>) => ({
    name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.31",
    gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
    env: "", region: "", provider: "", group: "", ageSeconds: 0,
    ...over,
  });

  it("hides tool sections detection says are absent (collapsed rail)", () => {
    useFleet.setState({ clusters: [cluster({ fluxPresent: false, argoPresent: false, certManagerPresent: false, externalSecretsPresent: false, gatewayAPIVersion: "" })] as never });
    useFleet.getState().openCluster("x");
    const { queryByLabelText } = render(<Sidebar />);
    expect(queryByLabelText("Flux")).toBeNull();
    expect(queryByLabelText("Argo CD")).toBeNull();
    expect(queryByLabelText("Certificates")).toBeNull();
    expect(queryByLabelText("External Secrets")).toBeNull();
    expect(queryByLabelText("Gateway API")).toBeNull();
    // Non-tool sections stay.
    expect(queryByLabelText("Pods")).toBeTruthy();
    expect(queryByLabelText("Helm")).toBeTruthy();
  });

  it("shows tool sections that are present (collapsed rail)", () => {
    useFleet.setState({ clusters: [cluster({ fluxPresent: true, argoPresent: true, certManagerPresent: true, externalSecretsPresent: true, gatewayAPIVersion: "v1" })] as never });
    useFleet.getState().openCluster("x");
    const { queryByLabelText } = render(<Sidebar />);
    expect(queryByLabelText("Flux")).toBeTruthy();
    expect(queryByLabelText("Argo CD")).toBeTruthy();
    expect(queryByLabelText("Certificates")).toBeTruthy();
    expect(queryByLabelText("External Secrets")).toBeTruthy();
    expect(queryByLabelText("Gateway API")).toBeTruthy();
  });

  it("hides absent tool groups in the expanded menu", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.setState({ clusters: [cluster({ fluxPresent: true, argoPresent: false, certManagerPresent: false, externalSecretsPresent: false, gatewayAPIVersion: "" })] as never });
    useFleet.getState().openCluster("x");
    const { queryByText } = render(<Sidebar />);
    expect(queryByText("Flux CD")).toBeTruthy();
    expect(queryByText("Argo CD")).toBeNull();
    expect(queryByText("cert-manager")).toBeNull();
    expect(queryByText("External Secrets")).toBeNull();
    expect(queryByText("Gateway API")).toBeNull();
  });

  it("shows everything outside a cluster (fleet root, disabled)", () => {
    useFleet.setState({ clusters: [cluster({ fluxPresent: false, argoPresent: false, certManagerPresent: false })] as never });
    const { queryByLabelText } = render(<Sidebar />);
    expect(queryByLabelText("Flux")).toBeTruthy();
    expect(queryByLabelText("Certificates")).toBeTruthy();
  });
});
