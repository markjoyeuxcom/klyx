import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";
import { CommandPalette } from "./CommandPalette";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, PodRowDTO } from "../store/fleet";

vi.mock("../bridge/pods", () => ({ openPodDetail: vi.fn(), listPods: vi.fn() }));
vi.mock("../bridge/helm", () => ({ openHelmRelease: vi.fn() }));

const mkCluster = (name: string, env: string, region: string): ClusterDTO =>
  ({ name, env, region } as ClusterDTO);

const mkPod = (namespace: string, name: string): PodRowDTO =>
  ({ namespace, name, uid: `uid-${name}`, health: { kind: "ok", label: "Running" } } as PodRowDTO);

function seedClusters() {
  useFleet.setState({
    clusters: [mkCluster("dev-we", "DEV", "westeurope"), mkCluster("prd-ne", "PRD", "northeurope")],
    route: { name: "fleet" },
  });
}

function pressCmdK() {
  fireEvent.keyDown(window, { key: "k", metaKey: true });
}

// Some tests stub openCluster on the store; every test starts from the real one.
const realOpenCluster = useFleet.getState().openCluster;

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.getState().clearPods();
  useFleet.setState({ clusters: [], route: { name: "fleet" }, openCluster: realOpenCluster });
});

describe("CommandPalette", () => {
  it("opens on ⌘K and closes on Esc", () => {
    seedClusters();
    const { queryByTestId } = render(<CommandPalette />);
    expect(queryByTestId("command-palette")).toBeNull();
    pressCmdK();
    expect(queryByTestId("command-palette")).not.toBeNull();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(queryByTestId("command-palette")).toBeNull();
  });

  it("filters and ranks as you type", () => {
    seedClusters();
    const { getByTestId, queryByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.change(getByTestId("command-palette-input"), { target: { value: "prd" } });
    expect(queryByTestId("command-row-cluster:prd-ne")).not.toBeNull();
    expect(queryByTestId("command-row-cluster:dev-we")).toBeNull();
  });

  it("runs the selected command on Enter", () => {
    const openCluster = vi.fn();
    seedClusters();
    useFleet.setState({ openCluster });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.change(getByTestId("command-palette-input"), { target: { value: "dev-we" } });
    fireEvent.keyDown(getByTestId("command-palette-input"), { key: "Enter" });
    expect(openCluster).toHaveBeenCalledWith("dev-we");
  });

  it("runs a command on click", () => {
    const openCluster = vi.fn();
    seedClusters();
    useFleet.setState({ openCluster });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.mouseDown(getByTestId("command-row-cluster:prd-ne"));
    expect(openCluster).toHaveBeenCalledWith("prd-ne");
  });

  it("does not open when ⌘K is pressed inside another input", () => {
    seedClusters();
    const { queryByTestId } = render(
      <div>
        <textarea data-testid="other" />
        <CommandPalette />
      </div>,
    );
    const ta = queryByTestId("other")!;
    fireEvent.keyDown(ta, { key: "k", metaKey: true });
    expect(queryByTestId("command-palette")).toBeNull();
  });

  it("shows a Pods group when the pods slice is seeded", () => {
    seedClusters();
    useFleet.getState().setPodsVisit("dev-we");
    useFleet.getState().setPodsFromRows("dev-we", { items: [mkPod("team", "api-1")], namespaces: ["team"], loading: false });
    const { getByTestId, queryByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.change(getByTestId("command-palette-input"), { target: { value: "api-1" } });
    expect(queryByTestId("command-row-pod:team/api-1")).not.toBeNull();
  });

  it("moves selection with arrow keys", () => {
    seedClusters();
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    // first row selected by default (Application leads the empty list)
    expect(getByTestId("command-row-fleet").getAttribute("data-selected")).toBe("true");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(getByTestId("command-row-fleet").getAttribute("data-selected")).toBe("false");
    expect(getByTestId("command-row-fleet:promotion").getAttribute("data-selected")).toBe("true");
  });

  it("scrolls the selected row into view when navigating", () => {
    // jsdom does not implement scrollIntoView; install a spy so the palette's
    // guarded call is observable.
    const scrollSpy = vi.fn();
    window.HTMLElement.prototype.scrollIntoView = scrollSpy;
    seedClusters();
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    scrollSpy.mockClear();
    fireEvent.keyDown(getByTestId("command-palette-input"), { key: "ArrowDown" });
    expect(getByTestId("command-row-fleet:promotion").getAttribute("data-selected")).toBe("true");
    expect(scrollSpy).toHaveBeenCalledWith({ block: "nearest" });
  });
});

describe("CommandPalette views from Fleet (redesign step 7c)", () => {
  const two = () => useFleet.setState({
    clusters: [{ name: "dev-we", state: "Synced" } as ClusterDTO, { name: "aks-prd-weu-platform-01", state: "Synced" } as ClusterDTO],
    route: { name: "fleet" },
  });
  const type = (input: HTMLElement, value: string) => fireEvent.change(input, { target: { value } });

  it("Events from Fleet opens Choose cluster; Back keeps the query; Escape changes nothing", () => {
    two();
    const { getByTestId, getByText, queryByText } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "Events");
    expect(getByTestId("command-row-view:events")).toBeTruthy();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(getByText("Choose cluster for Events")).toBeTruthy();
    expect(getByTestId("command-row-view:events@aks-prd-weu-platform-01")).toBeTruthy();
    fireEvent.keyDown(input, { key: "Backspace" });
    expect(queryByText("Choose cluster for Events")).toBeNull();
    expect((input as HTMLInputElement).value).toBe("Events");
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.keyDown(window, { key: "Escape" });
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("choosing a cluster opens the view there", () => {
    two();
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "All workloads");
    fireEvent.keyDown(input, { key: "Enter" });
    type(input, "prd");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "aks-prd-weu-platform-01", section: "workloads" });
  });

  it("a cluster-named query offers that cluster's view directly; the full long name matches", () => {
    two();
    const { getByTestId, queryByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "pods");
    expect(queryByTestId("command-row-view:pods@aks-prd-weu-platform-01")).toBeNull();
    type(input, "pods aks-prd-weu");
    expect(getByTestId("command-row-view:pods@aks-prd-weu-platform-01")).toBeTruthy();
    type(input, "aks-prd-weu-platform-01");
    expect(getByTestId("command-row-cluster:aks-prd-weu-platform-01")).toBeTruthy();
  });

  it("Port-forwards is an Application result, and a disabled view runs nothing", () => {
    useFleet.setState({ clusters: [{ name: "dev", state: "Connecting" } as ClusterDTO], route: { name: "fleet" } });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "Port-forwards");
    expect(getByTestId("command-row-forwards")).toBeTruthy();
    type(input, "Reconciliation");
    const row = getByTestId("command-row-view:flux");
    expect(row.getAttribute("aria-disabled")).toBe("true");
    expect(row.textContent).toContain("checking capability");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
    expect(getByTestId("command-palette")).toBeTruthy();
  });

  it("a long API kind is found by its kind name", () => {
    useFleet.setState({ clusters: [{ name: "dev", state: "Synced", ciliumPresent: true } as ClusterDTO], route: { name: "fleet" } });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    type(getByTestId("command-palette-input"), "CiliumClusterwideNetworkPolicy");
    expect(getByTestId("command-row-resource:res:cilium.io/v2/ciliumclusterwidenetworkpolicies")).toBeTruthy();
  });
});

describe("rank match quality (redesign step 7c)", () => {
  const cmd = (id: string, title: string, hint?: string) => ({ id, group: "Views" as const, title, hint, run: () => {} });
  it("scattered letters across a long title are not a match; tight ones and short queries are", async () => {
    const { rank } = await import("./CommandPalette");
    const cmds = [cmd("events", "Events", "Cluster"), cmd("sa", "Service Accounts", "Access · raw resource view")];
    expect(rank(cmds, "events").rows.map((r) => r.cmd.id)).toEqual(["events"]);
    expect(rank([cmd("aw", "All workloads")], "allwl").rows).toHaveLength(1);
    expect(rank(cmds, "sa").rows.map((r) => r.cmd.id)).toContain("sa");
  });
  it("an exact title outranks a frecency-boosted longer match", async () => {
    const { rank } = await import("./CommandPalette");
    const cmds = [cmd("light", "theme: bench light"), cmd("bench", "theme: bench")];
    const boost = (id: string) => (id === "light" ? 50 : 0);
    expect(rank(cmds, "theme: bench", boost).rows[0].cmd.id).toBe("bench");
  });

  it("hints match as a substring only", async () => {
    const { rank } = await import("./CommandPalette");
    const cmds = [cmd("p", "shop/api-1", "Pod · Running · dev")];
    expect(rank(cmds, "running").rows).toHaveLength(1);
    expect(rank(cmds, "rnng").rows).toHaveLength(0);
  });
});

describe("CommandPalette revalidates while open (Codex 7c review)", () => {
  const dev = { name: "dev", state: "Synced", fluxPresent: true } as ClusterDTO;
  const prd = { name: "prd", state: "Synced", fluxPresent: true } as ClusterDTO;
  const type = (input: HTMLElement, value: string) => fireEvent.change(input, { target: { value } });

  it("a cluster removed while the palette is open is never opened; the row explains", async () => {
    const noFlux = { ...prd, fluxPresent: false };
    useFleet.setState({ clusters: [dev, noFlux], route: { name: "fleet" } });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "Reconciliation");
    expect(getByTestId("command-row-view:flux").textContent).toContain("dev");
    act(() => { useFleet.setState({ clusters: [noFlux] }); });
    const row = getByTestId("command-row-view:flux");
    expect(row.getAttribute("aria-disabled")).toBe("true");
    expect(row.textContent).toContain("not detected on any connected cluster");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
    // With no cluster left at all, Enter is the row's explicit Connect action.
    act(() => { useFleet.setState({ clusters: [] }); });
    expect(getByTestId("command-row-view:flux").textContent).toContain("↵ Connect cluster");
  });

  it("a stale result's guard blocks activation and says why", async () => {
    useFleet.setState({ clusters: [dev], route: { name: "fleet" } });
    const { buildCommands } = await import("./commands");
    const flux = buildCommands(useFleet.getState()).find((c) => c.id === "view:flux")!;
    useFleet.setState({ clusters: [{ ...dev, fluxPresent: false }] });
    expect(flux.guard?.()).toBe("not installed on dev");
    useFleet.setState({ clusters: [{ ...dev, state: "Failed" }] });
    expect(flux.guard?.()).toMatch(/dev/);
    useFleet.setState({ clusters: [] });
    expect(flux.guard?.()).toBe("dev is no longer in the fleet");
  });

  it("Choose cluster follows membership 2 -> 1 -> 0 -> 2: one left stays a choice, none says so", () => {
    useFleet.setState({ clusters: [dev, prd], route: { name: "fleet" } });
    const { getByTestId, queryByTestId, getByText } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "Events");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(getByTestId("command-row-view:events@prd")).toBeTruthy();
    act(() => { useFleet.setState({ clusters: [dev] }); });
    expect(queryByTestId("command-row-view:events@prd")).toBeNull();
    expect(getByTestId("command-row-view:events@dev")).toBeTruthy();
    expect(useFleet.getState().route).toEqual({ name: "fleet" }); // never auto-navigated
    act(() => { useFleet.setState({ clusters: [] }); });
    expect(getByText("No eligible cluster remains for this view.")).toBeTruthy();
    act(() => { useFleet.setState({ clusters: [dev, prd] }); });
    expect(getByTestId("command-row-view:events@dev")).toBeTruthy();
    expect(getByTestId("command-row-view:events@prd")).toBeTruthy();
  });

  it("the Results button works from the keyboard and restores the query", () => {
    useFleet.setState({ clusters: [dev, prd], route: { name: "fleet" } });
    const { getByTestId, getByRole } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input") as HTMLInputElement;
    type(input, "Events");
    fireEvent.keyDown(input, { key: "Enter" });
    const results = getByRole("button", { name: "‹ Results" });
    results.focus();
    fireEvent.click(results); // keyboard Enter/Space activates as click
    expect(input.value).toBe("Events");
    expect(document.activeElement).toBe(input);
  });

  it("registry aliases are searchable (cnp, hpa)", () => {
    useFleet.setState({ clusters: [{ ...dev, ciliumPresent: true }], route: { name: "fleet" } });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "cnp");
    expect(getByTestId("command-row-resource:res:cilium.io/v2/ciliumnetworkpolicies")).toBeTruthy();
    type(input, "hpa");
    expect(getByTestId("command-row-resource:res:autoscaling/v2/horizontalpodautoscalers")).toBeTruthy();
  });

  it("frecency records the completed destination, not just the chooser", async () => {
    localStorage.clear();
    useFleet.setState({ clusters: [dev, prd], route: { name: "fleet" } });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    type(input, "Events");
    fireEvent.keyDown(input, { key: "Enter" });
    const { loadBoosts } = await import("./frecency");
    expect(loadBoosts()("view:events")).toBe(0);
    type(input, "prd");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(loadBoosts()("view:events")).toBeGreaterThan(0);
    expect(loadBoosts()("view:events@prd")).toBeGreaterThan(0);
  });
});
