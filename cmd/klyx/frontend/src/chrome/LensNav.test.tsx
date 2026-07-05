import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { LensNav } from "./LensNav";

vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));

const cluster = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({
  name: "dev", state: "Synced", reason: "", nodesReady: 3, nodesTotal: 3, pods: 42, version: "v1.31",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "dev", region: "weu", provider: "aks", group: "aks", ageSeconds: 10,
  fluxPresent: true, argoPresent: false, certManagerPresent: false, externalSecretsPresent: false,
  ...over,
});

beforeEach(() => {
  useFleet.setState({ clusters: [cluster()], route: { name: "fleet" }, forwards: [], newContexts: 0 });
});

describe("LensNav", () => {
  it("shows only fleet-level entries outside a cluster", () => {
    const { getByText, queryByText } = render(<LensNav />);
    expect(getByText("fleet")).toBeTruthy();
    expect(getByText("forwards")).toBeTruthy();
    expect(queryByText("workloads")).toBeNull();
  });

  it("capability-gates tool groups: no argo tab when argo is absent", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "dev", section: "overview" } });
    const { getByText, queryByText } = render(<LensNav />);
    expect(getByText("flux cd")).toBeTruthy();
    expect(queryByText("argo cd")).toBeNull();
  });

  it("marks the active group and renders its sub-row items", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "dev", section: "pods" } });
    const { getByText } = render(<LensNav />);
    const group = getByText("workloads").closest("button") as HTMLButtonElement;
    expect(group.getAttribute("aria-current")).toBe("true");
    // sub-row carries the Workloads group's items
    expect(getByText("pods")).toBeTruthy();
    expect(getByText("deployments")).toBeTruthy();
    expect((getByText("pods").closest("button") as HTMLButtonElement).getAttribute("aria-current")).toBe("true");
  });

  it("activates a group by opening its first item", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "dev", section: "overview" } });
    const { getByText } = render(<LensNav />);
    fireEvent.click(getByText("flux cd"));
    const route = useFleet.getState().route;
    expect(route.name === "cluster" && route.section).toBe("gitops");
  });

  it("shows the forwards count", () => {
    useFleet.setState({
      forwards: [{ id: "f1", cluster: "dev", namespace: "web", targetKind: "Pod", targetName: "api", localPort: 8080, targetPort: 80, status: "active", startedUnix: 1 }],
    });
    const { getByText } = render(<LensNav />);
    expect(getByText("1")).toBeTruthy();
  });
});
