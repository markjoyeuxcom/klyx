import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { core, kind, replace, summary, task, verdict } from "../test/healthFixtures";

vi.mock("../bridge/mesh", () => ({ getMeshGraph: vi.fn(async () => {}) }));
vi.mock("../bridge/fleetboard", () => ({ fetchFleetBoard: vi.fn(async () => {}) }));
vi.mock("./ClusterCard", () => ({
  ClusterCard: ({ c }: { c: ClusterDTO }) => <div data-testid="card">{c.name}</div>,
  clusterDiagnosticScore: () => 0,
}));
import { FleetView, fleetColumns } from "./FleetView";

const c = (name: string, state = "Synced"): ClusterDTO => ({ name, state, nodesTotal: 1, pods: 10 } as unknown as ClusterDTO);

beforeEach(() => { useFleet.setState({ clusters: [], fleetBoard: {}, mesh: null, route: { name: "fleet" } }); resetHealthStore(); });

describe("FleetView on the workspace template (redesign step 7d)", () => {
  it("no clusters: one empty state with the way forward", () => {
    render(<FleetView />);
    expect(screen.getByText("No clusters connected yet.")).toBeTruthy();
    expect(screen.queryByTestId("fleet-board")).toBeNull();
  });

  it("header facts replace the meter strip: clusters counted, zero neutral, faults shaped, nothing green", () => {
    useFleet.setState({ clusters: [c("a"), c("b"), c("d"), c("c", "Failed")] });
    applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 1, ok: 4 }, [kind("deployments")])])), {}, "a"));
    applySummary(summary(core(), {}, "b"));
    const { container } = render(<FleetView />);
    expect(screen.getByText("4")).toBeTruthy();
    expect(screen.getByText(/clusters · 3 connected/)).toBeTruthy();
    expect(screen.getByText("▲ 1 unreachable")).toBeTruthy();
    expect(screen.getByTestId("fleet-attention").textContent).toBe("▲ 1 needs attention");
    expect(screen.getByTestId("fleet-unknown").textContent).toBe("1 with unknown coverage"); // d: no summary yet
    expect(container.innerHTML).not.toMatch(/color-text-success/);
  });

  it("cards sit in the bounded grid; ClusterMesh only when a cluster participates", () => {
    useFleet.setState({ clusters: [c("a")], mesh: { nodes: [{ name: "a", state: "unavailable" }], edges: [] } as never });
    const { container, rerender } = render(<FleetView />);
    expect(container.querySelector(".fleet-grid")?.children).toHaveLength(1);
    expect(screen.queryByRole("region", { name: "ClusterMesh" })).toBeNull();
    useFleet.setState({ mesh: { nodes: [{ name: "a", state: "ok", peers: [] }], edges: [] } as never });
    rerender(<FleetView />);
    expect(screen.getByRole("region", { name: "ClusterMesh" })).toBeTruthy();
  });
});

describe("fleetColumns (spec §1 bounded grid)", () => {
  it("fits as many >=360px columns as the width allows, each capped at 480px", () => {
    expect(fleetColumns(1420)).toEqual({ count: 3, width: 468 });
    expect(fleetColumns(2400)).toEqual({ count: 6, width: 393 });
    expect(fleetColumns(1000)).toEqual({ count: 2, width: 480 });
    expect(fleetColumns(728)).toEqual({ count: 2, width: 360 });
    expect(fleetColumns(727)).toEqual({ count: 1, width: 480 });
  });
  it("below 360px one card takes the full width; unmeasured falls back to CSS", () => {
    expect(fleetColumns(300)).toEqual({ count: 1, width: 300 });
    expect(fleetColumns(0)).toEqual({ count: 1, width: 0 });
  });
});
