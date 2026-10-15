import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Pods lens mounted on the REAL rows adapter (health/usePodRows) and
// health store - only the transports are stubbed (slice 3 §5.9, held to the
// earlier migrations' reviews).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/pods", () => ({
  openPodDetail: vi.fn(async (_c: string, namespace: string, name: string, uid: string) => { useFleet.getState().selectPod({ namespace, name, uid }); }),
  deletePod: vi.fn(async () => {}),
  launchDiagnosticPod: vi.fn(async () => ({})),
}));
vi.mock("../bridge/workloads", () => ({ rolloutRestart: vi.fn(async () => {}) }));
vi.mock("../bridge/metrics", () => ({ getPodContainerUsage: vi.fn(async () => ({ available: true, message: "", updatedAtUnix: 0, items: [] })) }));
vi.mock("../bridge/windows", () => ({ openLogsWindow: vi.fn(async () => true) }));
vi.mock("../bridge/exec", () => ({
  copyExecCommand: vi.fn(async () => "copied"),
  openExecTerminal: vi.fn(async () => {}),
  openDebugTerminal: vi.fn(async () => {}),
  describeResource: vi.fn(async () => ({ ok: true, output: "", error: "" })),
}));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn(async () => ({ ok: true, error: "" })) }));
vi.mock("../bridge/crd", () => ({ copyText: vi.fn(async () => {}) }));
// every render's target is recorded: a log stream must never start against
// another cluster, not even for one render
const logsRenders: string[] = [];
vi.mock("./LogsPane", () => ({
  LogsPane: ({ cluster, pod }: { cluster: string; pod: { namespace: string; name: string } }) => {
    logsRenders.push(`${cluster}:${pod.namespace}/${pod.name}`);
    return <div data-testid="logs-pane">{`${cluster}:${pod.namespace}/${pod.name}`}</div>;
  },
}));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({}));
vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn(() => () => {}) } }));
import { openPodDetail, deletePod } from "../bridge/pods";
import { rolloutRestart } from "../bridge/workloads";
import { openDebugTerminal } from "../bridge/exec";
import { openLogsWindow } from "../bridge/windows";
import { PodsView } from "./PodsView";
import { _resetPodRowsForTest } from "../health/usePodRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type PodDetailDTO, type PodSummaryDTO } from "../store/fleet";
import { clock } from "../layout/coverage";
import { formatAge } from "../chrome/Age";
import { rowOf } from "../test/podRows";
import { buildCommands } from "../chrome/commands";
import { perfHooks } from "../health/perfHooks";

type Kind = "ok" | "fault" | "degraded" | "unknown" | "progressing";
const container = { name: "app", image: "nginx:1.27", ready: true, restarts: 0, state: "running", init: false };
const pod = (name: string, kind: Kind = "ok", over: Partial<PodSummaryDTO> = {}): PodSummaryDTO => ({
  namespace: "app", name, uid: `uid-${name}`, gvr: "v1/pods", ready: kind === "ok", phase: "Running", reason: "", rank: "healthy",
  restarts: 0, node: "n1", ip: "10.0.0.1", ownerKind: "Deployment", ownerName: "web", ownerUid: "uid-web", ageSeconds: 60,
  containers: [container], health: { kind, label: kind === "ok" ? "Running" : "CrashLoopBackOff", reason: kind, rule: `pod.${kind}`, rank: kind === "fault" ? 70 : 0 }, ...over,
});
const detail = (p: PodSummaryDTO, over: Partial<PodDetailDTO> = {}): PodDetailDTO => ({
  summary: p, labels: {}, annotations: {}, conditions: [], yaml: `kind: Pod\nmetadata:\n  name: ${p.name}`, qosClass: "Burstable", serviceAccount: "default",
  events: [{ type: "Normal", reason: `Event-of-${p.uid}`, message: "", count: 1, lastSeen: "2026-10-12T12:00:00Z" }], asOfUnix: 1_760_000_000, ...over,
});

const counted = (over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: "pods", role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const dep = (over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: "replicasets", role: "dependency", scope: "", coverage: "live", objects: 1, ...over });
const kindsOf = (pods: Partial<KindCoverage> = {}, rs: Partial<KindCoverage> = {}) => [counted(pods), dep(rs)];
const countsOf = (rows: PodSummaryDTO[]) => {
  const c = { total: rows.length, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
  for (const r of rows) c[(r.health?.kind ?? "unknown") as Kind]++;
  return c;
};
const part = (ver: number, kinds: KindCoverage[], counts: ReturnType<typeof countsOf>): HealthPart => ({
  id: "pods", entity: "pods", scope: "all namespaces", classifier: "verdict", coverage: "live", kinds, obs: ver, ver, counts,
});
// the wire form: bounded rows (addendum §3.2), in full
const rowsDTO = (ver: number, rows: PodSummaryDTO[], kinds = kindsOf()): RowsDTO => ({
  cluster: "x", incarnation: 1, task: "pods", part: part(ver, kinds, countsOf(rows)), evaluatedAt: "2026-10-12T12:00:00Z", rows: rows.map(rowOf), full: true,
});
// a delta against base: the rows of `after` whose wire form changed, and the UIDs gone
const deltaDTO = (base: number, ver: number, before: PodSummaryDTO[], after: PodSummaryDTO[]): RowsDTO => {
  const was = new Map(before.map((p) => [p.uid, JSON.stringify(rowOf(p))]));
  return {
    ...rowsDTO(ver, after), rows: null, full: false, base,
    upserts: after.filter((p) => was.get(p.uid) !== JSON.stringify(rowOf(p))).map(rowOf),
    removed: before.filter((p) => !after.some((a) => a.uid === p.uid)).map((p) => p.uid ?? ""),
  };
};
const summary = (seq: number, ver: number, counts: ReturnType<typeof countsOf>): HealthSummary => ({
  cluster: "x", incarnation: 1, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "pods", coverage: "live", parts: [part(ver, kindsOf(), counts)] }],
});

function seed(rows: PodSummaryDTO[], kinds = kindsOf()) {
  if (!useHealth.getState().subs.p) addSub("p", "x", "pods", "pods");
  openStarted("p", "tok-p");
  openSucceeded("p", "tok-p", 1);
  applyRows("p", rowsDTO(1, rows, kinds), 0);
}
const push = (ver: number, rows: PodSummaryDTO[], kinds = kindsOf()) => act(() => { applyRows("p", rowsDTO(ver, rows, kinds), 0); });

// the count sentence's number and marker (slice 11b): its noun follows
const headline = () => screen.getByTestId("pods-total").textContent?.split(" ")[0];
const rowNames = () => [...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent);
const select = (name: string) => fireEvent.click(screen.getAllByText(name)[0]);
const inspector = () => within(screen.getByTestId("lens-inspector"));
const landDetail = (d: PodDetailDTO) => act(() => {
  const sel = useFleet.getState().pods.selected!;
  useFleet.getState().setPodDetail(sel, d);
});
const chooseMore = (label: string | RegExp) => {
  fireEvent.click(inspector().getByRole("button", { name: /^More/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: label }));
};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  resetHealthStore();
  _resetPodRowsForTest();
  useFleet.getState().clearPods();
  useFleet.setState({ navIntent: null, actionStatus: null, clusters: [], route: { name: "cluster", cluster: "x", section: "pods" } });
});

describe("Pods rows, mounted end to end (slice 3 §5.9)", () => {
  it("lists all namespaces' rows, faults first in the lens's own order; the namespace is a list filter", () => {
    seed([pod("b-ok"), pod("z-crash", "fault"), pod("a-ok", "ok", { namespace: "data" })]);
    render(<PodsView cluster="x" />);
    expect(rowNames()).toEqual(["app / z-crash", "app / b-ok", "data / a-ok"]);
    expect(headline()).toBe("3");
    expect(screen.getByTestId("pods-attention").textContent).toBe("1 need attention");
    fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "data" } });
    expect(rowNames()).toEqual(["data / a-ok"]);
    expect(headline()).toBe("3"); // the global count stays the cluster's
    expect(screen.getByTestId("pods-scoped").textContent).toBe("in data: 1");
  });

  it("A4 moves the global counts with the notice; the scoped line stays row-scoped, as of its capture", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      seed([pod("ok"), pod("crash", "fault")]);
      render(<PodsView cluster="x" />);
      fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "app" } });
      act(() => { applySummary(summary(2, 2, { total: 9, fault: 4, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 5 }), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(headline()).toBe("9");
      expect(screen.getByTestId("pods-attention").textContent).toBe("4 need attention");
      expect(screen.getByTestId("pods-scoped").textContent).toMatch(/^in app: 2 · 1 need attention \(as of /);
      const gen = useHealth.getState().subs.p.requestGen;
      act(() => bootstrapSettled("p", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("an unreadable first read could not be read; checking is loading; stale empty is the last good read", () => {
    seed([], kindsOf({ coverage: "unreadable", class: "forbidden", error: "pods rbac" }));
    const { rerender } = render(<PodsView cluster="x" />);
    expect(screen.getByText("Pods could not be read.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull(); // the rows retry on their own
    push(2, [], kindsOf({ coverage: "checking", objects: 0 }));
    expect(screen.getByLabelText("loading pods")).toBeTruthy();
    const lastGood = "2026-10-12T09:00:00Z";
    push(3, [], kindsOf({ coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood }));
    expect(screen.getByText("No pods in all namespaces in the last good read.")).toBeTruthy();
    expect(screen.getByText(new RegExp(`last good ${clock(lastGood)}: Pod: transport: dial tcp`))).toBeTruthy();
    push(4, []);
    rerender(<PodsView cluster="x" />);
    expect(screen.getByText("No pods in all namespaces.")).toBeTruthy();
  });

  it("stale rows are shown under a read-failure banner with their last good time, never a present-tense quiet", () => {
    const lastGood = "2026-10-12T09:30:00Z";
    seed([pod("a")], kindsOf({ coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood }));
    render(<PodsView cluster="x" />);
    expect(headline()).toBe("1*");
    expect(screen.getByRole("alert").textContent).toContain(`showing the last good read from ${clock(lastGood)}`);
    expect(screen.getByText("no known attention")).toBeTruthy();
    push(2, [pod("a")]);
    expect(screen.getByText("none need attention")).toBeTruthy();
  });

  it("ReplicaSet evidence qualifies the owners, never the list or a pod's verdict", () => {
    const bare = pod("a", "ok", { ownerKind: "ReplicaSet", ownerName: "web-7d4", ownerUid: "uid-rs" });
    seed([bare], kindsOf({}, { coverage: "unreadable", class: "forbidden", error: "rs rbac" }));
    render(<PodsView cluster="x" />);
    expect(headline()).toBe("1");
    expect(screen.getByText("none need attention")).toBeTruthy(); // a pod's verdict is the pod's own
    expect(screen.getByTestId("owner-evidence").textContent).toMatch(/^ReplicaSets could not be read - a pod under a Deployment shows its ReplicaSet; owners are unresolved \(forbidden: rs rbac\)\.$/);
    select("a");
    expect(inspector().getByTestId("owner-unresolved").textContent).toMatch(/owner unresolved \(ReplicaSet read unavailable\)/);
    push(2, [bare], kindsOf({}, { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-12T09:00:00Z" }));
    expect(screen.getByTestId("owner-evidence").textContent).toMatch(/^ReplicaSet read failing - last good .* - pod owners are as last observed \(transport: dial tcp\)\.$/);
    push(3, [pod("a")]);
    expect(screen.queryByTestId("owner-evidence")).toBeNull();
    expect(inspector().queryByTestId("owner-unresolved")).toBeNull();
  });

  it("ticks the age from the creation time, not the capture's age", () => {
    vi.useFakeTimers({ now: 1_760_000_010_000 }); // on the shared clock's 30s tick
    try {
      seed([pod("a", "ok", { ageSeconds: 5, createdUnix: 1_760_000_010 - 7200 })]);
      render(<PodsView cluster="x" />);
      expect(screen.getByText(formatAge(7200))).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("a kept selection (slice 3 §5a)", () => {
  it("is removed only on a complete read; a stale read keeps it, unverified, with its actions", () => {
    seed([pod("a"), pod("b")]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(pod("a")));
    push(2, [pod("b")], kindsOf({ coverage: "stale", class: "transport", error: "dial tcp" }));
    expect(inspector().getByTestId("selection-unverified").textContent).toMatch(/Not in the current Pod read\..*unverified/);
    expect(inspector().getByText("Event-of-uid-a")).toBeTruthy(); // its last detail stays, said as such
    expect(inspector().getByTestId("pod-detail-asof").textContent).toMatch(/^Last read .*\.$/);
    expect(inspector().getByRole("button", { name: /^Logs/ })).toBeTruthy();
    push(3, [pod("b")]);
    expect(inspector().getByText("Pod removed.")).toBeTruthy();
    expect(inspector().getByTestId("pod-detail-asof").textContent).toMatch(/- before it was removed\.$/);
    expect(inspector().queryByRole("button", { name: /^Logs/ })).toBeNull();
    expect(inspector().queryByRole("button", { name: /^More/ })).toBeNull();
  });

  it("an operator filter that hides it closes it; a read change never does", () => {
    seed([pod("crash", "fault"), pod("b"), pod("c", "ok", { namespace: "data" })]);
    render(<PodsView cluster="x" />);
    select("crash");
    fireEvent.click(screen.getByRole("button", { name: /^Attention/ }));
    push(2, [pod("crash"), pod("b"), pod("c", "ok", { namespace: "data" })]); // it recovers
    expect(rowNames()).toEqual([]);
    expect(screen.getByTestId("lens-inspector")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "data" } });
    expect(screen.queryByTestId("lens-inspector")).toBeNull();
  });

  it("a cross-link resolves the name to the row's object, revealing it past the filters", () => {
    seed([pod("a"), pod("c", "ok", { namespace: "data" })]);
    useFleet.setState({ pods: { ...useFleet.getState().pods } });
    render(<PodsView cluster="x" />);
    fireEvent.change(screen.getByLabelText("Namespace"), { target: { value: "app" } });
    act(() => useFleet.getState().requestNavIntent({ kind: "select-pod", cluster: "x", namespace: "data", name: "c" }));
    expect(openPodDetail).toHaveBeenLastCalledWith("x", "data", "c", "uid-c");
    expect(useFleet.getState().pods.namespace).toBe("");
    expect(screen.getByTestId("lens-inspector")).toBeTruthy();
  });
});

// Design §5b: the detail is the selected OBJECT's, by UID.
describe("the detail is fenced by UID", () => {
  it("shows a detail only for the row's UID, with its read time", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    expect(openPodDetail).toHaveBeenCalledWith("x", "app", "a", "uid-a");
    landDetail(detail(pod("a", "ok", { uid: "uid-a-other" })));
    expect(inspector().getByTestId("pod-detail-unloaded").textContent).toBe("Detail not loaded for this object - the last read found another object with this name.");
    expect(inspector().queryByText("Event-of-uid-a-other")).toBeNull();
    landDetail(detail(pod("a")));
    expect(inspector().getByText("Event-of-uid-a")).toBeTruthy();
    expect(inspector().getByTestId("pod-detail-asof").textContent).toBe(`Detail read ${new Date(1_760_000_000_000).toLocaleTimeString()}`);
  });

  it("a re-read finding another object keeps this object's detail as last evidence, naming the mismatch", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(pod("a")));
    landDetail(detail(pod("a", "ok", { uid: "uid-a-2" })));
    expect(inspector().getByText("Event-of-uid-a")).toBeTruthy();
    expect(inspector().queryByText("Event-of-uid-a-2")).toBeNull();
    expect(inspector().getByTestId("pod-detail-asof").textContent).toMatch(/- the latest read found another object with this name \(UID uid-a-2, not uid-a\)\.$/);
  });

  it("a capture that changes nothing the detail explains re-reads nothing; one that does re-reads the same pod", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(pod("a")));
    expect(openPodDetail).toHaveBeenCalledTimes(1);
    push(2, [pod("a", "ok", { ageSeconds: 600 })]);
    expect(openPodDetail).toHaveBeenCalledTimes(1);
    push(3, [pod("a", "ok", { restarts: 1 })]);
    expect(openPodDetail).toHaveBeenCalledTimes(2);
  });

  // The Line slice 4 §6.3 (design review 37f00bc3): the inspector is pinned
  // to its pod's UID. A same-name pod with a new UID is "replaced": the
  // pinned pod's last evidence stays, nothing is read or run for the new
  // one, and only "Open the new object" moves the selection to it.
  it("a same-name replacement is shown as Replaced, never followed; Open the new object reads it", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(pod("a")));
    push(2, [pod("a", "ok", { uid: "uid-a-2", restarts: 1 })]);
    expect(openPodDetail).toHaveBeenCalledTimes(1); // the new pod is not read into the pinned inspector
    const insp = inspector();
    expect(insp.getByText("Replaced.")).toBeTruthy();
    expect(insp.queryByRole("button", { name: /^Logs/ })).toBeNull();
    expect(insp.queryByRole("button", { name: /^More/ })).toBeNull();
    fireEvent.click(insp.getByRole("button", { name: "Open the new object" }));
    expect(openPodDetail).toHaveBeenLastCalledWith("x", "app", "a", "uid-a-2");
    expect(inspector().queryByText("Replaced.")).toBeNull();
  });
});

// Design §5b: delete, debug and the owner restart are pinned to the armed objects.
describe("verbs are pinned to the armed pod and owner", () => {
  it("a pod replacement drops the delete confirm; nothing is sent", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    chooseMore("Delete pod");
    expect(screen.getByRole("dialog")).toBeTruthy();
    push(2, [pod("a", "ok", { uid: "uid-a-2" })]);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(deletePod).not.toHaveBeenCalled();
  });

  it("an owner replacement drops the restart confirm; nothing is sent", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    chooseMore(/^Restart deployment web/);
    expect(screen.getByRole("dialog")).toBeTruthy();
    push(2, [pod("a", "ok", { ownerUid: "uid-web-2" })]);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(rolloutRestart).not.toHaveBeenCalled();
  });

  it("the dispatch carries the armed UIDs - delete, debug and the owner restart", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    select("a");
    chooseMore("Delete pod");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    expect(deletePod).toHaveBeenCalledWith("x", "app", "a", "uid-a");
    chooseMore(/^Restart deployment web/);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Restart" }));
    expect(rolloutRestart).toHaveBeenCalledWith("x", "Deployment", "app", "web", "uid-web");
    chooseMore(/Debug shell/); // one container: no picker
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Attach" }));
    expect(openDebugTerminal).toHaveBeenCalledWith("x", "app", "a", "uid-a", "app");
  });

  it("an armed delete survives an incomplete read without its pod; the backend checks the UID", () => {
    seed([pod("a"), pod("b")]);
    render(<PodsView cluster="x" />);
    select("a");
    chooseMore("Delete pod");
    push(2, [pod("b")], kindsOf({ coverage: "stale", class: "transport", error: "dial tcp" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    expect(deletePod).toHaveBeenCalledWith("x", "app", "a", "uid-a");
  });

  it("a bulk delete is pinned to the objects it was confirmed for: a replaced target is skipped and named", async () => {
    seed([pod("a"), pod("b")]);
    render(<PodsView cluster="x" />);
    fireEvent.click(screen.getByLabelText("select pod a for bulk actions"));
    fireEvent.click(screen.getByLabelText("select pod b for bulk actions"));
    fireEvent.click(screen.getByRole("button", { name: "Delete selected" }));
    push(2, [pod("a"), pod("b", "ok", { uid: "uid-b-2" })]);
    await act(async () => { fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete 2" })); });
    expect(deletePod).toHaveBeenCalledTimes(1);
    expect(deletePod).toHaveBeenCalledWith("x", "app", "a", "uid-a");
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error", message: expect.stringMatching(/1 pod was replaced or removed since the delete was armed - nothing was sent for app\/b/) });
  });

  it("a bulk confirm whose every target was replaced is dropped", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    fireEvent.click(screen.getByLabelText("select pod a for bulk actions"));
    fireEvent.click(screen.getByRole("button", { name: "Delete selected" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    push(2, [pod("a", "ok", { uid: "uid-a-2" })]);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(deletePod).not.toHaveBeenCalled();
  });
});

// Codex review of 2ec1168: the logs dock is pinned to the cluster it was
// opened on - DEV/STG/PRD share namespace/name.
describe("the logs dock is pinned to its cluster", () => {
  it("a cluster switch closes it - never one render against the other cluster - and coming back does not reopen it", () => {
    logsRenders.length = 0;
    seed([pod("a")]);
    const { rerender } = render(<PodsView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: "logs for app/a" }));
    expect(screen.getByTestId("logs-pane").textContent).toBe("x:app/a");
    const before = logsRenders.length;
    rerender(<PodsView cluster="y" />);
    expect(screen.queryByTestId("logs-dock")).toBeNull();
    // the dock is not rendered on y's page at all, not even for one render
    expect(logsRenders.slice(before)).toEqual([]);
    rerender(<PodsView cluster="x" />);
    expect(screen.queryByTestId("logs-dock")).toBeNull();
  });

  it("pop-out opens the dock's own cluster, and a late completion never closes a later dock", async () => {
    seed([pod("a"), pod("b")]);
    render(<PodsView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: "logs for app/a" }));
    let done!: (ok: boolean) => void;
    vi.mocked(openLogsWindow).mockReturnValueOnce(new Promise((r) => { done = r; }));
    fireEvent.click(screen.getByRole("button", { name: "open logs in window" }));
    expect(openLogsWindow).toHaveBeenCalledWith("x", "app", "a", expect.any(String));
    fireEvent.click(screen.getByRole("button", { name: "logs for app/b" })); // a later dock
    await act(async () => { done(true); });
    expect(screen.getByTestId("logs-pane").textContent).toBe("x:app/b");
  });
});

// Addendum §3.2-§3.3: the rows travel bounded and as deltas; the inspector
// reads the verdict message and the container detail from the selected
// UID's detail, never from the row.
describe("bounded rows and deltas through the Pods lens", () => {
  const crash = (over: Partial<PodSummaryDTO> = {}) => pod("a", "fault", {
    containers: [{ ...container, ready: false, restarts: 3, state: "waiting:CrashLoopBackOff", lastReason: "OOMKilled", lastExitCode: 137, cpuRequest: 0.25,
      ports: [{ name: "http", port: 8080, protocol: "TCP" }] }],
    health: { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", rule: "pod.current-failure", rank: 70, message: "app: back-off 5m0s restarting" },
    ...over,
  });

  it("the verdict message and container detail are 'loading' until this UID's detail lands", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    select("a");
    expect(inspector().queryByText("app: back-off 5m0s restarting")).toBeNull();
    expect(inspector().getByTestId("verdict-message-pending").textContent).toMatch(/Reading the verdict message/);
    expect(inspector().getByTestId("container-detail-pending").textContent).toBe("loading…");
    expect(inspector().getByText("waiting · CrashLoopBackOff")).toBeTruthy(); // the live state is the row's
    landDetail(detail(crash()));
    expect(inspector().getByText("app: back-off 5m0s restarting")).toBeTruthy();
    expect(inspector().queryByTestId("verdict-message-pending")).toBeNull();
    expect(inspector().getByTitle("nginx:1.27")).toBeTruthy();
    expect(inspector().getByText(/OOMKilled \(exit 137\)/)).toBeTruthy();
    expect(inspector().getByText(/cpu 250m/)).toBeTruthy();
  });

  it("a detail of another verdict never lends its message", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(crash({ health: { kind: "degraded", label: "Pending", reason: "PendingTooLong", rule: "pod.pending-overdue", message: "pending for 9m" } })));
    expect(inspector().queryByText("pending for 9m")).toBeNull();
    expect(inspector().getByTestId("verdict-message-pending")).toBeTruthy();
    expect(inspector().getByTitle("nginx:1.27")).toBeTruthy(); // the container detail is this object's
  });

  it("another object's detail lends neither the message nor the container detail", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(crash({ uid: "uid-a-2" })));
    expect(inspector().queryByText("app: back-off 5m0s restarting")).toBeNull();
    expect(inspector().queryByTitle("nginx:1.27")).toBeNull();
    expect(inspector().getByTestId("container-detail-pending").textContent).toBe("not read");
    expect(inspector().getByTestId("verdict-message-pending").textContent).toBe("Verdict message not read.");
  });

  it("a failed detail read says the container detail was not read - never an empty claim", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(crash(), { error: "forbidden" }));
    expect(inspector().getByTestId("container-detail-pending").textContent).toBe("not read");
    expect(inspector().queryByTestId("verdict-message-pending")).toBeNull();
  });

  it("deltas upsert and remove rows through the real adapter; a wrong base changes nothing", () => {
    const a = pod("a"), b = pod("b"), c = pod("c");
    seed([a, b, c]);
    render(<PodsView cluster="x" />);
    expect(rowNames()).toEqual(["app / a", "app / b", "app / c"]);
    const b2 = pod("b", "fault");
    act(() => { applyRows("p", deltaDTO(1, 2, [a, b, c], [a, b2]), 0); });
    expect(rowNames()).toEqual(["app / b", "app / a"]); // the fault first, c removed
    expect(useFleet.getState().pods.rowsVer).toEqual({ incarnation: 1, ver: 2 });
    act(() => { applyRows("p", deltaDTO(5, 6, [a, b2], [a]), 0); }); // a lost delta: wrong base
    expect(rowNames()).toEqual(["app / b", "app / a"]);
    expect(useFleet.getState().pods.rowsVer).toEqual({ incarnation: 1, ver: 2 });
    expect(useHealth.getState().subs.p.resync).toBe(true);
  });

  it("the palette and the row read the row's own fields", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    const cmds = buildCommands(useFleet.getState());
    expect(cmds.find((c) => c.id === "pod:app/a")?.hint).toBe("Pod · CrashLoopBackOff · x");
  });

  it("port-forward suggests the detail's declared ports once it is read", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    select("a");
    chooseMore("Port-forward…");
    expect(screen.queryByRole("button", { name: "use port 8080" })).toBeNull(); // the row carries no ports
    landDetail(detail(crash()));
    expect(screen.getByRole("button", { name: "use port 8080" })).toBeTruthy();
  });

  it("a generation bump or a new reason under the same label re-reads the detail", () => {
    seed([crash()]);
    render(<PodsView cluster="x" />);
    select("a");
    landDetail(detail(crash()));
    expect(openPodDetail).toHaveBeenCalledTimes(1);
    push(2, [crash({ generation: 2 })]);
    expect(openPodDetail).toHaveBeenCalledTimes(2);
    push(3, [crash({ generation: 2, health: { kind: "fault", label: "CrashLoopBackOff", reason: "Exited", rule: "pod.current-failure", rank: 70 } })]);
    expect(openPodDetail).toHaveBeenCalledTimes(3);
  });

  it("the lens reports the rows Ver its committed render shows (the §6a probe's anchor)", () => {
    const commits: string[] = [];
    perfHooks.commit = (key, inc, ver) => { commits.push(`${key}@${inc}:${ver}`); };
    try {
      seed([pod("a")]);
      render(<PodsView cluster="x" />);
      push(2, [pod("a"), pod("b")]);
      expect(commits).toEqual(["pods/pods@1:1", "pods/pods@1:2"]);
      expect(rowNames()).toEqual(["app / a", "app / b"]); // that commit shows Ver 2's rows
    } finally {
      delete perfHooks.commit;
    }
  });
});

// The Line slice 9 note §4.2: an opening by UID (the console) pins that UID
// at once; another object at the name, or none, is said - never shown.
describe("an opening by UID", () => {
  const open = (name: string, uid: string) => act(() => useFleet.getState().requestNavIntent({ kind: "select-pod", cluster: "x", namespace: "app", name, uid }));

  it("the row with that UID opens as usual", () => {
    seed([pod("a")]);
    render(<PodsView cluster="x" />);
    open("a", "uid-a");
    expect(openPodDetail).toHaveBeenLastCalledWith("x", "app", "a", "uid-a");
    expect(screen.queryByTestId("lens-inspector")).toBeTruthy();
    expect(document.querySelector("[data-pinned-gone]")).toBeNull();
  });

  it("another UID at the name is Replaced, on a complete read; Open the new object reads it", () => {
    seed([pod("a", "ok", { uid: "uid-a-2" })]);
    render(<PodsView cluster="x" />);
    open("a", "uid-a");
    expect(openPodDetail).not.toHaveBeenCalled(); // the new pod is never read in its place
    const gone = document.querySelector('[data-pinned-gone="replaced"]');
    expect(gone?.textContent).toContain("UID uid-a");
    expect(inspector().queryByRole("button", { name: /^Logs/ })).toBeNull();
    fireEvent.click(inspector().getByRole("button", { name: "Open the new object" }));
    expect(openPodDetail).toHaveBeenLastCalledWith("x", "app", "a", "uid-a-2");
    expect(document.querySelector("[data-pinned-gone]")).toBeNull();
  });

  it("absent: unverified on an incomplete read, removed on a complete one; arriving later, it pins", () => {
    seed([pod("b")], kindsOf({ coverage: "stale", class: "transport", error: "dial tcp" }));
    render(<PodsView cluster="x" />);
    open("a", "uid-a");
    expect(document.querySelector('[data-pinned-gone="unverified"]')).toBeTruthy();
    push(2, [pod("b")]);
    expect(document.querySelector('[data-pinned-gone="removed"]')).toBeTruthy();
    push(3, [pod("a"), pod("b")]);
    expect(document.querySelector("[data-pinned-gone]")).toBeNull();
    expect(inspector().getByRole("button", { name: /^Logs/ })).toBeTruthy();
  });

  it("another UID on an incomplete read claims nothing", () => {
    seed([pod("a", "ok", { uid: "uid-a-2" })], kindsOf({ coverage: "stale", class: "transport", error: "dial tcp" }));
    render(<PodsView cluster="x" />);
    open("a", "uid-a");
    expect(document.querySelector('[data-pinned-gone="unverified"]')).toBeTruthy();
    expect(inspector().queryByRole("button", { name: "Open the new object" })).toBeNull();
  });
});

// Review of ff41862: a later opening by UID of an already-selected name
// establishes its own object, in both directions.
describe("repeated openings of a selected pod name", () => {
  it("an opening of the old UID over the selected new pod says replaced", () => {
    seed([pod("a", "ok", { uid: "uid-current" })]);
    render(<PodsView cluster="x" />);
    select("a");
    act(() => useFleet.getState().requestNavIntent({ kind: "select-pod", cluster: "x", namespace: "app", name: "a", uid: "uid-old" }));
    expect(document.querySelector("[data-pinned-gone]")?.getAttribute("data-pinned-gone")).toBe("replaced");
    expect(inspector().queryByRole("button", { name: /^Logs/ })).toBeNull();
  });

  it("an opening of the current UID over a replaced pin opens it", () => {
    seed([pod("a", "ok", { uid: "uid-old" })]);
    render(<PodsView cluster="x" />);
    select("a");
    push(2, [pod("a", "ok", { uid: "uid-current" })]);
    expect(inspector().getByText("Replaced.")).toBeTruthy();
    act(() => useFleet.getState().requestNavIntent({ kind: "select-pod", cluster: "x", namespace: "app", name: "a", uid: "uid-current" }));
    expect(inspector().queryByText("Replaced.")).toBeNull();
    expect(inspector().queryByRole("button", { name: "Open the new object" })).toBeNull();
  });
});
