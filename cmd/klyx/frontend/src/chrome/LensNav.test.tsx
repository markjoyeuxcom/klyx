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

  it("keeps the sub-row mounted when a custom kind filter matches no tab exactly", () => {
    useFleet.setState({
      route: { name: "cluster", cluster: "dev", section: "workloads" },
      workloads: { ...useFleet.getState().workloads, kindFilter: { Deployment: false, StatefulSet: true, DaemonSet: true } },
    });
    const { getByText } = render(<LensNav />);
    // no tab is pressed, but the row is still there
    expect(getByText("all workloads")).toBeTruthy();
    expect((getByText("all workloads").closest("button") as HTMLButtonElement).getAttribute("aria-current")).toBeNull();
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

describe("LensNav attention badges (health model)", () => {
  const fault = { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", rule: "pod.current-failure" };
  const pod = (name: string, health: object) => ({
    namespace: "shop", name, ready: false, phase: "Running", reason: "", rank: "unhealthy" as const, restarts: 0,
    node: "n", ip: "", ownerKind: "", ownerName: "", ageSeconds: 1, containers: [], health,
  });
  const seedPods = (over: object) => useFleet.setState((s) => ({
    route: { name: "cluster", cluster: "dev", section: "pods" },
    pods: { ...s.pods, cluster: "dev", namespace: "", items: [pod("a", fault), pod("b", fault)], loading: false, rowsScope: "dev|", error: null, truncated: false, ...over },
  }));
  const podsTab = (getByText: (t: string) => HTMLElement) => getByText("pods").closest("button") as HTMLButtonElement;

  it("shows the pods count for this cluster's complete all-namespaces read", () => {
    seedPods({});
    const { getByText } = render(<LensNav />);
    expect(podsTab(getByText).textContent).toContain("2");
  });

  it("drops the badge during a switch to a namespace (old all-namespaces rows retained)", () => {
    seedPods({ namespace: "shop", loading: true });
    const { getByText } = render(<LensNav />);
    expect(podsTab(getByText).textContent).not.toMatch(/\d/);
  });

  it("drops the badge for a capped, failed or other-cluster read", () => {
    for (const over of [{ truncated: true }, { error: "boom" }, { rowsScope: "prd|" }]) {
      seedPods(over);
      const { getByText, unmount } = render(<LensNav />);
      expect(podsTab(getByText).textContent).not.toMatch(/\d/);
      unmount();
    }
  });
});
