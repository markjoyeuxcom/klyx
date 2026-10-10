import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";
import { NodesView } from "./NodesView";
import { useFleet } from "../store/fleet";
import type { NodeSummaryDTO, NodeDetailDTO } from "../store/fleet";

// Mock the bridge/nodes module. cordonNode and startDrain/cancelDrain are
// also mocked so no Wails runtime is needed.
vi.mock("../bridge/nodes", () => ({
  listNodes: vi.fn().mockResolvedValue(undefined),
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
      rowsCluster: "homelab",
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
      rowsCluster: "homelab",
      loading: false,
      selected: { name: summary.name },
      detail,
      detailLoading: false,
    },
  }));
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
    expect(getByText(/node OS\/kubelet overhead not included/)).toBeTruthy();
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
    const select = getByLabelText("loss target") as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toContain("zone westeurope-1");
    expect([...select.options].map((o) => o.text)).toContain("pool workers");
    fireEvent.click(getByText("simulate"));
    const verdict = await findByTestId("survivability-verdict");
    expect(verdict.textContent).toContain("fits — 5 displaced pods reschedule onto 2 nodes");
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
    fireEvent.click(getByText("simulate"));
    const verdict = await findByTestId("survivability-verdict");
    expect(verdict.textContent).toContain("2 pods won't fit");
    expect(getByText("x/big")).toBeTruthy();
    expect(getByText(/pod list capped: the verdict may be optimistic/)).toBeTruthy();
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
    expect(openNodeDetail).toHaveBeenCalledWith("homelab", "node-healthy");
  });

  it("owns scrolling for the node list inside the hidden cluster shell", () => {
    seed([notReady, healthy, cordoned]);
    const { getByTestId } = render(<NodesView cluster="homelab" />);
    expect(getByTestId("nodes-list-scroll").style.overflowY).toBe("auto");
  });

  it("keeps node summary allocation values compact", () => {
    seed([healthy, cordoned]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("11.8/12.0")).toBeTruthy();
    expect(getByText("22.0Gi/24.0Gi")).toBeTruthy();
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
        selected: { name: "node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getAllByText, getByText } = render(<NodesView cluster="homelab" />);
    // Summary — version appears in both list row and detail panel; use getAllByText
    expect(getAllByText("v1.30.0").length).toBeGreaterThanOrEqual(1);
    // Conditions
    expect(getByText("kubelet is posting ready status")).toBeTruthy();
    // Taints
    expect(getByText("dedicated")).toBeTruthy();
    expect(getByText("NoSchedule")).toBeTruthy();
    // Events
    expect(getByText("Starting")).toBeTruthy();
  });

  it("describe button opens the read-only describe pane via the bridge", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByRole, findByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: "describe" }));
    // nodes are cluster-scoped: namespace is ""
    expect(describeResource).toHaveBeenCalledWith("homelab", "node", "", "node-healthy");
    expect(await findByText(/Name:\s+node-healthy/)).toBeTruthy();
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("renders healthy False pressure conditions as healthy", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText } = render(<NodesView cluster="homelab" />);
    const row = getByText("MemoryPressure").parentElement!;
    expect(row.querySelector("span")?.style.background).toBe("var(--color-text-success)");
  });

  it("detail panel shows pods-on-node list", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy" },
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
        selected: { name: "node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText, queryByText } = render(<NodesView cluster="homelab" />);
    // Labels collapsed: values not visible
    expect(queryByText(/kubernetes\.io\/hostname=node-healthy/)).toBeNull();
    fireEvent.click(getByText("Labels (2)"));
    expect(getByText(/kubernetes\.io\/hostname=node-healthy/)).toBeTruthy();
  });

  it("annotations section is collapsed by default, expandable", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByText, queryByText } = render(<NodesView cluster="homelab" />);
    expect(queryByText("cluster-autoscaler.kubernetes.io/scale-down-disabled=true")).toBeNull();
    fireEvent.click(getByText("Annotations (1)"));
    expect(getByText("cluster-autoscaler.kubernetes.io/scale-down-disabled=true")).toBeTruthy();
  });

  it("yaml tab renders yaml content", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByText("yaml"));
    expect(document.body.textContent).toContain("apiVersion: v1");
  });

  it("close button clears selected node", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: "close node detail panel" }));
    expect(useFleet.getState().nodes.selected).toBeNull();
  });

  it("pods-on-node click navigates to pods section", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      nodes: {
        ...s.nodes,
        selected: { name: "node-healthy" },
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
    const { getByLabelText, getByText } = render(<NodesView cluster="homelab" />);
    // Actions row shows "cordon" button (healthy node is schedulable)
    fireEvent.click(getByLabelText("cordon"));
    // Confirm dialog should appear
    expect(getByText("Cordon node")).toBeTruthy();
    expect(getByText("Cordon")).toBeTruthy();
  });

  it("confirming cordon calls cordonNode bridge", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("cordon"));
    // Click the confirm button in the dialog
    fireEvent.click(getByText("Cordon"));
    expect(cordonNode).toHaveBeenCalledWith("homelab", "node-healthy", true);
  });

  it("cancelling cordon dialog does not call cordonNode", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("cordon"));
    fireEvent.click(getByText("Cancel"));
    expect(cordonNode).not.toHaveBeenCalled();
  });

  it("uncordon button appears for cordoned node", () => {
    seedWithDetail(cordoned, cordonedDetail);
    const { getByLabelText } = render(<NodesView cluster="homelab" />);
    expect(getByLabelText("uncordon")).toBeTruthy();
  });

  it("confirming uncordon calls cordonNode with cordon=false", () => {
    seedWithDetail(cordoned, cordonedDetail);
    const { getByLabelText, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("uncordon"));
    fireEvent.click(getByText("Uncordon"));
    expect(cordonNode).toHaveBeenCalledWith("homelab", "b-cordoned", false);
  });

  // --- Drain action tests ---

  it("drain button appears in actions row", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText } = render(<NodesView cluster="homelab" />);
    expect(getByLabelText("drain")).toBeTruthy();
  });

  it("drain button opens the drain composer with safe defaults", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
    expect(getByRole("dialog", { name: "Drain node node-healthy" })).toBeTruthy();
    // Safe default: DaemonSets tolerated ON; emptyDir destruction and force OFF.
    expect(getByRole("button", { name: /Tolerate DaemonSets/ }).getAttribute("aria-pressed")).toBe("true");
    expect(getByRole("button", { name: /Delete emptyDir data/ }).getAttribute("aria-pressed")).toBe("false");
    expect(getByRole("button", { name: /^Force/ }).getAttribute("aria-pressed")).toBe("false");
  });

  it("command preview shows exactly the composed kubectl drain flags", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByRole, getByTestId } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    const { getByLabelText, getByRole, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    expect(startDrain).toHaveBeenCalledWith("homelab", "node-healthy", {
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
    const { getByLabelText, getByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
    fireEvent.click(getByRole("button", { name: /Delete emptyDir data/ }));
    await act(async () => {
      fireEvent.click(getByRole("button", { name: "Drain" }));
    });
    expect(startDrain).toHaveBeenCalledWith("homelab", "node-healthy", expect.objectContaining({
      deleteEmptyDirData: true,
    }), "test-stream-1");
  });

  it("cancelling the drain composer starts no drain", () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
    fireEvent.click(getByText("Cancel"));
    expect(startDrain).not.toHaveBeenCalled();
  });

  it("drain modal: Events.On subscribes to nodedrain:<streamId>", async () => {
    seedWithDetail(healthy, fakeDetail);
    const { getByLabelText, getByRole } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    const { getByLabelText, getByRole, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    const { getByLabelText, getByRole, getByText, queryByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    const { getByLabelText, getByRole, getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    const { getByLabelText, getByRole, unmount } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    const { getByLabelText, getByRole, getByText, getByTestId, queryByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByLabelText("drain"));
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
    fireEvent.click(pane.getByLabelText("drain"));
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
    fireEvent.click(getByRole("button", { name: /unknown\s*1/ }));
    expect(getByText("silent-node")).toBeTruthy();
    expect(queryByText(healthy.name)).toBeNull();
  });
});

describe("NodesView scope switch", () => {
  it("switching A->B with B's read deferred exposes none of A's numbers, roles or capacity", () => {
    useFleet.setState((s) => ({ nodes: { ...s.nodes, cluster: "blue", items: [healthy, notReady], rowsCluster: "homelab", loading: true, error: null } }));
    const { queryByText, getByRole } = render(<NodesView cluster="blue" />);
    expect(queryByText(healthy.name)).toBeNull();
    expect(queryByText("cpu alloc")).toBeNull();
    expect(queryByText("pod cap")).toBeNull();
    expect(getByRole("button", { name: /^all$/ })).toBeTruthy(); // no count from cluster A
  });
});
