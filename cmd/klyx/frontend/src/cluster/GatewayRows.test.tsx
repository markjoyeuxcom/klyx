import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Gateways list mounted on the REAL rows hook (health/useGatewayRows)
// and health store - only the transport is stubbed (slice 3 §5.6). The
// topology stays a fetch (stubbed).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}), abandonRouteMetrics: vi.fn() }));
import { GatewayApiView } from "./GatewayApiView";
import { buildCommands } from "../chrome/commands";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type CondDTO, type GatewayRefDTO } from "../store/fleet";
import { gatewayRule, withRule } from "../test/backendRules";
import { clock } from "../layout/coverage";

const cond = (status: string, reason = ""): CondDTO => ({ status, reason, message: "", observedGeneration: 1 });
const gw = (name: string, over: Partial<GatewayRefDTO> = {}): GatewayRefDTO => withRule<GatewayRefDTO>({
  namespace: "infra", name, className: "envoy", accepted: true, programmed: true, generation: 1, acceptedCond: cond("True"), programmedCond: cond("True"),
  addresses: [], listeners: [], uid: `uid-${name}`, ...over,
}, gatewayRule);
const brokenGw = (name: string) => gw(name, { accepted: false, programmed: false, acceptedCond: cond("False", "InvalidParameters"), programmedCond: cond("False") });

const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
// the production shape: Gateways counted, GatewayClasses a dependency
const kinds = (gateways: Partial<KindCoverage> = {}, classes: Partial<KindCoverage> = {}) => [kind("gateways", gateways), kind("gatewayclasses", { role: "dependency", ...classes })];
const part = (ver: number, k: KindCoverage[], native: { total: number; included: number }): HealthPart => ({
  id: "gateways", entity: "Gateways", scope: "", classifier: "native", coverage: "live", kinds: k, obs: ver, ver,
  native: { total: native.total, included: native.included, word: "", roles: { total: native.total, fault: native.included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: native.total - native.included } },
});
const rowsDTO = (incarnation: number, ver: number, rows: GatewayRefDTO[], k = kinds(), cluster = "x"): RowsDTO => ({
  cluster, incarnation, task: "gateway", part: part(ver, k, { total: rows.length, included: rows.filter((r) => r.rule.included).length }), evaluatedAt: "2026-10-11T12:00:00Z", rows,
});
const summary = (incarnation: number, seq: number, ver: number, total: number, included: number): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "gateway", coverage: "live", parts: [part(ver, kinds(), { total, included })] }],
});

function open(incarnation = 1, cluster = "x", h = "g") {
  if (!useHealth.getState().subs[h]) addSub(h, cluster, "gateway", "gateways");
  openStarted(h, `tok-${h}-${incarnation}`);
  openSucceeded(h, `tok-${h}-${incarnation}`, incarnation);
}
const headline = () => document.querySelector(".lens-header .t-number")?.textContent;
const rowNames = () => [...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent);

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gatewayApi" }, clusters: [] });
});

describe("Gateways rows, mounted end to end (slice 3 §5.6)", () => {
  it("a failing GatewayClass dependency is never the Gateways list's own evidence", () => {
    open();
    applyRows("g", rowsDTO(1, 1, [gw("eg")], kinds({}, { coverage: "unreadable", class: "forbidden", error: "rbac" })));
    render(<GatewayApiView cluster="x" />);
    expect(headline()).toBe("1"); // complete: unstarred, no read error
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("all programmed")).toBeTruthy();
  });

  it("A4 moves the global counts with the notice, beside a stale read", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      open();
      applyRows("g", rowsDTO(1, 1, [gw("ok"), brokenGw("bad")], kinds({ coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" })), 0);
      render(<GatewayApiView cluster="x" />);
      act(() => { applySummary(summary(1, 2, 2, 6, 4), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/read failed - showing the last good list from .*dial tcp/);
      expect(headline()).toBe("6*");
      expect(screen.getByTestId("gw-attention").textContent).toBe("4 need attention");
      expect(screen.getByRole("button", { name: /^Needs attention/ }).textContent?.trim()).toBe("Needs attention 4");
      act(() => bootstrapSettled("g", useHealth.getState().subs.g.requestGen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Final(1) -> summary(2) -> the new incarnation's rows: the old ones never stay", () => {
    open();
    applyRows("g", rowsDTO(1, 1, [gw("old")]));
    render(<GatewayApiView cluster="x" />);
    expect(rowNames()).toEqual(["infra/old"]);
    act(() => applySummary({ ...summary(1, 9, 1, 1, 0), final: true }));
    expect(rowNames()).toEqual([]);
    expect(screen.getByLabelText("reading Gateways")).toBeTruthy();
    act(() => applySummary(summary(2, 1, 1, 1, 0)));
    act(() => { open(2); applyRows("g", rowsDTO(2, 1, [gw("new")])); });
    expect(rowNames()).toEqual(["infra/new"]);
  });

  it("a confirmed absence is not installed; a stale empty read is last good; a discovery gap is named", () => {
    open();
    applyRows("g", rowsDTO(1, 1, [], kinds({ coverage: "absent" })));
    const { unmount } = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText(/Gateway API is not installed/)).toBeTruthy();
    unmount();
    const lastGood = "2026-10-11T09:30:00Z";
    act(() => { applyRows("g", rowsDTO(1, 2, [], kinds({ coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood }))); });
    const second = render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("No Gateways in the last good read.")).toBeTruthy();
    expect(screen.getByText(new RegExp(`last good ${clock(lastGood)}`))).toBeTruthy();
    second.unmount();
    act(() => { applyRows("g", rowsDTO(1, 3, [gw("eg")], kinds({ discovery: "forbidden: discovery re-check" }))); });
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/Gateway: forbidden: discovery re-check/);
    expect(headline()).toBe("1");
  });

  it("removal needs a complete read; a stale read keeps the selection, unverified", () => {
    open();
    applyRows("g", rowsDTO(1, 1, [gw("eg")]));
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(screen.getByText("eg").closest(".lens-row")!);
    act(() => { applyRows("g", rowsDTO(1, 2, [], kinds({ coverage: "stale", class: "transport", error: "dial tcp" }))); });
    const panel = () => within(screen.getByTestId("gateway-panel"));
    expect(panel().getByTestId("selection-unverified").textContent).toMatch(/Not in the current Gateway read\..*unverified/);
    act(() => { applyRows("g", rowsDTO(1, 3, [], kinds())); });
    expect(panel().getByText(/Gateway removed\./)).toBeTruthy();
    expect(panel().queryByRole("button", { name: "Open topology" })).toBeNull();
  });

  it("the palette indexes this cluster's current Gateway rows only", () => {
    // another cluster's subscription is registered first: it must still not answer
    open(1, "y", "h");
    applyRows("h", rowsDTO(1, 1, [gw("foreign")], kinds(), "y"));
    open(1, "x", "g");
    applyRows("g", rowsDTO(1, 1, [gw("eg")]));
    const ids = buildCommands(useFleet.getState()).map((c) => c.id);
    expect(ids).toContain("gateway:infra/eg");
    expect(ids).not.toContain("gateway:infra/foreign");
    act(() => applySummary({ ...summary(1, 9, 1, 1, 0), final: true }));
    expect(buildCommands(useFleet.getState()).map((c) => c.id)).not.toContain("gateway:infra/eg");
  });
});
