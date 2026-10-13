import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, within, fireEvent } from "@testing-library/react";

// The Nodes lens mounted on the REAL rows adapter (health/useNodeRows) and
// health store - only the transports are stubbed (slice 3 §5.7, held to the
// earlier migrations' reviews).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/nodes", () => ({
  openNodeDetail: vi.fn(async (_c: string, name: string, uid: string) => { useFleet.getState().selectNode({ name, uid }); }),
  cordonNode: vi.fn(async () => {}),
  startDrain: vi.fn(async () => ({ streamId: "test-stream-1" })),
  cancelDrain: vi.fn(async () => {}),
  getNodeHeadroom: vi.fn(async () => ({ result: { lostNodes: 0, survivorNodes: 0, displaced: 0, rescheduled: 0, unplaced: [], cpuShortfall: 0, memShortfall: 0 }, pdbBlockers: [], podListCapped: false })),
}));
vi.mock("../bridge/metrics", () => ({ getNodeUtilization: vi.fn(async () => ({ available: true, message: "", updatedAtUnix: 0, items: [] })) }));
vi.mock("../bridge/streams", () => ({ newStreamId: () => "test-stream-1" }));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn(async () => ({ ok: true, error: "" })) }));
vi.mock("../bridge/crd", () => ({ copyText: vi.fn(async () => {}) }));
vi.mock("../bridge/exec", () => ({ describeResource: vi.fn(async () => ({ ok: true, output: "", error: "" })) }));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({}));
vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn(() => () => {}) } }));
import { openNodeDetail, cordonNode, startDrain } from "../bridge/nodes";
import { NodesView } from "./NodesView";
import { _resetNodeRowsForTest } from "../health/useNodeRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type NodeDetailDTO, type NodeSummaryDTO } from "../store/fleet";
import { clock } from "../layout/coverage";
import { formatAge } from "../chrome/Age";

type Kind = "ok" | "fault" | "degraded" | "unknown" | "paused";
const LABEL: Record<Kind, string> = { ok: "Ready", fault: "NotReady", degraded: "MemoryPressure", unknown: "Ready not reported", paused: "Cordoned" };
const node = (name: string, kind: Kind = "ok", over: Partial<NodeSummaryDTO> = {}): NodeSummaryDTO => ({
  name, uid: `uid-${name}`, gvr: "v1/nodes", generation: 1, roles: ["worker"], ready: kind !== "fault", unschedulable: kind === "paused", problems: [],
  version: "v1.31.0", os: "linux", arch: "amd64", taintCount: 0, cpuCapacity: 4, cpuAllocatable: 4, memCapacity: 8e9, memAllocatable: 8e9, podCapacity: 110,
  ageSeconds: 600, health: { kind, label: LABEL[kind], reason: LABEL[kind], rule: `node.${kind}` }, ...over,
});
const detail = (n: NodeSummaryDTO, over: Partial<NodeDetailDTO> = {}): NodeDetailDTO => ({
  summary: n, labels: {}, annotations: {}, taints: [], conditions: [{ type: "Ready", status: "True", reason: "KubeletReady", message: "kubelet is posting ready status" }],
  events: [], yaml: `kind: Node\nmetadata:\n  name: ${n.name}`, podsOnNode: [{ namespace: "default", name: `pod-on-${n.name}`, phase: "Running" }], asOfUnix: 1_760_000_000, ...over,
});

const kind = (over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: "nodes", role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const countsOf = (rows: NodeSummaryDTO[]) => {
  const c = { total: rows.length, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
  for (const r of rows) c[(r.health?.kind ?? "unknown") as Kind]++;
  return c;
};
const part = (ver: number, k: KindCoverage, counts: ReturnType<typeof countsOf>): HealthPart => ({
  id: "nodes", entity: "nodes", scope: "cluster-scoped", classifier: "verdict", coverage: "live", kinds: [k], obs: ver, ver, counts,
});
const rowsDTO = (ver: number, rows: NodeSummaryDTO[], k: KindCoverage = kind(), incarnation = 1, evaluatedAt = "2026-10-12T12:00:00Z"): RowsDTO => ({
  cluster: "x", incarnation, task: "nodes", part: part(ver, k, countsOf(rows)), evaluatedAt, rows,
});
const summary = (seq: number, ver: number, counts: ReturnType<typeof countsOf>, incarnation = 1): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected",
  tasks: [{ id: "nodes", coverage: "live", parts: [part(ver, kind(), counts)] }],
});

function open(incarnation = 1) {
  if (!useHealth.getState().subs.n) addSub("n", "x", "nodes", "nodes");
  openStarted("n", `tok-n-${incarnation}`);
  openSucceeded("n", `tok-n-${incarnation}`, incarnation);
}
function seed(rows: NodeSummaryDTO[], k: KindCoverage = kind(), ver = 1) {
  open();
  applyRows("n", rowsDTO(ver, rows, k), 0);
}
const push = (ver: number, rows: NodeSummaryDTO[], k: KindCoverage = kind()) => act(() => { applyRows("n", rowsDTO(ver, rows, k), 0); });

const headline = () => screen.getByTestId("nodes-total").textContent;
const rowNames = () => [...document.querySelectorAll(".lens-row .lens-cell-name")].map((n) => n.textContent);
const select = (name: string) => fireEvent.click(screen.getAllByText(name)[0]);
const inspector = () => within(screen.getByTestId("lens-inspector"));
const landDetail = (d: NodeDetailDTO) => act(() => {
  const sel = useFleet.getState().nodes.selected!;
  useFleet.getState().setNodeDetail(sel, d);
});

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  _resetNodeRowsForTest();
  useFleet.getState().clearNodes();
  useFleet.setState({ navIntent: null, actionStatus: null, route: { name: "cluster", cluster: "x", section: "nodes" } });
});

describe("Nodes rows, mounted end to end (slice 3 §5.7)", () => {
  it("lists the captured rows with each row's captured verdict, attention first", () => {
    seed([node("b-ok"), node("z-down", "fault"), node("a-ok")]);
    render(<NodesView cluster="x" />);
    expect(rowNames()).toEqual(["z-down", "a-ok", "b-ok"]);
    expect(headline()).toBe("3");
    expect(screen.getByTestId("nodes-attention").textContent).toBe("▲ 1 need attention");
  });

  it("A4 moves the global counts with the notice; the breakdown stays row-scoped, as of its capture", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      open();
      applyRows("n", rowsDTO(1, [node("ok"), node("down", "fault"), node("cord", "paused")], kind({ coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: "2026-10-12T11:00:00Z" })), 0);
      render(<NodesView cluster="x" />);
      expect(headline()).toBe("3*");
      act(() => { applySummary(summary(2, 2, { total: 9, fault: 4, degraded: 1, unknown: 0, progressing: 0, paused: 0, ok: 4 }), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/read failed - showing the last good read from .*dial tcp/);
      expect(headline()).toBe("9*");
      expect(screen.getByTestId("nodes-attention").textContent).toBe("▲ 5* need attention");
      expect(screen.getByText(/4\* not ready · 1\* under pressure · 1\* cordoned \(as of /)).toBeTruthy();
      // the filter chips describe the rows the list can show
      expect(screen.getByRole("button", { name: /^Attention/ }).textContent).toBe("Attention 1");
      const gen = useHealth.getState().subs.n.requestGen;
      act(() => bootstrapSettled("n", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("an unreadable first read could not be read - never an empty cluster, no manual retry", () => {
    seed([], kind({ coverage: "unreadable", class: "forbidden", error: "nodes is forbidden" }));
    render(<NodesView cluster="x" />);
    expect(screen.getByText("Nodes could not be read.")).toBeTruthy();
    expect(screen.getByText(/forbidden: nodes is forbidden/)).toBeTruthy();
    expect(screen.queryByText("The read returned no nodes.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull(); // the rows retry on their own
  });

  it("a checking read is loading; a stale empty read is the last good read, never current", () => {
    seed([], kind({ coverage: "checking", objects: 0 }));
    render(<NodesView cluster="x" />);
    expect(screen.getByLabelText("loading nodes")).toBeTruthy();
    const lastGood = "2026-10-12T09:30:00Z";
    push(2, [], kind({ coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood }));
    expect(screen.getByText("No nodes in the last good read.")).toBeTruthy();
    expect(screen.getByText(new RegExp(`last good ${clock(lastGood)}: Node: transport: dial tcp`))).toBeTruthy();
    push(3, []);
    expect(screen.getByText("The read returned no nodes.")).toBeTruthy();
  });

  it("'none need attention' is only claimed from a complete, current read", () => {
    seed([node("a")], kind({ coverage: "stale", class: "transport", error: "dial tcp" }));
    render(<NodesView cluster="x" />);
    expect(screen.getByText("none known to need attention")).toBeTruthy();
    push(2, [node("a")]);
    expect(screen.getByText("none need attention")).toBeTruthy();
  });

  it("names a discovery gap without downgrading live rows", () => {
    seed([node("a")], kind({ discovery: "forbidden: discovery re-check" }));
    render(<NodesView cluster="x" />);
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/Node: forbidden: discovery re-check/);
    expect(headline()).toBe("1");
  });

  it("a Final drops incarnation 1's rows; the next incarnation's rows replace them", () => {
    seed([node("old")]);
    render(<NodesView cluster="x" />);
    expect(rowNames()).toEqual(["old"]);
    act(() => applySummary({ ...summary(9, 1, countsOf([node("old")])), final: true }));
    expect(rowNames()).toEqual([]);
    expect(screen.getByLabelText("loading nodes")).toBeTruthy();
    act(() => { applySummary(summary(1, 1, countsOf([node("old"), node("new")]), 2)); open(2); applyRows("n", rowsDTO(1, [node("old"), node("new")], kind(), 2)); });
    expect(rowNames()).toEqual(["new", "old"]);
  });

  it("ticks the age from the creation time, not the capture's age", () => {
    vi.useFakeTimers({ now: 1_760_000_010_000 }); // on the shared clock's 30s tick
    try {
      seed([node("a", "ok", { ageSeconds: 5, createdUnix: 1_760_000_010 - 7200 })]);
      render(<NodesView cluster="x" />);
      select("a");
      expect(inspector().getByText(new RegExp(`· ${formatAge(7200)} old`))).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("a kept selection (slice 3 §5a)", () => {
  it("is removed only on a complete read; a stale read keeps it, unverified, with its actions", () => {
    seed([node("a"), node("b")]);
    render(<NodesView cluster="x" />);
    select("a");
    landDetail(detail(node("a")));
    push(2, [node("b")], kind({ coverage: "stale", class: "transport", error: "dial tcp" }));
    expect(inspector().getByTestId("selection-unverified").textContent).toMatch(/Not in the current Node read\..*unverified/);
    expect(inspector().getByText("pod-on-a")).toBeTruthy(); // its last detail stays, said as such
    expect(inspector().getByTestId("node-detail-asof").textContent).toMatch(/^Last read .*\.$/);
    expect(inspector().getByRole("button", { name: "Cordon" })).toBeTruthy();
    push(3, [node("b")]);
    expect(inspector().getByText(/Node removed\./)).toBeTruthy();
    expect(inspector().getByTestId("node-detail-asof").textContent).toMatch(/^Last read .* - before it was removed\.$/);
    expect(inspector().queryByRole("button", { name: "Cordon" })).toBeNull();
    expect(inspector().queryByRole("button", { name: /^More/ })).toBeNull();
  });

  it("an operator filter that hides it closes it; a read change never does", () => {
    seed([node("a"), node("down", "fault")]);
    render(<NodesView cluster="x" />);
    select("down");
    fireEvent.click(screen.getByRole("button", { name: /^Attention/ }));
    expect(screen.getByTestId("lens-inspector")).toBeTruthy();
    push(2, [node("a"), node("down")]); // it recovers: no longer in the filter
    expect(rowNames()).toEqual([]);
    expect(screen.getByTestId("lens-inspector")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^All/ }));
    select("a");
    fireEvent.click(screen.getByRole("button", { name: /^Attention/ }));
    expect(screen.queryByTestId("lens-inspector")).toBeNull();
  });
});

// Design §5b: the detail is the selected OBJECT's, by UID.
describe("the detail is fenced by UID", () => {
  it("shows a detail only for the row's UID, with its read time", () => {
    seed([node("a")]);
    render(<NodesView cluster="x" />);
    select("a");
    expect(openNodeDetail).toHaveBeenCalledWith("x", "a", "uid-a");
    landDetail(detail(node("a", "ok", { uid: "uid-a-other" })));
    expect(inspector().getByTestId("node-detail-unloaded").textContent).toBe("Detail not loaded for this object - the last read found another object with this name.");
    expect(inspector().queryByText("pod-on-a")).toBeNull();
    landDetail(detail(node("a")));
    expect(inspector().getByText("pod-on-a")).toBeTruthy();
    expect(inspector().getByTestId("node-detail-asof").textContent).toBe(`Detail read ${new Date(1_760_000_000_000).toLocaleTimeString()}`);
  });

  it("a re-read finding another object keeps this object's detail as last evidence, naming the mismatch", () => {
    seed([node("a")]);
    render(<NodesView cluster="x" />);
    select("a");
    landDetail(detail(node("a")));
    expect(inspector().getByTestId("node-detail-asof").textContent).toMatch(/^Detail read /);
    landDetail(detail(node("a", "ok", { uid: "uid-a-2" }), { podsOnNode: [{ namespace: "default", name: "pod-on-replacement", phase: "Running" }] }));
    expect(inspector().getByText("pod-on-a")).toBeTruthy(); // uid-a's own last detail
    expect(inspector().queryByText("pod-on-replacement")).toBeNull();
    expect(inspector().getByTestId("node-detail-asof").textContent).toMatch(/^Last read .* - the latest read found another object with this name \(UID uid-a-2, not uid-a\)\.$/);
  });

  it("a same-name replacement is read afresh; the old object's detail never becomes the new one's", () => {
    seed([node("a")]);
    render(<NodesView cluster="x" />);
    select("a");
    landDetail(detail(node("a")));
    vi.mocked(openNodeDetail).mockClear();
    push(2, [node("a", "ok", { uid: "uid-a-2" })]);
    expect(openNodeDetail).toHaveBeenCalledTimes(1);
    expect(openNodeDetail).toHaveBeenCalledWith("x", "a", "uid-a-2");
    expect(inspector().queryByText("pod-on-a")).toBeNull();
  });

  it("re-reads on a change the detail explains, not on every capture", () => {
    seed([node("a")]);
    render(<NodesView cluster="x" />);
    select("a");
    expect(openNodeDetail).toHaveBeenCalledTimes(1);
    push(2, [node("a", "ok", { ageSeconds: 700 })]); // a new capture, same object and facts
    expect(openNodeDetail).toHaveBeenCalledTimes(1);
    push(3, [node("a", "degraded")]);
    expect(openNodeDetail).toHaveBeenCalledTimes(2);
    expect(openNodeDetail).toHaveBeenLastCalledWith("x", "a", "uid-a");
  });

  it("a failed re-read keeps the object's last detail, saying the re-read failed", () => {
    seed([node("a")]);
    render(<NodesView cluster="x" />);
    select("a");
    landDetail(detail(node("a")));
    landDetail({ ...detail(node("a")), summary: node(""), error: "the server is currently unable to handle the request" });
    expect(inspector().getByText("pod-on-a")).toBeTruthy();
    expect(inspector().getByTestId("node-detail-asof").textContent).toMatch(/- the re-read failed: the server is currently unable/);
  });
});

// Design §5b: cordon and drain are pinned to the armed node object.
describe("verbs are pinned to the armed node", () => {
  const recreate = (ver: number, uid: string) => push(ver, [node("a", "ok", { uid })]);

  it("a replacement drops the cordon confirm and the drain composer; nothing is sent", () => {
    seed([node("a")]);
    render(<NodesView cluster="x" />);
    select("a");
    fireEvent.click(inspector().getByRole("button", { name: "Cordon" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    recreate(2, "uid-a-2");
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(inspector().getByRole("button", { name: /^More/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /^Drain/ }));
    expect(screen.getByRole("dialog", { name: "Drain node a" })).toBeTruthy();
    recreate(3, "uid-a-3");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(cordonNode).not.toHaveBeenCalled();
    expect(startDrain).not.toHaveBeenCalled();
  });

  it("an armed verb survives an incomplete read without its node; the backend checks the UID", () => {
    seed([node("a"), node("b")]);
    render(<NodesView cluster="x" />);
    select("a");
    fireEvent.click(inspector().getByRole("button", { name: "Cordon" }));
    push(2, [node("b")], kind({ coverage: "stale", class: "transport", error: "dial tcp" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cordon" }));
    expect(cordonNode).toHaveBeenCalledWith("x", "a", "uid-a", true);
  });

  it("the dispatch carries the armed UID - cordon, uncordon and drain", async () => {
    seed([node("a"), node("c", "paused")]);
    render(<NodesView cluster="x" />);
    select("a");
    fireEvent.click(inspector().getByRole("button", { name: "Cordon" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cordon" }));
    expect(cordonNode).toHaveBeenCalledWith("x", "a", "uid-a", true);
    select("c");
    fireEvent.click(inspector().getByRole("button", { name: "Uncordon" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Uncordon" }));
    expect(cordonNode).toHaveBeenLastCalledWith("x", "c", "uid-c", false);
    fireEvent.click(inspector().getByRole("button", { name: /^More/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: /^Drain/ }));
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Drain" })); });
    expect(startDrain).toHaveBeenCalledWith("x", "c", "uid-c", expect.objectContaining({ ignoreDaemonSets: true }), "test-stream-1");
  });

  it("a palette verb arms the row's UID", () => {
    seed([node("a")]);
    useFleet.getState().requestNavIntent({ kind: "cordon-node", cluster: "x", node: "a", cordon: true });
    render(<NodesView cluster="x" />);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cordon" }));
    expect(cordonNode).toHaveBeenCalledWith("x", "a", "uid-a", true);
  });
});

// Slice 4 review of 4d8b545 #1-#2: select-node decides 'missing' from the
// Node read's own evidence, and reveals the node past the filters.
describe("select-node reveals through the filters and waits for evidence", () => {
  const want = (name: string) => act(() => { useFleet.getState().requestNavIntent({ kind: "select-node", cluster: "x", node: name }); });

  it("a node hidden by the search and a chip is revealed and selected", () => {
    seed([node("unique-a"), node("down", "fault")]);
    render(<NodesView cluster="x" />);
    fireEvent.change(screen.getByPlaceholderText("Search nodes"), { target: { value: "unique-a" } });
    expect(rowNames()).toEqual(["unique-a"]);
    want("down");
    expect(rowNames()).toContain("down");
    expect(useFleet.getState().nodes.selected?.name).toBe("down");
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("an unread or stale Node read waits; a complete empty read is missing", () => {
    seed([node("a")], kind({ coverage: "stale", lastGoodAt: "2026-10-12T11:00:00Z" }));
    const { unmount } = render(<NodesView cluster="x" />);
    want("b");
    expect(useFleet.getState().navIntent).toMatchObject({ node: "b" });
    unmount();
    resetHealthStore();
    useFleet.setState({ navIntent: null });
    seed([]);
    render(<NodesView cluster="x" />);
    want("b");
    expect(useFleet.getState().navIntent).toBeNull();
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error", message: expect.stringMatching(/b is no longer present/) });
  });
});
