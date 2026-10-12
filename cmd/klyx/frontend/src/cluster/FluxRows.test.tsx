import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Flux lens mounted on the REAL rows adapter (health/useFluxRows) and
// health store - only the transport is stubbed (slice 3 §5.3, held to the
// Certificates and External Secrets migrations' reviews).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/gitops", () => ({
  getResourceDetail: vi.fn(async () => {}),
  reconcile: vi.fn(), reconcileWithSource: vi.fn(), setSuspend: vi.fn(), resolveGitLink: vi.fn(), fluxDiff: vi.fn(),
}));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn(async () => ({ ok: true, error: "" })) }));
import { getResourceDetail, reconcile, reconcileWithSource, setSuspend } from "../bridge/gitops";
import { GitOps } from "./GitOps";
import { _resetFluxRowsForTest } from "../health/useFluxRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, openFailed, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type ClusterDTO, type FluxResourceDTO, type FluxSourceDTO, type ResourceDetailDTO } from "../store/fleet";
import { fluxRule, fluxSourceRule, withRule } from "../test/backendRules";
import { clock } from "../layout/coverage";

const res = (name: string, over: Partial<FluxResourceDTO> = {}): FluxResourceDTO => withRule<FluxResourceDTO>({
  kind: "Kustomization", namespace: "flux-system", name, ready: "Ready", reason: "", message: "", revision: "main@abc",
  readySinceSeconds: 60, suspended: false, sourceKind: "GitRepository", sourceName: "repo", sourceNamespace: "flux-system", uid: `uid-${name}`, ...over,
}, fluxRule);
const src = (name: string, over: Partial<FluxSourceDTO> = {}): FluxSourceDTO => withRule<FluxSourceDTO>({
  kind: "GitRepository", namespace: "flux-system", name, ready: "Ready", reason: "", message: "", revision: "main@def", url: "https://x/y", suspended: false, ...over,
}, fluxSourceRule);

const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const REC_KINDS = (over: Record<string, Partial<KindCoverage>> = {}) => ["kustomizations", "helmreleases"].map((k) => kind(k, over[k]));
const SRC_KINDS = (over: Record<string, Partial<KindCoverage>> = {}) => ["gitrepositories", "ocirepositories", "buckets", "helmrepositories", "helmcharts"].map((k) => kind(k, over[k]));
const part = (id: string, ver: number, kinds: KindCoverage[], native: { total: number; included: number }): HealthPart => ({
  id, entity: id, scope: "", classifier: "native", coverage: "live", kinds, obs: ver, ver,
  native: { total: native.total, included: native.included, word: "", roles: { total: native.total, fault: native.included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: native.total - native.included } },
});
const rowsDTO = (incarnation: number, id: string, ver: number, rows: { rule: { included: boolean } }[], kinds: KindCoverage[], evaluatedAt = "2026-10-11T12:00:00Z"): RowsDTO => ({
  cluster: "x", incarnation, task: "flux", part: part(id, ver, kinds, { total: rows.length, included: rows.filter((r) => r.rule.included).length }), evaluatedAt, rows,
});
const summary = (incarnation: number, seq: number, recs: [number, number, number], srcs: [number, number, number]): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "flux", coverage: "live", parts: [part("reconciliations", recs[0], REC_KINDS(), { total: recs[1], included: recs[2] }), part("sources", srcs[0], SRC_KINDS(), { total: srcs[1], included: srcs[2] })] }],
});

const clusterDTO = { name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Absent", networkReason: "",
  env: "", region: "", provider: "", group: "", ageSeconds: 0 } as ClusterDTO;

function open(incarnation = 1) {
  for (const [h, id] of [["r", "reconciliations"], ["s", "sources"]] as const) {
    if (!useHealth.getState().subs[h]) addSub(h, "x", "flux", id);
    openStarted(h, `tok-${h}-${incarnation}`);
    openSucceeded(h, `tok-${h}-${incarnation}`, incarnation);
  }
}
function seed(recs: FluxResourceDTO[], srcs: FluxSourceDTO[] = [src("repo")]) {
  open();
  applyRows("r", rowsDTO(1, "reconciliations", 1, recs, REC_KINDS()), 0);
  applyRows("s", rowsDTO(1, "sources", 1, srcs, SRC_KINDS()), 0);
}
const setView = (v: "reconciliation" | "sources") => act(() => useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gitops", fluxView: v } }));

const headline = () => document.querySelector(".lens-header .t-number")?.textContent;
const chip = (re: RegExp) => screen.getByRole("button", { name: re }).textContent;
const panel = () => within(screen.getByTestId("flux-detail-panel"));
const detailFor = (name: string, over: Partial<ResourceDetailDTO> = {}): ResourceDetailDTO => ({
  kind: "Kustomization", namespace: "flux-system", name, uid: `uid-${name}`, appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
  conditions: [], inventory: [], asOfUnix: 1_760_000_000, ...over,
});
const select = (name: string) => act(() => useFleet.getState().expand(`Kustomization/flux-system/${name}`));
const land = (d: ResourceDetailDTO) => act(() => useFleet.getState().setDetail(d, "x"));

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  _resetFluxRowsForTest();
  useFleet.getState().clearGitOps();
  useFleet.setState({ clusters: [clusterDTO], route: { name: "cluster", cluster: "x", section: "gitops" } });
});

describe("Flux rows, mounted end to end (slice 3 §5.3)", () => {
  it("A4 moves the global counts with the notice, in both views, beside a read error", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      open();
      applyRows("r", rowsDTO(1, "reconciliations", 1, [res("apps"), res("infra", { ready: "Failed", reason: "BuildFailed" })], REC_KINDS()), 0);
      applyRows("s", rowsDTO(1, "sources", 1, [src("repo"), src("charts", { kind: "HelmRepository", ready: "Failed" })],
        SRC_KINDS({ helmrepositories: { coverage: "stale", class: "forbidden", error: "rbac", lastGoodAt: "2026-10-11T11:00:00Z" } })), 0);
      render(<GitOps cluster="x" />);
      expect(chip(/^Not ready/)).toBe("Not ready 1");
      act(() => { applySummary(summary(1, 2, [2, 7, 4], [2, 6, 3]), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/watch failed - showing the last good state from .*rbac/);
      expect(headline()).toBe("7");
      expect(screen.getByText(/^▲ 4 not ready$/)).toBeTruthy();
      expect(chip(/^Not ready/)).toBe("Not ready 4");
      expect(screen.getByTestId("flux-breakdown").textContent).toMatch(/2 ks · 0 hr · 1 failed \(as of /);
      setView("sources");
      expect(headline()).toBe("6*"); // the summary's source total, last observed (a stale source kind)
      expect(screen.getByText("3 not ready")).toBeTruthy();
      expect(chip(/^Not ready/)).toBe("Not ready 3");
      const gen = useHealth.getState().subs.r.requestGen;
      act(() => bootstrapSettled("r", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Final(1) -> summary(2) -> staggered parts: incarnation 1's rows never stay", () => {
    seed([res("old")]);
    render(<GitOps cluster="x" />);
    expect(screen.getByText("old")).toBeTruthy();
    act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
    expect(screen.queryByText("old")).toBeNull();
    expect(screen.getByLabelText("loading reconciliation state")).toBeTruthy();
    act(() => applySummary(summary(2, 1, [1, 2, 0], [1, 1, 0])));
    act(() => { open(2); applyRows("r", rowsDTO(2, "reconciliations", 1, [res("old"), res("new")], REC_KINDS())); });
    expect(screen.getByText("new")).toBeTruthy();
    setView("sources");
    expect(screen.getByLabelText("loading reconciliation state")).toBeTruthy(); // the sources part has not answered
    expect(useFleet.getState().gitops.sources).toEqual([]);
    act(() => { applyRows("s", rowsDTO(2, "sources", 1, [src("repo2")], SRC_KINDS())); });
    expect([...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent)).toEqual(["flux-system / repo2"]);
  });

  it("discovery gaps and last-good times are surfaced, without downgrading live rows", () => {
    open();
    applyRows("r", rowsDTO(1, "reconciliations", 1, [res("apps")], REC_KINDS({ kustomizations: { discovery: "forbidden: discovery re-check" } })));
    applyRows("s", rowsDTO(1, "sources", 1, [src("repo")], SRC_KINDS()));
    render(<GitOps cluster="x" />);
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/Kustomization: forbidden: discovery re-check/);
    expect(headline()).toBe("1");
    expect(screen.queryByRole("alert")).toBeNull();
    act(() => { applyRows("r", rowsDTO(1, "reconciliations", 2, [res("apps")], REC_KINDS())); });
    expect(screen.queryByTestId("discovery-gap")).toBeNull();
    const lastGood = "2026-10-11T09:30:00Z";
    act(() => { applyRows("r", rowsDTO(1, "reconciliations", 3, [res("apps")], REC_KINDS({ helmreleases: { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood } }))); });
    expect(screen.getByRole("alert").textContent).toContain(`showing the last good state from ${clock(lastGood)}`);
    expect(headline()).toBe("1*");
    expect(screen.getByText("none known not ready (last observed)")).toBeTruthy();
  });

  it("the ready-since age ticks between pushes", () => {
    vi.useFakeTimers({ now: new Date("2026-10-11T12:00:00Z") });
    try {
      const nowS = Math.floor(Date.now() / 1000);
      seed([res("apps", { readySinceUnix: nowS - 120, readySinceSeconds: 120 })]);
      render(<GitOps cluster="x" />);
      const since = () => screen.getByText("apps").closest(".lens-row")!.textContent ?? "";
      expect(since()).toContain("2m ago");
      act(() => { vi.advanceTimersByTime(30 * 60_000); });
      expect(since()).toContain("32m ago"); // no new push: the age moved with the clock
    } finally {
      vi.useRealTimers();
    }
  });

  // §5a: removal reads the selected object's OWN kind - a failing
  // HelmRelease read never blocks a Kustomization's removal, and a failing
  // Kustomization read keeps it, unverified.
  it("removal needs the resource's own live read; its stale read keeps the selection", () => {
    seed([res("apps")]);
    render(<GitOps cluster="x" />);
    select("apps");
    act(() => { applyRows("r", rowsDTO(1, "reconciliations", 2, [], REC_KINDS({ kustomizations: { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" } }))); });
    expect(panel().getByTestId("selection-unverified").textContent).toMatch(/Not in the current Kustomization read\..*unverified/);
    expect(panel().queryByText(/Kustomization removed\./)).toBeNull();
    act(() => { applyRows("r", rowsDTO(1, "reconciliations", 3, [], REC_KINDS({ helmreleases: { coverage: "stale", class: "transport", error: "dial tcp" } }))); });
    expect(panel().getByText(/Kustomization removed\./)).toBeTruthy(); // its own kind is live; the HelmRelease read's failure never vouches
  });

  // Codex review of 7d389eb #2: a source's consumers are a join on the
  // reconciliation kinds - their coverage qualifies it, never a false zero.
  describe("source usage is qualified by the reconciliation reads", () => {
    const usedBy = () => within(screen.getByTestId("flux-source-panel"));
    const openSource = () => { setView("sources"); fireEvent.click(screen.getByTestId("flux-source-row")); };
    const NO_CONSUMER = "No Kustomization or HelmRelease in this cluster names this source.";

    it("a failed first reconciliation read leaves usage unverified, not zero", () => {
      addSub("r", "x", "flux", "reconciliations");
      addSub("s", "x", "flux", "sources");
      openStarted("r", "tok-r"); openFailed("r", "tok-r", "kustomizations is forbidden", 0);
      openStarted("s", "tok-s"); openSucceeded("s", "tok-s", 1);
      applyRows("s", rowsDTO(1, "sources", 1, [src("repo")], SRC_KINDS()));
      render(<GitOps cluster="x" />);
      openSource();
      expect(usedBy().getByText("Kustomization and HelmRelease reads unavailable - usage unverified, not zero.")).toBeTruthy();
      expect(usedBy().getByTestId("used-by-count").textContent).toBe("?");
      expect(usedBy().queryByText(NO_CONSUMER)).toBeNull();
    });

    it("a new incarnation whose sources land first leaves usage unverified until reconciliations do", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      openSource();
      expect(usedBy().getByText("apps", { exact: false })).toBeTruthy();
      act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
      act(() => { open(2); applyRows("s", rowsDTO(2, "sources", 1, [src("repo")], SRC_KINDS())); });
      expect(usedBy().getByText("Kustomization and HelmRelease reads unavailable - usage unverified, not zero.")).toBeTruthy();
      expect(usedBy().queryByText(NO_CONSUMER)).toBeNull();
      act(() => { applyRows("r", rowsDTO(2, "reconciliations", 1, [res("apps")], REC_KINDS())); });
      expect(usedBy().queryByTestId("used-by-count")).toBeNull();
      expect(screen.getByText(/^Used by · 1$/)).toBeTruthy();
    });

    it("mixed KS/HR coverage qualifies the observed subset as a lower bound", () => {
      open();
      applyRows("r", rowsDTO(1, "reconciliations", 1, [res("apps")], REC_KINDS({ helmreleases: { coverage: "unreadable", class: "forbidden", error: "rbac" } })));
      applyRows("s", rowsDTO(1, "sources", 1, [src("repo")], SRC_KINDS()));
      render(<GitOps cluster="x" />);
      openSource();
      expect(usedBy().getByTestId("used-by-count").textContent).toBe("1+");
      expect(usedBy().getByText("HelmRelease read unavailable - usage unverified beyond the Kustomizations.")).toBeTruthy();
    });

    it("a stale reconciliation read is last observed, and recovery drops the qualifier", () => {
      open();
      applyRows("r", rowsDTO(1, "reconciliations", 1, [res("apps")], REC_KINDS({ kustomizations: { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" } })));
      applyRows("s", rowsDTO(1, "sources", 1, [src("repo"), src("unused", { name: "unused" })], SRC_KINDS()));
      render(<GitOps cluster="x" />);
      setView("sources");
      fireEvent.click(screen.getAllByTestId("flux-source-row").find((r) => r.textContent?.includes("unused"))!);
      expect(usedBy().getByText("None in the last observed Kustomization list.")).toBeTruthy();
      expect(usedBy().getByText("From the last observed Kustomization list.")).toBeTruthy();
      expect(usedBy().getByTestId("used-by-count").textContent).toBe("0*");
      act(() => { applyRows("r", rowsDTO(1, "reconciliations", 2, [res("apps")], REC_KINDS())); });
      expect(usedBy().getByText(NO_CONSUMER)).toBeTruthy();
      expect(usedBy().queryByTestId("used-by-count")).toBeNull();
    });
  });

  // Codex review of 7d389eb #1: a pending verb is pinned to the OBJECT it was
  // armed against - a same-name replacement drops the composer, and the
  // dispatch carries the armed UID for the backend's guarded write.
  describe("verbs are pinned to the armed object (design §5b)", () => {
    const recreate = () => act(() => { applyRows("r", rowsDTO(1, "reconciliations", 2, [res("apps", { uid: "uid-apps-2" })], REC_KINDS())); });

    it("suspend: a replacement drops the confirm; nothing is sent", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      fireEvent.click(panel().getByRole("button", { name: /^Suspend/ }));
      expect(screen.getByRole("dialog")).toBeTruthy(); // the confirm is open
      recreate();
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(setSuspend).not.toHaveBeenCalled();
    });

    it("reconcile and reconcile-with-source: a replacement drops the composer", () => {
      for (const withSource of [false, true]) {
        resetHealthStore(); _resetFluxRowsForTest(); useFleet.getState().clearGitOps();
        seed([res("apps")]);
        const { unmount } = render(<GitOps cluster="x" />);
        select("apps");
        fireEvent.click(panel().getByRole("button", { name: /^Reconcile/ }));
        if (withSource) fireEvent.click(screen.getByText("Re-fetch source first"));
        recreate();
        expect(screen.queryByRole("dialog")).toBeNull();
        unmount();
      }
      expect(reconcile).not.toHaveBeenCalled();
      expect(reconcileWithSource).not.toHaveBeenCalled();
    });

    it("the dispatch carries the armed UID, for every verb", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      fireEvent.click(panel().getByRole("button", { name: /^Reconcile/ }));
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Reconcile$/ }));
      expect(reconcile).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "apps", "uid-apps");
      fireEvent.click(panel().getByRole("button", { name: /^Reconcile/ }));
      fireEvent.click(screen.getByText("Re-fetch source first"));
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Reconcile$/ }));
      expect(reconcileWithSource).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "apps", "uid-apps");
      fireEvent.click(panel().getByRole("button", { name: /^Suspend/ }));
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Suspend$/ }));
      expect(setSuspend).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "apps", "uid-apps", true);
    });
  });

  describe("detail results (design §5b)", () => {
    it("a failed read is shown as the request's error, never an empty detail", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      land({ kind: "Kustomization", namespace: "flux-system", name: "apps", error: "kustomizations is forbidden" } as ResourceDetailDTO);
      expect(panel().getByTestId("detail-error").textContent).toMatch(/Detail read failed: kustomizations is forbidden - the detail is unverified, not empty/);
      expect(panel().queryByText("Loading detail…")).toBeNull();
    });

    it("a detail without the selected row's UID is not its detail", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      land(detailFor("apps", { uid: "", applyFailed: true, attemptedRevision: "main@uidless" }));
      expect(panel().queryByText(/Apply failed at/)).toBeNull();
      land(detailFor("apps", { applyFailed: true, attemptedRevision: "main@thisobject" }));
      expect(panel().getByText(/Apply failed at/)).toBeTruthy();
    });

    it("a failed re-read keeps the same object's last detail, labelled as such", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      land(detailFor("apps", { applyFailed: true, attemptedRevision: "main@readbefore" }));
      land({ kind: "Kustomization", namespace: "flux-system", name: "apps", error: "context deadline exceeded" } as ResourceDetailDTO);
      expect(panel().getByTestId("detail-error").textContent).toMatch(/Detail read failed: context deadline exceeded$/);
      expect(panel().getByText(/Apply failed at/)).toBeTruthy();
      expect(panel().getByText(/^Last read /)).toBeTruthy();
    });

    it("a recreation is read afresh and never shown the old object's detail", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      land(detailFor("apps", { applyFailed: true, attemptedRevision: "main@oldobject" }));
      const before = (getResourceDetail as ReturnType<typeof vi.fn>).mock.calls.length;
      act(() => { applyRows("r", rowsDTO(1, "reconciliations", 2, [res("apps", { uid: "uid-apps-2" })], REC_KINDS())); });
      expect(panel().queryByText(/Apply failed at/)).toBeNull();
      expect(panel().getByTestId("detail-unloaded").textContent).toMatch(/earlier object with this name/);
      expect((getResourceDetail as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(before);
    });

    it("a bound source that could not be read is named, never a silent gap", () => {
      seed([res("apps")]);
      render(<GitOps cluster="x" />);
      select("apps");
      land(detailFor("apps", { sourceWarning: "bound source GitRepository flux-system/repo is unverified: forbidden" }));
      expect(panel().getByTestId("source-warning").textContent).toBe("bound source GitRepository flux-system/repo is unverified: forbidden");
    });
  });
});

// Health 1c slice 4 §5.4: a summary Top link to a source opens the Sources
// view and selects it past the filters.
describe("a Top link reveals a source", () => {
  it("opens Sources and selects the source by kind, namespace and name", () => {
    seed([res("app")], [src("repo"), src("charts", { kind: "HelmRepository", ready: "False", reason: "IndexationFailed" })]);
    setView("sources");
    render(<GitOps cluster="x" />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } });
    setView("reconciliation");
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "flux", part: "sources", ref: { gvr: "source.toolkit.fluxcd.io/v1/helmrepositories", namespace: "flux-system", name: "charts" } }); });
    expect(useFleet.getState().route).toMatchObject({ fluxView: "sources" });
    expect(document.querySelector('[data-selected="true"]')?.textContent ?? "").toContain("charts");
    expect(useFleet.getState().navIntent).toBeNull();
  });
});

// Slice 4 review of 4d8b545 #1: select-flux decides 'missing' from the
// target's own kind - a HelmRelease row never vouches for Kustomizations.
describe("select-flux decides 'missing' from the target's own kind", () => {
  const want = (fluxKind: string, name: string) =>
    act(() => { useFleet.getState().requestNavIntent({ kind: "select-flux", cluster: "x", fluxKind, namespace: "flux-system", name }); });

  it("a Kustomization target waits while Kustomizations are unreadable, beside HelmRelease rows; it selects on recovery", () => {
    open();
    applyRows("r", rowsDTO(1, "reconciliations", 1, [res("hr", { kind: "HelmRelease" })], REC_KINDS({ kustomizations: { coverage: "unreadable", class: "forbidden" } })), 0);
    applyRows("s", rowsDTO(1, "sources", 1, [src("repo")], SRC_KINDS()), 0);
    setView("reconciliation");
    render(<GitOps cluster="x" />);
    want("Kustomization", "app");
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-flux", name: "app" });
    expect(useFleet.getState().actionStatus).toBeNull();
    act(() => { applyRows("r", rowsDTO(1, "reconciliations", 2, [res("hr", { kind: "HelmRelease" }), res("app")], REC_KINDS()), 0); });
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("a complete empty reconciliation read reports the target missing - never waits forever", () => {
    open();
    applyRows("r", rowsDTO(1, "reconciliations", 1, [], REC_KINDS()), 0);
    applyRows("s", rowsDTO(1, "sources", 1, [src("repo")], SRC_KINDS()), 0);
    setView("reconciliation");
    render(<GitOps cluster="x" />);
    want("Kustomization", "app");
    expect(useFleet.getState().navIntent).toBeNull();
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error", message: expect.stringMatching(/app is no longer present/) });
  });
});
