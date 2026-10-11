import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { HubbleStatsDTO } from "../store/fleet";

vi.mock("../bridge/gateway", () => ({
  getHubbleFlowStats: vi.fn(async () => ({}) as HubbleStatsDTO),
}));
import { getHubbleFlowStats } from "../bridge/gateway";
import { HubbleFlowsView } from "./HubbleFlowsView";

const seed = (hasHubble: boolean) => useFleet.setState(() => ({
  route: { name: "cluster", cluster: "x", section: "flows" },
  clusters: [{ name: "x", state: "Synced", hasHubble } as never],
}));

beforeEach(() => { vi.clearAllMocks(); });

describe("HubbleFlowsView (redesign step 7a)", () => {
  it("is its own destination - no Gateway API needed; zero drops never glow", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue({
      forwardRate: 120, dropRate: 0, dropReasons: [], dnsErrorRate: null, dnsTotalRate: null, httpErrorRate: null, httpTotalRate: null,
      status: { available: true, message: "", updatedAt: "2026-10-10T00:00:00Z" },
    });
    seed(true);
    render(<HubbleFlowsView cluster="x" />);
    expect(await screen.findByText("no drops")).toBeTruthy();
    expect(screen.queryByText(/dropped\/s/)).toBeNull();
    expect(screen.getByText("No drops in the window.")).toBeTruthy();
  });

  it("without Hubble it says so and reads nothing", () => {
    seed(false);
    render(<HubbleFlowsView cluster="x" />);
    expect(screen.getByText("Hubble is not detected on this cluster.")).toBeTruthy();
    expect(getHubbleFlowStats).not.toHaveBeenCalled();
  });

  it("a failed read is unreadable with Retry, never an empty success", async () => {
    vi.mocked(getHubbleFlowStats).mockRejectedValue(new Error("prometheus unreachable"));
    seed(true);
    render(<HubbleFlowsView cluster="x" />);
    expect(await screen.findByText("Could not read Hubble metrics.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});
