import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { core, kind, replace, summary, task, verdict } from "../test/healthFixtures";
import { FleetIndicator } from "./FleetIndicator";
import { StationSwitcher } from "./StationSwitcher";

// The Line slice 4 §2.1 and §3.2: the fleet indicator's chip and list, and
// the station switcher that keeps the spine's reach to every cluster.

const cl = (name: string, state = "Synced", over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, state, reason: "", env: "", region: "", ...over } as ClusterDTO);
const faulty = (cluster: string) => applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 2, ok: 1 }, [kind("deployments")])])), {}, cluster));
const warned = (cluster: string) => applySummary(summary(replace(core(), task("nodes", [verdict("nodes", { degraded: 1 }, [kind("nodes")])])), {}, cluster));
const quiet = (cluster: string) => applySummary(summary(core(), {}, cluster));

beforeEach(() => {
  resetHealthStore();
  useFleet.setState({ route: { name: "fleet" }, clusters: [] });
});

describe("the fleet indicator", () => {
  it("renders nothing with no clusters", () => {
    const { container } = render(<FleetIndicator />);
    expect(container.firstChild).toBeNull();
  });

  it("the chip shows each non-zero axis in order, and its name counts clusters in words", () => {
    faulty("a"); warned("b"); quiet("q");
    useFleet.setState({ clusters: [cl("q"), cl("b"), cl("a"), cl("x", "Failed")] });
    const { getByRole } = render(<FleetIndicator />);
    const chip = getByRole("button", { name: "Fleet: 1 cluster with a fault, 1 with a warning, 1 unreachable, of 4" });
    expect([...chip.querySelectorAll("[data-axis]")].map((e) => [e.getAttribute("data-axis"), e.textContent])).toEqual([["fault", "1"], ["warning", "1"], ["unreachable", "1"]]);
    expect(chip.getAttribute("aria-expanded")).toBe("false");
    expect(chip.querySelector('[data-aspect="fault"]')).toBeTruthy();
  });

  it("all zero: the chip reads the fleet's size with no mark", () => {
    quiet("a"); quiet("b");
    useFleet.setState({ clusters: [cl("a"), cl("b")] });
    const { getByRole } = render(<FleetIndicator />);
    const chip = getByRole("button", { name: "Fleet: 2 clusters" });
    expect(chip.textContent).toBe("2 clusters");
    expect(chip.querySelector("[data-aspect]")).toBeNull();
    fireEvent.click(chip);
    expect(getByRole("menu", { name: "Fleet" }).textContent).toContain("No cluster has a fault, a warning, a connection problem or an unverified reading.");
  });

  it("the list groups clusters by axis, a cluster on two axes under each, with its reason; a row opens it", () => {
    faulty("d");
    useFleet.setState({ clusters: [cl("d", "Degraded", { reason: "metrics API down", env: "prd", region: "weu" })] });
    const { getByRole, getAllByRole, queryByRole } = render(<FleetIndicator />);
    fireEvent.click(getByRole("button", { name: /^Fleet:/ }));
    expect(getByRole("button", { name: /^Fleet:/ }).getAttribute("aria-expanded")).toBe("true");
    const groups = getAllByRole("group").map((g) => g.getAttribute("aria-label"));
    expect(groups).toEqual(["Fault, 1", "Connection stale or degraded, 1"]);
    const rows = getAllByRole("menuitem");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toMatch(/^prd · weud.*Workloads: 2 need attention/);
    expect(rows[1].textContent).toContain("degraded: metrics API down");
    fireEvent.click(rows[0]);
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "d", section: "overview" });
    expect(queryByRole("menu")).toBeNull();
  });

  it("keyboard: opening focuses the first row, arrows rove, Escape closes and returns focus", () => {
    warned("a"); warned("b");
    useFleet.setState({ clusters: [cl("a"), cl("b")] });
    const { getByRole, getAllByRole, queryByRole } = render(<FleetIndicator />);
    const chip = getByRole("button", { name: /^Fleet:/ });
    fireEvent.click(chip);
    const rows = getAllByRole("menuitem");
    expect(document.activeElement).toBe(rows[0]);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(rows[1]);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(rows[0]);
    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(document.activeElement).toBe(rows[1]);
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(chip);
  });

  it("a remote cluster's fault shows while inside another cluster", () => {
    quiet("open"); faulty("remote");
    useFleet.setState({ clusters: [cl("open"), cl("remote")], route: { name: "cluster", cluster: "open", section: "pods" } });
    const { getByRole } = render(<FleetIndicator />);
    expect(getByRole("button", { name: "Fleet: 1 cluster with a fault, of 2" })).toBeTruthy();
  });
});

describe("the station switcher", () => {
  it("lists every cluster in station order with its mark and mod+N hints; the current one is marked", () => {
    faulty("p"); quiet("d"); quiet("s");
    const clusters = [cl("p", "Synced", { env: "prd" }), cl("d", "Synced", { env: "dev" }), cl("s", "Synced", { env: "stg" })];
    useFleet.setState({ clusters, route: { name: "cluster", cluster: "s", section: "overview" } });
    const { getByRole, getAllByRole } = render(<StationSwitcher current={clusters[2]} />);
    fireEvent.click(getByRole("button", { name: "Station s. Switch cluster" }));
    const rows = getAllByRole("menuitem");
    expect(rows.map((r) => r.getAttribute("aria-label"))).toEqual(["d", "s, current", "p, fault"]);
    expect(rows[1].getAttribute("aria-current")).toBe("page");
    expect(rows.map((r) => r.querySelector("kbd")?.textContent?.slice(-1))).toEqual(["1", "2", "3"]);
    expect(rows[2].querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(rows[0].querySelector("[data-aspect]")).toBeNull();
  });

  it("selecting a cluster opens it; Escape returns focus to the crumb", () => {
    quiet("a"); quiet("b");
    const clusters = [cl("a"), cl("b")];
    useFleet.setState({ clusters, route: { name: "cluster", cluster: "a", section: "pods" } });
    const { getByRole, getAllByRole, queryByRole } = render(<StationSwitcher current={clusters[0]} />);
    const crumb = getByRole("button", { name: "Station a. Switch cluster" });
    fireEvent.click(crumb);
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(crumb);
    fireEvent.click(crumb);
    fireEvent.click(getAllByRole("menuitem")[1]);
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "b", section: "overview" });
  });

  it("only the first nine carry a shortcut hint", () => {
    const clusters = Array.from({ length: 11 }, (_, i) => cl(`c${i}`));
    useFleet.setState({ clusters });
    const { getByRole, getAllByRole } = render(<StationSwitcher current={clusters[0]} />);
    fireEvent.click(getByRole("button", { name: "Station c0. Switch cluster" }));
    expect(getAllByRole("menuitem").map((r) => !!r.querySelector("kbd"))).toEqual([...Array(9).fill(true), false, false]);
  });
});
