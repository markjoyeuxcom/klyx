import { describe, it, expect, beforeEach, vi } from "vitest";
import { render } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { ClusterDetail } from "./ClusterDetail";

// The Flux rows hook is the transport; this test seeds the gitops slice it fills.
vi.mock("../health/useFluxRows", async (orig) => ({ ...(await orig<typeof import("../health/useFluxRows")>()), useFluxRows: () => {} }));
vi.mock("../bridge/gitops", () => ({ getResourceDetail: async () => {} }));

vi.mock("../bridge/metrics", () => ({
  getClusterMetrics: vi.fn(),
  getClusterSparklines: vi.fn().mockResolvedValue({ available: false, message: "", cpu: [], mem: [] }),
}));

vi.mock("../bridge/crd", () => ({
  listCRDs: vi.fn(async () => {}),
  countKind: vi.fn(async () => {}),
  loadInstances: vi.fn(async () => {}),
}));

const dto = (over: Partial<ClusterDTO>): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v9",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "", region: "", provider: "", group: "", ageSeconds: 0, ...over,
});

beforeEach(() => useFleet.setState({ clusters: [], route: { name: "fleet" } }));

describe("ClusterDetail", () => {
  it("renders Overview for the selected cluster", () => {
    useFleet.setState({ clusters: [dto({ name: "x" })], route: { name: "cluster", cluster: "x", section: "overview" } });
    const { getByText } = render(<ClusterDetail />);
    expect(getByText("v9")).toBeTruthy();
  });
  it("renders the GitOps view for the gitops section", () => {
    useFleet.setState({
      clusters: [dto({ name: "x", gitopsTier: "Healthy" })],
      route: { name: "cluster", cluster: "x", section: "gitops" },
      gitops: { cluster: "x", resources: [], sources: [], loading: false, expandedKey: null, detail: null },
    });
    const { getByText } = render(<ClusterDetail />);
    expect(getByText(/No Flux resources on this cluster/i)).toBeTruthy();
  });
  it("shows a notice when the cluster is gone", () => {
    useFleet.setState({ clusters: [], route: { name: "cluster", cluster: "ghost", section: "overview" } });
    const { getByText } = render(<ClusterDetail />);
    expect(getByText(/no longer in the fleet/i)).toBeTruthy();
  });

  it("renders BuiltinsView for the resources section", () => {
    useFleet.setState({
      clusters: [dto({ name: "x" })],
      route: { name: "cluster", cluster: "x", section: "resources" },
      crd: { cluster: "x", groups: [], loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null },
    });
    const { getByRole } = render(<ClusterDetail />);
    expect(getByRole("heading", { name: "Built-in resources" })).toBeTruthy();
  });

  it("renders CRDBrowser for the crds section", () => {
    useFleet.setState({
      clusters: [dto({ name: "x" })],
      route: { name: "cluster", cluster: "x", section: "crds" },
      crd: { cluster: "x", groups: [], loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null },
    });
    const { getByText } = render(<ClusterDetail />);
    expect(getByText(/No custom resources/i)).toBeTruthy();
  });

  it("renders InstanceList drill from resources section", () => {
    useFleet.setState({
      clusters: [dto({ name: "x" })],
      route: { name: "cluster", cluster: "x", section: "resources", resource: { group: "", version: "v1", plural: "configmaps", kind: "ConfigMap", scope: "Namespaced" } },
      instances: { cluster: "x", ref: { group: "", version: "v1", plural: "configmaps", kind: "ConfigMap", scope: "Namespaced" }, rows: [], nextToken: "", loading: false, filter: "", riskOnly: false },
    });
    // InstanceList renders without crashing (verifies drill wiring for resources)
    render(<ClusterDetail />);
  });

  it("renders InstanceList drill from crds section", () => {
    useFleet.setState({
      clusters: [dto({ name: "x" })],
      route: { name: "cluster", cluster: "x", section: "crds", resource: { group: "cilium.io", version: "v2", plural: "ciliumendpoints", kind: "CiliumEndpoint", scope: "Namespaced" } },
      instances: { cluster: "x", ref: { group: "cilium.io", version: "v2", plural: "ciliumendpoints", kind: "CiliumEndpoint", scope: "Namespaced" }, rows: [], nextToken: "", loading: false, filter: "", riskOnly: false },
    });
    // InstanceList renders without crashing (verifies drill wiring for crds)
    render(<ClusterDetail />);
  });
});
