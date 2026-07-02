import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import { GatewayApiView } from "./GatewayApiView";

vi.mock("../bridge/gateway", () => ({ listGateways: vi.fn(async () => {}), getGatewayTopology: vi.fn(async () => {}) }));

function net(over: Partial<ReturnType<typeof useFleet.getState>["network"]> = {}) {
  useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gatewayApi" }, network: { cluster: "x", served: true, gateways: [], listLoading: false, selected: null, topology: null, topologyLoading: false, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false, ...over } });
}

beforeEach(() => vi.clearAllMocks());

describe("GatewayApiView", () => {
  it("shows 'Gateway API not installed' when not served", () => {
    net({ served: false, gateways: [] });
    const { getByText } = render(<GatewayApiView cluster="x" />);
    expect(getByText(/Gateway API is not installed/i)).toBeTruthy();
  });

  it("shows 'No Gateways' when served but empty", () => {
    net({ served: true, gateways: [] });
    const { getByText } = render(<GatewayApiView cluster="x" />);
    expect(getByText(/No Gateways found/i)).toBeTruthy();
  });

  it("lists gateways and selecting one opens the topology route", () => {
    net({ served: true, gateways: [{ namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true }] });
    const { getByText } = render(<GatewayApiView cluster="x" />);
    fireEvent.click(getByText("eg"));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.gateway).toEqual({ namespace: "infra", name: "eg" });
  });

  it("shows gateway address and listeners in the first list", () => {
    net({ served: true, gateways: [{
      namespace: "infra",
      name: "eg",
      className: "envoy-gateway",
      addresses: [{ type: "IPAddress", value: "192.0.2.10" }],
      listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443 }],
      accepted: true,
      programmed: true,
    }] });
    const { getByText } = render(<GatewayApiView cluster="x" />);
    expect(getByText("Gateway topology")).toBeTruthy();
    expect(getByText("Gateway API")).toBeTruthy();
    expect(getByText("address")).toBeTruthy();
    expect(getByText("listeners")).toBeTruthy();
    expect(getByText("192.0.2.10")).toBeTruthy();
    expect(getByText("https HTTPS:443")).toBeTruthy();
  });

  it("owns scrolling for the gateway list inside the hidden cluster shell", () => {
    net({ served: true, gateways: [{ namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true }] });
    const { getByTestId } = render(<GatewayApiView cluster="x" />);
    expect(getByTestId("gateway-list-scroll").style.overflowY).toBe("auto");
  });
});
