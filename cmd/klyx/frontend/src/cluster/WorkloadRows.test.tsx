import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Workloads lens mounted on the REAL rows adapter (health/useWorkloadRows)
// and health store - only the transports are stubbed (slice 3 §5.8, held to
// the earlier migrations' reviews).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/workloads", () => ({
  rolloutRestart: vi.fn(async () => {}),
  rolloutHistory: vi.fn(async () => ({ revisions: [] })),
  rolloutUndo: vi.fn(async () => {}),
  rolloutPause: vi.fn(async () => {}),
  scaleWorkload: vi.fn(async () => {}),
  workloadCompare: vi.fn(() => new Promise(() => {})),
}));
const usageFeed = { current: {} as Record<string, { cpuUsage: number | null; memUsage: number | null }> };
vi.mock("../bridge/workload-metrics", () => ({
  getWorkloadMetrics: vi.fn(async (cluster: string, namespace: string) => {
    useFleet.getState().setWorkloadUsage(cluster, namespace, { status: { available: true, message: "", updatedAt: "t" }, usage: usageFeed.current });
  }),
}));
vi.mock("../bridge/pods", () => ({ deletePod: vi.fn(async () => {}), openPodDetail: vi.fn(async () => {}) }));
vi.mock("../bridge/windows", () => ({ openWorkloadLogsWindow: vi.fn(async () => true) }));
vi.mock("../bridge/metrics", () => ({ getWorkloadSparklines: vi.fn(async () => ({ available: false, cpu: [], mem: [] })) }));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn(async () => ({ ok: true, error: "" })) }));
vi.mock("../bridge/crd", () => ({ copyText: vi.fn(async () => {}) }));
vi.mock("./LogsPane", () => ({ LogsPane: () => null }));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({}));
import { rolloutRestart, scaleWorkload } from "../bridge/workloads";
import { deletePod } from "../bridge/pods";
import { WorkloadsView } from "./WorkloadsView";
import { _resetWorkloadRowsForTest } from "../health/useWorkloadRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type WorkloadDTO } from "../store/fleet";

type Kind = "ok" | "fault" | "degraded" | "unknown" | "progressing" | "paused";
const noResources = { cpu: { usage: null, request: null, limit: 1 }, mem: { usage: null, request: null, limit: 1e9 } };
const wl = (name: string, kind: Kind = "ok", over: Partial<WorkloadDTO> = {}): WorkloadDTO => ({
  kind: "Deployment", namespace: "app", name, uid: `uid-${name}`, gvr: "apps/v1/deployments", generation: 1, desired: 1, ready: kind === "ok" ? 1 : 0, available: 1, updated: 1,
  restarts: 0, reason: "", rank: "healthy", gitops: null, resources: noResources,
  pods: [{ name: `${name}-p1`, uid: `uid-${name}-p1`, ready: kind === "ok", restarts: 0, reason: "", node: "n1", ageSeconds: 60 }],
  health: { kind, label: kind === "ok" ? "Available" : "CrashLoopBackOff", reason: kind, rule: `workload.${kind}` }, ...over,
});

const counted = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const dep = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "dependency", scope: "", coverage: "live", objects: 1, ...over });
const kindsOf = (over: Record<string, Partial<KindCoverage>> = {}) => [
  counted("deployments", over.deployments), counted("statefulsets", over.statefulsets), counted("daemonsets", over.daemonsets),
  dep("pods", over.pods), dep("replicasets", over.replicasets),
];
const countsOf = (rows: WorkloadDTO[]) => {
  const c = { total: rows.length, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
  for (const r of rows) c[(r.health?.kind ?? "unknown") as Kind]++;
  return c;
};
const part = (ver: number, kinds: KindCoverage[], counts: ReturnType<typeof countsOf>): HealthPart => ({
  id: "workloads", entity: "workloads", scope: "all namespaces", classifier: "verdict", coverage: "live", kinds, obs: ver, ver, counts,
});
const rowsDTO = (ver: number, rows: WorkloadDTO[], kinds = kindsOf(), facts: Record<string, unknown> = { fluxPresent: true }): RowsDTO => ({
  cluster: "x", incarnation: 1, task: "workloads", part: part(ver, kinds, countsOf(rows)), evaluatedAt: "2026-10-12T12:00:00Z", rows, facts,
});
const summary = (seq: number, ver: number, counts: ReturnType<typeof countsOf>): HealthSummary => ({
  cluster: "x", incarnation: 1, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "workloads", coverage: "live", parts: [part(ver, kindsOf(), counts)] }],
});

function seed(rows: WorkloadDTO[], kinds = kindsOf(), facts?: Record<string, unknown>) {
  if (!useHealth.getState().subs.w) addSub("w", "x", "workloads", "workloads");
  openStarted("w", "tok-w");
  openSucceeded("w", "tok-w", 1);
  applyRows("w", rowsDTO(1, rows, kinds, facts), 0);
}
const push = (ver: number, rows: WorkloadDTO[], kinds = kindsOf(), facts?: Record<string, unknown>) => act(() => { applyRows("w", rowsDTO(ver, rows, kinds, facts), 0); });

const headline = () => screen.getByTestId("workloads-total").textContent;
const rowNames = () => [...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent);
const select = (name: string) => fireEvent.click(screen.getAllByText(name)[0]);
const inspector = () => within(screen.getByTestId("lens-inspector"));

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  usageFeed.current = {};
  resetHealthStore();
  _resetWorkloadRowsForTest();
  useFleet.getState().clearWorkloads();
  useFleet.setState({ navIntent: null, actionStatus: null, clusters: [], route: { name: "cluster", cluster: "x", section: "workloads" } });
});

describe("Workloads rows, mounted end to end (slice 3 §5.8)", () => {
  it("lists all namespaces' rows with each row's captured verdict; the namespace is a list filter", () => {
    seed([wl("api"), wl("crash", "fault"), wl("db", "ok", { namespace: "data" })]);
    render(<WorkloadsView cluster="x" />);
    expect(rowNames()).toEqual(["app / crash", "app / api", "data / db"]);
    expect(headline()).toBe("3");
    expect(screen.getByTestId("workloads-attention").textContent).toBe("▲ 1 need attention");
    fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "data" } });
    expect(rowNames()).toEqual(["data / db"]);
    expect(headline()).toBe("3"); // the global count stays the cluster's
    expect(screen.getByTestId("workloads-scoped").textContent).toBe("in data: 1");
    expect([...(screen.getByLabelText("Namespace") as HTMLSelectElement).options].map((o) => o.value)).toEqual(["", "app", "data"]);
  });

  it("A4 moves the global counts with the notice; the scoped line stays row-scoped, as of its capture", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      seed([wl("api"), wl("crash", "fault")]);
      render(<WorkloadsView cluster="x" />);
      fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "app" } });
      act(() => { applySummary(summary(2, 2, { total: 9, fault: 4, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 5 }), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(headline()).toBe("9");
      expect(screen.getByTestId("workloads-attention").textContent).toBe("▲ 4 need attention");
      expect(screen.getByTestId("workloads-scoped").textContent).toMatch(/^in app: 2 · 1 need attention \(as of /);
    } finally {
      vi.useRealTimers();
    }
  });

  it("each counted kind is its own evidence: an unreadable kind beside live ones marks the list, never empties it", () => {
    seed([wl("api")], kindsOf({ statefulsets: { coverage: "unreadable", class: "forbidden", error: "rbac" } }));
    render(<WorkloadsView cluster="x" />);
    expect(rowNames()).toEqual(["app / api"]);
    expect(headline()).toBe("1+");
    expect(screen.getByRole("alert").textContent).toMatch(/StatefulSet: forbidden: rbac/);
    expect(screen.getByText("no known attention")).toBeTruthy(); // never "none need attention"
    push(2, [wl("api"), wl("crash", "fault")], kindsOf({ statefulsets: { coverage: "unreadable", class: "forbidden", error: "rbac" } }));
    expect(screen.getByText("1+ fault")).toBeTruthy(); // the breakdown carries the same qualifier
  });

  it("an unreadable first read could not be read; a stale empty read is the last good read", () => {
    const bad = { coverage: "unreadable" as const, class: "forbidden", error: "rbac" };
    seed([], kindsOf({ deployments: bad, statefulsets: bad, daemonsets: bad }));
    render(<WorkloadsView cluster="x" />);
    expect(screen.getByText("Workloads could not be read.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull(); // the rows retry on their own
    const stale = { coverage: "stale" as const, class: "transport", error: "dial tcp", lastGoodAt: "2026-10-12T09:00:00Z" };
    push(2, [], kindsOf({ deployments: stale, statefulsets: stale, daemonsets: stale }));
    expect(screen.getByText("No workloads in all namespaces in the last good read.")).toBeTruthy();
    push(3, []);
    expect(screen.getByText("No workloads in all namespaces.")).toBeTruthy();
  });

  it("pod attribution evidence qualifies the verdicts, never the list", () => {
    seed([wl("api")], kindsOf({ pods: { coverage: "unreadable", class: "forbidden", error: "pods rbac" } }));
    render(<WorkloadsView cluster="x" />);
    expect(headline()).toBe("1"); // the counted kinds are live
    expect(screen.getByTestId("attribution-evidence").textContent).toMatch(/^Pods could not be read - verdicts that need pod evidence are unverified/);
    select("api");
    expect(inspector().getByTestId("pods-evidence").textContent).toBe("pod read unverified");
    push(2, [wl("api", "ok", { pods: [] })], kindsOf({ pods: { coverage: "unreadable", class: "forbidden", error: "pods rbac" } }));
    expect(inspector().getByText("Pods are not read - the selector match is unverified.")).toBeTruthy();
    push(3, [wl("api", "ok", { pods: [] })]);
    expect(screen.queryByTestId("attribution-evidence")).toBeNull();
    expect(inspector().getByText("No pods match this workload's selector.")).toBeTruthy();
  });

  it("the fluxPresent fact travels with the rows it was evaluated with", () => {
    seed([wl("api")], kindsOf(), { fluxPresent: false });
    render(<WorkloadsView cluster="x" />);
    expect(useFleet.getState().workloads.fluxPresent).toBe(false);
    push(2, [wl("api")], kindsOf(), { fluxPresent: true });
    expect(useFleet.getState().workloads.fluxPresent).toBe(true);
  });

  it("live usage joins the rows at render and survives a rows push", async () => {
    usageFeed.current = { "Deployment/app/api": { cpuUsage: 0.5, memUsage: null } };
    seed([wl("api")]);
    render(<WorkloadsView cluster="x" />);
    await act(async () => {});
    expect(useFleet.getState().workloads.usage["Deployment/app/api"]).toEqual({ cpu: 0.5, mem: null });
    push(2, [wl("api", "ok", { restarts: 1 })]);
    select("api");
    expect(inspector().getByText(/usage 500m/)).toBeTruthy();
  });
});

describe("a kept selection (slice 3 §5a)", () => {
  it("is removed only on a complete read of its own kind; otherwise unverified with its actions", () => {
    seed([wl("api"), wl("other")]);
    render(<WorkloadsView cluster="x" />);
    select("api");
    push(2, [wl("other")], kindsOf({ deployments: { coverage: "stale", class: "transport", error: "dial tcp" } }));
    expect(inspector().getByTestId("selection-unverified").textContent).toMatch(/Not in the current Deployment read\..*unverified/);
    expect(inspector().getByRole("button", { name: "Restart" })).toBeTruthy();
    push(3, [wl("other")], kindsOf({ statefulsets: { coverage: "stale", class: "transport", error: "dial tcp" } })); // another kind's failure
    expect(inspector().getByText(/Workload removed\./)).toBeTruthy();
    expect(inspector().queryByRole("button", { name: "Restart" })).toBeNull();
  });

  it("an operator filter that hides it closes it; a read change never does", () => {
    seed([wl("crash", "fault"), wl("api"), wl("db", "ok", { namespace: "data" })]);
    render(<WorkloadsView cluster="x" />);
    select("crash");
    fireEvent.click(screen.getByRole("button", { name: /^Attention/ }));
    push(2, [wl("crash"), wl("api"), wl("db", "ok", { namespace: "data" })]); // it recovers: no longer in the filter
    expect(rowNames()).toEqual([]);
    expect(screen.getByTestId("lens-inspector")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "data" } });
    expect(screen.queryByTestId("lens-inspector")).toBeNull();
  });
});

// Design §5b: the verbs are pinned to the armed objects.
describe("verbs are pinned to the armed workload and pod", () => {
  it("a replacement drops the restart confirm; nothing is sent", () => {
    seed([wl("api")]);
    render(<WorkloadsView cluster="x" />);
    select("api");
    fireEvent.click(inspector().getByRole("button", { name: "Restart" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    push(2, [wl("api", "ok", { uid: "uid-api-2" })]);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(rolloutRestart).not.toHaveBeenCalled();
  });

  it("the dispatch carries the armed UIDs - restart, scale and pod delete", () => {
    seed([wl("api")]);
    render(<WorkloadsView cluster="x" />);
    select("api");
    fireEvent.click(inspector().getByRole("button", { name: "Restart" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Restart" }));
    expect(rolloutRestart).toHaveBeenCalledWith("x", "Deployment", "app", "api", "uid-api");
    fireEvent.click(inspector().getByRole("button", { name: /^More/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /^Scale/ }));
    fireEvent.change(screen.getByRole("spinbutton", { name: /replica count/i }), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "apply scale" }));
    expect(scaleWorkload).toHaveBeenCalledWith("x", "Deployment", "app", "api", "uid-api", 3);
    fireEvent.click(inspector().getByRole("button", { name: "delete pod api-p1" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    expect(deletePod).toHaveBeenCalledWith("x", "app", "api-p1", "uid-api-p1");
  });

  it("a pod delete is pinned to the pod object: a same-name pod replacement drops it", () => {
    seed([wl("api")]);
    render(<WorkloadsView cluster="x" />);
    select("api");
    fireEvent.click(inspector().getByRole("button", { name: "delete pod api-p1" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    push(2, [wl("api", "ok", { pods: [{ name: "api-p1", uid: "uid-api-p1-new", ready: true, restarts: 0, reason: "", node: "n1", ageSeconds: 5 }] })]);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(deletePod).not.toHaveBeenCalled();
  });

  it("an armed pod delete survives an incomplete pod read without its pod; the backend checks the UID", () => {
    seed([wl("api")]);
    render(<WorkloadsView cluster="x" />);
    select("api");
    fireEvent.click(inspector().getByRole("button", { name: "delete pod api-p1" }));
    push(2, [wl("api", "ok", { pods: [] })], kindsOf({ pods: { coverage: "stale", class: "transport", error: "dial tcp" } }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    expect(deletePod).toHaveBeenCalledWith("x", "app", "api-p1", "uid-api-p1");
  });
});
