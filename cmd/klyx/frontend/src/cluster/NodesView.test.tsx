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
}));
import { openNodeDetail, cordonNode, startDrain, cancelDrain } from "../bridge/nodes";

// ConfirmDialog registers protected-cluster grants via the config bridge;
// mock it so no Wails runtime (bindings chain) is needed.
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

// Mock the Wails Events.On used by DrainModal.
vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn().mockReturnValue(() => {}),
  },
}));
import { Events } from "@wailsio/runtime";

const healthy: NodeSummaryDTO = {
  name: "node-healthy",
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
      selected: { name: summary.name },
      detail,
      detailLoading: false,
    },
  }));
}

describe("NodesView", () => {
  beforeEach(() => {
    useFleet.getState().clearNodes();
    vi.clearAllMocks();
  });

  it("renders rows from seeded store", () => {
    seed([notReady, healthy]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("node-healthy")).toBeTruthy();
    expect(getByText("a-notready")).toBeTruthy();
  });

  it("shows problem text for not-ready node", () => {
    seed([notReady]);
    const { getByText } = render(<NodesView cluster="homelab" />);
    expect(getByText("NotReady")).toBeTruthy();
  });

  it("shows cordoned text for unschedulable node", () => {
    seed([cordoned]);
    const { getAllByText } = render(<NodesView cluster="homelab" />);
    expect(getAllByText("cordoned").length).toBeGreaterThan(0);
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
    const names = getAllByText(/a-notready|b-cordoned|node-healthy/).map((el) => el.textContent);
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
    expect(getByText(/apiVersion: v1/)).toBeTruthy();
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
    const { getByText } = render(<NodesView cluster="homelab" />);
    fireEvent.click(getByText("✕"));
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
    });
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
    }));
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
    // After startDrain resolves, Events.On should be called with nodedrain:test-stream-1
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
});
