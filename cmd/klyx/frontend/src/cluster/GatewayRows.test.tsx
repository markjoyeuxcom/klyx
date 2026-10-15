import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Gateways list mounted on the REAL rows hook (health/useGatewayRows)
// and health store - only the transport is stubbed (slice 3 §5.6). The
// topology stays a fetch (stubbed).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/ruleMetrics", () => ({ ruleMetrics: () => Object.assign(Promise.resolve({ status: { available: false, message: "", updatedAt: "" }, scope: "", start: "", end: "", stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "", notes: [] }), { cancel: () => {} }) }));
vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}), abandonRouteMetrics: vi.fn() }));
import { GatewayApiView } from "./GatewayApiView";
import { buildCommands } from "../chrome/commands";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type CondDTO, type GatewayRefDTO, type GatewayRouteRowDTO } from "../store/fleet";
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

// Codex review of f9a52c7: the routes part view (slice 3 §4a) - one row per
// route UID with its worst attachment, the part's own counts, own-kind
// evidence and dependency qualifiers, Open topology on the worst parent.
describe("the Routes part view", () => {
  const att = (parent: string, role: string, label: string, included = false) => ({ parent, rule: { included, role, label } });
  const route = (name: string, attachments: { parent: string; rule: { included: boolean; role: string; label: string } }[]): GatewayRouteRowDTO => {
    const rank: Record<string, number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, paused: 4, ok: 5 };
    const worst = [...attachments].sort((a, b) => rank[a.rule.role] - rank[b.rule.role] || a.parent.localeCompare(b.parent))[0];
    return { uid: `uid-${name}`, namespace: "apps", name, parent: worst.parent, rule: { ...worst.rule, included: attachments.some((a) => a.rule.included) }, attachments };
  };
  const routeKinds = (own: Partial<KindCoverage> = {}, deps: Record<string, Partial<KindCoverage>> = {}) => [
    kind("httproutes", own),
    ...["gateways", "services", "endpointslices"].map((k) => kind(k, { role: "dependency", ...deps[k] })),
  ];
  const routesDTO = (incarnation: number, ver: number, rows: GatewayRouteRowDTO[], k = routeKinds()): RowsDTO => ({
    cluster: "x", incarnation, task: "gateway", evaluatedAt: "2026-10-11T12:00:00Z", rows,
    part: { ...part(ver, k, { total: rows.length, included: rows.filter((r) => r.rule.included).length }), id: "routes", entity: "HTTPRoutes with a failing attachment" },
  });
  const openRoutes = (incarnation = 1) => {
    if (!useHealth.getState().subs.r) addSub("r", "x", "gateway", "routes");
    openStarted("r", `tok-r-${incarnation}`);
    openSucceeded("r", `tok-r-${incarnation}`, incarnation);
  };
  const toRoutes = () => fireEvent.click(screen.getByRole("button", { name: /^Routes/ }));

  it("a healthy Gateway with a broken route: the Routes view carries the route's own count", () => {
    open(); applyRows("g", rowsDTO(1, 1, [gw("eg")]));
    openRoutes(); applyRows("r", routesDTO(1, 1, [route("web", [att("infra/eg", "fault", "no ready endpoints", true)])]));
    render(<GatewayApiView cluster="x" />);
    expect(screen.getByText("all programmed")).toBeTruthy(); // the Gateway itself is healthy
    expect(screen.getByRole("button", { name: /^Routes/ }).textContent).toBe("Routes 1");
    toRoutes();
    expect(headline()).toBe("1");
    expect(screen.getByTestId("route-attention").textContent).toBe("1 with a failing attachment");
    expect(screen.getByRole("button", { name: /^Needs attention/ }).textContent?.trim()).toBe("Needs attention 1");
  });

  it("healthy and rejecting parents: counted once, by the worst parent, and Open topology targets it", () => {
    open(); applyRows("g", rowsDTO(1, 1, [gw("a"), gw("b")]));
    openRoutes(); applyRows("r", routesDTO(1, 1, [route("web", [att("infra/a", "ok", "attached"), att("infra/b", "fault", "not accepted", true)])]));
    render(<GatewayApiView cluster="x" />);
    toRoutes();
    expect(rowNames()).toEqual(["apps/web"]);
    expect(headline()).toBe("1");
    fireEvent.click(screen.getByText("web").closest(".lens-row")!);
    const panel = within(screen.getByTestId("route-row-panel"));
    expect(panel.getAllByTestId("route-attachment").map((n) => n.textContent)).toEqual([expect.stringContaining("infra/a"), expect.stringContaining("infra/b")]);
    fireEvent.click(panel.getByRole("button", { name: "Open topology (infra/b)" }));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.gateway).toEqual({ namespace: "infra", name: "b" });
    expect(useFleet.getState().network.selectedRoute).toBe("apps/web");
  });

  it("A4 moves the routes part's counts with the notice", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      openRoutes(); applyRows("r", routesDTO(1, 1, [route("web", [att("infra/eg", "ok", "attached")])]), 0);
      render(<GatewayApiView cluster="x" />);
      toRoutes();
      act(() => {
        applySummary({ cluster: "x", incarnation: 1, seq: 2, evaluatedAt: "", connection: "connected", tasks: [{ id: "gateway", coverage: "live", parts: [
          { ...part(2, routeKinds(), { total: 5, included: 3 }), id: "routes", entity: "routes" },
        ] }] }, 0);
        dueForBootstrap(1500);
      });
      expect(screen.getByTestId("rows-refreshing")).toBeTruthy();
      expect(headline()).toBe("5");
      expect(screen.getByTestId("route-attention").textContent).toBe("3 with a failing attachment");
    } finally {
      vi.useRealTimers();
    }
  });

  it("an incomplete dependency qualifies the verdicts, never the list's completeness", () => {
    openRoutes(); applyRows("r", routesDTO(1, 1, [route("web", [att("infra/eg", "fault", "no ready endpoints", true)])], routeKinds({}, { services: { coverage: "unreadable", class: "forbidden", error: "rbac" } })));
    render(<GatewayApiView cluster="x" />);
    toRoutes();
    expect(headline()).toBe("1"); // the HTTPRoute read itself is complete
    expect(screen.getByTestId("route-deps").textContent).toMatch(/Route verdicts are unverified: they were evaluated against Service without a current read/);
    expect(screen.getByTestId("route-attention").textContent).toBe("1 with a failing attachment (unverified)");
    expect(screen.getByText("web").closest(".lens-row")!.textContent).toContain("no ready endpoints *");
  });

  // Codex review of b2eff54 #1: membership in the attached set needs the
  // HTTPRoute read AND the parent Gateways read; Services and EndpointSlices
  // never block it; a confirmed absence is "no longer attached", not deletion.
  describe("attached-set membership", () => {
    const select = () => {
      openRoutes(); applyRows("r", routesDTO(1, 1, [route("web", [att("infra/eg", "ok", "attached")])]));
      render(<GatewayApiView cluster="x" />);
      toRoutes();
      fireEvent.click(screen.getByText("web").closest(".lens-row")!);
    };
    const panel = () => within(screen.getByTestId("route-row-panel"));

    it("a stale HTTPRoute read keeps the selection, unverified", () => {
      select();
      act(() => { applyRows("r", routesDTO(1, 2, [], routeKinds({ coverage: "stale", class: "transport", error: "dial tcp" }))); });
      expect(panel().getByTestId("selection-unverified")).toBeTruthy();
    });

    for (const gwCov of ["checking", "unreadable", "stale"] as const) {
      it(`a live HTTPRoute read with the parent Gateways ${gwCov} never confirms a detachment`, () => {
        select();
        act(() => { applyRows("r", routesDTO(1, 2, [], routeKinds({}, { gateways: { coverage: gwCov, class: "transport", error: "dial tcp" } }))); });
        expect(panel().getByTestId("selection-unverified")).toBeTruthy();
        expect(panel().queryByTestId("route-detached")).toBeNull();
        expect(headline()).not.toBe("0"); // the attached count is qualified, never a bare 0
      });
    }

    for (const dep of ["services", "endpointslices"] as const) {
      it(`an unreadable ${dep} dependency never blocks a confirmed detachment`, () => {
        select();
        act(() => { applyRows("r", routesDTO(1, 2, [], routeKinds({}, { [dep]: { coverage: "unreadable", class: "forbidden", error: "rbac" } }))); });
        expect(panel().getByTestId("route-detached").textContent).toMatch(/No longer attached to a Gateway\..*may still exist/);
      });
    }

    it("the parent removed while the route still exists reads as detached, never removed", () => {
      select();
      act(() => { applyRows("r", routesDTO(1, 2, [], routeKinds())); }); // the parent Gateway is gone; the route was not deleted
      expect(panel().getByTestId("route-detached")).toBeTruthy();
      expect(panel().queryByText(/removed/)).toBeNull();
      expect(panel().queryByRole("button", { name: /^Open topology/ })).toBeNull();
    });

    it("an empty attached set under an unreadable parent read is unverified, not none", () => {
      openRoutes(); applyRows("r", routesDTO(1, 1, [], routeKinds({}, { gateways: { coverage: "unreadable", class: "forbidden", error: "rbac" } })));
      render(<GatewayApiView cluster="x" />);
      toRoutes();
      expect(screen.getByText("Attached HTTPRoutes are unverified.")).toBeTruthy();
      expect(screen.queryByText("No HTTPRoutes attached to a Gateway.")).toBeNull();
    });
  });

  it("a dependency's discovery gap is named too", () => {
    openRoutes(); applyRows("r", routesDTO(1, 1, [route("web", [att("infra/eg", "ok", "attached")])], routeKinds({}, { gateways: { discovery: "forbidden: discovery re-check" } })));
    render(<GatewayApiView cluster="x" />);
    toRoutes();
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/Gateway: forbidden: discovery re-check/);
  });

  // Codex review of b2eff54 #2: an operator filter edit closes an excluded
  // selection; a read change does not.
  it("an attention-filter or search edit that hides the selection closes it", () => {
    openRoutes(); applyRows("r", routesDTO(1, 1, [route("ok", [att("infra/eg", "ok", "attached")]), route("bad", [att("infra/eg", "fault", "not accepted", true)])]));
    render(<GatewayApiView cluster="x" />);
    toRoutes();
    fireEvent.click(screen.getByText("ok").closest(".lens-row")!);
    fireEvent.click(screen.getByRole("button", { name: /^Needs attention/ }));
    expect(screen.queryByTestId("route-row-panel")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /^All/ }));
    fireEvent.click(screen.getByText("bad").closest(".lens-row")!);
    fireEvent.change(screen.getByLabelText("Search HTTPRoutes"), { target: { value: "ok" } });
    expect(screen.queryByTestId("route-row-panel")).toBeNull();
  });
});

// Health 1c slice 4 §5.4: a summary Top link reveals its object - the
// outer view switches, then that view's list selects it past its filters.
describe("a Top link reveals its object", () => {
  const selectedRow = () => document.querySelector('[data-selected="true"]')?.textContent ?? "";
  const reveal = (part: string, ref: { gvr: string; namespace?: string; name: string; parent?: string }) =>
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "gateway", part, ref }); });
  const routeRow = (name: string): GatewayRouteRowDTO => ({ uid: `uid-${name}`, namespace: "apps", name, parent: "infra/eg",
    rule: { included: true, role: "fault", label: "no ready endpoints" }, attachments: [{ parent: "infra/eg", rule: { included: true, role: "fault", label: "no ready endpoints" } }] });

  it("a route opens the Routes view and is selected; a Gateway is selected in the Gateways view", () => {
    open(); applyRows("g", rowsDTO(1, 1, [gw("eg"), brokenGw("bad")]));
    if (!useHealth.getState().subs.r) addSub("r", "x", "gateway", "routes");
    openStarted("r", "tok-r"); openSucceeded("r", "tok-r", 1);
    applyRows("r", { cluster: "x", incarnation: 1, task: "gateway", evaluatedAt: "x", rows: [routeRow("web"), routeRow("api")],
      part: { ...part(1, [kind("httproutes"), ...["gateways", "services", "endpointslices"].map((k) => kind(k, { role: "dependency" }))], { total: 2, included: 2 }), id: "routes" } });
    render(<GatewayApiView cluster="x" />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } }); // the Gateways list's filter
    reveal("routes", { gvr: "gateway.networking.k8s.io/v1/httproutes", namespace: "apps", name: "api", parent: "gateway infra/eg" });
    expect(screen.getByRole("button", { name: /^Routes/ }).getAttribute("aria-pressed")).toBe("true");
    expect(selectedRow()).toContain("api");
    expect(useFleet.getState().navIntent).toBeNull();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } }); // the Routes list's filter
    reveal("routes", { gvr: "gateway.networking.k8s.io/v1/httproutes", namespace: "apps", name: "web", parent: "gateway infra/eg" });
    expect(selectedRow()).toContain("web");
    fireEvent.click(screen.getByRole("button", { name: /^Gateways/ }));
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } }); // the Gateways list's own filter
    reveal("gateways", { gvr: "gateway.networking.k8s.io/v1/gateways", namespace: "infra", name: "bad" });
    expect(screen.getByRole("button", { name: /^Gateways/ }).getAttribute("aria-pressed")).toBe("true");
    expect(selectedRow()).toContain("bad");
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("a route absent from a complete membership read is detached, not proven deleted (review of 4d8b545 #3)", () => {
    open(); applyRows("g", rowsDTO(1, 1, [gw("eg")]));
    if (!useHealth.getState().subs.r) addSub("r", "x", "gateway", "routes");
    openStarted("r", "tok-r"); openSucceeded("r", "tok-r", 1);
    // the parent was removed: the HTTPRoute still exists but is attached to nothing
    applyRows("r", { cluster: "x", incarnation: 1, task: "gateway", evaluatedAt: "x", rows: [],
      part: { ...part(1, [kind("httproutes"), ...["gateways", "services", "endpointslices"].map((k) => kind(k, { role: "dependency" }))], { total: 0, included: 0 }), id: "routes" } });
    render(<GatewayApiView cluster="x" />);
    reveal("routes", { gvr: "gateway.networking.k8s.io/v1/httproutes", namespace: "apps", name: "web", parent: "gateway infra/eg" });
    expect(useFleet.getState().navIntent).toBeNull();
    expect(useFleet.getState().actionStatus?.message).toBe("route apps/web is no longer attached to a Gateway - it may still exist");
  });
});


// The Line slice 9 note §4.2: a reveal by UID pins that UID; a route absent
// from the attached set reads detached, not removed.
describe("Gateway API reveals by UID", () => {
  const gone = () => document.querySelector("[data-pinned-gone]")?.getAttribute("data-pinned-gone") ?? null;
  it("a Gateway replaced at its name is said; an absent route is no longer attached", () => {
    open(); applyRows("g", rowsDTO(1, 1, [gw("eg", { uid: "uid-eg-2" })]));
    if (!useHealth.getState().subs.r) addSub("r", "x", "gateway", "routes");
    openStarted("r", "tok-r"); openSucceeded("r", "tok-r", 1);
    applyRows("r", { cluster: "x", incarnation: 1, task: "gateway", evaluatedAt: "x", rows: [],
      part: { ...part(1, [kind("httproutes"), ...["gateways", "services", "endpointslices"].map((k) => kind(k, { role: "dependency" }))], { total: 0, included: 0 }), id: "routes" } });
    render(<GatewayApiView cluster="x" />);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "gateway", part: "gateways", ref: { gvr: "gateway.networking.k8s.io/v1/gateways", namespace: "infra", name: "eg", uid: "uid-eg" } }); });
    expect(gone()).toBe("replaced");
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "gateway", part: "routes", ref: { gvr: "gateway.networking.k8s.io/v1/httproutes", namespace: "apps", name: "old", uid: "uid-old" } }); });
    expect(screen.getByRole("button", { name: /^Routes/ }).getAttribute("aria-pressed")).toBe("true");
    expect(gone()).toBe("removed");
    expect(document.querySelector("[data-pinned-gone]")?.textContent).toContain("No longer attached to a Gateway.");
  });
});
