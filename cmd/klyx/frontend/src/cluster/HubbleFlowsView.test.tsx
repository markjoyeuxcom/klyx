import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { HubbleStatsDTO } from "../store/fleet";

vi.mock("../bridge/gateway", () => ({
  getHubbleFlowStats: vi.fn(async () => ({}) as HubbleStatsDTO),
}));
import { getHubbleFlowStats } from "../bridge/gateway";
import { HubbleFlowsView } from "./HubbleFlowsView";

const seed = (hasHubble: boolean, state = "Synced") => useFleet.setState(() => ({
  route: { name: "cluster", cluster: "x", section: "flows" },
  clusters: [{ name: "x", state, hasHubble } as never],
}));

beforeEach(() => { vi.clearAllMocks(); });

describe("HubbleFlowsView (redesign step 7a)", () => {
  it("is its own destination - no Gateway API needed (none detected here); zero drops never glow", async () => {
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

  it("names where Hubble's metrics are read (Settings S3b §3.4)", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue({
      forwardRate: 120, dropRate: 0, dropReasons: [], dnsErrorRate: null, dnsTotalRate: null, httpErrorRate: null, httpTotalRate: null,
      status: { available: true, message: "", updatedAt: "2026-10-10T00:00:00Z", sentence: "from lgtm-we-metrics, hosted by prd, filtered by cluster=x" },
    });
    seed(true);
    render(<HubbleFlowsView cluster="x" />);
    expect((await screen.findByTestId("hubble-source")).textContent).toBe("Hubble's Prometheus metrics, read from lgtm-we-metrics, hosted by prd, filtered by cluster=x · on demand");
  });

  it("confirmed absent says so and reads nothing", () => {
    seed(false);
    render(<HubbleFlowsView cluster="x" />);
    expect(screen.getByText("Hubble is not detected on this cluster.")).toBeTruthy();
    expect(getHubbleFlowStats).not.toHaveBeenCalled();
  });

  it("a restored route while connecting says checking, never absent", () => {
    seed(false, "Connecting");
    render(<HubbleFlowsView cluster="x" />);
    expect(screen.getByText("Checking for Hubble…")).toBeTruthy();
    expect(screen.queryByText(/not detected/)).toBeNull();
    expect(getHubbleFlowStats).not.toHaveBeenCalled();
  });

  it("an unreadable cluster cannot claim absence", () => {
    seed(false, "Failed");
    render(<HubbleFlowsView cluster="x" />);
    expect(screen.getByText("Cannot tell whether Hubble is installed.")).toBeTruthy();
    expect(screen.queryByText(/not detected/)).toBeNull();
  });

  it("a failed read is unreadable with Retry, never an empty success", async () => {
    vi.mocked(getHubbleFlowStats).mockRejectedValue(new Error("prometheus unreachable"));
    seed(true);
    render(<HubbleFlowsView cluster="x" />);
    expect(await screen.findByText("Could not read Hubble metrics.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});
