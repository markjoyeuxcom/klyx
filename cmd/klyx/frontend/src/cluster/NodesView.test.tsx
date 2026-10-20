import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, screen } from "@testing-library/react";
import { NodesView } from "./NodesView";
import { useFleet } from "../store/fleet";
import type { NodeSummaryDTO, NodeDetailDTO } from "../store/fleet";

// Mock the bridge/nodes module. cordonNode and startDrain/cancelDrain are
// also mocked so no Wails runtime is needed.
// The rows adapter is mocked: these tests seed the nodes slice directly
// (NodesRows.test.tsx mounts the real adapter over the health store).
vi.mock("../health/useNodeRows", () => ({ useNodeRows: () => {} }));
// coverage S11a: the extended task's rows (tests set extendedValue)
let extendedValue: import("../health/useExtendedRows").ExtendedRows = {
  reservations: new Map(), reservationMarks: { nodes: "", requested: "", dra: "" }, reservationsDropped: 0,
  totals: [], totalMarks: { nodes: "", requested: "", dra: "" }, totalsOmitted: 0, reservationsRead: true, totalsRead: true,
};
vi.mock("../health/useExtendedRows", async (orig) => ({ ...(await orig<typeof import("../health/useExtendedRows")>()), useExtendedRows: () => extendedValue }));

vi.mock("../bridge/nodes", () => ({
  openNodeDetail: vi.fn().mockResolvedValue(undefined),
  cordonNode: vi.fn().mockResolvedValue(undefined),
  startDrain: vi.fn().mockResolvedValue({ streamId: "test-stream-1" }),
  cancelDrain: vi.fn().mockResolvedValue(undefined),
  getNodeHeadroom: vi.fn(async () => ({
    result: { lostNodes: 0, survivorNodes: 0, displaced: 0, rescheduled: 0, unplaced: [], cpuShortfall: 0, memShortfall: 0 },
    pdbBlockers: [], podListCapped: false,
  })),
}));
import { openNodeDetail, cordonNode, startDrain, cancelDrain, getNodeHeadroom } from "../bridge/nodes";

// The drain modal mints its stream id before starting (review 2026-10-03 #8).
vi.mock("../bridge/streams", () => ({ newStreamId: () => "test-stream-1" }));

// ConfirmDialog registers protected-cluster grants via the config bridge;
// mock it so no Wails runtime (bindings chain) is needed.
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

// CopyButton copies via the crd bridge; mock it so no bindings chain loads.
vi.mock("../bridge/crd", () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
}));

// Live node utilization: default unavailable; per-test overrides seed items.
vi.mock("../bridge/metrics", () => ({
  getNodeUtilization: vi.fn(async () => ({ available: false, message: "no metrics source", updatedAtUnix: 0, items: [] })),
}));
import { getNodeUtilization } from "../bridge/metrics";

// DescribePane runs kubectl describe via the exec bridge; mocked so the
// describe test never needs a Wails runtime.
vi.mock("../bridge/exec", () => ({
  describeResource: vi.fn().mockResolvedValue({ ok: true, output: "Name:               node-healthy", error: "" }),
}));
import { describeResource } from "../bridge/exec";

// useListKeys -> CommandPalette -> commands.ts pulls every bridge, whose
// generated bindings import runtime.Create; stub the bindings module so the
// chain never reaches the Wails runtime.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({}));

// Mock the Wails Events.On used by DrainModal.
vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn().mockReturnValue(() => {}),
  },
}));
import { Events } from "@wailsio/runtime";

const healthy: NodeSummaryDTO = {
  name: "node-healthy",
  uid: "uid-node-healthy",
  pool: "workers",
  zone: "westeurope-1",
  roles: ["worker"],
  ready: true,
  unschedulable: false,
  problems: [],
  version: "v1.30.0",
  os: "linux",
  arch: "amd64",
  taintCount: 0,
  cpuCapacity: 8,
  cpuAllocatable: 7.8,
  memCapacity: 16 * 1073741824,
  memAllocatable: 14 * 1073741824,
  podCapacity: 110,
  ageSeconds: 86400,
  health: { kind: "ok", label: "Ready", reason: "Ready", rule: "node.ready" },
};

const notReady: NodeSummaryDTO = {
  name: "a-notready",
  uid: "uid-a-notready",
  roles: [],
  ready: false,
  unschedulable: false,
  problems: ["NotReady"],
  version: "v1.30.0",
  os: "linux",
  arch: "amd64",
  taintCount: 1,
  cpuCapacity: 4,
  cpuAllocatable: 3.8,
  memCapacity: 8 * 1073741824,
  memAllocatable: 7 * 1073741824,
  podCapacity: 110,
  ageSeconds: 3600,
  health: { kind: "fault", label: "NotReady", reason: "KubeletNotReady", rule: "node.not-ready" },
};

const cordoned: NodeSummaryDTO = {
  name: "b-cordoned",
  uid: "uid-b-cordoned",
  roles: ["worker"],
  ready: true,
  unschedulable: true,
  problems: [],
  version: "v1.30.0",
  os: "linux",
  arch: "amd64",
  taintCount: 0,
  cpuCapacity: 4,
  cpuAllocatable: 4,
  memCapacity: 8 * 1073741824,
  memAllocatable: 8 * 1073741824,
  podCapacity: 110,
  ageSeconds: 7200,
  health: { kind: "paused", label: "Cordoned", reason: "Cordoned", rule: "node.cordoned" },
};

const fakeDetail: NodeDetailDTO = {
  summary: healthy,
  labels: { "kubernetes.io/hostname": "node-healthy", region: "westeurope" },
  annotations: { "cluster-autoscaler.kubernetes.io/scale-down-disabled": "true" },
  taints: [{ key: "dedicated", value: "gpu", effect: "NoSchedule" }],
  conditions: [
    { type: "Ready", status: "True", reason: "KubeletReady", message: "kubelet is posting ready status" },
    { type: "MemoryPressure", status: "False", reason: "KubeletHasSufficientMemory", message: "" },
  ],
  events: [
    { type: "Normal", reason: "Starting", message: "kubelet starting", count: 1, lastSeen: "2026-06-10T00:00:00Z" },
  ],
  yaml: "apiVersion: v1\nkind: Node",
  podsOnNode: [
    { namespace: "kube-system", name: "coredns-abc", phase: "Running" },
    { namespace: "monitoring", name: "prometheus-0", phase: "Running" },
  ],
};

const cordonedDetail: NodeDetailDTO = {
  ...fakeDetail,
  summary: cordoned,
};

function seed(items: NodeSummaryDTO[]) {
  useFleet.setState((s) => ({
    nodes: {
      ...s.nodes,
      cluster: "homelab",
      items,
      loading: false,
    },
  }));
}

function seedWithDetail(summary: NodeSummaryDTO, detail: NodeDetailDTO) {
  useFleet.setState((s) => ({
    nodes: {
      ...s.nodes,
      cluster: "homelab",
      items: [summary],
      loading: false,
      selected: { name: summary.name, uid: summary.uid ?? "" },
      detail,
      detailLoading: false,
    },
  }));
}

// Redesign anatomy helpers: Drain/Describe live in the inspector's More menu;
// the survivability simulator opens from the header's "Simulate loss".
function chooseMore(label: string | RegExp) {
  fireEvent.click(screen.getByRole("button", { name: /^More/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: label }));
}
function openSimulator() {
  fireEvent.click(screen.getByRole("button", { name: "Simulate loss" }));
}

describe("NodesView", () => {
  beforeEach(() => {
    useFleet.getState().clearNodes();
    useFleet.setState({ navIntent: null, actionStatus: null });
    vi.clearAllMocks();
  });

  it("renders rows from seeded store", () => {
    seed([notReady, healthy]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("node-healthy")).toBeTruthy();
    expect(getByText("a-notready")).toBeTruthy();
  });

  it("renders live usage against allocatable when utilization is available", async () => {
    vi.mocked(getNodeUtilization).mockResolvedValue({
      available: true, updatedAtUnix: 1700, message: "",
      items: [{ node: "node-healthy", cpu: 3.9, mem: 10.5 * 1073741824, sampledPods: 12 }],
    });
    seed([healthy, notReady]);
    const { findByText, getAllByText, getByText } = render(<NodesView cluster="homelab" />);
    // 3.9 of 7.8 cpu allocatable = 50%; 10.5 of 14Gi mem = 75% (warn tier).
    expect(await findByText(/\(50%\)/)).toBeTruthy();
    expect(getByText(/\(75%\)/)).toBeTruthy();
    // The caveat lives on the usage column headers (tooltip), not a caption row.
    expect(screen.getAllByTitle(/node OS\/kubelet overhead not included/).length).toBe(2);
    // The unsampled node renders "—", never a zero bar.
    expect(getAllByText("—").length).toBeGreaterThan(0);
  });

  it("degrades the usage columns to an honest caption when metrics are unavailable", async () => {
    vi.mocked(getNodeUtilization).mockResolvedValue({ available: false, message: "no metrics source", updatedAtUnix: 0, items: [] });
    seed([healthy]);
    const { findByTestId } = render(<NodesView cluster="homelab" />);
    const cap = await findByTestId("node-util-unavailable");
    expect(cap.textContent).toContain("no metrics source");
  });

  it("surfaces the pod-list cap under-count caveat", async () => {
    vi.mocked(getNodeUtilization).mockResolvedValue({
      available: true, updatedAtUnix: 1700, message: "pod list capped - node usage may under-count",
      items: [{ node: "node-healthy", cpu: 1, mem: null, sampledPods: 5 }],
    });
    seed([healthy]);
    const { findByText } = render(<NodesView cluster="homelab" />);
    expect(await findByText(/pod list capped/)).toBeTruthy();
  });

  it("survivability: targets derive from loaded nodes and the verdict renders", async () => {
    vi.mocked(getNodeHeadroom).mockResolvedValue({
      result: { lostNodes: 1, survivorNodes: 2, displaced: 5, rescheduled: 5, unplaced: [], cpuShortfall: 0, memShortfall: 0 },
      pdbBlockers: [{ namespace: "team-a", name: "pg-pdb", disruptionsAllowed: 0 }],
      podListCapped: false,
    });
    seed([healthy, notReady]);
    const { getByText, getByLabelText, findByTestId } = render(<NodesView cluster="homelab" />);
    // Zone + pool + per-node targets from the loaded rows.
    const select = (openSimulator(), getByLabelText("loss target")) as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toContain("zone westeurope-1");
    expect([...select.options].map((o) => o.text)).toContain("pool workers");
    fireEvent.click(getByText("simulate"));
    const verdict = await findByTestId("survivability-verdict");
    expect(verdict.textContent).toContain("fits - 5 displaced pods reschedule onto 2 nodes");
    expect(getByText(/drain blockers: team-a\/pg-pdb/)).toBeTruthy();
    expect(getByText(/requests-based upper bound/)).toBeTruthy();
  });

  it("survivability: a shortfall names the pods that won't fit", async () => {
    vi.mocked(getNodeHeadroom).mockResolvedValue({
      result: { lostNodes: 1, survivorNodes: 1, displaced: 3, rescheduled: 1, unplaced: [{ namespace: "x", name: "big", cpuReq: 4, memReq: 8 * 1073741824 }, { namespace: "x", name: "small", cpuReq: 1, memReq: 1073741824 }], cpuShortfall: 5, memShortfall: 9 * 1073741824 },
      pdbBlockers: [], podListCapped: true,
    });
    seed([healthy]);
    const { getByText, findByTestId } = render(<NodesView cluster="homelab" />);
    openSimulator(); fireEvent.click(getByText("simulate"));
    const verdict = await findByTestId("survivability-verdict");
    expect(verdict.textContent).toContain("2 pods won't fit");
    expect(getByText("x/big")).toBeTruthy();
    expect(getByText(/pod list capped: the verdict may be optimistic/)).toBeTruthy();
  });

  // coverage S6 §2: the reservation is an estimate under a named
  // assumption; a fit that a scheduler one minor behind would break depends
  // on the scheduler version, never "fits"
  it("survivability: a version-dependent fit says so, with the assumption and the difference", async () => {
    vi.mocked(getNodeHeadroom).mockResolvedValue({
      result: { lostNodes: 1, survivorNodes: 2, displaced: 2, rescheduled: 2, unplaced: [], cpuShortfall: 0, memShortfall: 0, altCpu: 3, altMem: 0, versionDependent: ["node-healthy"] },
      pdbBlockers: [], podListCapped: false, schedAssumption: "assuming kube-scheduler 1.37 at its default gates",
    });
    seed([healthy]);
    const { getByText, findByTestId, getByTestId } = render(<NodesView cluster="homelab" />);
    openSimulator(); fireEvent.click(getByText("simulate"));
    const verdict = await findByTestId("survivability-verdict");
    expect(verdict.textContent).toContain("depends on the scheduler version");
    expect(verdict.textContent).not.toContain("fits -");
    expect(getByTestId("survivability-alt").textContent).toContain("+3.00 cpu");
    expect(getByTestId("survivability-alt").textContent).toContain("short on node-healthy");
    expect(getByTestId("survivability-caveat").textContent).toContain("assuming kube-scheduler 1.37");
    expect(getByTestId("survivability-caveat").textContent).toContain("displaced pods by their desired requests");
  });

  it("consumes a parked drain-node intent by opening the drain composer", () => {
    seed([healthy]);
    useFleet.getState().requestNavIntent({ kind: "drain-node", cluster: "homelab", node: "node-healthy" });
    const { getByRole } = render(<NodesView cluster="homelab" />);
    expect(getByRole("dialog", { name: "Drain node node-healthy" })).toBeTruthy();
    expect(useFleet.getState().navIntent).toBeNull(); // one-shot: consumed
    expect(startDrain).not.toHaveBeenCalled(); // opening the composer never starts the drain
  });

  it("a drain intent for a vanished node clears with an error toast, not a dialog", () => {
    seed([healthy]);
    useFleet.getState().requestNavIntent({ kind: "drain-node", cluster: "homelab", node: "gone-node" });
    const { queryByRole } = render(<NodesView cluster="homelab" />);
    expect(queryByRole("dialog")).toBeNull();
    expect(useFleet.getState().navIntent).toBeNull();
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error" });
  });

  it("ignores an intent addressed to another cluster", () => {
    seed([healthy]);
    useFleet.getState().requestNavIntent({ kind: "drain-node", cluster: "other", node: "node-healthy" });
    const { queryByRole } = render(<NodesView cluster="homelab" />);
    expect(queryByRole("dialog")).toBeNull();
    expect(useFleet.getState().navIntent).not.toBeNull(); // parked for its owner
  });

  it("shows problem text for not-ready node", () => {
    seed([notReady]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText(/NotReady/)).toBeTruthy();
  });

  it("shows cordoned text for unschedulable node", () => {
    seed([cordoned]);
    const { getAllByText } = render(<NodesView cluster="homelab" />);
    expect(getAllByText(/cordoned/i).length).toBeGreaterThan(0);
  });

  it("shows '-' roles for node with empty roles", () => {
    seed([notReady]); // notReady has roles: []
    const { getAllByText } = render(<NodesView cluster="homelab" />);
    // The roles cell for this node is "-"
    expect(getAllByText("-").length).toBeGreaterThan(0);
  });

  it("row click calls openNodeDetail", () => {
    seed([healthy]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByText("node-healthy"));
    expect(openNodeDetail).toHaveBeenCalledWith("homelab", "node-healthy", "uid-node-healthy");
  });

  it("owns scrolling for the node list inside the hidden cluster shell", () => {
    seed([notReady, healthy, cordoned]);
    const { getByTestId } = render(<NodesView cluster="homelab" />);
    expect(getByTestId("nodes-list-scroll").style.overflowY).toBe("auto");
  });

  it("keeps node summary allocation values compact", () => {
    seed([healthy, cordoned]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText(/allocatable 11\.8 CPU/)).toBeTruthy();
    expect(getByText(/22\.0Gi · /)).toBeTruthy();
  });

  it("sorts nodes with problems above quiet nodes", () => {
    seed([healthy, cordoned, notReady]);
    const { getAllByText } = render(<NodesView cluster="homelab" />);
    // Anchored: the survivability dropdown options ("node node-healthy")
    // must not match - only the row name cells.
    const names = getAllByText(/^(a-notready|b-cordoned|node-healthy)$/).map((el) => el.textContent);
    expect(names[0]).toBe("a-notready");
  });

  it("detail panel renders summary, conditions, taints, events", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy", uid: "uid-node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getAllByText, getByText } = render(<NodesView cluster="homelab" />);
    // Summary — version appears in both list row and detail panel; use getAllByText
    expect(getAllByText("v1.30.0").length).toBeGreaterThanOrEqual(1);
    // Successful conditions are collapsed, one click away
    fireEvent.click(getByText(/^Conditions · /));
    expect(getByText("kubelet is posting ready status")).toBeTruthy();
    // Taints
    expect(getByText(/^dedicated/)).toBeTruthy();
    expect(getByText("NoSchedule")).toBeTruthy();
    // Events
    expect(getByText("Starting")).toBeTruthy();
  });

  it("describe button opens the read-only describe pane via the bridge", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click((fireEvent.click(screen.getByRole("button", { name: /^More/ })), screen.getByRole("menuitem", { name: "Describe" })));
    // nodes are cluster-scoped: namespace is ""
    expect(describeResource).toHaveBeenCalledWith("homelab", "node", "", "node-healthy");
    expect((await screen.findByTestId("describe-formatted")).textContent).toContain("node-healthy");
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("renders healthy False pressure conditions as healthy", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByText(/^Conditions · /));
    // a healthy condition carries no mark (slice 15b)
    const row = getByText("MemoryPressure").closest(".cond")!;
    expect(row.getAttribute("data-aspect")).toBe("none");
  });

  it("detail panel shows pods-on-node list", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy", uid: "uid-node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("coredns-abc")).toBeTruthy();
    expect(getByText("prometheus-0")).toBeTruthy();
  });

  it("labels section is collapsed by default, expandable", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy", uid: "uid-node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText, queryByText } = render(<NodesView cluster="homelab" />);
    // Labels collapsed: values not visible
    expect(queryByText(/kubernetes\.io\/hostname=node-healthy/)).toBeNull();
    fireEvent.click(getByText("Labels · 2"));
    expect(getByText(/kubernetes\.io\/hostname=node-healthy/)).toBeTruthy();
  });

  it("annotations section is collapsed by default, expandable", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText, queryByText } = render(<NodesView cluster="homelab" />);
    expect(queryByText("cluster-autoscaler.kubernetes.io/scale-down-disabled=true")).toBeNull();
    fireEvent.click(getByText("Annotations · 1"));
    expect(getByText("cluster-autoscaler.kubernetes.io/scale-down-disabled=true")).toBeTruthy();
  });

  it("yaml tab renders yaml content", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy", uid: "uid-node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(document.body.textContent).toContain("apiVersion: v1");
  });

  it("close button clears selected node", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy", uid: "uid-node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: "close details" }));
    expect(useFleet.getState().nodes.selected).toBeNull();
  });

  it("pods-on-node click navigates to pods section", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy", uid: "uid-node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    // Set up cluster route
    useFleet.getState().openCluster("homelab");
    const { getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByText("coredns-abc"));
    expect(useFleet.getState().route).toMatchObject({ section: "pods" });
  });

  // --- Cordon / Uncordon action tests ---

  it("cordon button appears for schedulable node, shows confirm dialog on click", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText } = render(<NodesView cluster="homelab" />);
    // Actions row shows "cordon" button (healthy node is schedulable)
    fireEvent.click(screen.getByRole("button", { name: "Cordon" }));
    // Confirm dialog should appear
    expect(getByText("Cordon node")).toBeTruthy();
    expect(screen.getAllByText("Cordon").length).toBe(2); // panel action + dialog confirm
  });

  it("confirming cordon calls cordonNode bridge", () => {
    seedWithDetail(healthy, fakeDetail);
    render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("button", { name: "Cordon" }));
    // Click the confirm button in the dialog
    fireEvent.click(screen.getAllByText("Cordon").slice(-1)[0]);
    expect(cordonNode).toHaveBeenCalledWith("homelab", "node-healthy", "uid-node-healthy", true);
  });

  it("cancelling cordon dialog does not call cordonNode", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("button", { name: "Cordon" }));
    fireEvent.click(getByText("Cancel"));
    expect(cordonNode).not.toHaveBeenCalled();
  });

  it("uncordon button appears for cordoned node", () => {
    seedWithDetail(cordoned, cordonedDetail);
    render(<NodesView cluster="homelab" />);
    expect(screen.getByRole("button", { name: "Uncordon" })).toBeTruthy();
  });

  it("confirming uncordon calls cordonNode with cordon=false", () => {
    seedWithDetail(cordoned, cordonedDetail);
    render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("button", { name: "Uncordon" }));
    fireEvent.click(screen.getAllByText("Uncordon").slice(-1)[0]);
    expect(cordonNode).toHaveBeenCalledWith("homelab", "b-cordoned", "uid-b-cordoned", false);
  });

  // --- Drain action tests ---

  it("drain button appears in actions row", () => {
    seedWithDetail(healthy, fakeDetail);
    render(<NodesView cluster="homelab" />);
    expect((fireEvent.click(screen.getByRole("button", { name: /^More/ })), screen.getByRole("menuitem", { name: /^Drain/ }))).toBeTruthy();
  });

  it("drain button opens the drain composer with safe defaults", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    expect(getByRole("dialog", { name: "Drain node node-healthy" })).toBeTruthy();
    // Safe default: DaemonSets tolerated ON; emptyDir destruction and force OFF.
    expect(getByRole("button", { name: /Tolerate DaemonSets/ }).getAttribute("aria-pressed")).toBe("true");
    expect(getByRole("button", { name: /Delete emptyDir data/ }).getAttribute("aria-pressed")).toBe("false");
    expect(getByRole("button", { name: /^Force/ }).getAttribute("aria-pressed")).toBe("false");
  });

  it("command preview shows exactly the composed kubectl drain flags", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByRole, getByTestId } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    const preview = getByTestId("drain-command-preview");
    expect(preview.textContent).toBe("kubectl drain node-healthy --ignore-daemonsets --timeout=120s");
    fireEvent.click(getByRole("button", { name: /Delete emptyDir data/ }));
    fireEvent.click(getByRole("button", { name: /^Force/ }));
    expect(preview.textContent).toBe("kubectl drain node-healthy --ignore-daemonsets --delete-emptydir-data --force --timeout=120s");
    fireEvent.click(getByRole("button", { name: /Tolerate DaemonSets/ }));
    fireEvent.change(getByLabelText("drain timeout"), { target: { value: "300" } });
    expect(preview.textContent).toBe("kubectl drain node-healthy --delete-emptydir-data --force --timeout=300s");
  });

  it("confirming drain with defaults calls startDrain with the safe options and opens the modal", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole, getByText } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    expect(startDrain).toHaveBeenCalledWith("homelab", "node-healthy", "uid-node-healthy", {
      ignoreDaemonSets: true,
      deleteEmptyDirData: false,
      force: false,
      timeoutSeconds: 120,
    }, "test-stream-1");
    // Drain modal title should be visible
    expect(getByText(/Drain node:/)).toBeTruthy();
  });

  it("opting into emptyDir destruction threads deleteEmptyDirData to startDrain", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    fireEvent.click(getByRole("button", { name: /Delete emptyDir data/ }));
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    expect(startDrain).toHaveBeenCalledWith("homelab", "node-healthy", "uid-node-healthy", expect.objectContaining({
      deleteEmptyDirData: true,
    }), "test-stream-1");
  });

  it("cancelling the drain composer starts no drain", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    fireEvent.click(getByText("Cancel"));
    expect(startDrain).not.toHaveBeenCalled();
  });

  it("drain modal: Events.On subscribes to nodedrain:<streamId>", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    // Subscribed on the minted id, which is the id startDrain was given.
    expect(Events.On).toHaveBeenCalledWith(
      "nodedrain:test-stream-1",
      expect.any(Function),
    );
  });

  it("drain modal: cancel button calls cancelDrain", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole, getByText } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    // Cancel drain button is in the modal
    await act(async () => {
      fireEvent.click(getByText("Cancel drain"));
    });
    expect(cancelDrain).toHaveBeenCalledWith("test-stream-1");
  });

  it("drain modal: appended log lines appear when event fires", async () => {
    // Make Events.On capture the callback so we can fire it
    let capturedCb: ((ev: { data: { lines: string[]; eof: boolean } }) => void) | null = null;
    vi.mocked(Events.On).mockImplementation((_name, cb) => {
      capturedCb = cb as typeof capturedCb;
      return () => {};
    });

    seedWithDetail(healthy, fakeDetail);
    const { getByRole, getByText, queryByText } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });

    // Fire a log chunk event
    await act(async () => {
      capturedCb!({ data: { lines: ["evicting pod foo/bar", "evicting pod foo/baz"], eof: false } });
    });
    expect(getByText("evicting pod foo/bar")).toBeTruthy();
    expect(getByText("evicting pod foo/baz")).toBeTruthy();

    // Fire EOF
    await act(async () => {
      capturedCb!({ data: { lines: [], eof: true } });
    });
    expect(queryByText("Cancel drain")).toBeNull();
    expect(getByText("Close")).toBeTruthy();
  });

  // Review 2026-10-03 #8: kubectl output emitted before startDrain returns
  // reaches the modal - it subscribed first.
  it("drain modal: shows output emitted before startDrain resolves", async () => {
    let capturedCb: ((ev: { data: { lines: string[]; eof: boolean } }) => void) | null = null;
    vi.mocked(Events.On).mockImplementation((_name, cb) => {
      capturedCb = cb as typeof capturedCb;
      return () => {};
    });
    vi.mocked(startDrain).mockImplementationOnce(async () => {
      capturedCb!({ data: { lines: ["node/node-healthy cordoned"], eof: false } });
      return { streamId: "test-stream-1" };
    });
    seedWithDetail(healthy, fakeDetail);
    const { getByRole, getByText } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    expect(getByText("node/node-healthy cordoned")).toBeTruthy();
  });

  // Review 2026-10-03 (G): the modal closing while startDrain is in flight
  // used to return early and orphan the drain.
  it("drain modal: a drain started after the modal closed is cancelled", async () => {
    let resolveStart!: (v: { streamId: string }) => void;
    vi.mocked(startDrain).mockImplementationOnce(() => new Promise((r) => { resolveStart = r; }));
    seedWithDetail(healthy, fakeDetail);
    const { getByRole, unmount } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    unmount();
    vi.mocked(cancelDrain).mockClear();
    await act(async () => { resolveStart({ streamId: "test-stream-1" }); });
    expect(cancelDrain).toHaveBeenCalledWith("test-stream-1");
  });

  // Review 2026-10-03 (H): a cancelled drain is reported as cancelled with the
  // node still cordoned - not as "complete", and not by silently vanishing.
  it("drain modal: cancel keeps the modal open for the cancelled report", async () => {
    let capturedCb: ((ev: { data: { lines: string[]; eof: boolean; cancelled?: boolean; error?: string } }) => void) | null = null;
    vi.mocked(Events.On).mockImplementation((_name, cb) => {
      capturedCb = cb as typeof capturedCb;
      return () => {};
    });
    seedWithDetail(healthy, fakeDetail);
    const { getByRole, getByText, getByTestId, queryByText } = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    await act(async () => {
      fireEvent.click(getByText("Cancel drain"));
    });
    expect(cancelDrain).toHaveBeenCalledWith("test-stream-1");
    expect(getByText("cancelling…")).toBeTruthy();
    await act(async () => {
      capturedCb!({ data: { lines: [], eof: true, cancelled: true, error: "drain cancelled - node-healthy stays cordoned and may be partly drained; uncordon it if you are not retrying" } });
    });
    expect(getByText("cancelled")).toBeTruthy();
    expect(getByTestId("drain-cancelled").textContent).toMatch(/node-healthy stays cordoned/);
    expect(queryByText("complete")).toBeNull();
    expect(getByText("Close")).toBeTruthy();
  });

  // Batch-3 review (non-blocking, adopted): re-rendering the view for another
  // cluster must not restart the confirmed drain there with the old node.
  it("drain modal: switching cluster cannot restart the drain on the new cluster", async () => {
    seedWithDetail(healthy, fakeDetail);
    const pane = render(<NodesView cluster="homelab" />);
    chooseMore(/^Drain/);
    await act(async () => {
      fireEvent.click(pane.getByRole("button", { name: "Drain" }));
    });
    expect(startDrain).toHaveBeenCalledTimes(1);
    await act(async () => { pane.rerender(<NodesView cluster="other-cluster" />); });
    expect(vi.mocked(startDrain).mock.calls.map((c) => c[0])).toEqual(["homelab"]);
  });
});

describe("NodesView health model (1b rework)", () => {
  it("offers an unknown filter that isolates nodes whose readiness is not reported", () => {
    const silent: NodeSummaryDTO = { ...healthy, name: "silent-node", health: { kind: "unknown", label: "Ready not reported", reason: "ReadyMissing", rule: "node.ready-missing" } };
    seed([healthy, silent]);
    const { getByRole, getByText, queryByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^Unknown\s*1$/ }));
    expect(getByText("silent-node")).toBeTruthy();
    expect(queryByText(healthy.name)).toBeNull();
  });
});

describe("NodesView scope switch", () => {
  it("switching A->B with B's read deferred exposes none of A's numbers, roles or capacity", () => {
    useFleet.setState((s) => ({ nodes: { ...s.nodes, cluster: "homelab", items: [healthy, notReady], loading: false, error: null } }));
    const { queryByText, getByRole } = render(<NodesView cluster="blue" />);
    expect(queryByText(healthy.name)).toBeNull();
    expect(queryByText("cpu alloc")).toBeNull();
    expect(queryByText("pod cap")).toBeNull();
    expect(getByRole("button", { name: /^All$/ })).toBeTruthy(); // no count from cluster A
  });
});

describe("NodesView workspace template (redesign)", () => {
  beforeEach(() => {
    useFleet.getState().clearNodes();
    useFleet.setState({ navIntent: null, actionStatus: null });
    vi.clearAllMocks();
  });

  it("auto-selects the first attention node once, from a fresh read only", () => {
    seed([healthy, notReady]);
    render(<NodesView cluster="homelab" />);
    expect(openNodeDetail).not.toHaveBeenCalled(); // cached rows at entry
    act(() => seed([healthy, notReady]));
    expect(openNodeDetail).toHaveBeenCalledTimes(1);
    expect(openNodeDetail).toHaveBeenCalledWith("homelab", "a-notready", "uid-a-notready");
    act(() => seed([healthy, notReady, { ...notReady, name: "b-notready", uid: "uid-b-notready" }]));
    expect(openNodeDetail).toHaveBeenCalledTimes(1);
  });

  it("no idle inspector when nothing is selected", () => {
    seed([healthy]);
    const { queryByTestId } = render(<NodesView cluster="homelab" />);
    expect(queryByTestId("lens-inspector")).toBeNull();
    expect(queryByTestId("inspector-idle")).toBeNull();
  });

  it("a node gone from a complete read shows 'Node removed' and cancels a pending cordon", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText, queryByText, queryByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("button", { name: "Cordon" }));
    expect(getByText("Cordon node")).toBeTruthy();
    act(() => seed([notReady]));
    expect(getByText("Node removed.")).toBeTruthy();
    expect(queryByText("Cordon node")).toBeNull();
    expect(queryByRole("button", { name: /^More/ })).toBeNull();
  });

  it("usage columns give way entirely when no metrics source exists", async () => {
    vi.mocked(getNodeUtilization).mockResolvedValue({ available: false, message: "no metrics source", updatedAtUnix: 0, items: [] });
    seed([healthy]);
    const { findByTestId, queryByText } = render(<NodesView cluster="homelab" />);
    await findByTestId("node-util-unavailable");
    expect(queryByText("CPU used")).toBeNull();
    expect(queryByText("Memory used")).toBeNull();
  });
});

describe("NodesView pending verbs are pinned to their own target (Codex review, Nodes)", () => {
  beforeEach(() => {
    useFleet.getState().clearNodes();
    useFleet.setState({ navIntent: null, actionStatus: null });
    vi.clearAllMocks();
  });

  it("a drain raised without selection is cancelled when its node leaves a complete read", () => {
    seed([healthy, cordoned]);
    useFleet.getState().requestNavIntent({ kind: "drain-node", cluster: "homelab", node: "node-healthy" });
    const { getByRole, queryByRole } = render(<NodesView cluster="homelab" />);
    expect(getByRole("dialog", { name: "Drain node node-healthy" })).toBeTruthy();
    act(() => useFleet.getState().selectNode(null)); // selection is not the owner
    act(() => seed([cordoned]));
    expect(queryByRole("dialog", { name: /Drain node/ })).toBeNull();
    expect(startDrain).not.toHaveBeenCalled();
  });

  it("a cordon survives a selection change and is cancelled when its own node is removed", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText, queryByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("button", { name: "Cordon" }));
    expect(getByText("Cordon node")).toBeTruthy();
    act(() => seed([healthy, cordoned]));
    act(() => useFleet.getState().selectNode({ name: "b-cordoned", uid: "uid-b-cordoned" }));
    expect(getByText("Cordon node")).toBeTruthy(); // its target still exists
    act(() => seed([cordoned]));
    expect(queryByText("Cordon node")).toBeNull();
    expect(cordonNode).not.toHaveBeenCalled();
  });

  it("a cluster switch never retargets a pending cordon onto a same-name node", () => {
    seedWithDetail(healthy, fakeDetail);
    const { queryByText, rerender } = render(<NodesView cluster="homelab" />);
    fireEvent.click(screen.getByRole("button", { name: "Cordon" }));
    rerender(<NodesView cluster="other" />);
    act(() => useFleet.setState((s) => ({ nodes: { ...s.nodes, cluster: "other", items: [healthy], loading: false } })));
    expect(queryByText("Cordon node")).toBeNull();
    expect(cordonNode).not.toHaveBeenCalled();
  });

  it("unknown conditions are unverified, never failing; pressure is amber, Ready=False red", () => {
    const mixed: NodeDetailDTO = {
      ...fakeDetail,
      conditions: [
        { type: "Ready", status: "Unknown", reason: "NodeStatusUnknown", message: "" },
        { type: "DiskPressure", status: "Unknown", reason: "NodeStatusUnknown", message: "" },
        { type: "MemoryPressure", status: "True", reason: "KubeletHasInsufficientMemory", message: "" },
      ],
    };
    seedWithDetail(healthy, mixed);
    const { getByText, queryByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("Failing conditions · 1")).toBeTruthy();
    expect(getByText("Unverified conditions · 2")).toBeTruthy();
    const failing = getByText("Failing conditions · 1").closest("section")!;
    expect(failing.textContent).toContain("MemoryPressure");
    expect(failing.textContent).not.toContain("Ready");
    expect(queryByText(/Failing conditions · 3/)).toBeNull();
  });
});

describe("NodesView condition polarity (Codex re-review)", () => {
  beforeEach(() => { useFleet.getState().clearNodes(); vi.clearAllMocks(); });

  it("an unfamiliar condition type is neutral evidence whether True or False; odd statuses are unverified", () => {
    const custom: NodeDetailDTO = {
      ...fakeDetail,
      conditions: [
        { type: "Ready", status: "True", reason: "KubeletReady", message: "" },
        { type: "KernelDeadlock", status: "True", reason: "KernelHasDeadlock", message: "task hung" },
        { type: "FrequentKubeletRestart", status: "False", reason: "NoFrequentKubeletRestart", message: "" },
        { type: "DiskPressure", status: "Maybe", reason: "Odd", message: "" },
      ],
    };
    seedWithDetail(healthy, custom);
    const { queryByText, getByText } = render(<NodesView cluster="homelab" />);
    expect(queryByText(/^Failing conditions/)).toBeNull();
    const unverified = getByText("Unverified conditions · 1").closest("section")!;
    expect(unverified.textContent).toContain("DiskPressure");
    // the custom conditions keep their native facts, uncoloured by health
    fireEvent.click(getByText(/^Conditions/));
    for (const name of ["KernelDeadlock", "FrequentKubeletRestart"]) {
      const el = getByText(name);
      const colored = el.closest("[style*='--color-text-success'], [style*='--color-text-warning'], [style*='--color-text-danger']");
      expect(colored).toBeNull();
    }
    expect(getByText(/task hung/)).toBeTruthy();
  });
});

// Coverage S11a §6: the GPU column, the footer's totals and the node
// inspector's extended resources.
describe("NodesView extended resources (S11a)", () => {
  const gpuNode: NodeSummaryDTO = {
    ...healthy, name: "gpu-1", uid: "uid-gpu-1",
    health: { kind: "degraded", label: "Resources not allocatable", reason: "NotAllocatable", rule: "node.resources-not-allocatable" },
    extended: { migStrategy: "mixed", omitted: 1, factsOmitted: 2, entries: [
      { name: "nvidia.com/gpu", capacity: "8", allocatable: "6", accelerator: true, unit: "GPU", sharing: "shared (time-slicing): each GPU is advertised 4 times, so these count shares of a GPU",
        facts: [{ key: "product", value: "NVIDIA-A100" }, { key: "replicas", value: "4" }] },
      { name: "example.com/resource", capacity: "2", allocatable: "2", facts: [] },
    ] },
  };
  const reservation = { uid: "uid-gpu-1", node: "gpu-1", untracked: true, omitted: 2, entries: [
    { name: "nvidia.com/gpu", advertised: true, requested: "3", servedByDra: "1", notAdvertised: "0" },
    { name: "example.com/resource", advertised: true, requested: "1", servedByDra: "0", notAdvertised: "0" },
    { name: "example.com/license", requested: "0", servedByDra: "0", notAdvertised: "2" },
  ] };
  const live = { nodes: "" as const, requested: "" as const, dra: "" as const };
  const base = () => ({ reservations: new Map([["uid-gpu-1", reservation]]), reservationMarks: live, reservationsDropped: 0,
    totals: [{ name: "nvidia.com/gpu", capacity: "8", allocatable: "6", requested: "3" }], totalMarks: live, totalsOmitted: 0, reservationsRead: true, totalsRead: true });

  it("no accelerator, no GPU column and no totals", () => {
    extendedValue = { ...base(), reservations: new Map(), totals: [] };
    seed([healthy]);
    const { container, queryByText } = render(<NodesView cluster="homelab" />);
    expect(queryByText("GPU")).toBeNull();
    expect(container.querySelector("[data-testid='nodes-extended-totals']")).toBeNull();
  });

  it("the column reads requested / allocatable with the gap, and the footer its totals", () => {
    extendedValue = base();
    seed([gpuNode, healthy]);
    const { getAllByTestId, getByTestId, getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("GPU")).toBeTruthy();
    const cells = getAllByTestId("nodes-gpu-cell").map((c) => c.textContent);
    expect(cells).toContain("3 / 6 · 2 not allocatable");
    expect(cells).toContain("-");
    expect(getByTestId("nodes-extended-totals").textContent).toBe(" · 6 nvidia.com/gpu allocatable (3 requested)");
  });

  it("a requested figure without its row, or from an unestablished read, is ?", () => {
    extendedValue = { ...base(), reservations: new Map(), reservationsDropped: 1 };
    seed([gpuNode]);
    const { getAllByTestId } = render(<NodesView cluster="homelab" />);
    expect(getAllByTestId("nodes-gpu-cell")[0].textContent).toBe("? / 6 · 2 not allocatable");
  });

  it("the inspector names each resource, its sharing and facts, the other requests and every omission", () => {
    extendedValue = { ...base(), reservationMarks: { nodes: "", requested: "*", dra: "" } };
    seedWithDetail(gpuNode, { ...fakeDetail, summary: gpuNode });
    const { getByTestId, getAllByTestId } = render(<NodesView cluster="homelab" />);
    const block = getByTestId("node-extended");
    expect(block.textContent).toContain("MIG strategy: mixed (each profile is its own resource)");
    const lines = getAllByTestId("node-extended-line").map((l) => l.textContent);
    expect(lines[0]).toContain("nvidia.com/gpu · 3* requested · 6 allocatable · 8 capacity · 2 not allocatable · 1 served by DRA");
    expect(lines[0]).toContain("each GPU is advertised 4 times");
    expect(lines[0]).toContain("product NVIDIA-A100");
    expect(lines[2]).toContain("example.com/license · 2* requested, not advertised by this node");
    const notes = getAllByTestId("node-extended-note").map((n) => n.textContent);
    expect(notes).toEqual(expect.arrayContaining(["1 more extended resource not shown", "vendor facts for 2 more resources not shown", "2 more requested resources not shown",
      "requested from the last observed read"]));
    expect(notes.some((n) => n?.startsWith("more than 256 distinct names"))).toBe(true);
  });

  it("the header calls a node degraded, never under pressure: S11a's reading is not pressure", () => {
    extendedValue = base();
    seed([gpuNode, healthy]);
    const { container } = render(<NodesView cluster="homelab" />);
    expect(container.textContent).toContain("1 degraded");
    expect(container.textContent).not.toContain("under pressure");
  });

  it("the inspector says a dropped row, and a withdrawn plugin as 0 advertised", () => {
    extendedValue = { ...base(), reservations: new Map(), reservationsDropped: 1 };
    const withdrawn: NodeSummaryDTO = { ...gpuNode, extended: { entries: [{ ...gpuNode.extended!.entries[0], capacity: "0", allocatable: "0", sharing: undefined, facts: [] }] } };
    seedWithDetail(withdrawn, { ...fakeDetail, summary: withdrawn });
    const { getAllByTestId } = render(<NodesView cluster="homelab" />);
    const line = getAllByTestId("node-extended-line")[0].textContent;
    expect(line).toBe("nvidia.com/gpu · 0 advertisedunit: GPU"); // the unit is its own line
    const notes = getAllByTestId("node-extended-note").map((n) => n.textContent);
    expect(notes.some((n) => n?.startsWith("requested not shown: this node's reservations are not in the current read"))).toBe(true);
  });

  it("a node-row-only update re-renders the block, from the joined rows", () => {
    extendedValue = base();
    seedWithDetail(gpuNode, { ...fakeDetail, summary: gpuNode });
    const { getAllByTestId } = render(<NodesView cluster="homelab" />);
    expect(getAllByTestId("node-extended-line")[0].textContent).toContain("6 allocatable");
    act(() => {
      useFleet.setState((s) => ({ nodes: { ...s.nodes, items: [{ ...gpuNode, extended: { entries: [{ ...gpuNode.extended!.entries[0], allocatable: "8" }] } }] } }));
    });
    expect(getAllByTestId("node-extended-line")[0].textContent).toContain("8 allocatable");
    expect(getAllByTestId("node-extended-line")[0].textContent).not.toContain("not allocatable");
  });
});
