import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Argo CD lens mounted on the REAL rows adapter (health/useArgoRows) and
// health store - only the transport is stubbed (slice 3 §5.4, held to the
// earlier migrations' reviews). AppProjects stay a fetch (stubbed empty).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/argo", () => ({
  listArgoProjects: vi.fn(async () => {}),
  refreshArgoApp: vi.fn(async () => {}),
  syncArgoApp: vi.fn(async () => {}),
}));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn(async () => ({ ok: true, error: "" })) }));
import { refreshArgoApp, syncArgoApp } from "../bridge/argo";
import { ArgoView } from "./ArgoView";
import { _resetArgoRowsForTest } from "../health/useArgoRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type ArgoAppDTO, type ArgoApplicationSetDTO } from "../store/fleet";
import { argoAppRule, argoAppSetRule, withRule } from "../test/backendRules";
import { clock } from "../layout/coverage";

const app = (name: string, over: Partial<ArgoAppDTO> = {}): ArgoAppDTO => withRule<ArgoAppDTO>({
  namespace: "argocd", name, project: "default", syncStatus: "Synced", healthStatus: "Healthy", broken: false,
  revision: "abc", repoURL: "", path: "", chart: "", targetRevision: "main", extraSources: 0, destNamespace: name, autoSync: false,
  opPhase: "", opMessage: "", conditions: [], resources: [], reconciledUnix: 0, uid: `uid-${name}`, ...over,
}, argoAppRule);
const brokenApp = (name: string, over: Partial<ArgoAppDTO> = {}) => app(name, { syncStatus: "OutOfSync", healthStatus: "Degraded", broken: true, ...over });
const appSet = (name: string, over: Partial<ArgoApplicationSetDTO> = {}): ArgoApplicationSetDTO => withRule<ArgoApplicationSetDTO>({
  namespace: "argocd", name, generators: ["list"], templateName: "", project: "default", repoURL: "", path: "", targetRevision: "",
  destNamespace: "", destServer: "", generatedApps: 0, generatedNames: [], healthy: true, conditions: [], ...over,
}, argoAppSetRule);

const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const part = (id: string, ver: number, kinds: KindCoverage[], native: { total: number; included: number }): HealthPart => ({
  id, entity: id, scope: "", classifier: "native", coverage: "live", kinds, obs: ver, ver,
  native: { total: native.total, included: native.included, word: "", roles: { total: native.total, fault: native.included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: native.total - native.included } },
});
const rowsDTO = (incarnation: number, id: string, ver: number, rows: { rule: { included: boolean } }[], k: KindCoverage, evaluatedAt = "2026-10-11T12:00:00Z"): RowsDTO => ({
  cluster: "x", incarnation, task: "argo", part: part(id, ver, [k], { total: rows.length, included: rows.filter((r) => r.rule.included).length }), evaluatedAt, rows,
});
const summary = (incarnation: number, seq: number, apps: [number, number, number], sets: [number, number, number]): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "argo", coverage: "live", parts: [part("applications", apps[0], [kind("applications")], { total: apps[1], included: apps[2] }), part("applicationsets", sets[0], [kind("applicationsets")], { total: sets[1], included: sets[2] })] }],
});

function open(incarnation = 1) {
  for (const [h, id] of [["a", "applications"], ["s", "applicationsets"]] as const) {
    if (!useHealth.getState().subs[h]) addSub(h, "x", "argo", id);
    openStarted(h, `tok-${h}-${incarnation}`);
    openSucceeded(h, `tok-${h}-${incarnation}`, incarnation);
  }
}
function seed(apps: ArgoAppDTO[], sets: ArgoApplicationSetDTO[] = [appSet("set")]) {
  open();
  applyRows("a", rowsDTO(1, "applications", 1, apps, kind("applications")), 0);
  applyRows("s", rowsDTO(1, "applicationsets", 1, sets, kind("applicationsets")), 0);
}

const headline = () => document.querySelector(".lens-header .t-number")?.textContent;
const rowNames = () => [...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent);
const selectApp = (name: string) => fireEvent.click(screen.getAllByText(name)[0]);
const inspector = () => within(screen.getByTestId("lens-inspector"));

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  _resetArgoRowsForTest();
  useFleet.getState().clearArgo();
  useFleet.setState({ route: { name: "cluster", cluster: "x", section: "argo" } });
});

describe("Argo CD rows, mounted end to end (slice 3 §5.4)", () => {
  it("sorts broken first from the capture order", () => {
    seed([app("b-ok"), brokenApp("z-broken"), app("a-ok")]);
    render(<ArgoView cluster="x" />);
    expect(rowNames()).toEqual(["z-broken", "a-ok", "b-ok"]);
  });

  it("A4 moves the global counts with the notice, in both views, beside a read error", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      open();
      applyRows("a", rowsDTO(1, "applications", 1, [app("ok"), brokenApp("bad")], kind("applications", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" })), 0);
      applyRows("s", rowsDTO(1, "applicationsets", 1, [appSet("set")], kind("applicationsets")), 0);
      const { rerender } = render(<ArgoView cluster="x" />);
      act(() => { applySummary(summary(1, 2, [2, 9, 5], [2, 4, 3]), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/read failed - showing the last good read from .*dial tcp/);
      expect(headline()).toBe("9*");
      expect(screen.getByText(/5 out of sync or unhealthy$/)).toBeTruthy();
      expect(screen.getByRole("button", { name: /^Attention/ }).textContent).toBe("Attention 5");
      expect(screen.getByTestId("argo-breakdown").textContent).toMatch(/1 degraded · 1 out of sync \(as of /);
      rerender(<ArgoView cluster="x" mode="applicationsets" />);
      expect(headline()).toBe("4");
      expect(screen.getByText("3 need attention")).toBeTruthy();
      const gen = useHealth.getState().subs.a.requestGen;
      act(() => bootstrapSettled("a", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Final(1) -> summary(2) -> staggered parts: incarnation 1's rows never stay", () => {
    seed([app("old")]);
    const { rerender } = render(<ArgoView cluster="x" />);
    expect(rowNames()).toEqual(["old"]);
    act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
    expect(rowNames()).toEqual([]);
    expect(screen.getByLabelText("loading applications")).toBeTruthy();
    act(() => applySummary(summary(2, 1, [1, 2, 0], [1, 1, 0])));
    act(() => { open(2); applyRows("a", rowsDTO(2, "applications", 1, [app("old"), app("new")], kind("applications"))); });
    expect(rowNames()).toEqual(["new", "old"]);
    rerender(<ArgoView cluster="x" mode="applicationsets" />);
    expect(screen.getByLabelText("loading ApplicationSets")).toBeTruthy(); // that part has not answered
    act(() => { applyRows("s", rowsDTO(2, "applicationsets", 1, [appSet("set2")], kind("applicationsets"))); });
    expect(rowNames()).toEqual(["argocd / set2"]);
  });

  it("an absent kind is not installed; an unreadable one could not be read - never the same", () => {
    open();
    applyRows("a", rowsDTO(1, "applications", 1, [], kind("applications", { coverage: "unreadable", class: "forbidden", error: "rbac" })));
    applyRows("s", rowsDTO(1, "applicationsets", 1, [], kind("applicationsets", { coverage: "absent" })));
    const { rerender } = render(<ArgoView cluster="x" />);
    expect(screen.getByText("Argo CD applications could not be read.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull(); // the rows retry on their own
    rerender(<ArgoView cluster="x" mode="applicationsets" />);
    expect(screen.getByText("Argo CD ApplicationSets are not served on this cluster.")).toBeTruthy();
  });

  it("discovery gaps and last-good times are surfaced, without downgrading live rows", () => {
    open();
    applyRows("a", rowsDTO(1, "applications", 1, [app("demo")], kind("applications", { discovery: "forbidden: discovery re-check" })));
    applyRows("s", rowsDTO(1, "applicationsets", 1, [], kind("applicationsets")));
    render(<ArgoView cluster="x" />);
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/Application: forbidden: discovery re-check/);
    expect(headline()).toBe("1");
    act(() => { applyRows("a", rowsDTO(1, "applications", 2, [app("demo")], kind("applications"))); });
    expect(screen.queryByTestId("discovery-gap")).toBeNull();
    const lastGood = "2026-10-11T09:30:00Z";
    act(() => { applyRows("a", rowsDTO(1, "applications", 3, [app("demo")], kind("applications", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood }))); });
    expect(screen.getByRole("alert").textContent).toContain(`showing the last good read from ${clock(lastGood)}`);
    expect(headline()).toBe("1*");
  });

  it("removal needs a complete read; a stale read keeps the selection, unverified", () => {
    seed([app("demo")]);
    render(<ArgoView cluster="x" />);
    selectApp("demo");
    act(() => { applyRows("a", rowsDTO(1, "applications", 2, [], kind("applications", { coverage: "stale", class: "transport", error: "dial tcp" }))); });
    expect(inspector().getByTestId("selection-unverified").textContent).toMatch(/Not in the current Application read\..*unverified/);
    act(() => { applyRows("a", rowsDTO(1, "applications", 3, [], kind("applications"))); });
    expect(inspector().getByText(/Application removed\./)).toBeTruthy();
  });

  // Codex review of 0777b3b: the rows' evidence is the Observer's per-kind
  // coverage - never inferred from loading/error/row count.
  describe("evidence is the per-kind coverage", () => {
    const project = (name: string) => ({ namespace: "argocd", name, description: "", sourceRepos: ["https://git"], destinations: ["in-cluster/*"], roles: 0, roleNames: [], orphanWarn: false,
      clusterResourceWhitelist: [], clusterResourceBlacklist: [], namespaceResourceWhitelist: [], namespaceResourceBlacklist: [] });
    const withProjects = () => act(() => useFleet.getState().setArgoProjects("x", { available: true, projects: [project("platform"), project("idle")] }));

    it("a live empty read that goes stale is last-good empty evidence, never unreadable", () => {
      open();
      applyRows("a", rowsDTO(1, "applications", 1, [], kind("applications")));
      applyRows("s", rowsDTO(1, "applicationsets", 1, [appSet("set", { project: "platform", generatedNames: ["gen-app"] })], kind("applicationsets")));
      const { rerender } = render(<ArgoView cluster="x" />);
      withProjects();
      expect(screen.getByText("No Argo CD applications on this cluster.")).toBeTruthy();
      act(() => { applyRows("a", rowsDTO(1, "applications", 2, [], kind("applications", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" }))); });
      expect(screen.queryByText("Argo CD applications could not be read.")).toBeNull();
      expect(screen.getByText("No Argo CD applications in the last good read.")).toBeTruthy();
      expect(headline()).toBe("0*");
      rerender(<ArgoView cluster="x" mode="projects" />);
      withProjects();
      expect(screen.getByText(/2 unused \(last good Application read\)/)).toBeTruthy();
      expect(screen.getByRole("button", { name: /^Unused/ }).textContent).toBe("Unused 2*");
      rerender(<ArgoView cluster="x" mode="applicationsets" />);
      expect(screen.getByText(/1 missing generated apps \(last good Application read\)/)).toBeTruthy();
    });

    it("retained rows under a checking read are unverified - joins qualified, never complete", () => {
      open();
      applyRows("a", rowsDTO(1, "applications", 1, [app("demo", { project: "platform" })], kind("applications")));
      applyRows("s", rowsDTO(1, "applicationsets", 1, [appSet("set", { project: "platform", generatedNames: ["gen-app"] })], kind("applicationsets")));
      const { rerender } = render(<ArgoView cluster="x" />);
      expect(headline()).toBe("1");
      act(() => { applyRows("a", rowsDTO(1, "applications", 2, [app("demo", { project: "platform" })], kind("applications", { coverage: "checking" }))); });
      expect(rowNames()).toEqual(["demo"]); // the rows are still shown
      expect(headline()).toBe("1+");
      rerender(<ArgoView cluster="x" mode="projects" />);
      withProjects();
      expect(screen.getByText(/app usage unverified \(Application read unavailable\)/)).toBeTruthy();
      const platform = screen.getAllByText("platform")[0].closest(".lens-row")!;
      expect(platform.textContent).toContain("?"); // its app count is unverified, not 1
      rerender(<ArgoView cluster="x" mode="applicationsets" />);
      expect(screen.getByText(/generated apps unverified \(Application read unavailable\)/)).toBeTruthy();
    });
  });

  // Design §5b: refresh and sync are pinned to the armed Application.
  describe("verbs are pinned to the armed Application", () => {
    const recreate = () => act(() => { applyRows("a", rowsDTO(1, "applications", 2, [app("demo", { uid: "uid-demo-2" })], kind("applications"))); });

    it("a replacement drops the refresh confirm and the sync composer; nothing is sent", () => {
      seed([app("demo")]);
      render(<ArgoView cluster="x" />);
      selectApp("demo");
      fireEvent.click(inspector().getByRole("button", { name: /^Refresh/ }));
      expect(screen.getByRole("dialog")).toBeTruthy();
      recreate();
      expect(screen.queryByRole("dialog")).toBeNull();
      fireEvent.click(inspector().getByRole("button", { name: /^Sync/ }));
      expect(screen.getByRole("dialog")).toBeTruthy();
      act(() => { applyRows("a", rowsDTO(1, "applications", 3, [app("demo", { uid: "uid-demo-3" })], kind("applications"))); });
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(refreshArgoApp).not.toHaveBeenCalled();
      expect(syncArgoApp).not.toHaveBeenCalled();
    });

    it("the dispatch carries the armed UID", () => {
      seed([app("demo")]);
      render(<ArgoView cluster="x" />);
      selectApp("demo");
      fireEvent.click(inspector().getByRole("button", { name: /^Refresh/ }));
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Refresh" }));
      expect(refreshArgoApp).toHaveBeenCalledWith("x", "argocd", "demo", "uid-demo");
      fireEvent.click(inspector().getByRole("button", { name: /^Sync/ }));
      fireEvent.click(screen.getByRole("button", { name: "Synchronize" }));
      expect(syncArgoApp).toHaveBeenCalledWith("x", "argocd", "demo", "uid-demo", expect.objectContaining({ prune: false }));
    });
  });
});

// Health 1c slice 4 §5.4: a summary Top link reveals its object past the
// filters (the router opened the part's view).
describe("a Top link reveals its object", () => {
  const selectedRow = () => document.querySelector('[data-selected="true"]')?.textContent ?? "";
  const reveal = (part: string, name: string) =>
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "argo", part, ref: { gvr: `argoproj.io/v1alpha1/${part}`, namespace: "argocd", name } }); });

  it("an Application and an ApplicationSet are selected in their views, past the search", () => {
    seed([app("web"), brokenApp("api")], [appSet("set"), appSet("fleet", { healthy: false })]);
    const { rerender } = render(<ArgoView cluster="x" />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } });
    reveal("applications", "web");
    expect(selectedRow()).toContain("web");
    expect(useFleet.getState().navIntent).toBeNull();
    rerender(<ArgoView cluster="x" mode="applicationsets" />);
    reveal("applicationsets", "fleet");
    expect(selectedRow()).toContain("fleet");
    expect(useFleet.getState().navIntent).toBeNull();
  });
});
