import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Karpenter lens mounted on the REAL rows hook (health/useKarpenterRows)
// and health store - only the transports are stubbed (slice 3 §5.5). The
// controller probe and disruption feed stay a fetch (stubbed healthy).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/karpenter", () => ({ getKarpenterState: vi.fn(async () => ({ detected: true, controllerVerified: true, controllerHealthy: true, events: [] })) }));
import { KarpenterView } from "./KarpenterView";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type KarpenterClaimDTO, type KarpenterPoolDTO } from "../store/fleet";
import { claimRule, poolRule, withRule } from "../test/backendRules";

const nowS = () => Math.floor(Date.now() / 1000);
const claim = (name: string, over: Partial<KarpenterClaimDTO> = {}): KarpenterClaimDTO => withRule<KarpenterClaimDTO>({
  name, pool: "default", nodeName: `node-${name}`, ready: "ready", failingType: "", failingReason: "", failingMessage: "",
  drifted: false, consolidatable: false, capacityType: "spot", instanceType: "m6i.large", ageSeconds: 600, createdUnix: nowS() - 600, uid: `uid-${name}`, ...over,
}, claimRule);
const badClaim = (name: string) => claim(name, { ready: "not-ready", failingType: "Launched", failingStatus: "False", failingReason: "InsufficientCapacity" });
const pool = (name: string, over: Partial<KarpenterPoolDTO> = {}): KarpenterPoolDTO => withRule<KarpenterPoolDTO>({
  name, ready: "ready", reason: "", message: "", weight: 10, cpuLimit: null, cpuUsed: null, memLimit: null, memUsed: null, nodes: 2, uid: `uid-${name}`, ...over,
}, poolRule);

const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const part = (id: string, ver: number, kinds: KindCoverage[], native: { total: number; included: number }): HealthPart => ({
  id, entity: id, scope: "", classifier: "native", coverage: "live", kinds, obs: ver, ver,
  native: { total: native.total, included: native.included, word: "", roles: { total: native.total, fault: native.included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: native.total - native.included } },
});
const rowsDTO = (incarnation: number, id: string, ver: number, rows: { rule: { included: boolean } }[], k: KindCoverage, evaluatedAt = "2026-10-11T12:00:00Z"): RowsDTO => ({
  cluster: "x", incarnation, task: "karpenter", part: part(id, ver, [k], { total: rows.length, included: rows.filter((r) => r.rule.included).length }), evaluatedAt, rows,
});
const summary = (incarnation: number, seq: number, pools: [number, number, number], claims: [number, number, number]): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "karpenter", coverage: "live", parts: [part("nodepools", pools[0], [kind("nodepools")], { total: pools[1], included: pools[2] }), part("nodeclaims", claims[0], [kind("nodeclaims")], { total: claims[1], included: claims[2] })] }],
});

function open(incarnation = 1) {
  for (const [h, id] of [["p", "nodepools"], ["c", "nodeclaims"]] as const) {
    if (!useHealth.getState().subs[h]) addSub(h, "x", "karpenter", id);
    openStarted(h, `tok-${h}-${incarnation}`);
    openSucceeded(h, `tok-${h}-${incarnation}`, incarnation);
  }
}
function seed(claims: KarpenterClaimDTO[], pools: KarpenterPoolDTO[] = [pool("default")]) {
  open();
  applyRows("p", rowsDTO(1, "nodepools", 1, pools, kind("nodepools")), 0);
  applyRows("c", rowsDTO(1, "nodeclaims", 1, claims, kind("nodeclaims")), 0);
}
const headline = () => document.querySelector(".lens-header .t-number")?.textContent;
const rowNames = () => [...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent);
const row = (name: string) => screen.getByText(name).closest(".lens-row")!.textContent ?? "";
const inspector = () => within(screen.getByTestId("lens-inspector"));

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  useFleet.setState({ clusters: [{ name: "x", karpenterPresent: true } as never] });
});

describe("Karpenter rows, mounted end to end (slice 3 §5.5)", () => {
  it("A4 moves the global counts with the notice, beside a read error", async () => {
    vi.useFakeTimers({ now: 0, toFake: ["setTimeout", "setInterval", "Date"] });
    try {
      open();
      applyRows("p", rowsDTO(1, "nodepools", 1, [pool("default")], kind("nodepools", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-11T11:00:00Z" })), 0);
      applyRows("c", rowsDTO(1, "nodeclaims", 1, [claim("ok"), badClaim("bad")], kind("nodeclaims")), 0);
      render(<KarpenterView cluster="x" />);
      await act(async () => { await Promise.resolve(); });
      act(() => { applySummary(summary(1, 2, [1, 1, 0], [2, 7, 4]), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/read failed - showing the last good state from .*dial tcp/);
      expect(headline()).toBe("7");
      expect(screen.getByTestId("karp-global-attention").textContent).toBe("4 not ready or appear stuck");
      expect(screen.getByRole("button", { name: /^NodeClaims/ }).textContent).toBe("NodeClaims 4");
      const gen = useHealth.getState().subs.c.requestGen;
      act(() => bootstrapSettled("c", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Final(1) -> summary(2) -> staggered parts: incarnation 1's rows never stay", async () => {
    seed([claim("old")]);
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("old")).toBeTruthy();
    act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
    expect(rowNames()).toEqual([]);
    expect(screen.getByLabelText("reading NodePools and NodeClaims")).toBeTruthy();
    act(() => applySummary(summary(2, 1, [1, 1, 0], [1, 2, 0])));
    act(() => { open(2); applyRows("c", rowsDTO(2, "nodeclaims", 1, [claim("old"), claim("new")], kind("nodeclaims"))); });
    expect(rowNames().sort()).toEqual(["new", "old"]);
    fireEvent.click(screen.getByRole("button", { name: /^NodePools/ }));
    expect(screen.getByLabelText("reading NodePools and NodeClaims")).toBeTruthy(); // that part has not answered
    act(() => { applyRows("p", rowsDTO(2, "nodepools", 1, [pool("p2")], kind("nodepools"))); });
    expect(rowNames()).toEqual(["p2"]);
  });

  it("discovery gaps and last-good times are surfaced; a stale empty read is last good, never 'No NodeClaims'", async () => {
    open();
    applyRows("p", rowsDTO(1, "nodepools", 1, [pool("default")], kind("nodepools")));
    applyRows("c", rowsDTO(1, "nodeclaims", 1, [claim("a")], kind("nodeclaims", { discovery: "forbidden: discovery re-check" })));
    render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("discovery-gap")).textContent).toMatch(/NodeClaim: forbidden: discovery re-check/);
    expect(headline()).toBe("1");
    const lastGood = "2026-10-11T09:30:00Z";
    act(() => { applyRows("c", rowsDTO(1, "nodeclaims", 2, [], kind("nodeclaims", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood }))); });
    expect(screen.queryByTestId("discovery-gap")).toBeNull();
    expect(screen.getByText("No NodeClaims in the last good read.")).toBeTruthy();
    expect(screen.queryByText("No NodeClaims.")).toBeNull();
  });

  it("a claim's age ticks between pushes", async () => {
    vi.useFakeTimers({ now: new Date("2026-10-11T12:00:00Z"), toFake: ["setTimeout", "setInterval", "Date"] });
    try {
      seed([claim("a", { createdUnix: nowS() - 120, ageSeconds: 120 })]);
      render(<KarpenterView cluster="x" />);
      await act(async () => { await Promise.resolve(); });
      expect(row("a")).toContain("2m");
      act(() => { vi.advanceTimersByTime(30 * 60_000); });
      expect(row("a")).toContain("32m");
    } finally {
      vi.useRealTimers();
    }
  });

  // §5a joins: a pool's node count is read off the NodeClaims.
  it("a pool's node count carries the NodeClaim read's coverage", async () => {
    open();
    applyRows("p", rowsDTO(1, "nodepools", 1, [pool("default", { nodes: 3 })], kind("nodepools")));
    applyRows("c", rowsDTO(1, "nodeclaims", 1, [claim("a")], kind("nodeclaims", { coverage: "stale", class: "transport", error: "dial tcp" })));
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^NodePools/ }));
    expect(row("default")).toContain("3*");
    act(() => { applyRows("c", rowsDTO(1, "nodeclaims", 2, [], kind("nodeclaims", { coverage: "unreadable", class: "forbidden", error: "rbac" }))); });
    expect(row("default")).toContain("?");
    fireEvent.click(screen.getByText("default").closest(".lens-row")!);
    expect(inspector().getByText(/nodes unverified \(NodeClaim read unavailable\)/)).toBeTruthy();
  });

  it("removal needs a complete read; a stale read keeps the selection, unverified", async () => {
    seed([claim("a")]);
    render(<KarpenterView cluster="x" />);
    fireEvent.click((await screen.findByText("a")).closest(".lens-row")!);
    act(() => { applyRows("c", rowsDTO(1, "nodeclaims", 2, [], kind("nodeclaims", { coverage: "stale", class: "transport", error: "dial tcp" }))); });
    expect(inspector().getByTestId("selection-unverified").textContent).toMatch(/Not in the current NodeClaim read\..*unverified/);
    act(() => { applyRows("c", rowsDTO(1, "nodeclaims", 3, [], kind("nodeclaims"))); });
    expect(inspector().getByText(/NodeClaim removed\./)).toBeTruthy();
    expect(inspector().queryByRole("button", { name: "Open node" })).toBeNull();
  });
});
