import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { CondDTO, GatewayRefDTO } from "../store/fleet";

vi.mock("../bridge/gateway", () => ({
  getGatewayTopology: vi.fn(async () => {}),
  getRouteMetrics: vi.fn(async () => {}),
  abandonRouteMetrics: vi.fn(),
}));
// The Gateways are rows (health/useGatewayRows): these tests hand the list a
// read with its coverage; the mounted transport tests are GatewayRows.test.
let rowsValue: GatewayRows;
vi.mock("../bridge/ruleMetrics", () => ({ ruleMetrics: () => Object.assign(Promise.resolve({ status: { available: false, message: "", updatedAt: "" }, scope: "", start: "", end: "", stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "", notes: [] }), { cancel: () => {} }) }));
vi.mock("../health/useGatewayRows", () => ({ useGatewayRows: () => rowsValue }));
vi.mock("../health/useGatewayRouteRows", () => ({ useGatewayRouteRows: () => ({ routes: [], coverage: [], dependencies: [], watchError: null, loading: true, rowsState: { updating: false, refreshing: false, evaluatedAt: null }, partCounts: null }) }));
import type { GatewayRows } from "../health/useGatewayRows";
import { GatewayApiView } from "./GatewayApiView";
import { gatewayRule, withRule } from "../test/backendRules";

const cond = (status: string, reason = "", observedGeneration = 1): CondDTO => ({ status, reason, message: "", observedGeneration });
const gw = (name: string, over: Partial<GatewayRefDTO> = {}): GatewayRefDTO => withRule<GatewayRefDTO>({
  namespace: "infra", name, className: "envoy-gateway", accepted: true, programmed: true, generation: 1, acceptedCond: cond("True"), programmedCond: cond("True"),
  addresses: [{ type: "IPAddress", value: "192.0.2.10" }], listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443 }], ...over,
}, gatewayRule);

type Cov = NonNullable<GatewayRows["coverage"][number]["coverage"]>;
// net seeds the route and the rows read: coverage live unless said otherwise
function net(over: { gateways?: GatewayRefDTO[]; coverage?: Cov; error?: string } = {}) {
  useFleet.setState(() => ({
    route: { name: "cluster", cluster: "x", section: "gatewayApi" },
    clusters: [],
  }));
  const coverage = over.coverage ?? "live";
  rowsValue = {
    gateways: over.gateways ?? [],
    coverage: [{ kind: "Gateway", served: coverage !== "absent", synced: coverage === "live" || coverage === "stale", coverage, error: over.error }],
    watchError: over.error ? `Gateway: ${over.error}` : null,
    loading: false,
    rowsState: { updating: false, refreshing: false, evaluatedAt: null },
    partCounts: null,
  };
}

beforeEach(() => { vi.clearAllMocks(); });

describe("GatewayApiView (redesign step 6)", () => {
  it("reads the Go-computed rule, never its own: verdict and the Needs attention count (1c §2)", () => {
    net({ gateways: [
      gw("flagged", { rule: { included: true, role: "fault", label: "Flagged by backend" } }),
      gw("cleared", { accepted: false, acceptedCond: cond("False", "InvalidParameters"), rule: { included: false, role: "ok", label: "programmed" } }),
    ] });
    render(<GatewayApiView cluster="x" />);
    const rows = screen.getAllByRole("button").filter((b) => b.classList.contains("lens-row"));
    expect(rows[0].textContent).toContain("flagged");
    expect(rows[0].textContent).toContain("Flagged by backend");
    expect(screen.getByRole("button", { name: /^Needs attention/ }).textContent?.trim()).toBe("Needs attention 1");
  });

  it("orders not accepted first and auto-selects it once, naming the condition honestly", () => {
    net({ gateways: [gw("ok"), gw("pending", { programmed: false, programmedCond: cond("False", "AddressNotAssigned") }), gw("broken", { accepted: false, programmed: false, acceptedCond: cond("False", "InvalidParameters"), programmedCond: cond("False") })] });
    render(<GatewayApiView cluster="x" />);
    const rows = screen.getAllByRole("button").filter((b) => b.classList.contains("lens-row"));
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringContaining("broken"), expect.stringContaining("pending"), expect.stringContaining("ok")]);
    const panel = screen.getByTestId("gateway-panel");
    expect(within(panel).getByText(/Accepted is False - InvalidParameters/)).toBeTruthy();
    // the header's count sentence: its Aspect carries the fault's shape (slice 11b)
    const attention = screen.getByTestId("gw-attention");
    expect(attention.textContent).toBe("1 not accepted · 1 needs attention");
    expect(attention.querySelector('[data-aspect="fault"]')).toBeTruthy();
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
    net({ gateways: [gw("eg", { stale: true, generation: 7, observedGeneration: 6, acceptedCond: cond("True", "", 6), programmedCond: cond("True", "", 6) })] });
    render(<GatewayApiView cluster="x" />);
    const row = screen.getByText("eg").closest(".lens-row") as HTMLElement;
    expect(row.textContent).toContain("syncing");
    expect(row.textContent).not.toContain("programmed");
    expect(row.dataset.stale).toBe("true"); // dims every cell but the row's identity (layout.css)
  });

  it("absent, Unknown, explicit False and last-observed False are four different claims", () => {
    net({ gateways: [
      gw("absent", { accepted: false, programmed: false, acceptedCond: cond(""), programmedCond: cond("") }),
      gw("pending", { accepted: false, programmed: false, acceptedCond: cond("Unknown", "Pending"), programmedCond: cond("") }),
      gw("rejected", { accepted: false, programmed: false, acceptedCond: cond("False", "InvalidParameters"), programmedCond: cond("") }),
      gw("lastseen", { accepted: false, programmed: false, generation: 3, acceptedCond: cond("False", "InvalidParameters", 2), programmedCond: cond("") }),
    ] });
    render(<GatewayApiView cluster="x" />);
    const text = (name: string) => [...document.querySelectorAll(".lens-row")].find((r) => r.textContent?.includes(`infra / ${name}`))!.textContent;
    expect(text("absent")).toContain("no status reported");
    expect(text("pending")).toContain("acceptance pending");
    expect(text("rejected")).toContain("not accepted");
    const rejected = [...document.querySelectorAll(".lens-row")].find((r) => r.textContent?.includes("infra / rejected")) as HTMLElement;
    expect(rejected.querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(text("lastseen")).toContain("not accepted (last observed)");
    expect(text("lastseen")).not.toContain("▲");
    expect(screen.getByText("2 unverified")).toBeTruthy();
  });

  it("a last-observed negative is worded as last observed, never as a current outage", () => {
    net({ gateways: [
      gw("lastseen", { accepted: false, programmed: false, generation: 3, acceptedCond: cond("False", "InvalidParameters", 2), programmedCond: cond("") }),
      gw("unprog", { programmed: false, generation: 3, acceptedCond: cond("True", "", 3), programmedCond: cond("False", "AddressNotAssigned", 0) }),
    ] });
    render(<GatewayApiView cluster="x" />);
    const row = (name: string) => [...document.querySelectorAll(".lens-row")].find((r) => r.textContent?.includes(`infra / ${name}`)) as HTMLElement;
    if (!screen.queryByTestId("gateway-panel")?.textContent?.includes("lastseen")) fireEvent.click(row("lastseen"));
    let panel = screen.getByTestId("gateway-panel");
    expect(within(panel).getByText("Accepted was False for generation 2 - InvalidParameters. The controller has not reported on generation 3; whether the current Gateway is accepted is unverified.")).toBeTruthy();
    expect(within(panel).queryByText(/does not serve routes/)).toBeNull();
    fireEvent.click(row("unprog"));
    panel = screen.getByTestId("gateway-panel");
    expect(within(panel).getByText("Programmed was False for an unreported generation - AddressNotAssigned. The controller has not reported on generation 3; whether the data plane is configured now is unverified.")).toBeTruthy();
  });

  it("not served (a confirmed absence), served-but-empty and a failed read are three different states", () => {
    net({ coverage: "absent" });
    const { unmount } = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText(/Gateway API is not installed/)).toBeTruthy();
    unmount();
    net();
    const second = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("No Gateways on this cluster.")).toBeTruthy();
    second.unmount();
    net({ coverage: "unreadable", error: "connection refused" });
    const third = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("Could not list Gateways.")).toBeTruthy();
    expect(screen.getByText("Gateway: connection refused")).toBeTruthy();
    third.unmount();
    net({ coverage: "checking" }); // checking is not absence
    render(<GatewayApiView cluster="x" />);
    expect(screen.queryByText(/Gateway API is not installed/)).toBeNull();
    expect(screen.getByLabelText("reading Gateways")).toBeTruthy();
  });

  it("a stale read keeps the last list, marked - never 'all programmed'", () => {
    net({ gateways: [gw("eg")], coverage: "stale", error: "forbidden" });
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("eg")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/Gateways read failed - showing the last good list.*forbidden/);
    expect(screen.queryByText(/all programmed/)).toBeNull();
    expect(document.querySelector(".lens-header .t-number")?.textContent).toBe("1* gateways");
  });

  it("a filter edit that hides the selection closes it", () => {
    net({ gateways: [gw("broken", { accepted: false, acceptedCond: cond("False", "InvalidParameters") }), gw("ok")] });
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("gateway-panel")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Programmed" }));
    expect(screen.queryByTestId("gateway-panel")).toBeNull();
  });
});
