import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { TopBar } from "./TopBar";
import { useFleet } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { core, dep, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import type { ForwardDTO } from "../store/fleet";

// Mock the forwards bridge so no Wails runtime is needed; assert dispatches.
vi.mock("../bridge/forwards", () => ({
  stopForward: vi.fn().mockResolvedValue(undefined),
  stopAllForwards: vi.fn().mockResolvedValue(undefined),
}));
import { stopAllForwards } from "../bridge/forwards";

// ThemeToggle touches matchMedia; stub it.
vi.mock("./ThemeToggle", () => ({ ThemeToggle: () => null }));
vi.mock("./CommandPalette", () => ({ openCommandPalette: vi.fn() }));
import { openCommandPalette } from "./CommandPalette";
vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
import { openTerminal } from "../bridge/configsvc";

const active: ForwardDTO = {
  id: "c/team/api#1",
  cluster: "dev",
  namespace: "team",
  targetKind: "Pod",
  targetName: "api",
  localPort: 34567,
  targetPort: 8080,
  startedUnix: 1700000000,
  status: "active",
};
const broken: ForwardDTO = { ...active, id: "c/team/web#2", targetName: "web", status: "broken" };

describe("TopBar forwards indicator", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().setForwards([]);
  });

  it("with no forwards there is no live chip - the Port-forwards utility opens the view", () => {
    useFleet.setState({ route: { name: "fleet" } });
    const { queryByTestId, getByRole } = render(<TopBar />);
    expect(queryByTestId("forwards-chip")).toBeNull();
    fireEvent.click(getByRole("button", { name: "Port-forwards" }));
    expect(useFleet.getState().route).toEqual({ name: "forwards" });
  });

  it("opens the command palette when the command bar is clicked", () => {
    const { getByTestId } = render(<TopBar />);
    fireEvent.click(getByTestId("command-bar"));
    expect(openCommandPalette).toHaveBeenCalled();
  });

  it("renders the chip with the forward count", () => {
    useFleet.getState().setForwards([active, broken]);
    const { getByTestId } = render(<TopBar />);
    const chip = getByTestId("forwards-chip");
    expect(chip.textContent).toContain("2");
  });

  it("clicking a row navigates to the forwards section and closes the panel", () => {
    useFleet.getState().setForwards([active]);
    const { getByTestId, queryByTestId } = render(<TopBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    fireEvent.click(getByTestId(`forward-row-${active.id}`));
    expect(useFleet.getState().route).toEqual({ name: "forwards" });
    expect(queryByTestId("forwards-panel")).toBeNull();
  });

  it("dispatches stop all", () => {
    useFleet.getState().setForwards([active, broken]);
    const { getByTestId } = render(<TopBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    fireEvent.click(getByTestId("forwards-stop-all"));
    expect(stopAllForwards).toHaveBeenCalled();
  });

  it("styles a broken row with the warning colour", () => {
    useFleet.getState().setForwards([broken]);
    const { getByTestId } = render(<TopBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    const row = getByTestId(`forward-row-${broken.id}`);
    // Broken rows render warning-coloured (presence assertion on the token).
    expect(row.getAttribute("style")).toContain("var(--color-text-warning)");
    expect(row.textContent).toContain("broken");
  });

  it("has no per-row stop buttons - management lives in the Forwards section", () => {
    useFleet.getState().setForwards([active, broken]);
    const { getByTestId, queryByTestId } = render(<TopBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    expect(queryByTestId(`forward-stop-${active.id}`)).toBeNull();
    expect(queryByTestId("forwards-view-all")).toBeNull();
  });
});

describe("TopBar app utilities (redesign step 7a)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ route: { name: "fleet" }, forwards: [], newContexts: 0 });
  });

  it("Activity and Settings are mast utilities with accessible names; the current one is marked", () => {
    const { getByRole } = render(<TopBar />);
    fireEvent.click(getByRole("button", { name: "Activity" }));
    expect(useFleet.getState().route).toEqual({ name: "activity" });
    fireEvent.click(getByRole("button", { name: "Settings" }));
    expect(useFleet.getState().route).toEqual({ name: "settings" });
    expect(getByRole("button", { name: "Settings" }).getAttribute("aria-current")).toBe("page");
  });

  it("Terminal launches the escape hatch without changing route", () => {
    const { getByRole } = render(<TopBar />);
    fireEvent.click(getByRole("button", { name: "Terminal" }));
    expect(openTerminal).toHaveBeenCalledTimes(1);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("Settings carries the new-context count in its name, not only in a tooltip", () => {
    useFleet.setState({ newContexts: 2 });
    const { getByRole } = render(<TopBar />);
    expect(getByRole("button", { name: "Settings, 2 new kubeconfig contexts" }).textContent).toContain("2");
  });
});

describe("TopBar mast identity and history (redesign step 7b)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ forwards: [], newContexts: 0, clusters: [{ name: "aks-prd-weu-platform-01", state: "Synced", protected: true, protectedInferred: false } as never] });
  });

  it("names the cluster and the place in it; the protected cue is present", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "aks-prd-weu-platform-01", section: "gitops", fluxView: "sources" } });
    const { container, getByText } = render(<TopBar />);
    expect(container.querySelector(".mast-cluster")?.textContent).toBe("aks-prd-weu-platform-01");
    expect(container.querySelector(".mast-place")?.textContent).toBe("Sources");
    expect(getByText("prd lock")).toBeTruthy();
    // No global green freshness readout in the mast.
    expect(container.textContent).not.toMatch(/watching/i);
  });

  it("Back is disabled with no history and names its target when there is one", async () => {
    const { _resetHistoryForTest, initHistory } = await import("../nav/history");
    _resetHistoryForTest();
    useFleet.setState({ route: { name: "fleet" } });
    const off = initHistory();
    const { getByRole, rerender } = render(<TopBar />);
    expect((getByRole("button", { name: "Back" }) as HTMLButtonElement).disabled).toBe(true);
    useFleet.getState().openCluster("aks-prd-weu-platform-01");
    await new Promise<void>((r) => queueMicrotask(r));
    rerender(<TopBar />);
    expect((getByRole("button", { name: "Back to Fleet board" }) as HTMLButtonElement).disabled).toBe(false);
    off();
  });
});

describe("TopBar Attention button (health 1c slice 4)", () => {
  const dev = (over: Record<string, unknown> = {}) => ({ name: "dev", state: "Synced", ...over } as never);
  const sum = (tasks: Parameters<typeof summary>[0], over: Parameters<typeof summary>[1] = {}) => summary(tasks, over, "dev");
  beforeEach(() => {
    resetHealthStore();
    useFleet.setState({ route: { name: "cluster", cluster: "dev", section: "pods" }, clusters: [dev()], forwards: [] });
  });
  const button = (r: ReturnType<typeof render>) => r.getByRole("button", { name: /^Attention/ });

  it("names areas needing attention (not an object sum) and opens the view tree", () => {
    applySummary(sum(replace(replace(core(), task("workloads", [verdict("workloads", { fault: 3, ok: 2 }, [kind("deployments")])])),
      task("helm", [native("releases", { fault: 1, ok: 1 }, 1, "failed or pending", [kind("helm")])]))));
    const opened = vi.fn();
    window.addEventListener("klyx:open-nav-tree", opened);
    const r = render(<TopBar />);
    const btn = button(r);
    expect(btn.getAttribute("aria-label")).toMatch(/^Attention: ▲ 2 areas need attention\./);
    fireEvent.click(btn);
    expect(opened).toHaveBeenCalledTimes(1);
    window.removeEventListener("klyx:open-nav-tree", opened);
  });

  it("checking before anything answers; an unreadable area is a gap and not read, never quiet", () => {
    const r1 = render(<TopBar />);
    expect(button(r1).getAttribute("aria-label")).toMatch(/^Attention: Checking…/);
    r1.unmount();
    applySummary(sum(replace(core(), task("pods", [verdict("pods", {}, [kind("pods", { coverage: "unreadable", class: "forbidden" })], { coverage: "unreadable" })]))));
    const r2 = render(<TopBar />);
    expect(button(r2).getAttribute("aria-label")).toMatch(/^Attention: No attention in 4 areas read · 1 unverified\./);
  });

  it("no Attention button outside a cluster", () => {
    useFleet.setState({ route: { name: "fleet" } });
    const { queryByRole } = render(<TopBar />);
    expect(queryByRole("button", { name: /^Attention/ })).toBeNull();
  });

  it("never an unqualified all-clear: installed domains the summary does not carry are checking gaps (slice 6 §4)", () => {
    useFleet.setState({ clusters: [dev({ certManagerPresent: true, gatewayAPIVersion: "v1.2" })] });
    applySummary(sum(core()));
    const btn = button(render(<TopBar />));
    expect(btn.getAttribute("aria-label")).toMatch(/^Attention: No attention in 5 areas read · 2 unverified\./);
    expect(btn.getAttribute("title")).toMatch(/Certificates\n {2}Certificates: checking… - not read yet/);
    expect(btn.getAttribute("title")).toMatch(/Gateways\n {2}Gateways: checking… - not read yet/);
    expect(btn.getAttribute("title")).not.toContain("not covered");
  });

  it("a fault with a gap is lit AND a gap; the tooltip names each line's provenance", () => {
    applySummary(sum(replace(core(), task("pods", [verdict("pods", { fault: 1, ok: 4 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])]))));
    const btn = button(render(<TopBar />));
    expect(btn.getAttribute("aria-label")).toMatch(/^Attention: ▲ 1 area need attention · 1 unverified\./);
    expect(btn.getAttribute("title")).toMatch(/Pods: 1 need attention · ReplicaSets could not be read - a dependency - cluster summary as of/);
  });

  it("after a Final the observation has ended: no counts, nothing quiet", () => {
    applySummary(sum(core()));
    applySummary(sum([], { seq: 2, final: true, evaluatedAt: "2026-10-12T12:05:00Z" }));
    expect(button(render(<TopBar />)).getAttribute("aria-label")).toMatch(/^Attention: Observation ended at /);
  });
});
