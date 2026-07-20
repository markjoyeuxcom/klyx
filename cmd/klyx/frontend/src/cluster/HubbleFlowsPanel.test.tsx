import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { HubbleStatsDTO } from "../store/fleet";
import { HubbleFlowsPanel } from "./HubbleFlowsPanel";

vi.mock("../bridge/gateway", () => ({
  getHubbleFlowStats: vi.fn(async () => ({}) as HubbleStatsDTO),
}));
import { getHubbleFlowStats } from "../bridge/gateway";

const stats = (over: Partial<HubbleStatsDTO> = {}): HubbleStatsDTO => ({
  forwardRate: 120.5, dropRate: 3.25,
  dropReasons: [
    { reason: "Policy denied", protocol: "TCP", rate: 3.0 },
    { reason: "Invalid destination mac", protocol: "UDP", rate: 0.25 },
  ],
  dnsErrorRate: 2.5, dnsTotalRate: 52.5, httpErrorRate: 1.5, httpTotalRate: 81.5,
  status: { available: true, message: "", updatedAt: "2026-08-04T10:00:00Z" },
  ...over,
});

describe("HubbleFlowsPanel", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders verdict meters and drop reasons ranked by rate", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue(stats());
    const { findByText, getByText } = render(<HubbleFlowsPanel cluster="x" />);
    expect(await findByText("Policy denied")).toBeTruthy();
    expect(getByText("dropped/s")).toBeTruthy();
    expect(getByText("forwarded/s")).toBeTruthy();
    expect(getByText("dns errors/s")).toBeTruthy();
    expect(getByText("http 5xx/s")).toBeTruthy();
  });

  it("a degraded status renders its reason, never an empty panel", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue(stats({
      forwardRate: null, dropRate: null, dropReasons: [], dnsTotalRate: null, dnsErrorRate: null, httpTotalRate: null, httpErrorRate: null,
      status: { available: false, message: "metrics unavailable: no Prometheus endpoint", updatedAt: "" },
    }));
    const { findByText } = render(<HubbleFlowsPanel cluster="x" />);
    expect(await findByText(/metrics unavailable: no Prometheus endpoint/)).toBeTruthy();
  });

  it("the all-empty state says HOW to enable hubble metrics", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue(stats({
      forwardRate: null, dropRate: null, dropReasons: [], dnsTotalRate: null, dnsErrorRate: null, httpTotalRate: null, httpErrorRate: null,
      status: { available: true, message: "no Hubble metric series found - enable hubble.metrics (drop, flow, dns, http) in the Cilium config and scrape hubble-metrics (:9965)", updatedAt: "" },
    }));
    const { findByText } = render(<HubbleFlowsPanel cluster="x" />);
    expect(await findByText(/enable hubble.metrics/)).toBeTruthy();
  });

  it("zero-error sections do not glow (no dns/http meters at zero)", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue(stats({ dnsErrorRate: 0, httpErrorRate: 0, dropReasons: [] }));
    const { findByText, queryByText } = render(<HubbleFlowsPanel cluster="x" />);
    await findByText("forwarded/s");
    expect(queryByText("dns errors/s")).toBeNull();
    expect(queryByText("http 5xx/s")).toBeNull();
  });
});
