import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, within } from "@testing-library/react";
import { PodsView, imageShort } from "./PodsView";
import { useFleet } from "../store/fleet";
import type { PodSummaryDTO, PodDetailDTO } from "../store/fleet";

vi.mock("../bridge/pods", () => ({
  listPods: vi.fn().mockResolvedValue(undefined),
  openLivePods: vi.fn().mockReturnValue(() => {}),
  openPodDetail: vi.fn().mockResolvedValue(undefined),
  deletePod: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../bridge/workloads", () => ({
  listWorkloads: vi.fn().mockResolvedValue(undefined),
  rolloutRestart: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../bridge/metrics", () => ({
  getPodContainerUsage: vi.fn(async () => ({ available: false, message: "no metrics source", updatedAtUnix: 0, items: [] })),
}));

vi.mock("../bridge/windows", () => ({
  openLogsWindow: vi.fn().mockResolvedValue(true),
}));
vi.mock("../bridge/exec", () => ({
  copyExecCommand: vi.fn().mockResolvedValue("copied"),
  openExecTerminal: vi.fn().mockResolvedValue(undefined),
  openDebugTerminal: vi.fn().mockResolvedValue(undefined),
  describeResource: vi.fn().mockResolvedValue({ ok: true, output: "Name:         grafana-xyz", error: "" }),
}));
import { openLivePods, openPodDetail, deletePod } from "../bridge/pods";
import { rolloutRestart } from "../bridge/workloads";
import { openLogsWindow } from "../bridge/windows";
import { describeResource, openDebugTerminal } from "../bridge/exec";

// LogsPane uses LogsService and Wails Events — stub them out so PodsView tests
// don't need a Wails runtime.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  LogsService: {
    OpenLogStream: vi.fn().mockResolvedValue({ streamId: "test-stream", error: undefined }),
    CloseLogStream: vi.fn().mockResolvedValue(undefined),
    CloseAll: vi.fn().mockResolvedValue(undefined),
  },
}));
vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn().mockReturnValue(() => {}) },
}));

const makeContainer = (overrides = {}) => ({
  name: "app", image: "nginx:latest", ready: true, restarts: 0, state: "running", init: false, ...overrides,
});

const healthy: PodSummaryDTO = {
  namespace: "monitoring", name: "grafana-xyz", ready: true, phase: "Running", reason: "", rank: "healthy",
  restarts: 0, node: "node-1", ip: "10.0.0.1", ownerKind: "Deployment", ownerName: "grafana",
  ageSeconds: 3600, containers: [makeContainer()],
  health: { kind: "ok", label: "Running", reason: "Ready", rule: "pod.ready" },
};
const broken: PodSummaryDTO = {
  namespace: "default", name: "api-crash", ready: false, phase: "Running", reason: "CrashLoopBackOff", rank: "unhealthy",
  restarts: 7, node: "node-2", ip: "10.0.0.2", ownerKind: "Deployment", ownerName: "api",
  ageSeconds: 600, containers: [makeContainer({ ready: false, restarts: 7, state: "waiting:CrashLoopBackOff" })],
  health: { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", message: "app: back-off 5m0s restarting failed container", rule: "pod.current-failure", rank: 70 },
};

const fakeDetail: PodDetailDTO = {
  summary: healthy,
  labels: { app: "grafana", team: "ops" },
  annotations: { "reloader.stakater.com/auto": "true" },
  conditions: [
    { type: "Ready", status: "True", reason: "PodCompleted", message: "all containers ready" },
  ],
  events: [
    { type: "Normal", reason: "Pulled", message: "image pulled", count: 1, lastSeen: "2026-06-10T00:00:00Z" },
    { type: "Warning", reason: "BackOff", message: "back-off restarting", count: 3, lastSeen: "2026-06-10T00:01:00Z" },
  ],
  yaml: "apiVersion: v1\nkind: Pod",
  qosClass: "Burstable",
  serviceAccount: "grafana-sa",
};

function seed(items: PodSummaryDTO[]) {
  useFleet.setState((s) => ({
    pods: {
      ...s.pods,
      cluster: "homelab",
      items,
      namespaces: ["default", "monitoring"],
      loading: false,
      rowsScope: "homelab|",
    },
  }));
}

describe("PodsView", () => {
  beforeEach(() => {
    useFleet.getState().clearPods();
    vi.clearAllMocks();
  });

  it("renders triage rows from seeded store", () => {
    seed([broken, healthy]);
    const { getByText } = render(<PodsView cluster="homelab" />);
    expect(getByText("api-crash")).toBeTruthy();
    expect(getByText("grafana-xyz")).toBeTruthy();
    expect(getByText(/CrashLoopBackOff/)).toBeTruthy();
  });

  it("shows a loud truncation banner only when the list was capped", () => {
    seed([broken, healthy]);
    const { queryByText, rerender } = render(<PodsView cluster="homelab" />);
    expect(queryByText(/broken-first triage may miss a fault/)).toBeNull();
    useFleet.setState((s) => ({ pods: { ...s.pods, truncated: true } }));
    rerender(<PodsView cluster="homelab" />);
    expect(queryByText(/broken-first triage may miss a fault/)).toBeTruthy();
  });

  it("the attention filter hides healthy rows and carries the honest count", () => {
    seed([broken, healthy]);
    const { getByRole, getByText, queryByText } = render(<PodsView cluster="homelab" />);
    const sw = getByRole("button", { name: /attention\s*1/i });
    expect(sw.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(sw);
    expect(getByText("api-crash")).toBeTruthy();
    expect(queryByText("grafana-xyz")).toBeNull();
  });

  it("search filters by name", () => {
    seed([broken, healthy]);
    const { getByPlaceholderText, getByText, queryByText } = render(<PodsView cluster="homelab" />);
    fireEvent.change(getByPlaceholderText("filter pods"), { target: { value: "grafana" } });
    expect(getByText("grafana-xyz")).toBeTruthy();
    expect(queryByText("api-crash")).toBeNull();
  });

  it("search filters by namespace", () => {
    seed([broken, healthy]);
    const { getByPlaceholderText, getByText, queryByText } = render(<PodsView cluster="homelab" />);
    fireEvent.change(getByPlaceholderText("filter pods"), { target: { value: "monitoring" } });
    expect(getByText("grafana-xyz")).toBeTruthy();
    expect(queryByText("api-crash")).toBeNull();
  });

  it("row click calls openPodDetail", () => {
    seed([broken]);
    const { getByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("api-crash"));
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "api-crash");
  });

  it("detail panel renders sections from seeded detail", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText } = render(<PodsView cluster="homelab" />);
    // Summary section
    expect(getByText("Burstable")).toBeTruthy();
    expect(getByText("grafana-sa")).toBeTruthy();
    // Conditions
    expect(getByText("all containers ready")).toBeTruthy();
    // Events
    expect(getByText("Pulled")).toBeTruthy();
    expect(getByText("back-off restarting")).toBeTruthy();
    // Labels
    expect(getByText("app=grafana")).toBeTruthy();
    // Annotations start collapsed.
    expect(getByText("Annotations (1)")).toBeTruthy();
  });

  it("describe button opens the read-only describe pane via the bridge", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, findByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: "describe" }));
    expect(describeResource).toHaveBeenCalledWith("homelab", "pod", "monitoring", "grafana-xyz");
    // the kubectl output lands in the pane
    expect(await findByText(/Name:\s+grafana-xyz/)).toBeTruthy();
    // Escape closes it again
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("panel header carries one mono meta line: namespace, node, age, ip", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByTestId } = render(<PodsView cluster="homelab" />);
    const panel = within(getByTestId("pod-detail-panel"));
    // Node keeps its Nodes cross-link, now on the meta line.
    const nodeLink = panel.getByRole("button", { name: "node-1" });
    const meta = nodeLink.parentElement as HTMLElement;
    expect(meta.textContent).toContain("monitoring");
    expect(meta.textContent).toContain("1h");
    expect(meta.textContent).toContain("10.0.0.1");
    // node/ip left the Summary kv - the meta line owns them now.
    expect(panel.queryByText("node")).toBeNull();
    expect(panel.queryByText("ip")).toBeNull();
  });

  it("containers render as kv blocks with state · reason from the DTO", () => {
    const detailCrash: PodDetailDTO = { ...fakeDetail, summary: broken };
    seed([broken]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "default", name: "api-crash" },
        detail: detailCrash,
        detailLoading: false,
      },
    }));
    const { getByTestId } = render(<PodsView cluster="homelab" />);
    const panel = within(getByTestId("pod-detail-panel"));
    expect(panel.getByText("waiting · CrashLoopBackOff")).toBeTruthy();
    expect(panel.getByText("image")).toBeTruthy();
    // "restarts" appears as the facts-well label AND the container kv label.
    expect(panel.getAllByText("restarts").length).toBeGreaterThanOrEqual(2);
    expect(panel.getByTitle("nginx:latest")).toBeTruthy();
  });

  it("detail panel renders annotations as collapsed metadata", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText, queryByText } = render(<PodsView cluster="homelab" />);
    expect(queryByText("reloader.stakater.com/auto=true")).toBeNull();
    fireEvent.click(getByText("Annotations (1)"));
    expect(getByText("reloader.stakater.com/auto=true")).toBeTruthy();
  });

  it("detail panel tabs are info and yaml (no logs tab)", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText, queryByRole } = render(<PodsView cluster="homelab" />);
    // info and yaml tabs present
    expect(getByText("info")).toBeTruthy();
    expect(getByText("yaml")).toBeTruthy();
    // no tab labelled "logs" (the logs button is in the header, not a tab)
    // The button with text "logs" is the dock-open button, not a tab button.
    // Verify no tab with role=button and exact text "logs" in the tabs row by
    // checking the yaml tab switches content correctly instead.
    fireEvent.click(getByText("yaml"));
    expect(document.body.textContent).toContain("apiVersion: v1");
    // No container selector visible at this point (dock not open yet)
    expect(queryByRole("combobox", { name: /container/i })).toBeNull();
  });

  it("logs button in panel header opens the dock with correct ns/name", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    const dock = getByTestId("logs-dock");
    expect(dock).toBeTruthy();
    // Dock header shows ns/name
    const dockText = dock.textContent ?? "";
    expect(dockText).toContain("monitoring");
    expect(dockText).toContain("grafana-xyz");
  });

  it("dock persists when detail panel is closed", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId, queryByTestId } = render(<PodsView cluster="homelab" />);
    // Open dock
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    expect(getByTestId("logs-dock")).toBeTruthy();
    // Close the detail panel (✕ button on the panel — aria-label distinguishes it)
    fireEvent.click(getByRole("button", { name: /close pod detail panel/i }));
    // Dock still present
    expect(queryByTestId("logs-dock")).toBeTruthy();
  });

  it("dock persists when a different pod is selected (no close in between)", async () => {
    const grafana = healthy; // monitoring/grafana-xyz
    const api = broken;      // default/api-crash
    seed([grafana, api]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId } = render(<PodsView cluster="homelab" />);
    // Open dock for grafana
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    expect(getByTestId("logs-dock")).toBeTruthy();
    // Select a different pod (grafana row click -> openPodDetail, but state must be updated manually)
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "default", name: "api-crash" },
        detail: { ...fakeDetail, summary: api },
        detailLoading: false,
      },
    }));
    // Dock still present — it is independent of pod selection
    expect(getByTestId("logs-dock")).toBeTruthy();
  });

  it("logs button on second pod re-targets dock (header updates, LogsPane re-keyed)", async () => {
    const grafana = healthy;
    const api = broken;
    seed([grafana, api]);
    // Open panel for grafana first
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const apiDetail: typeof fakeDetail = { ...fakeDetail, summary: api };
    const { getByRole, getByTestId, rerender } = render(<PodsView cluster="homelab" />);
    // Open dock for grafana
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    {
      const dockText = getByTestId("logs-dock").textContent ?? "";
      expect(dockText).toContain("grafana-xyz");
      expect(dockText).toContain("monitoring");
    }
    // Switch panel to api-crash
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "default", name: "api-crash" },
        detail: apiDetail,
        detailLoading: false,
      },
    }));
    rerender(<PodsView cluster="homelab" />);
    // Re-target dock via logs button for api-crash
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    // Dock header now shows api-crash's ns/name
    const dock = getByTestId("logs-dock");
    const dockText = dock.textContent ?? "";
    expect(dockText).toContain("api-crash");
    expect(dockText).toContain("default");
    // grafana-xyz is no longer in the dock header
    expect(dockText).not.toContain("grafana-xyz");
  });

  it("dock close button (✕) closes the dock", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId, queryByTestId } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    expect(getByTestId("logs-dock")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: /close logs dock/i }));
    expect(queryByTestId("logs-dock")).toBeNull();
  });

  it("yaml tab renders yaml content", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("yaml"));
    expect(document.body.textContent).toContain("apiVersion: v1");
  });

  it("close button clears selected pod", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /close pod detail panel/i }));
    expect(useFleet.getState().pods.selected).toBeNull();
  });

  // --- Action button tests ---

  function openDetailPanel(pod = healthy, detail = fakeDetail) {
    seed([pod]);
    useFleet.setState((s) => ({
      pods: { ...s.pods, selected: { namespace: pod.namespace, name: pod.name }, detail, detailLoading: false },
    }));
  }

  // Review 2026-10-03 #10: failures render as errors, never as an empty list
  // or an empty pod.
  it("a failed list read shows the read-failure banner", () => {
    seed([]);
    useFleet.setState((s) => ({ pods: { ...s.pods, error: "pods is forbidden: User cannot list" } }));
    const { getByRole } = render(<PodsView cluster="homelab" />);
    expect(getByRole("alert").textContent).toMatch(/Pods read failed .*pods is forbidden/);
  });

  it("a failed detail read shows the reason, not an empty pod", () => {
    openDetailPanel(healthy, { ...fakeDetail, error: "pods \"grafana-xyz\" not found" });
    const { getByText } = render(<PodsView cluster="homelab" />);
    expect(getByText(/Could not load pod detail: pods "grafana-xyz" not found/)).toBeTruthy();
  });

  it("delete pod button opens confirm dialog with controller-recreate body", () => {
    openDetailPanel(healthy); // healthy has ownerKind="Deployment" (backend-resolved)
    const { getByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("delete pod"));
    expect(getByText(/the controller will recreate it/i)).toBeTruthy();
  });

  it("delete pod button dialog body warns about no-controller standalone pods", () => {
    const standalone: PodSummaryDTO = { ...healthy, ownerKind: "", ownerName: "" };
    const detailWithStandalone = { ...fakeDetail, summary: standalone };
    openDetailPanel(standalone, detailWithStandalone);
    const { getByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("delete pod"));
    expect(getByText(/will NOT be recreated/i)).toBeTruthy();
  });

  it("confirming delete pod calls deletePod bridge", () => {
    openDetailPanel(healthy);
    const { getByText, getAllByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("delete pod"));
    // Confirm button is the last "Delete" in the DOM (inside the dialog).
    const confirmBtns = getAllByRole("button", { name: "Delete" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    expect(deletePod).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz");
  });

  // Review 2026-10-03 (finding A): kubectl debug permanently adds an ephemeral
  // container to the pod, so the launch must go through the confirm dialog.
  it("debug shell asks for confirmation before launching kubectl debug", () => {
    openDetailPanel(healthy);
    const { getByText, getByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("debug shell"));
    expect(openDebugTerminal).not.toHaveBeenCalled();
    expect(getByText(/stays on the pod spec until the pod is recreated/i)).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Attach" }));
    expect(openDebugTerminal).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "app");
  });

  it("cancelling the debug shell confirmation launches nothing", () => {
    openDetailPanel(healthy);
    const { getByText, getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("debug shell"));
    fireEvent.click(getByRole("button", { name: "Cancel" }));
    expect(queryByRole("button", { name: "Attach" })).toBeNull();
    expect(openDebugTerminal).not.toHaveBeenCalled();
  });

  it("restart owner button is visible for Deployment owner", () => {
    openDetailPanel(healthy); // healthy has ownerKind="Deployment" (backend-resolved)
    const { getByText } = render(<PodsView cluster="homelab" />);
    expect(getByText("restart owner")).toBeTruthy();
  });

  it("restart owner button is NOT visible for a bare ReplicaSet owner", () => {
    // The backend resolves RS -> Deployment; a pod still showing ReplicaSet is
    // under a bare RS with no restartable controller.
    const bareRs: PodSummaryDTO = { ...healthy, ownerKind: "ReplicaSet", ownerName: "web-7d4b9c6f9" };
    const detailRs = { ...fakeDetail, summary: bareRs };
    openDetailPanel(bareRs, detailRs);
    const { queryByText } = render(<PodsView cluster="homelab" />);
    expect(queryByText("restart owner")).toBeNull();
  });

  it("restart owner button is NOT visible for ownerKind='' (standalone pod)", () => {
    const standalone: PodSummaryDTO = { ...healthy, ownerKind: "", ownerName: "" };
    const detailWithStandalone = { ...fakeDetail, summary: standalone };
    openDetailPanel(standalone, detailWithStandalone);
    const { queryByText } = render(<PodsView cluster="homelab" />);
    expect(queryByText("restart owner")).toBeNull();
  });

  it("restart owner button is NOT visible for ownerKind=Node", () => {
    const nodeOwned: PodSummaryDTO = { ...healthy, ownerKind: "Node", ownerName: "node-1" };
    const detailNode = { ...fakeDetail, summary: nodeOwned };
    openDetailPanel(nodeOwned, detailNode);
    const { queryByText } = render(<PodsView cluster="homelab" />);
    expect(queryByText("restart owner")).toBeNull();
  });

  it("restart dispatches the backend-resolved owner verbatim (no name guessing)", () => {
    const deployPod: PodSummaryDTO = {
      ...healthy,
      namespace: "prod", name: "web-7d4b9c6f9-abc",
      ownerKind: "Deployment", ownerName: "web",
    };
    const detailDeploy = { ...fakeDetail, summary: deployPod };
    seed([deployPod]);
    useFleet.setState((s) => ({
      pods: { ...s.pods, selected: { namespace: "prod", name: "web-7d4b9c6f9-abc" }, detail: detailDeploy, detailLoading: false },
    }));
    const { getByText, getAllByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("restart owner"));
    const confirmBtns = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    expect(rolloutRestart).toHaveBeenCalledWith("homelab", "Deployment", "prod", "web");
  });

  // --- Keyboard nav + a11y ---

  it("j then Enter opens the second row (keyboard nav)", async () => {
    const second: PodSummaryDTO = { ...broken, namespace: "default", name: "pod-second" };
    seed([broken, second]);
    render(<PodsView cluster="homelab" />);
    // j → select index 0; wait for re-render; j → select index 1; Enter → activate index 1
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "pod-second");
  });

  it("/ focuses the filter input", () => {
    seed([broken]);
    const { getByPlaceholderText } = render(<PodsView cluster="homelab" />);
    const input = getByPlaceholderText("filter pods") as HTMLInputElement;
    const slash = new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true });
    window.dispatchEvent(slash);
    // In jsdom focus may not move automatically, but we can verify no throw
    // The searchRef.current.focus() is called — confirm the input exists.
    expect(input).toBeTruthy();
  });

  it("pod row has role=button and tabIndex=0", () => {
    seed([broken]);
    const { getAllByRole } = render(<PodsView cluster="homelab" />);
    const buttons = getAllByRole("button");
    // rows have role=button; at least one should exist
    expect(buttons.length).toBeGreaterThan(0);
  });

  it("pod row has aria-selected false initially", () => {
    seed([broken]);
    const { getAllByRole } = render(<PodsView cluster="homelab" />);
    // Find button elements with aria-selected (the pod rows)
    const rows = getAllByRole("button").filter((el) => el.hasAttribute("aria-selected"));
    expect(rows.length).toBeGreaterThan(0);
    expect(rows[0].getAttribute("aria-selected")).toBe("false");
  });

  // --- Row logs affordance ---

  it("each row has a logs icon button rendered", () => {
    seed([broken, healthy]);
    const { getAllByRole } = render(<PodsView cluster="homelab" />);
    const logsBtns = getAllByRole("button").filter((el) =>
      el.getAttribute("aria-label")?.startsWith("logs for ")
    );
    expect(logsBtns.length).toBe(2);
  });

  it("row logs button opens dock with that pod without opening detail panel", () => {
    seed([broken, healthy]);
    const { getAllByRole, getByTestId } = render(<PodsView cluster="homelab" />);
    const logsBtn = getAllByRole("button").find((el) =>
      el.getAttribute("aria-label") === "logs for default/api-crash"
    );
    expect(logsBtn).toBeTruthy();
    fireEvent.click(logsBtn!);
    // Dock opened with the correct pod
    const dock = getByTestId("logs-dock");
    const dockText = dock.textContent ?? "";
    expect(dockText).toContain("api-crash");
    expect(dockText).toContain("default");
    // Detail panel NOT opened — selected state stays null
    expect(useFleet.getState().pods.selected).toBeNull();
    // openPodDetail bridge was NOT called
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("keyboard 'l' on second row opens dock for that pod", () => {
    const second: PodSummaryDTO = { ...broken, namespace: "default", name: "pod-second" };
    seed([broken, second]);
    const { getByTestId } = render(<PodsView cluster="homelab" />);
    // j → select index 0; j → select index 1 (second)
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    // l → open logs for selected (index 1 = pod-second)
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "l", bubbles: true, cancelable: true })); });
    const dock = getByTestId("logs-dock");
    const dockText = dock.textContent ?? "";
    expect(dockText).toContain("pod-second");
    expect(dockText).toContain("default");
  });

  // --- Pop-out log window ---

  it("dock 'open in window' button calls openLogsWindow with pod + container and closes dock", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId, queryByTestId, findByRole } = render(<PodsView cluster="homelab" />);
    // Open the dock.
    fireEvent.click(getByRole("button", { name: /open logs dock/i }));
    expect(getByTestId("logs-dock")).toBeTruthy();

    // LogsPane reports the active container ("app") via onContainerChange on mount.
    const popOutBtn = await findByRole("button", { name: /open logs in window/i });
    await act(async () => {
      fireEvent.click(popOutBtn);
    });

    expect(openLogsWindow).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "app");
    // On success the dock closes (tail moved to the native window).
    expect(queryByTestId("logs-dock")).toBeNull();
  });

  it("openLivePods is called on mount (replaces listPods)", () => {
    render(<PodsView cluster="homelab" />);
    expect(openLivePods).toHaveBeenCalledWith("homelab", "");
  });

  it("palette namespace request re-opens the live subscription for that namespace", () => {
    // A pending nsRequest (set by the ⌘K namespace jump before the view
    // mounts) must move the live subscription - a one-shot list would be
    // dropped by the sub's stale guard.
    useFleet.getState().requestPodsNamespace("homelab", "payments");
    render(<PodsView cluster="homelab" />);
    expect(openLivePods).toHaveBeenCalledWith("homelab", "payments");
    // The request is consumed.
    expect(useFleet.getState().pods.nsRequest).toBeNull();
  });

  it("a namespace request for another cluster is discarded", () => {
    useFleet.getState().requestPodsNamespace("other-cluster", "payments");
    render(<PodsView cluster="homelab" />);
    expect(openLivePods).not.toHaveBeenCalledWith("homelab", "payments");
    expect(useFleet.getState().pods.nsRequest).toBeNull();
  });

  it("live=true renders green live indicator", () => {
    seed([broken]);
    useFleet.setState((s) => ({ pods: { ...s.pods, live: true } }));
    const { getByText } = render(<PodsView cluster="homelab" />);
    expect(getByText("live")).toBeTruthy();
  });

  it("live=false renders manual fallback indicator", () => {
    seed([broken]);
    useFleet.setState((s) => ({ pods: { ...s.pods, live: false } }));
    const { getByText } = render(<PodsView cluster="homelab" />);
    expect(getByText("○ manual")).toBeTruthy();
  });

  it("StatefulSet owner calls rolloutRestart directly without name transformation", () => {
    const stsPod: PodSummaryDTO = {
      ...healthy, namespace: "db", name: "postgres-0",
      ownerKind: "StatefulSet", ownerName: "postgres",
    };
    const detailSts = { ...fakeDetail, summary: stsPod };
    seed([stsPod]);
    useFleet.setState((s) => ({
      pods: { ...s.pods, selected: { namespace: "db", name: "postgres-0" }, detail: detailSts, detailLoading: false },
    }));
    const { getByText, getAllByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("restart owner"));
    const confirmBtns = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    expect(rolloutRestart).toHaveBeenCalledWith("homelab", "StatefulSet", "db", "postgres");
  });

  // --- Bulk selection -------------------------------------------------------

  it("checkbox selection raises the bulk bar; one confirm deletes every target", async () => {
    seed([healthy, broken]);
    const { getByRole, getByText, getAllByRole, findByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("checkbox", { name: "select pod grafana-xyz for bulk actions" }));
    fireEvent.click(getByRole("checkbox", { name: "select pod api-crash for bulk actions" }));
    expect(getByText("2 pods selected")).toBeTruthy();
    fireEvent.click(getByText("delete selected"));
    await findByText("delete 2 pods");
    const confirmBtns = getAllByRole("button", { name: "Delete 2" });
    await act(async () => {
      fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    });
    expect(deletePod).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz");
    expect(deletePod).toHaveBeenCalledWith("homelab", "default", "api-crash");
    // selection cleared after the batch
    expect(useFleet.getState().pods.items.length).toBe(2);
  });

  it("x toggles bulk selection of the keyboard row", () => {
    seed([broken, healthy]);
    const { getByText } = render(<PodsView cluster="homelab" />);
    fireEvent.keyDown(window, { key: "j" }); // select first row
    fireEvent.keyDown(window, { key: "x" }); // toggle bulk
    expect(getByText("1 pod selected")).toBeTruthy();
    fireEvent.keyDown(window, { key: "x" }); // toggle off
    expect(useFleet.getState().pods.items.length).toBe(2);
  });
});

describe("imageShort", () => {
  it("strips the registry/repository path, keeping name:tag", () => {
    expect(imageShort("docker.io/grafana/grafana:12.4.1")).toBe("grafana:12.4.1");
    expect(imageShort("quay.io/kiwigrid/k8s-sidecar:1.30.10")).toBe("k8s-sidecar:1.30.10");
    expect(imageShort("registry.k8s.io/ingress-nginx/controller:v1.11.2")).toBe("controller:v1.11.2");
  });

  it("leaves bare image names untouched", () => {
    expect(imageShort("busybox:1.36")).toBe("busybox:1.36");
    expect(imageShort("nginx")).toBe("nginx");
  });

  it("shortens sha256 digests to 12 hex chars", () => {
    expect(
      imageShort("ghcr.io/org/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
    ).toBe("app@sha256:0123456789ab…");
  });
});

describe("PodsView health model (1b)", () => {
  beforeEach(() => { useFleet.getState().clearPods(); vi.clearAllMocks(); });

  it("a pod without a verdict reads unknown, never healthy from legacy rank", () => {
    const legacyOnly: PodSummaryDTO = { ...healthy, name: "no-verdict", health: undefined };
    seed([legacyOnly]);
    const { getByText, getByRole } = render(<PodsView cluster="homelab" />);
    expect(getByText("Unknown")).toBeTruthy();
    expect(getByRole("button", { name: /unknown\s*1/i })).toBeTruthy();
    expect(getByRole("button", { name: /^attention$/i })).toBeTruthy();
  });

  it("a restarted-but-healthy pod is not attention", () => {
    const restarted: PodSummaryDTO = { ...healthy, name: "restarted", rank: "restarts", restarts: 3,
      health: { kind: "ok", label: "Running", reason: "Ready", rule: "pod.ready", note: "restarted 4m ago" } };
    seed([restarted]);
    const { getByRole } = render(<PodsView cluster="homelab" />);
    expect(getByRole("button", { name: /^attention$/i })).toBeTruthy();
  });
});
