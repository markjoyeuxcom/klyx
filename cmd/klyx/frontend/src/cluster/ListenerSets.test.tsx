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

  it("a Gateway with no allowance, no ListenerSet and no count has no block; a failing listener is the detail", () => {
    seed({ gateways: [gw("eg", { allowedListeners: { set: false, from: "" }, attachedListenerSets: null, rule: { included: true, role: "degraded", label: "listener https: InvalidCertificateRef" } })], sets: [] });
    render(<GatewayApiView cluster="x" />);
    const panel = screen.getByTestId("gateway-panel");
    expect(within(panel).queryByTestId("gateway-listenersets")).toBeNull();
    expect(within(panel).getByText("Accepted and programmed, but its controller reports a listener failing (below).")).toBeTruthy();
  });

  it("a differing attached count is said, never reconciled", () => {
    seed({ gateways: [gw("eg", { attachedListenerSets: 5 })], sets: [first] });
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(screen.getByText("eg"));
    expect(screen.getByTestId("gateway-listenersets-count").textContent).toBe("1 found naming this Gateway, 1 accepted · the controller reports 5 attached - they differ");
  });
});

// Codex's S7a implementation review: findings 2, 4 and 5, mounted.
const openListenerSets = () => fireEvent.click(within(screen.getByTestId("gateway-views")).getByRole("button", { name: /^ListenerSets/ }));
const truncatedLS = (notRetained = 1) => [{ kind: "ListenerSet", served: true, synced: true, coverage: "live" as const, truncated: true, notRetained }];

describe("the ListenerSet detail says programmed only when it is (finding 5)", () => {
  it.each([["Unknown", "progressing", /programming is in progress/], ["", "unknown", /no Programmed condition has been written/]])("programming %s", (status, role, want) => {
    seed({ sets: [ls("infra", "pending", 1, { programmedCond: cond(status, status ? "Pending" : ""), rule: { included: false, role, label: "programming" } })] });
    render(<GatewayApiView cluster="x" />);
    openListenerSets();
    fireEvent.click(screen.getByTestId("listenerset-row"));
    const text = screen.getByTestId("listenerset-panel").textContent ?? "";
    expect(text).toMatch(want);
    expect(text).not.toContain("Accepted and programmed");
  });

  it("a historical rejection is said as last observed, never as programmed", () => {
    seed({ sets: [ls("infra", "historical", 1, { generation: 2, stale: true, acceptedCond: cond("False", "NotAllowed", 1), programmedCond: cond("False", "Invalid", 1),
      rule: { included: true, role: "degraded", label: "not accepted (last observed): NotAllowed" } })] });
    render(<GatewayApiView cluster="x" />);
    openListenerSets();
    const text = screen.getByTestId("listenerset-panel").textContent ?? "";
    expect(text).toContain("Accepted was False for generation 1 - NotAllowed");
    expect(text).toContain("whether it is accepted now is unverified");
    expect(text).not.toContain("Accepted and programmed");
  });
});

describe("a truncated or stale read is never complete (finding 2)", () => {
  it("a wholly omitted ListenerSet kind is not proven empty", () => {
    seed({ sets: [] });
    lsRows = { ...lsRows, coverage: truncatedLS(3) };
    render(<GatewayApiView cluster="x" />);
    openListenerSets();
    expect(screen.queryByText("No ListenerSets on this cluster.")).toBeNull();
    expect(screen.getByText("No ListenerSets kept within Klyx's budget.")).toBeTruthy();
  });

  it("a budget eviction is unverified, never a removal", () => {
    seed({ sets: [ls("infra", "keep", 1, { rule: { included: true, role: "fault", label: "not accepted" } })] });
    const v = render(<GatewayApiView cluster="x" />);
    openListenerSets();
    expect(screen.getByTestId("listenerset-panel")).toBeTruthy();
    lsRows = { ...lsRows, sets: [], coverage: truncatedLS(1) };
    v.rerender(<GatewayApiView cluster="x" />);
    expect(screen.queryByText("ListenerSet removed.")).toBeNull();
    expect(screen.getByTestId("selection-unverified")).toBeTruthy();
    expect(screen.getByTestId("listenersets-capped").textContent).toContain("1 ListenerSet not retained");
  });

  it("a truncated ListenerSet read gives a lower bound and claims no difference", () => {
    seed({ sets: [first] });
    lsRows = { ...lsRows, coverage: truncatedLS(1) };
    render(<GatewayApiView cluster="x" />);
    const row = screen.getByText("eg").closest(".lens-row") as HTMLElement;
    expect(row.textContent).toContain("+1 or more from ListenerSets");
    fireEvent.click(row);
    const count = screen.getByTestId("gateway-listenersets-count").textContent ?? "";
    expect(count).toContain("1+ found");
    expect(count).not.toContain("they differ");
  });

  it("a stale ListenerSet read supplies no merged count, and its block is dated", () => {
    seed({ sets: [first] });
    lsRows = { ...lsRows, coverage: [{ kind: "ListenerSet", served: true, synced: true, coverage: "stale", lastGoodAt: "2026-10-18T09:00:00Z", error: "forbidden" }], watchError: "ListenerSet: forbidden" };
    render(<GatewayApiView cluster="x" />);
    const row = screen.getByText("eg").closest(".lens-row") as HTMLElement;
    expect(row.textContent).not.toContain("from ListenerSets");
    fireEvent.click(row);
    const count = screen.getByTestId("gateway-listenersets-count").textContent ?? "";
    expect(count).toContain("(the last good read");
    expect(count).not.toContain("they differ");
  });

  it("a truncated Gateway read keeps an evicted selection unverified", () => {
    seed({ gateways: [gw("eg", { uid: "gw-1", rule: { included: true, role: "fault", label: "not accepted" } })] });
    const v = render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("gateway-panel")).toBeTruthy();
    gwRows = { ...gwRows, gateways: [], coverage: [{ kind: "Gateway", served: true, synced: true, coverage: "live", truncated: true, notRetained: 1 }] };
    v.rerender(<GatewayApiView cluster="x" />);
    expect(screen.queryByText(/Gateway removed/)).toBeNull();
    expect(screen.getByTestId("gateways-capped")).toBeTruthy();
  });

  it("a parent found only in a stale read is said to be the last good read", () => {
    seed({ sets: [ls("infra", "old-parent", 1, { parentCurrent: false, allowance: { from: "Same", allowed: "", text: "the Gateways read is not current: the parent's allowedListeners (Same when last read) is not established", disagrees: false } })] });
    render(<GatewayApiView cluster="x" />);
    openListenerSets();
    const row = screen.getByTestId("listenerset-row");
    expect(row.textContent).toContain("infra/eg · last good read");
    fireEvent.click(row);
    expect(screen.getByTestId("listenerset-parent").textContent).toContain("found in the last good read of the Gateways - not a current read");
  });
});

describe("a replaced Gateway takes none of its successor's ListenerSets (finding 4)", () => {
  it("the pinned old Gateway shows no live join", () => {
    seed({ gateways: [gw("eg", { uid: "old-gw", rule: { included: true, role: "fault", label: "not accepted" } })], sets: [] });
    const v = render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("gateway-panel").querySelector("#inspector-identity")?.getAttribute("data-pin-uid")).toBe("old-gw");
    gwRows = { ...gwRows, gateways: [gw("eg", { uid: "new-gw" })] };
    lsRows = { ...lsRows, sets: [ls("infra", "REPLACEMENT_ONLY", 1)] };
    v.rerender(<GatewayApiView cluster="x" />);
    const panel = screen.getByTestId("gateway-panel");
    expect(panel.querySelector("#inspector-identity")?.getAttribute("data-pin-uid")).toBe("old-gw");
    expect(panel.textContent).not.toContain("REPLACEMENT_ONLY");
    expect(within(panel).getByTestId("gateway-listenersets-withheld")).toBeTruthy();
  });
});

describe("a conflict written on several conditions is said once", () => {
  it("Envoy Gateway's shared message is grouped", () => {
    const msg = "All listeners for a given port must use a unique hostname";
    const c = (type: string, status: string) => ({ type, status, reason: "HostnameConflict", message: msg, observedGeneration: 1, stale: false });
    seed({ sets: [ls("infra", "dup", 1, { rule: { included: true, role: "fault", label: "not accepted: ListenersNotValid" }, acceptedCond: cond("False", "ListenersNotValid"),
      listeners: [listener("extra", { status: { attachedRoutes: 0, supportedKinds: [], conditions: [c("Accepted", "False"), c("Conflicted", "True"), c("Programmed", "False"),
        { type: "ResolvedRefs", status: "True", reason: "ResolvedRefs", message: "", observedGeneration: 1, stale: false }] } })] })] });
    render(<GatewayApiView cluster="x" />);
    openListenerSets();
    const issues = within(screen.getByTestId("listenerset-panel")).getAllByTestId("listener-issue");
    expect(issues.map((e) => e.textContent)).toEqual([`Accepted False, Conflicted True, Programmed False - HostnameConflict: ${msg}`]);
  });
});

// Codex's S7a re-review: the Gateway's own read guards its claims, and the
// deciding condition's freshness decides the detail.
describe("the Gateway's own read and the deciding condition (re-review)", () => {
  it("a truncated Gateway read cannot certify all programmed", () => {
    seed({ gateways: [gw("eg")] });
    gwRows = { ...gwRows, coverage: [{ kind: "Gateway", served: true, synced: true, coverage: "live", truncated: true, notRetained: 1 }] };
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByTestId("gateways-capped")).toBeTruthy();
    expect(screen.queryByText("all programmed")).toBeNull();
  });

  it("a stale Gateway's count is historical and disagrees with nothing", () => {
    seed({ gateways: [gw("eg", { attachedListenerSets: 0 })], sets: [first] });
    gwRows = { ...gwRows, coverage: [{ kind: "Gateway", served: true, synced: true, coverage: "stale", lastGoodAt: "2026-10-18T09:00:00Z", error: "forbidden" }], watchError: "Gateway: forbidden" };
    render(<GatewayApiView cluster="x" />);
    fireEvent.click(screen.getByText("eg"));
    const count = screen.getByTestId("gateway-listenersets-count").textContent ?? "";
    expect(count).toContain("the controller reported 0 attached when the Gateways were last read");
    expect(count).not.toContain("they differ");
  });

  it("a current programming failure beside an older acceptance is current", () => {
    seed({ sets: [ls("infra", "mixed", 1, { generation: 2, observedGeneration: 2, acceptedCond: cond("True", "Accepted", 1), programmedCond: cond("False", "Invalid", 2),
      rule: { included: true, role: "fault", label: "not programmed: Invalid" } })] });
    render(<GatewayApiView cluster="x" />);
    openListenerSets();
    const text = screen.getByTestId("listenerset-panel").textContent ?? "";
    expect(text).toContain("Accepted, but not programmed - Invalid");
    expect(text).toContain("Its acceptance was last reported for generation 1.");
    expect(text).not.toContain("has not reported on generation 2");
  });
});
