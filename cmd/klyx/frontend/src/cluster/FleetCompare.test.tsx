import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, within } from "@testing-library/react";
import { useFleet, WorkloadCompareDTO, ClusterDTO } from "../store/fleet";
import { FleetCompare } from "./FleetCompare";

vi.mock("../bridge/workloads", () => ({
  workloadCompare: vi.fn(async () => ({}) as WorkloadCompareDTO),
}));
import { workloadCompare } from "../bridge/workloads";

const cl = (name: string, env: string): ClusterDTO => ({
  name, state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  env, region: "westeurope", group: "", provider: "", ageSeconds: 0,
});

const result = (over: Partial<WorkloadCompareDTO> = {}): WorkloadCompareDTO => ({
  found: true, kind: "Deployment", namespace: "shop", name: "cart",
  desiredReplicas: 2, readyReplicas: 2,
  containers: [{ name: "app", image: "shop/cart:v1.2.3", resources: "req 100m/128Mi" }],
  specHash: "aaaa11112222", ownerKind: "Kustomization", ownerNamespace: "flux-system", ownerName: "apps",
  ownerRevision: "main@sha1:abc", error: "", ...over,
});

describe("FleetCompare", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ clusters: [cl("prd-we", "prd"), cl("dev-we", "dev"), cl("stg-we", "stg")] });
  });

  it("orders columns dev -> stg -> prd and marks the origin", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) => result({ name: cluster }));
    const { findAllByTestId, getByTestId } = render(
      <FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />,
    );
    const cols = await findAllByTestId(/compare-col-/);
    expect(cols.map((c) => c.getAttribute("data-testid"))).toEqual([
      "compare-col-dev-we", "compare-col-stg-we", "compare-col-prd-we",
    ]);
    expect(within(getByTestId("compare-col-dev-we")).getByText("origin")).toBeTruthy();
  });

  it("shows image + revision values per column and honest not-present/degraded states", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) => {
      if (cluster === "dev-we") return result();
      if (cluster === "stg-we") return result({ found: false, containers: [], specHash: "", ownerKind: "", ownerRevision: "" });
      return result({ found: false, containers: [], error: "deployments is forbidden" });
    });
    const { findByText, getByTestId } = render(
      <FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />,
    );
    expect(await findByText("shop/cart:v1.2.3")).toBeTruthy();
    expect(await findByText("main@sha1:abc")).toBeTruthy();
    expect(within(getByTestId("compare-col-stg-we")).getByText(/not present/)).toBeTruthy();
    // A failed lookup is degraded, never "not present".
    expect(within(getByTestId("compare-col-prd-we")).getByText(/check failed: deployments is forbidden/)).toBeTruthy();
  });

  it("flags other spec drift when hashes differ but images and resources match", async () => {
    vi.mocked(workloadCompare).mockImplementation(async (cluster) =>
      cluster === "prd-we" ? result({ specHash: "bbbb33334444" }) : result());
    const { getByTestId, findByText } = render(
      <FleetCompare origin="dev-we" kind="Deployment" namespace="shop" name="cart" onClose={() => {}} />,
    );
    await findByText("origin"); // settle
    expect(within(getByTestId("compare-col-prd-we")).getByText(/other spec drift/)).toBeTruthy();
    // Matching columns carry no such flag.
    expect(within(getByTestId("compare-col-stg-we")).queryByText(/other spec drift/)).toBeNull();
  });
});
