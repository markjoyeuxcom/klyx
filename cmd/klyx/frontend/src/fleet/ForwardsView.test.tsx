import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ForwardsView } from "./ForwardsView";
import { useFleet } from "../store/fleet";
import type { ForwardDTO } from "../store/fleet";

vi.mock("../bridge/forwards", () => ({
  stopForward: vi.fn().mockResolvedValue(undefined),
  stopAllForwards: vi.fn().mockResolvedValue(undefined),
}));
const mockOpenURL = vi.fn().mockResolvedValue(undefined);
vi.mock("@wailsio/runtime", () => ({
  Browser: { OpenURL: (...args: unknown[]) => mockOpenURL(...args) },
}));
import { stopForward, stopAllForwards } from "../bridge/forwards";

const active: ForwardDTO = {
  id: "nelli/monitoring/grafana#1", cluster: "homelab-nelli", namespace: "monitoring",
  targetKind: "Pod", targetName: "grafana-74c", localPort: 54321, targetPort: 3000,
  startedUnix: Math.floor(Date.now() / 1000) - 90, status: "active",
};
const brokenSvc: ForwardDTO = {
  id: "blue/db/postgres#2", cluster: "homelab-blue", namespace: "db",
  targetKind: "Service", targetName: "postgres", localPort: 54322, targetPort: 5432,
  startedUnix: Math.floor(Date.now() / 1000) - 3700, status: "broken",
};

describe("ForwardsView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().setForwards([]);
  });

  it("renders the empty state when no forwards exist", () => {
    const { getByText } = render(<ForwardsView />);
    expect(getByText(/No active port-forwards/)).toBeTruthy();
  });

  it("renders one detailed row per forward with cluster, target, and tunnel", () => {
    useFleet.getState().setForwards([active, brokenSvc]);
    const { getByText, getByTestId } = render(<ForwardsView />);
    expect(getByTestId(`forwards-view-row-${active.id}`).textContent).toContain("homelab-nelli");
    expect(getByText(/grafana-74c/)).toBeTruthy();
    expect(getByText(/localhost:54321/)).toBeTruthy();
    expect(getByText("svc")).toBeTruthy(); // Service kind shortened
    expect(getByText(/1m ago/)).toBeTruthy();
  });

  it("header counts actives and broken honestly", () => {
    useFleet.getState().setForwards([active, brokenSvc]);
    const { getByText, getByLabelText } = render(<ForwardsView />);
    expect(getByText("2")).toBeTruthy();
    expect(getByText(/active across the fleet/)).toBeTruthy();
    expect(getByText("▲ 1 broken")).toBeTruthy();
    // an active tunnel is neutral; only the broken one is marked
    expect(getByLabelText("active").textContent).toBe("·");
    expect(getByLabelText("broken").textContent).toBe("▲");
  });

  it("stop dispatches stopForward with the forward id", () => {
    useFleet.getState().setForwards([active]);
    const { getByRole } = render(<ForwardsView />);
    fireEvent.click(getByRole("button", { name: `stop forward ${active.id}` }));
    expect(stopForward).toHaveBeenCalledWith(active.id);
  });

  it("stop all dispatches stopAllForwards", () => {
    useFleet.getState().setForwards([active, brokenSvc]);
    const { getByTestId } = render(<ForwardsView />);
    fireEvent.click(getByTestId("forwards-view-stop-all"));
    expect(stopAllForwards).toHaveBeenCalled();
  });

  it("open-in-browser opens the localhost URL", () => {
    useFleet.getState().setForwards([active]);
    const { getByRole } = render(<ForwardsView />);
    fireEvent.click(getByRole("button", { name: `open ${active.id} in browser` }));
    expect(mockOpenURL).toHaveBeenCalledWith("http://localhost:54321");
  });
});
