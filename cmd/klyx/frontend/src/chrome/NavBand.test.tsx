import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within, act } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { NavBand, fitTabs } from "./NavBand";
import { useListKeys } from "./useListKeys";

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
    const menu = screen.getByRole("menu", { name: "Resources" });
    expect(within(menu).getByRole("menuitem", { name: "Deployments" })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /^Jobs/ })).toBeTruthy();
  });

  it("kind shortcuts are filtered workload views, never labelled raw; GVR views are", () => {
    at("pods");
    render(<NavBand />);
    for (const [label, kind] of [["Deployments", "Deployment"], ["StatefulSets", "StatefulSet"], ["DaemonSets", "DaemonSet"]] as const) {
      fireEvent.click(screen.getByRole("button", { name: /Resources/ }));
      const menu = screen.getByRole("menu", { name: "Resources" });
      const item = within(menu).getByRole("menuitem", { name: new RegExp(`^${label}`) });
      expect(item.textContent).toBe(label);
      expect(item.getAttribute("title")).toBeNull();
      fireEvent.click(item);
      const r = routeOf();
      expect(r.name === "cluster" && r.section).toBe("workloads");
      expect(useFleet.getState().workloads.kindFilter[kind]).toBe(true);
    }
    fireEvent.click(screen.getByRole("button", { name: /Resources/ }));
    const jobs = within(screen.getByRole("menu", { name: "Resources" })).getByRole("menuitem", { name: /^Jobs/ });
    expect(jobs.textContent).toContain("raw");
    expect(jobs.getAttribute("title")).toMatch(/raw resource view/);
  });

  it("a focused nav control owns its keys: Enter, j/k and lens verbs never reach the list", () => {
    at("pods");
    const onActivate = vi.fn();
    const onSelect = vi.fn();
    const verb = vi.fn();
    function List() {
      useListKeys({ count: 3, selected: 0, onSelect, onActivate, extraKeys: { r: verb, s: verb } });
      return null;
    }
    render(<><NavBand /><List /></>);
    for (const el of [screen.getByRole("button", { name: /all views/ }), tab("Pods"), screen.getByRole("button", { name: /Resources/ })]) {
      el.focus();
      for (const key of ["Enter", "j", "k", "r", "s"]) fireEvent.keyDown(el, { key });
    }
    expect(onActivate).not.toHaveBeenCalled();
    expect(onSelect).not.toHaveBeenCalled();
    expect(verb).not.toHaveBeenCalled();
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

  it("Fleet task tabs: board, promotion and expiry are fleet views (step 7d)", () => {
    render(<NavBand />);
    fireEvent.click(screen.getByRole("button", { name: "Promotion" }));
    expect(routeOf()).toEqual({ name: "fleet", view: "promotion" });
    expect(screen.getByRole("button", { name: "Promotion" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("button", { name: "Fleet board" }).getAttribute("aria-current")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Expiry" }));
    expect(routeOf()).toEqual({ name: "fleet", view: "expiry" });
    fireEvent.click(screen.getByRole("button", { name: "Fleet board" }));
    expect(routeOf()).toEqual({ name: "fleet" });
  });
});

describe("NavBand reads the shared attention summary (step 7e)", () => {
  const ok = { coverage: "complete" as const, counts: { total: 5, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 5, attention: 0 } };
  it("group counts come from the summary; gaps are named beside them; the mast event opens the tree", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "dev", section: "overview" },
      attention: { cluster: "dev", loading: false, at: 1, counts: { workloads: { ...ok, counts: { ...ok.counts, fault: 2, ok: 3, attention: 2 } }, pods: "unreadable", events: 0, nodes: ok, helm: 0, flux: 1, argo: "absent" } } });
    render(<NavBand />);
    act(() => { window.dispatchEvent(new Event("klyx:open-nav-tree")); });
    const tree = screen.getByRole("dialog", { name: "All views" });
    const wl = within(tree).getByText("Workloads").parentElement as HTMLElement;
    expect(wl.textContent).toContain("▲ 2");
    expect(wl.textContent).toContain("unreadable");
    const flux = within(tree).getByText("Flux CD").parentElement as HTMLElement;
    expect(flux.textContent).toContain("▲ 1");
  });

  it("the tree names what the summary does not cover and areas shown from a failed re-poll", () => {
    useFleet.setState({ clusters: [cluster({ certManagerPresent: true, gatewayAPIVersion: "v1.2" })], route: { name: "cluster", cluster: "dev", section: "overview" },
      attention: { cluster: "dev", loading: false, at: 2, stale: ["nodes"], areaAt: { nodes: 1 }, counts: { workloads: ok, pods: ok, events: 0, nodes: ok, helm: 0, flux: 0, argo: "absent" } } });
    render(<NavBand />);
    act(() => { window.dispatchEvent(new Event("klyx:open-nav-tree")); });
    const tree = screen.getByRole("dialog", { name: "All views" });
    expect((within(tree).getByText("cert-manager").parentElement as HTMLElement).textContent).toContain("not summarized");
    expect(within(tree).getByRole("option", { name: /^Gateway topology/ }).textContent).toContain("not summarized");
    expect((within(tree).getByText("Cluster").parentElement as HTMLElement).textContent).toContain("last good");
  });

  it("opened by the Attention button, the tree starts on the first destination needing attention", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "dev", section: "overview" },
      attention: { cluster: "dev", loading: false, at: 1, counts: { workloads: { ...ok, counts: { ...ok.counts, fault: 2, ok: 3, attention: 2 } }, pods: ok, events: 3, nodes: ok, helm: 0, flux: 0, argo: "absent" } } });
    render(<NavBand />);
    act(() => { window.dispatchEvent(new CustomEvent("klyx:open-nav-tree", { detail: { focus: "attention" } })); });
    // Events (evidence, no tone) is skipped; All workloads (fault) is chosen.
    const selected = screen.getAllByRole("option").find((o) => o.getAttribute("aria-selected") === "true");
    expect(selected?.textContent).toMatch(/^All workloads/);
  });
});

describe("NavBand counts (health model)", () => {
  const fault = { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", rule: "pod.current-failure" };
  const pod = (name: string, health: object) => ({
    namespace: "shop", name, uid: `uid-${name}`, ready: false, restarts: 0,
    node: "n", ip: "", ownerKind: "", ownerName: "", containers: [], health,
  });
  // The Pods lens holds the Observer's rows with the global verdict counts
  // (1c slice 3 §4a); a complete Pod read wins over the poll.
  const live = [{ kind: "Pod", served: true, synced: true, coverage: "live" as const }];
  const seedPods = (over: object) => useFleet.setState((s) => ({
    route: { name: "cluster", cluster: "dev", section: "pods" },
    pods: { ...s.pods, cluster: "dev", namespace: "", items: [pod("a", fault), pod("b", fault)], loading: false, error: null, coverage: live,
      counts: { total: 2, fault: 2, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 }, ...over },
  }));

  it("shows the pods count for this cluster's complete read, with fault shape - whatever the namespace filter", () => {
    for (const over of [{}, { namespace: "shop" }]) {
      seedPods(over);
      const { unmount } = render(<NavBand />);
      expect(tab("Pods").textContent).toContain("▲ 2");
      unmount();
    }
  });

  it("drops the count for an incomplete or other-cluster read", () => {
    for (const over of [
      { coverage: [{ kind: "Pod", served: true, synced: true, coverage: "stale", error: "boom" }], error: "Pod: boom" },
      { coverage: [{ kind: "Pod", served: true, synced: false, coverage: "checking" }] },
      { loading: true },
      { cluster: "prd" },
    ]) {
      seedPods(over);
      const { unmount } = render(<NavBand />);
      expect(tab("Pods").textContent).not.toMatch(/\d/);
      unmount();
    }
  });
});

describe("fitTabs (Codex 7a review)", () => {
  const ids = (set: Set<number>) => [...set].sort((a, b) => a - b);

  it("everything fits: all tabs", () => {
    expect(ids(fitTabs([60, 60, 60], 1, 400, 50, 80))).toEqual([0, 1, 2]);
  });

  it("a long active tab is reserved first; neighbours join only if they really fit", () => {
    // room 300 - Resources 82 - More 52 = 166 for tabs; active needs 152.
    expect(ids(fitTabs([60, 60, 150], 2, 300, 50, 80))).toEqual([2]);
    expect(ids(fitTabs([10, 60, 150], 2, 300, 50, 80))).toEqual([0, 2]);
  });

  it("priority order: a later short tab never jumps a skipped earlier one", () => {
    expect(ids(fitTabs([60, 120, 20], 0, 200, 50, 0))).toEqual([0]);
  });

  it("real but insufficient room keeps only the active tab, never all", () => {
    expect(ids(fitTabs([60, 60, 60], 1, 0, 50, 80))).toEqual([1]);
    expect(ids(fitTabs([60, 60, 60], -1, -10, 50, 80))).toEqual([]);
  });

  it("unmeasured layout (no widths anywhere) shows every tab", () => {
    expect(ids(fitTabs([0, 0, 0], 1, 0, 0, 0))).toEqual([0, 1, 2]);
  });
});
