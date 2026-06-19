import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { Sidebar } from "./Sidebar";

const cluster = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({
  name: "homelab-nelli",
  state: "Synced",
  reason: "",
  nodesReady: 1,
  nodesTotal: 1,
  pods: 69,
  version: "v1.36.1",
  gitopsTier: "Healthy",
  gitopsReason: "",
  fluxPresent: true,
  fluxHealthy: true,
  networkTier: "Healthy",
  networkReason: "",
  gatewayAPIVersion: "v1",
  ciliumPresent: true,
  clusterMesh: true,
  env: "local",
  region: "",
  provider: "",
  group: "",
  ageSeconds: 10,
  ...over,
});

beforeEach(() => {
  localStorage.clear();
  useFleet.setState({
    clusters: [],
    route: { name: "fleet" },
    fleetBoard: {},
    metrics: { cluster: null, dto: null, loading: false },
    crd: { cluster: null, groups: [], loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null },
  });
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
  it("renders sections in triage-first grouped order when expanded", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    const { getAllByRole } = render(<Sidebar />);
    // Collect visible text labels from buttons (expanded mode shows text spans).
    const buttons = getAllByRole("button");
    const labels = buttons
      .map((b) => b.textContent?.trim())
      .filter(Boolean);
    // Fleet is first; then the 11 sections (grouped); then Terminal, Settings, collapse sidebar.
    const expectedOrder = [
      "Fleet",
      "Forwards",
      "Overview",
      "Workloads", "Pods", "Nodes", "Events",
      "Flux", "Argo CD", "Helm",
      "Network",
      "Resources", "CRDs",
      "Terminal", "Settings",
    ];
    expectedOrder.forEach((label, i) => {
      expect(labels[i]).toBe(label);
    });
  });

  it("gitops section button is labelled Flux (design principle 8)", () => {
    const { getByLabelText } = render(<Sidebar />);
    expect(getByLabelText("Flux")).toBeTruthy();
  });

  it("renders 4 dividers between the 5 section groups", () => {
    const { getAllByRole } = render(<Sidebar />);
    const dividers = getAllByRole("separator");
    expect(dividers).toHaveLength(4);
  });
});

describe("Sidebar cluster capabilities", () => {
  it("renders the expanded cluster capability strip from capability data", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.setState({
      clusters: [cluster()],
      route: { name: "cluster", cluster: "homelab-nelli", section: "overview" },
      metrics: {
        cluster: "homelab-nelli",
        loading: false,
        dto: { available: true, mode: "prometheus", source: "monitoring/kube-prometheus-stack", warning: "", reason: "", cpuFraction: null, memFraction: null },
      },
    });

    const { getByLabelText } = render(<Sidebar />);
    const block = getByLabelText("cluster capabilities");

    expect(within(block).getByText("flux")).toBeTruthy();
    expect(within(block).getByText("ready")).toBeTruthy();
    expect(within(block).getByText("cilium")).toBeTruthy();
    expect(within(block).getByText("mesh")).toBeTruthy();
    expect(within(block).getByText("gateway api")).toBeTruthy();
    expect(within(block).getByText("v1")).toBeTruthy();
    expect(within(block).getByText("cert-manager")).toBeTruthy();
    expect(within(block).getByText("prometheus")).toBeTruthy();
    expect(within(block).getByText("lgtm")).toBeTruthy();
  });

  it("uses live board and mesh data when static capability detection is stale", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.setState({
      clusters: [cluster({
        name: "homelab-orange",
        networkTier: "Absent",
        gatewayAPIVersion: "",
        ciliumPresent: false,
        clusterMesh: false,
      })],
      route: { name: "cluster", cluster: "homelab-orange", section: "overview" },
      fleetBoard: {
        "homelab-orange": {
          cpuFraction: null,
          memFraction: null,
          workloadsTotal: 32,
          broken: 0,
          flux: null,
          argo: null,
          gateway: { served: true, gateways: 1, routes: 2, brokenRoutes: 0, unprogrammed: 0 },
        },
      },
      mesh: {
        nodes: [{ cluster: "homelab-orange", name: "homelab-orange", clusterId: 3, state: "peered", present: true }],
        edges: [{ a: "homelab-orange", b: "homelab-blue", mutual: true }],
      },
      metrics: {
        cluster: "homelab-orange",
        loading: false,
        dto: { available: true, mode: "prometheus", source: "monitoring/kube-prometheus-stack", warning: "", reason: "", cpuFraction: null, memFraction: null },
      },
    });

    const { getByLabelText } = render(<Sidebar />);
    const block = getByLabelText("cluster capabilities");

    expect(within(block).getByText("cilium")).toBeTruthy();
    expect(within(block).getByText("mesh")).toBeTruthy();
    expect(within(block).getByText("gateway api")).toBeTruthy();
    expect(within(block).getByText("2 routes")).toBeTruthy();
    expect(within(block).queryByText("absent")).toBeNull();
  });

  it("keeps cluster capabilities out of collapsed mode", () => {
    useFleet.setState({
      clusters: [cluster()],
      route: { name: "cluster", cluster: "homelab-nelli", section: "overview" },
    });
    const { queryByLabelText } = render(<Sidebar />);
    expect(queryByLabelText("cluster capabilities")).toBeNull();
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
    // The label <span> inside the active button should have fontWeight 500.
    const labelSpan = overview.querySelector("span:last-child") as HTMLElement;
    expect(labelSpan.style.fontWeight).toBe("500");
  });

  it("inactive section label is normal weight in expanded mode", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    const { getByLabelText } = render(<Sidebar />);
    const workloads = getByLabelText("Workloads") as HTMLButtonElement;
    const labelSpan = workloads.querySelector("span:last-child") as HTMLElement;
    expect(labelSpan.style.fontWeight).toBe("400");
  });
});

// --- category sub-nav ---

describe("Sidebar category sub-nav", () => {
  it("expanded + resources active shows six category sub-items", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
	    useFleet.getState().setSection("resources");
	    const { getByLabelText } = render(<Sidebar />);
	    expect(getByLabelText("category Workloads")).toBeTruthy();
	    expect(getByLabelText("category Config & Secrets")).toBeTruthy();
	    expect(getByLabelText("category Services & Network")).toBeTruthy();
	    expect(getByLabelText("category Storage")).toBeTruthy();
	    expect(getByLabelText("category Cluster & Scheduling")).toBeTruthy();
	    expect(getByLabelText("category RBAC & Admission")).toBeTruthy();
	  });

	  it("keeps resource sub-items inside a scrollable sidebar nav area", () => {
	    localStorage.setItem("klyx-sidebar-expanded", "1");
	    useFleet.getState().openCluster("x");
	    useFleet.getState().setSection("resources");
	    const { getByTestId } = render(<Sidebar />);
	    expect(getByTestId("sidebar-nav-scroll").style.overflowY).toBe("auto");
	  });

  it("clicking a sub-item sets builtinCategory in the store", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
    useFleet.getState().openCluster("x");
    useFleet.getState().setSection("resources");
    const setBuiltinCategory = vi.spyOn(useFleet.getState(), "setBuiltinCategory");
    const { getByLabelText } = render(<Sidebar />);
	    fireEvent.click(getByLabelText("category Config & Secrets"));
	    expect(useFleet.getState().crd.builtinCategory).toBe("Config & Secrets");
	    setBuiltinCategory.mockRestore();
	  });

  it("collapsed mode does not show sub-items even when resources is active", () => {
    // Default is collapsed (no localStorage entry)
	    useFleet.getState().openCluster("x");
	    useFleet.getState().setSection("resources");
	    const { queryByLabelText } = render(<Sidebar />);
	    expect(queryByLabelText("category Config & Secrets")).toBeNull();
	  });

  it("expanded + non-resources section active does not show sub-items", () => {
    localStorage.setItem("klyx-sidebar-expanded", "1");
	    useFleet.getState().openCluster("x");
	    useFleet.getState().setSection("workloads");
	    const { queryByLabelText } = render(<Sidebar />);
	    expect(queryByLabelText("category Config & Secrets")).toBeNull();
	  });

  it("active category sub-item gets active background when builtinCategory matches", () => {
	    localStorage.setItem("klyx-sidebar-expanded", "1");
	    useFleet.getState().openCluster("x");
	    useFleet.getState().setSection("resources");
	    useFleet.setState({ crd: { ...useFleet.getState().crd, builtinCategory: "Config & Secrets" } });
	    const { getByLabelText } = render(<Sidebar />);
	    const btn = getByLabelText("category Config & Secrets") as HTMLButtonElement;
	    expect(btn.style.background).toContain("--color-background-primary");
	  });

  it("no sub-item highlighted when builtinCategory is null", () => {
	    localStorage.setItem("klyx-sidebar-expanded", "1");
	    useFleet.getState().openCluster("x");
	    useFleet.getState().setSection("resources");
	    const { getByLabelText } = render(<Sidebar />);
	    const btn = getByLabelText("category Config & Secrets") as HTMLButtonElement;
    // background should be transparent (not the active color)
    expect(btn.style.background).toBe("transparent");
  });
});
