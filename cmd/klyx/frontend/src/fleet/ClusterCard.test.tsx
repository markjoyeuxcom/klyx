import { beforeEach, describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { ClusterCard } from "./ClusterCard";
import type { ClusterDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";

const base: ClusterDTO = {
  name: "plt-sea-prd-we-aks-01", state: "Synced", reason: "",
  nodesReady: 12, nodesTotal: 12, pods: 487, version: "v1.30.4",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "prd", region: "we", provider: "aks", group: "prd-we", ageSeconds: 15,
};

beforeEach(() => {
  useFleet.setState({ fleetBoard: {}, mesh: null, route: { name: "fleet" } });
});

describe("ClusterCard", () => {
  it("renders name, version and counts", () => {
    const { getByText } = render(<ClusterCard c={base} />);
    expect(getByText("plt-sea-prd-we-aks-01")).toBeTruthy();
    expect(getByText(/v1\.30\.4/)).toBeTruthy(); // provider · version joined in the header
    // counter wells split value and label into separate elements
    expect(getByText("12/12")).toBeTruthy();
    expect(getByText("487")).toBeTruthy();
    expect(getByText("pods")).toBeTruthy();
  });

  it("shows the reason for a failed cluster", () => {
    const { getByText } = render(
      <ClusterCard c={{ ...base, state: "Failed", reason: "connect timed out" }} />,
    );
    expect(getByText(/connect timed out/i)).toBeTruthy();
  });

  it("shows a lock affordance for a protected cluster", () => {
    const { queryByTitle, getByText } = render(<ClusterCard c={{ ...base, protected: true }} />);
    expect(queryByTitle("protected")).toBeTruthy();
    expect(getByText(/prd lock/)).toBeTruthy();
  });

  it("has no lock for an unprotected cluster", () => {
    const { queryByTitle } = render(<ClusterCard c={{ ...base, protected: false }} />);
    expect(queryByTitle("protected")).toBeNull();
  });

  it("contains long EKS ARN context names inside the card", () => {
    const arn = "arn:aws:eks:us-east-1:934692410245:cluster/eks-prod-platform-observability-us-east-1";
    const { getByTestId, getByText, getByTitle } = render(<ClusterCard c={{ ...base, name: arn, provider: "", region: "" }} />);
    const card = getByTestId(`cluster-card-${arn}`);
    expect(card.style.minWidth).toBe("0px");
    expect(card.style.overflow).toBe("hidden");
    expect(getByText("eks-prod-platform-observability-us-east-1")).toBeTruthy();
    expect(getByTitle(arn)).toBeTruthy();
  });
});

it("drills into the cluster on click", () => {
  useFleet.setState({ route: { name: "fleet" } });
  const { getByText } = render(<ClusterCard c={base} />);
  getByText("plt-sea-prd-we-aks-01").click();
  expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "plt-sea-prd-we-aks-01" });
});

it("shows the mesh row from the graph: peered / asymmetric / standalone / off-fleet", () => {
  useFleet.setState({ mesh: {
    nodes: [
      { cluster: "ctx-blue", name: "homelab-blue", clusterId: 1, state: "peered", present: true },
      { cluster: "ctx-nelli", name: "homelab-nelli", clusterId: null, state: "enabled", present: true },
    ],
    edges: [{ a: "ctx-blue", b: "ctx-orange", mutual: true }],
  }});
  // a peered cluster shows its peer
  const blue = { name: "ctx-blue", state: "Ready", networkTier: "Healthy" } as any;
  const { getByText } = render(<ClusterCard c={blue} />);
  expect(getByText("mesh")).toBeTruthy();
  expect(getByText(/ctx-orange peer/)).toBeTruthy();
});

it("shows 'mesh enabled, no peers' for an installed-but-peerless cluster", () => {
  useFleet.setState({ mesh: {
    nodes: [{ cluster: "ctx-nelli", name: "homelab-nelli", clusterId: null, state: "enabled", present: true }],
    edges: [],
  }});
  const nelli = { name: "ctx-nelli", state: "Ready", networkTier: "Healthy" } as any;
  const { getByText } = render(<ClusterCard c={nelli} />);
  expect(getByText("no peers")).toBeTruthy();
});

describe("ClusterCard fleet board", () => {
  // --- Fleet board enrichment ---

  it("renders the gitops line in each tool's vocabulary when board data exists", () => {
    useFleet.setState({ fleetBoard: { [base.name]: { cpuFraction: 0.62, memFraction: 0.81, workloadsTotal: 142, broken: 0, flux: { total: 47, notReady: 0 }, argo: { total: 6, broken: 0 }, gateway: { served: true, gateways: 2, routes: 18, brokenRoutes: 0, unprogrammed: 0 } } } });
    const { getByText } = render(<ClusterCard c={base} />);
    expect(getByText("142 ok")).toBeTruthy();
    expect(getByText("flux 47 ready · argo 6 synced")).toBeTruthy();
    expect(getByText("18 routes")).toBeTruthy();
    expect(getByText("live")).toBeTruthy();
  });

  it("broken workloads show a count, never hidden", () => {
    useFleet.setState({ fleetBoard: { [base.name]: { cpuFraction: null, memFraction: null, workloadsTotal: 10, broken: 3, flux: null, argo: null, gateway: { served: false, gateways: 0, routes: null, brokenRoutes: null, unprogrammed: 0 } } } });
    const { getByText } = render(<ClusterCard c={base} />);
    expect(getByText("3 bad")).toBeTruthy();
    expect(getByText("not detected")).toBeTruthy(); // no GitOps tools detected never implies healthy
    expect(getByText(/raised because workloads/i)).toBeTruthy();
  });

  it("an unreachable cluster renders as a ghost stating reason and recovery", () => {
    const down = { ...base, name: "homelab-orange", state: "Failed", reason: "connect timed out", ageSeconds: 7200 };
    const { getByText, queryByText } = render(<ClusterCard c={down} />);
    expect(getByText(/failed · 2h/)).toBeTruthy();
    expect(getByText("connect timed out")).toBeTruthy();
    expect(getByText(/reconnects automatically/)).toBeTruthy();
    expect(queryByText(/nodes/)).toBeNull(); // no fabricated stats
  });
});
