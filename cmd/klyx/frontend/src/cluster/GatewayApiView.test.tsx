import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { GatewayRefDTO, HubbleStatsDTO } from "../store/fleet";

vi.mock("../bridge/gateway", () => ({
  listGateways: vi.fn(async () => {}),
  getGatewayTopology: vi.fn(async () => {}),
  getRouteMetrics: vi.fn(async () => {}),
  getHubbleFlowStats: vi.fn(async () => ({}) as HubbleStatsDTO),
}));
import { getHubbleFlowStats } from "../bridge/gateway";
import { GatewayApiView } from "./GatewayApiView";

const gw = (name: string, over: Partial<GatewayRefDTO> = {}): GatewayRefDTO => ({
  namespace: "infra", name, className: "envoy-gateway", accepted: true, programmed: true,
  addresses: [{ type: "IPAddress", value: "192.0.2.10" }], listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443 }], ...over,
});

function net(over: Partial<ReturnType<typeof useFleet.getState>["network"]> = {}, hubble = false) {
  useFleet.setState((s) => ({
    route: { name: "cluster", cluster: "x", section: "gatewayApi" },
    clusters: hubble ? [{ ...(s.clusters[0] ?? {}), name: "x", hasHubble: true } as never] : [],
    network: { cluster: "x", served: true, gateways: [], listLoading: false, listError: null, selected: null, topology: null, topologyLoading: false, topologyError: null, topologyAt: null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false, ...over },
  }));
}

beforeEach(() => { vi.clearAllMocks(); });

describe("GatewayApiView (redesign step 6)", () => {
  it("orders not accepted first and auto-selects it once, naming the condition honestly", () => {
    net({ gateways: [gw("ok"), gw("pending", { programmed: false }), gw("broken", { accepted: false, programmed: false })] });
    render(<GatewayApiView cluster="x" />);
    const rows = screen.getAllByRole("button").filter((b) => b.classList.contains("lens-row"));
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringContaining("broken"), expect.stringContaining("pending"), expect.stringContaining("ok")]);
    const panel = screen.getByTestId("gateway-panel");
    expect(within(panel).getByText(/Accepted is not True \(False, Unknown or not reported\)/)).toBeTruthy();
    expect(screen.getByText("▲ 1 not accepted · 1 not programmed")).toBeTruthy();
  });

  it("the inspector's primary action opens the topology", () => {
    net({ gateways: [gw("eg")] });
    render(<GatewayApiView cluster="x" />);
    expect(screen.queryByTestId("gateway-panel")).toBeNull(); // nothing broken: no auto-select
    fireEvent.click(screen.getByText("eg"));
    fireEvent.click(within(screen.getByTestId("gateway-panel")).getByRole("button", { name: "Open topology" }));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.gateway).toEqual({ namespace: "infra", name: "eg" });
  });

  it("a stale Gateway is syncing - never confirmed programmed - and dims", () => {
    net({ gateways: [gw("eg", { stale: true, generation: 7, observedGeneration: 6 })] });
    render(<GatewayApiView cluster="x" />);
    const row = screen.getByText("eg").closest(".lens-row") as HTMLElement;
    expect(row.textContent).toContain("syncing");
    expect(row.textContent).not.toContain("programmed");
    expect(row.style.opacity).toBe("0.6");
  });

  it("a stale Gateway that is not accepted keeps its fault", () => {
    net({ gateways: [gw("eg", { accepted: false, programmed: false, stale: true, generation: 3, observedGeneration: 2 })] });
    render(<GatewayApiView cluster="x" />);
    expect(document.querySelector(".lens-row")!.textContent).toContain("▲ not accepted · syncing");
  });

  it("not served, served-but-empty and a failed list are three different states", () => {
    net({ served: false });
    const { unmount } = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText(/Gateway API is not installed/)).toBeTruthy();
    unmount();
    net({ served: true });
    const second = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("No Gateways on this cluster.")).toBeTruthy();
    second.unmount();
    net({ served: false, listError: "connection refused" });
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("Could not list Gateways.")).toBeTruthy();
    expect(screen.getByText("connection refused")).toBeTruthy();
  });

  it("a failed refresh keeps the last list, marked", () => {
    net({ gateways: [gw("eg")], listError: "forbidden" });
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("eg")).toBeTruthy();
    expect(screen.getByText("Read failed - showing the last good list.")).toBeTruthy();
    expect(screen.getByText("all programmed at the last read")).toBeTruthy();
  });

  it("another cluster's gateways are never shown", () => {
    net({ cluster: "other", gateways: [gw("foreign")] });
    render(<GatewayApiView cluster="x" />);
    expect(screen.queryByText("foreign")).toBeNull();
  });

  it("a filter edit that hides the selection closes it", () => {
    net({ gateways: [gw("broken", { accepted: false }), gw("ok")] });
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("gateway-panel")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Programmed" }));
    expect(screen.queryByTestId("gateway-panel")).toBeNull();
  });

  it("Hubble flows are a second view, shown only with Hubble; zero drops never glow", async () => {
    vi.mocked(getHubbleFlowStats).mockResolvedValue({
      forwardRate: 120, dropRate: 0, dropReasons: [], dnsErrorRate: null, dnsTotalRate: null, httpErrorRate: null, httpTotalRate: null,
      status: { available: true, message: "", updatedAt: "2026-10-10T00:00:00Z" },
    });
    net({ gateways: [gw("eg")] }, true);
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: "Flows (Hubble)" }));
    expect(await screen.findByText("no drops")).toBeTruthy();
    expect(screen.queryByText(/dropped\/s/)).toBeNull();
    expect(screen.getByText("No drops in the window.")).toBeTruthy();
  });

  it("without Hubble there is no Flows view", () => {
    net({ gateways: [gw("eg")] });
    render(<GatewayApiView cluster="x" />);
    expect(screen.queryByRole("button", { name: "Flows (Hubble)" })).toBeNull();
  });
});
