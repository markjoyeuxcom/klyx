import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { NavBand } from "./NavBand";

vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));

const cluster = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({
  name: "dev", state: "Synced", reason: "", nodesReady: 3, nodesTotal: 3, pods: 42, version: "v1.31",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "dev", region: "weu", provider: "aks", group: "aks", ageSeconds: 10,
  fluxPresent: true, argoPresent: false, certManagerPresent: false, externalSecretsPresent: false,
  ...over,
});
const at = (section: string, extra: object = {}) => useFleet.setState({ route: { name: "cluster", cluster: "dev", section, ...extra } as never });
const routeOf = () => useFleet.getState().route;
const tab = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name}`) });
const openTree = () => fireEvent.click(screen.getByRole("button", { expanded: false, name: /all views/ }));

beforeEach(() => {
  useFleet.setState((s) => ({ clusters: [cluster()], route: { name: "fleet" }, forwards: [], newContexts: 0, fleetBoard: {},
    workloads: { ...s.workloads, kindFilter: { Deployment: true, StatefulSet: true, DaemonSet: true } } }));
});

describe("NavBand (redesign step 7a)", () => {
  it("is one row: no Fleet duplicate, no app utilities, no second tier", () => {
    at("pods");
    const { container } = render(<NavBand />);
    expect(container.querySelectorAll("nav")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /^fleet$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /forwards|activity|settings|terminal/i })).toBeNull();
  });

  it("shows the current group and its tasks; the active destination is aria-current", () => {
    at("pods");
    render(<NavBand />);
    expect(screen.getByRole("button", { name: /^Workloads/ })).toBeTruthy();
    expect(tab("Pods").getAttribute("aria-current")).toBe("page");
    expect(tab("All workloads").getAttribute("aria-current")).toBeNull();
    // Kind links and raw views are in the Resources menu, not the row.
    expect(screen.queryByRole("button", { name: /^Deployments/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Resources/ }));
    const menu = screen.getByRole("menu", { name: "Raw resource views" });
    expect(within(menu).getByRole("menuitem", { name: "Deployments" })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: "Jobs" })).toBeTruthy();
  });

  it("a kind link opens All workloads filtered to that kind", () => {
    at("pods");
    render(<NavBand />);
    fireEvent.click(screen.getByRole("button", { name: /Resources/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "StatefulSets" }));
    const r = routeOf();
    expect(r.name === "cluster" && r.section).toBe("workloads");
    expect(useFleet.getState().workloads.kindFilter).toEqual({ Deployment: false, StatefulSet: true, DaemonSet: false });
  });

  it("an open raw resource view stays visible as its own tab, labelled raw", () => {
    at("resources", { resource: { group: "batch", version: "v1", plural: "jobs", kind: "Job", scope: "Namespaced" } });
    render(<NavBand />);
    expect(screen.getByRole("button", { name: /^Workloads/ })).toBeTruthy();
    const jobs = tab("Jobs");
    expect(jobs.getAttribute("aria-current")).toBe("page");
    expect(jobs.textContent).toContain("raw");
  });

  it("a custom kind mix keeps the Workloads group without pressing a tab", () => {
    useFleet.setState((s) => ({ workloads: { ...s.workloads, kindFilter: { Deployment: false, StatefulSet: true, DaemonSet: true } } }));
    at("workloads");
    render(<NavBand />);
    expect(screen.getByRole("button", { name: /^Workloads/ })).toBeTruthy();
    expect(tab("All workloads").getAttribute("aria-current")).toBeNull();
  });

  it("Flux Reconciliation and Sources are destinations carried by the route", () => {
    at("gitops");
    render(<NavBand />);
    expect(tab("Reconciliation").getAttribute("aria-current")).toBe("page");
    fireEvent.click(tab("Sources"));
    const r = routeOf();
    expect(r.name === "cluster" && r.section === "gitops" && r.fluxView).toBe("sources");
  });

  it("the tree is capability-gated: absent tools are omitted, a connecting cluster says checking", () => {
    at("overview");
    const { unmount } = render(<NavBand />);
    openTree();
    const tree = screen.getByRole("dialog", { name: "All views" });
    expect(within(tree).getByText("Flux CD")).toBeTruthy();
    expect(within(tree).queryByText("Argo CD")).toBeNull();
    expect(within(tree).queryByText("Hubble flows")).toBeNull();
    unmount();

    useFleet.setState({ clusters: [cluster({ state: "Connecting", fluxPresent: false })] });
    render(<NavBand />);
    openTree();
    const t2 = screen.getByRole("dialog", { name: "All views" });
    const flux = within(t2).getByText("Flux CD").parentElement as HTMLElement;
    expect(flux.textContent).toContain("checking capability");
    const rec = within(t2).getByRole("option", { name: /Reconciliation/ });
    expect(rec.getAttribute("aria-disabled")).toBe("true");
  });

  it("the tree searches labels and aliases; Enter opens the match and closes", () => {
    at("overview");
    render(<NavBand />);
    openTree();
    const input = screen.getByRole("combobox");
    fireEvent.change(input, { target: { value: "pvc" } });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["PersistentVolumeClaims"]);
    fireEvent.keyDown(input, { key: "Enter" });
    const r = routeOf();
    expect(r.name === "cluster" && r.resource?.plural).toBe("persistentvolumeclaims");
    expect(screen.queryByRole("dialog", { name: "All views" })).toBeNull();
  });

  it("search matches word starts, not substrings: 'source' is Flux Sources, never ResourceQuotas", () => {
    at("overview");
    render(<NavBand />);
    openTree();
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "source" } });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Sources"]);
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "cluster role" } });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Cluster Roles", "Cluster Role Bindings"]);
  });

  it("Escape closes the tree without navigating and returns focus to the selector", () => {
    at("overview");
    render(<NavBand />);
    openTree();
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "All views" })).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: /all views/ }));
    const r = routeOf();
    expect(r.name === "cluster" && r.section).toBe("overview");
  });

  it("Fleet and app routes show their own identity, never cluster groups", () => {
    render(<NavBand />);
    expect(screen.getByRole("button", { name: "Fleet board" }).getAttribute("aria-current")).toBe("page");
    expect(screen.queryByText("Workloads")).toBeNull();
  });
});

describe("NavBand counts (health model)", () => {
  const fault = { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", rule: "pod.current-failure" };
  const pod = (name: string, health: object) => ({
    namespace: "shop", name, ready: false, phase: "Running", reason: "", rank: "unhealthy" as const, restarts: 0,
    node: "n", ip: "", ownerKind: "", ownerName: "", ageSeconds: 1, containers: [], health,
  });
  const seedPods = (over: object) => useFleet.setState((s) => ({
    route: { name: "cluster", cluster: "dev", section: "pods" },
    pods: { ...s.pods, cluster: "dev", namespace: "", items: [pod("a", fault), pod("b", fault)], loading: false, rowsScope: "dev|", error: null, truncated: false, ...over },
  }));

  it("shows the pods count for this cluster's complete all-namespaces read, with fault shape", () => {
    seedPods({});
    render(<NavBand />);
    expect(tab("Pods").textContent).toContain("▲ 2");
  });

  it("drops the count during a namespace switch, a capped, failed or other-cluster read", () => {
    for (const over of [{ namespace: "shop", loading: true }, { truncated: true }, { error: "boom" }, { rowsScope: "prd|" }]) {
      seedPods(over);
      const { unmount } = render(<NavBand />);
      expect(tab("Pods").textContent).not.toMatch(/\d/);
      unmount();
    }
  });
});
