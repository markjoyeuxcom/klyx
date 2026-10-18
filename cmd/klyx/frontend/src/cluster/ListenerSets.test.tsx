import { describe, it, expect, beforeEach, vi } from "vitest";
import { act, render, fireEvent, screen, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { CondDTO, GatewayRefDTO, ListenerDTO, ListenerSetRowDTO } from "../store/fleet";
import type { GatewayRows } from "../health/useGatewayRows";
import type { ListenerSetRows } from "../health/useListenerSetRows";

vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}), abandonRouteMetrics: vi.fn() }));
let gwRows: GatewayRows;
let lsRows: ListenerSetRows;
vi.mock("../health/useGatewayRows", () => ({ useGatewayRows: () => gwRows }));
vi.mock("../health/useListenerSetRows", () => ({ useListenerSetRows: () => lsRows }));
vi.mock("../health/useGatewayRouteRows", () => ({ useGatewayRouteRows: () => ({ routes: [], coverage: [], dependencies: [], watchError: null, loading: true, rowsState: { updating: false, refreshing: false, evaluatedAt: null }, partCounts: null }) }));
import { GatewayApiView } from "./GatewayApiView";

const cond = (status: string, reason = "", observedGeneration = 1): CondDTO => ({ status, reason, message: "", observedGeneration });
const rows = { updating: false, refreshing: false, evaluatedAt: null };
const listener = (name: string, over: Partial<ListenerDTO> = {}): ListenerDTO => ({ name, protocol: "HTTPS", hostname: `${name}.example.com`, port: 8443, ...over });
const healthy = (generation = 1): ListenerDTO["status"] => ({ attachedRoutes: 1, supportedKinds: [{ group: "gateway.networking.k8s.io", kind: "HTTPRoute" }],
  conditions: ["Accepted", "ResolvedRefs", "Programmed"].map((t) => ({ type: t, status: "True", reason: t, message: "", observedGeneration: generation, stale: false })) });

const gw = (name: string, over: Partial<GatewayRefDTO> = {}): GatewayRefDTO => ({
  namespace: "infra", name, className: "envoy", accepted: true, programmed: true, generation: 1, acceptedCond: cond("True"), programmedCond: cond("True"),
  listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443 }], rule: { included: false, role: "ok", label: "programmed" },
  allowedListeners: { set: true, from: "Same" }, attachedListenerSets: 2, ...over,
});
const ls = (ns: string, name: string, precedence: number, over: Partial<ListenerSetRowDTO> = {}): ListenerSetRowDTO => ({
  uid: `uid-${ns}-${name}`, gvr: "gateway.networking.k8s.io/v1/listenersets", generation: 1, namespace: ns, name,
  parent: { group: "gateway.networking.k8s.io", kind: "Gateway", namespace: "infra", name: "eg" }, parentState: "found",
  listeners: [listener("extra", { status: healthy() })], acceptedCond: cond("True", "Accepted"), programmedCond: cond("True", "Programmed"),
  stale: false, observedGeneration: 1, allowance: { from: "Same", allowed: "yes", text: "the Gateway allows ListenerSets from its own namespace (Same)", disagrees: false },
  precedence, siblings: 3, precedenceComplete: true, rule: { included: false, role: "ok", label: "programmed" }, ...over,
});

function seed(o: { gateways?: GatewayRefDTO[]; sets?: ListenerSetRowDTO[]; lsCoverage?: "live" | "absent" } = {}) {
  useFleet.setState(() => ({ route: { name: "cluster", cluster: "x", section: "gatewayApi" }, clusters: [] }));
  gwRows = { gateways: o.gateways ?? [gw("eg")], coverage: [{ kind: "Gateway", served: true, synced: true, coverage: "live" }], watchError: null, loading: false, rowsState: rows, partCounts: null };
  const c = o.lsCoverage ?? "live";
  lsRows = { sets: o.sets ?? [], coverage: [{ kind: "ListenerSet", served: c !== "absent", synced: c === "live", coverage: c }], dependencies: [], watchError: null, loading: false, rowsState: rows, partCounts: null };
}

const conflicted = ls("infra", "third", 3, {
  rule: { included: true, role: "degraded", label: "listener extra: HostnameConflict" },
  listeners: [listener("extra", { status: { attachedRoutes: 0, supportedKinds: [], conditions: [
    { type: "Accepted", status: "True", reason: "Accepted", message: "", observedGeneration: 1, stale: false },
    { type: "Conflicted", status: "True", reason: "HostnameConflict", message: "a.example.com is claimed by a listener that ranks earlier", observedGeneration: 1, stale: false },
  ] } })],
});
const notAllowed = ls("team-b", "second", 2, {
  acceptedCond: cond("False", "NotAllowed"), programmedCond: cond("False", "Invalid"), rule: { included: true, role: "fault", label: "not accepted: NotAllowed" },
  allowance: { from: "Same", allowed: "no", text: "the Gateway allows ListenerSets from its own namespace (Same); this one is in team-b", disagrees: false },
});
const first = ls("infra", "first", 1);

beforeEach(() => { vi.clearAllMocks(); });

describe("the ListenerSets view (coverage S7a §9)", () => {
  it("lists broken first and inspects the parent, merge order, allowance and listeners", () => {
    seed({ sets: [first, conflicted, notAllowed] });
    render(<GatewayApiView cluster="x" />);
    const views = screen.getByTestId("gateway-views");
    fireEvent.click(within(views).getByRole("button", { name: /^ListenerSets/ }));
    const listRows = screen.getAllByTestId("listenerset-row");
    expect(listRows.map((r) => r.textContent)).toEqual([expect.stringContaining("second"), expect.stringContaining("third"), expect.stringContaining("first")]);
    // the first broken one is selected once
    const panel = screen.getByTestId("listenerset-panel");
    expect(within(panel).getByText(/Its controller did not accept it - NotAllowed/)).toBeTruthy();
    expect(within(panel).getByTestId("listenerset-precedence").textContent).toBe("2nd of 3 ListenerSets naming this parent, after the Gateway's own listeners");
    expect(within(panel).getByTestId("listenerset-allowance").textContent).toContain("this one is in team-b");
    expect(within(panel).getByText(/read as listenersets.v1/)).toBeTruthy();
    // the conflicted one names the controller's reason, never Klyx's own
    fireEvent.click(listRows[1]);
    const issue = within(screen.getByTestId("listenerset-panel")).getByTestId("listener-issue");
    expect(issue.textContent).toBe("Conflicted True - HostnameConflict: a.example.com is claimed by a listener that ranks earlier");
  });

  it("says a disagreement, keeps the controller's verdict, and qualifies an incomplete merge order", () => {
    const disagree = ls("team-c", "odd", 1, { precedenceComplete: false, siblings: 1,
      allowance: { from: "None", allowed: "no", text: "the controller accepted it although the Gateway allows no ListenerSets (allowedListeners None)", disagrees: true } });
    seed({ sets: [disagree] });
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(within(screen.getByTestId("gateway-views")).getByRole("button", { name: /^ListenerSets/ }));
    const row = screen.getByTestId("listenerset-row");
    expect(row.textContent).toContain("None · no · disagrees");
    expect(row.textContent).toContain("programmed");
    fireEvent.click(row);
    const panel = screen.getByTestId("listenerset-panel");
    expect(within(panel).getByTestId("listenerset-allowance").textContent).toMatch(/^the controller accepted it although/);
    expect(within(panel).getByTestId("listenerset-precedence").textContent).toContain("the ListenerSets read is incomplete");
  });

  it("a parent absent from an incomplete read is not established, never not found", () => {
    seed({ sets: [ls("infra", "lost", 1, { parentState: "not established", rule: { included: false, role: "unknown", label: "parent Gateway not established" } })] });
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(within(screen.getByTestId("gateway-views")).getByRole("button", { name: /^ListenerSets/ }));
    fireEvent.click(screen.getByTestId("listenerset-row"));
    const panel = screen.getByTestId("listenerset-panel");
    expect(within(panel).getByTestId("listenerset-parent").textContent).toContain("not established - the Gateways read is incomplete");
    expect(within(panel).queryByText(/not found/)).toBeNull();
    expect(within(panel).queryByRole("button", { name: "Open parent topology" })).toBeNull();
  });

  it("is not offered where ListenerSets are confirmed not served", () => {
    seed({ lsCoverage: "absent" });
    render(<GatewayApiView cluster="x" />);
    const views = screen.getByTestId("gateway-views");
    expect(within(views).queryByRole("button", { name: /^ListenerSets/ })).toBeNull();
    // and the Gateway inspector has no ListenerSets block
    fireEvent.click(screen.getByText("eg"));
    expect(screen.queryByTestId("gateway-listenersets")).toBeNull();
  });

  it("a Top link reveals the ListenerSet", () => {
    seed({ sets: [first, conflicted] });
    render(<GatewayApiView cluster="x" />);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "gateway", part: "listenersets", ref: { gvr: "gateway.networking.k8s.io/v1/listenersets", namespace: "infra", name: "first" } }); });
    const panel = screen.getByTestId("listenerset-panel");
    expect((panel.querySelector("#inspector-identity") as HTMLElement).dataset.pinUid).toBe("uid-infra-first");
  });
});

describe("the Gateway inspector's listeners and ListenerSets (coverage S7a §3, §9)", () => {
  it("shows each listener's status, a failing one named, an older one shown but not judged", () => {
    const g = gw("eg", { rule: { included: true, role: "degraded", label: "listener https: InvalidCertificateRef" }, generation: 4, listeners: [
      { name: "https", protocol: "HTTPS", hostname: "*.example.com", port: 443, tlsMode: "Terminate", status: { attachedRoutes: 2, supportedKinds: [{ group: "gateway.networking.k8s.io", kind: "HTTPRoute" }], conditions: [
        { type: "Accepted", status: "True", reason: "Accepted", message: "", observedGeneration: 4, stale: false },
        { type: "ResolvedRefs", status: "False", reason: "InvalidCertificateRef", message: "secret wildcard not found", observedGeneration: 4, stale: false },
      ] } },
      { name: "http", protocol: "HTTP", hostname: "", port: 80, allowedRoutes: { namespaces: { set: true, from: "All" }, kinds: [] }, status: { attachedRoutes: 0, supportedKinds: [], conditions: [
        { type: "Programmed", status: "False", reason: "PortUnavailable", message: "", observedGeneration: 3, stale: true },
      ] } },
    ] });
    seed({ gateways: [g] });
    render(<GatewayApiView cluster="x" />);
    const list = within(screen.getByTestId("gateway-panel")).getByTestId("gateway-listeners");
    const https = within(list).getByTestId("listener-https");
    expect(https.textContent).toContain("HTTPS:443 · *.example.com · TLS Terminate");
    expect(https.textContent).toContain("routes from Same (the default)");
    expect(https.textContent).toContain("2 routes attached · supports HTTPRoute");
    expect(within(https).getByTestId("listener-issue").textContent).toBe("ResolvedRefs False - InvalidCertificateRef: secret wildcard not found");
    const http = within(list).getByTestId("listener-http");
    expect(http.textContent).toContain("routes from All");
    expect(within(http).queryByTestId("listener-issue")).toBeNull();
    expect(within(http).getByTestId("listener-older").textContent).toBe("Programmed False - observed at generation 3, shown, not judged");
  });

  it("lists the ListenerSets naming it in merge order, and says when the controller's count differs", () => {
    seed({ sets: [conflicted, first, notAllowed, ls("infra", "elsewhere", 1, { parent: { group: "gateway.networking.k8s.io", kind: "Gateway", namespace: "infra", name: "other" } })] });
    render(<GatewayApiView cluster="x" />);
    // the list's listener column counts the accepted ListenerSets' listeners
    const row = screen.getByText("eg").closest(".lens-row") as HTMLElement;
    expect(row.textContent).toContain("https HTTPS:443 +2 from ListenerSets");
    fireEvent.click(row);
    const block = within(screen.getByTestId("gateway-panel")).getByTestId("gateway-listenersets");
    expect(within(block).getAllByTestId("gateway-listenerset").map((e) => e.textContent?.slice(0, 22))).toEqual([
      expect.stringContaining("1st · infra/first"), expect.stringContaining("2nd · team-b/second"), expect.stringContaining("3rd · infra/third")]);
    expect(within(block).getByText(/ListenerSets from the Gateway's own namespace \(Same\)/)).toBeTruthy();
    expect(within(block).getByTestId("gateway-listenersets-count").textContent).toBe("3 found naming this Gateway, 2 accepted · the controller reports 2 attached");
  });

  it("a differing attached count is said, never reconciled", () => {
    seed({ gateways: [gw("eg", { attachedListenerSets: 5 })], sets: [first] });
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(screen.getByText("eg"));
    expect(screen.getByTestId("gateway-listenersets-count").textContent).toBe("1 found naming this Gateway, 1 accepted · the controller reports 5 attached - they differ");
  });
});
