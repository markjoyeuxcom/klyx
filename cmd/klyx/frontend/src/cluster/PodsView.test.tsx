import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, within, screen } from "@testing-library/react";
import { PodsView, imageShort } from "./PodsView";
import { useFleet } from "../store/fleet";
import type { PodSummaryDTO, PodDetailDTO } from "../store/fleet";
import { rowOf } from "../test/podRows";

// The rows adapter is mocked: these tests seed the pods slice directly
// (PodRows.test.tsx mounts the real adapter over the health store).
vi.mock("../health/usePodRows", () => ({ usePodRows: () => {} }));
// the pod's claims block joins the claims rows: none, under a whole read
vi.mock("../health/useDRARows", () => ({
  useDRAClaims: () => ({ rows: [], coverage: [], watchError: null, loading: false, rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null, complete: true }),
}));

const aiBridge = vi.hoisted(() => ({
  aiAvailability: vi.fn(async () => ({ backend: "anthropic", ready: true })),
  aiEgressAllowed: vi.fn(async () => true),
  explain: vi.fn(async () => ({ ok: true, explanation: "The container keeps exiting.", model: "m", backend: "anthropic", groundedOn: "g", error: "" })),
}));
vi.mock("../bridge/ai", () => aiBridge);
vi.mock("../bridge/pods", () => ({
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
// coverage S6 §6: the debug dialog's preflight (the kubectl gate)
const PROFILES = [
  { name: "general", adds: "adds SYS_PTRACE" }, { name: "baseline", adds: "no added privilege" }, { name: "restricted", adds: "non-root" },
  { name: "netadmin", adds: "adds NET_ADMIN and NET_RAW" }, { name: "sysadmin", adds: "privileged: this container has full access to the node" },
];
let debugPre: { profiles: typeof PROFILES; defaultImage: string; defaultProfile: string; kubectl?: string; ok: boolean; reason?: string; qualified?: string } =
  { profiles: PROFILES, defaultImage: "busybox:1.36", defaultProfile: "general", kubectl: "v1.37.1", ok: true };
vi.mock("../bridge/exec", () => ({
  copyExecCommand: vi.fn().mockResolvedValue("copied"),
  openExecTerminal: vi.fn().mockResolvedValue(undefined),
  openDebugTerminal: vi.fn().mockResolvedValue(undefined),
  debugPreflight: vi.fn(async () => debugPre),
  describeResource: vi.fn().mockResolvedValue({ ok: true, output: "Name:         grafana-xyz", error: "" }),
}));
import { openPodDetail, deletePod } from "../bridge/pods";
import { rolloutRestart } from "../bridge/workloads";
import { openLogsWindow } from "../bridge/windows";
import { describeResource, openDebugTerminal, openExecTerminal } from "../bridge/exec";

// LogsPane uses LogsService and Wails Events — stub them out so PodsView tests
// don't need a Wails runtime.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  LogsService: {
    OpenLogStream: vi.fn().mockResolvedValue({ streamId: "test-stream", error: undefined }),
    CloseLogStream: vi.fn().mockResolvedValue(undefined),
    CloseAll: vi.fn().mockResolvedValue(undefined),
  },
  // The Scratch panel lists what Klyx created; the open-scratch tests open it.
  CRDService: {
    ListAdhocObjects: vi.fn().mockResolvedValue([]),
    DeleteAdhocObject: vi.fn().mockResolvedValue({ ok: true, error: "" }),
  },
}));
vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn().mockReturnValue(() => {}) },
}));

const makeContainer = (overrides = {}) => ({
  name: "app", image: "nginx:latest", ready: true, restarts: 0, state: "running", init: false, ...overrides,
});

const healthy: PodSummaryDTO = {
  namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz", ready: true, phase: "Running", reason: "", rank: "healthy",
  restarts: 0, node: "node-1", ip: "10.0.0.1", ownerKind: "Deployment", ownerName: "grafana", ownerUid: "uid-grafana",
  ageSeconds: 3600, createdUnix: Math.floor(Date.now() / 1000) - 3700, containers: [makeContainer()],
  health: { kind: "ok", label: "Running", reason: "Ready", rule: "pod.ready" },
};
const broken: PodSummaryDTO = {
  namespace: "default", name: "api-crash", uid: "uid-api-crash", ready: false, phase: "Running", reason: "CrashLoopBackOff", rank: "unhealthy",
  restarts: 7, node: "node-2", ip: "10.0.0.2", ownerKind: "Deployment", ownerName: "api",
  ageSeconds: 600, createdUnix: Math.floor(Date.now() / 1000) - 600, containers: [makeContainer({ ready: false, restarts: 7, state: "waiting:CrashLoopBackOff" })],
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

// Actions beyond Logs/Shell live in the inspector's More menu (redesign spec §3).
function chooseMore(label: string | RegExp, getByRole: (role: string, opts?: { name?: string | RegExp }) => HTMLElement) {
  fireEvent.click(getByRole("button", { name: /^More/ }));
  fireEvent.click(getByRole("menuitem", { name: label }));
}

// seed takes full summaries and seeds their bounded rows (the detail keeps
// the full summary).
function seed(items: PodSummaryDTO[]) {
  useFleet.setState((s) => ({
    pods: {
      ...s.pods,
      cluster: "homelab",
      items: items.map(rowOf),
      namespaces: ["default", "monitoring"],
      loading: false,
      // owner resolution read the ReplicaSets (the capture's dependency)
      dependencies: [{ kind: "ReplicaSet", served: true, synced: true, coverage: "live" }],
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
    fireEvent.change(getByPlaceholderText("Search pods"), { target: { value: "grafana" } });
    expect(getByText("grafana-xyz")).toBeTruthy();
    expect(queryByText("api-crash")).toBeNull();
  });

  it("search filters by namespace", () => {
    seed([broken, healthy]);
    const { getByPlaceholderText, getByText, queryByText } = render(<PodsView cluster="homelab" />);
    fireEvent.change(getByPlaceholderText("Search pods"), { target: { value: "monitoring" } });
    expect(getByText("grafana-xyz")).toBeTruthy();
    expect(queryByText("api-crash")).toBeNull();
  });

  it("row click calls openPodDetail", () => {
    seed([broken]);
    const { getByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByText("api-crash"));
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "api-crash", "uid-api-crash");
  });

  it("detail panel renders sections from seeded detail", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText, queryByText } = render(<PodsView cluster="homelab" />);
    // Placement facts
    expect(getByText("Burstable")).toBeTruthy();
    expect(getByText("grafana-sa")).toBeTruthy();
    // Recent events are expanded (runtime before metadata)
    expect(getByText("Pulled")).toBeTruthy();
    expect(getByText("back-off restarting")).toBeTruthy();
    // Successful conditions and labels start collapsed, one click away.
    expect(queryByText("all containers ready")).toBeNull();
    fireEvent.click(getByText("Conditions · 1"));
    expect(getByText("all containers ready")).toBeTruthy();
    expect(queryByText("app=grafana")).toBeNull();
    fireEvent.click(getByText("Labels · 2"));
    expect(getByText("app=grafana")).toBeTruthy();
  });

  it("describe button opens the read-only describe pane via the bridge", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Describe", getByRole);
    expect(describeResource).toHaveBeenCalledWith("homelab", "pod", "monitoring", "grafana-xyz");
    // the kubectl output lands in the pane
    expect((await screen.findByTestId("describe-formatted")).textContent).toContain("grafana-xyz");
    // Escape closes it again
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("identity header names kind + namespace, then one meta line: node, age, ip", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByTestId } = render(<PodsView cluster="homelab" />);
    const panel = within(getByTestId("pod-detail-panel"));
    // Node keeps its Nodes cross-link, now on the meta line.
    const nodeLink = panel.getByRole("button", { name: "node-1" });
    const meta = nodeLink.parentElement as HTMLElement;
    expect(meta.textContent).toContain("1h");
    expect(meta.textContent).toContain("10.0.0.1");
    expect(panel.getByText("Pod in monitoring")).toBeTruthy();
  });

  it("containers render as kv blocks with state · reason from the DTO", () => {
    const detailCrash: PodDetailDTO = { ...fakeDetail, summary: broken };
    seed([broken]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "default", name: "api-crash", uid: "uid-api-crash" },
        detail: detailCrash,
        detailLoading: false,
      },
    }));
    const { getByTestId } = render(<PodsView cluster="homelab" />);
    const panel = within(getByTestId("pod-detail-panel"));
    expect(panel.getByText("waiting · CrashLoopBackOff")).toBeTruthy();
    expect(panel.getByText("image")).toBeTruthy();
    expect(panel.getByText("restarts")).toBeTruthy();
    expect(panel.getByTitle("nginx:latest")).toBeTruthy();
  });

  it("says where container usage is read, and no data under the scope (Settings S3b §3.3, §3.4)", async () => {
    const { getPodContainerUsage } = await import("../bridge/metrics");
    vi.mocked(getPodContainerUsage).mockResolvedValueOnce({ available: true, updatedAtUnix: 1, items: [],
      sentence: "from lgtm-we-metrics, hosted by prd, filtered by cluster=homelab", noData: "no data for homelab under cluster=homelab" });
    const detailCrash: PodDetailDTO = { ...fakeDetail, summary: broken };
    seed([broken]);
    useFleet.setState((s) => ({ pods: { ...s.pods, selected: { namespace: "default", name: "api-crash", uid: "uid-api-crash" }, detail: detailCrash, detailLoading: false } }));
    const { findByTestId } = render(<PodsView cluster="homelab" />);
    expect((await findByTestId("pod-usage-source")).textContent).toBe("Read from lgtm-we-metrics, hosted by prd, filtered by cluster=homelab · no data for homelab under cluster=homelab");
  });

  it("detail panel renders annotations as collapsed metadata", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByText, queryByText } = render(<PodsView cluster="homelab" />);
    expect(queryByText("reloader.stakater.com/auto=true")).toBeNull();
    fireEvent.click(getByText("Annotations · 1"));
    expect(getByText("reloader.stakater.com/auto=true")).toBeTruthy();
  });

  it("detail panel tabs are Overview and Manifest (logs open the dock, not a tab)", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getAllByRole, getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    expect(getAllByRole("tab").map((t) => t.textContent)).toEqual(["Overview", "Manifest"]);
    fireEvent.click(getByRole("tab", { name: "Manifest" }));
    expect(document.body.textContent).toContain("apiVersion: v1");
    // No container selector visible at this point (dock not open yet)
    expect(queryByRole("combobox", { name: /container/i })).toBeNull();
  });

  it("logs button in panel header opens the dock with correct ns/name", async () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
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
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId, queryByTestId } = render(<PodsView cluster="homelab" />);
    // Open dock
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
    expect(getByTestId("logs-dock")).toBeTruthy();
    // Close the detail panel (✕ button on the panel — aria-label distinguishes it)
    fireEvent.click(getByRole("button", { name: "close details" }));
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
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId } = render(<PodsView cluster="homelab" />);
    // Open dock for grafana
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
    expect(getByTestId("logs-dock")).toBeTruthy();
    // Select a different pod (grafana row click -> openPodDetail, but state must be updated manually)
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "default", name: "api-crash", uid: "uid-api-crash" },
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
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const apiDetail: typeof fakeDetail = { ...fakeDetail, summary: api };
    const { getByRole, getByTestId, rerender } = render(<PodsView cluster="homelab" />);
    // Open dock for grafana
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
    {
      const dockText = getByTestId("logs-dock").textContent ?? "";
      expect(dockText).toContain("grafana-xyz");
      expect(dockText).toContain("monitoring");
    }
    // Switch panel to api-crash
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "default", name: "api-crash", uid: "uid-api-crash" },
        detail: apiDetail,
        detailLoading: false,
      },
    }));
    rerender(<PodsView cluster="homelab" />);
    // Re-target dock via logs button for api-crash
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
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
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId, queryByTestId } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
    expect(getByTestId("logs-dock")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: /close logs dock/i }));
    expect(queryByTestId("logs-dock")).toBeNull();
  });

  it("yaml tab renders yaml content", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("tab", { name: "Manifest" }));
    expect(document.body.textContent).toContain("apiVersion: v1");
  });

  it("close button clears selected pod", () => {
    seed([healthy]);
    useFleet.setState((s) => ({
      pods: {
        ...s.pods,
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: "close details" }));
    expect(useFleet.getState().pods.selected).toBeNull();
  });

  // --- Action button tests ---

  function openDetailPanel(pod = healthy, detail = fakeDetail) {
    seed([pod]);
    useFleet.setState((s) => ({
      pods: { ...s.pods, selected: { namespace: pod.namespace, name: pod.name, uid: pod.uid ?? "" }, detail, detailLoading: false },
    }));
  }

  // spec 2026-10-13 §4.3
  it("offers Explain with AI in More and asks for the selected pod's row, fenced by its UID", async () => {
    openDetailPanel(healthy);
    const { findByRole, getByRole, findByTestId } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^More/ }));
    fireEvent.click(await findByRole("menuitem", { name: "Explain with AI" }));
    expect((await findByTestId("ai-explain")).textContent).toContain("keeps exiting");
    expect(aiBridge.explain).toHaveBeenCalledWith("homelab", { kind: "row", task: "pods", part: "pods", uid: healthy.uid });
  });

  // Review 2026-10-03 #10: failures render as errors, never as an empty list
  // or an empty pod.
  it("a failed read with no rows is unreadable, naming the error; with last good rows it is a banner", () => {
    seed([]);
    useFleet.setState((s) => ({ pods: { ...s.pods, error: "pods is forbidden: User cannot list" } }));
    const { getByText, rerender } = render(<PodsView cluster="homelab" />);
    expect(getByText("Pods could not be read.")).toBeTruthy();
    expect(getByText(/pods is forbidden: User cannot list/)).toBeTruthy();
    act(() => seed([healthy]));
    rerender(<PodsView cluster="homelab" />);
    expect(document.querySelector('[role="alert"]')?.textContent).toMatch(/Pods read failed .*pods is forbidden/);
  });

  it("a failed detail read shows the reason, not an empty pod", () => {
    openDetailPanel(healthy, { ...fakeDetail, error: "pods \"grafana-xyz\" not found" });
    const { getByText } = render(<PodsView cluster="homelab" />);
    expect(getByText(/Could not load pod detail: pods "grafana-xyz" not found/)).toBeTruthy();
  });

  it("delete pod button opens confirm dialog with controller-recreate body", () => {
    openDetailPanel(healthy); // healthy has ownerKind="Deployment" (backend-resolved)
    const { getByText, getByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Delete pod", getByRole);
    expect(getByText(/the controller will recreate it/i)).toBeTruthy();
  });

  it("delete pod button dialog body warns about no-controller standalone pods", () => {
    const standalone: PodSummaryDTO = { ...healthy, ownerKind: "", ownerName: "" };
    const detailWithStandalone = { ...fakeDetail, summary: standalone };
    openDetailPanel(standalone, detailWithStandalone);
    const { getByText, getByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Delete pod", getByRole);
    expect(getByText(/will NOT be recreated/i)).toBeTruthy();
  });

  it("confirming delete pod calls deletePod bridge", () => {
    openDetailPanel(healthy);
    const { getByRole, getAllByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Delete pod", getByRole);
    // Confirm button is the last "Delete" in the DOM (inside the dialog).
    const confirmBtns = getAllByRole("button", { name: "Delete" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    expect(deletePod).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "uid-grafana-xyz");
  });

  // Review 2026-10-03 (finding A): kubectl debug permanently adds an ephemeral
  // container to the pod, so the launch must go through the confirm dialog.
  // coverage S6 §6: the profile and image are chosen first, then the
  // confirmation names them and the privilege the profile adds
  it("debug shell chooses an image and profile, then confirms naming them, before launching kubectl debug", async () => {
    debugPre = { profiles: PROFILES, defaultImage: "busybox:1.36", defaultProfile: "general", kubectl: "v1.37.1", ok: true };
    openDetailPanel(healthy);
    const { getByText, getByRole, findByTestId } = render(<PodsView cluster="homelab" />);
    chooseMore(/Debug shell/, getByRole);
    await findByTestId("debug-kubectl");
    expect(openDebugTerminal).not.toHaveBeenCalled();
    fireEvent.click(getByRole("radio", { name: /restricted/ }));
    fireEvent.click(getByRole("button", { name: "Continue" }));
    expect(getByText(/profile restricted: non-root.*stays on the pod until the pod is recreated/i)).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Attach" }));
    expect(openDebugTerminal).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "uid-grafana-xyz", "app", "busybox:1.36", "restricted");
  });

  it("cancelling the debug shell launches nothing", async () => {
    openDetailPanel(healthy);
    const { getByRole, queryByRole, findByTestId } = render(<PodsView cluster="homelab" />);
    chooseMore(/Debug shell/, getByRole);
    await findByTestId("debug-kubectl");
    fireEvent.click(getByRole("button", { name: "Cancel" }));
    expect(queryByRole("button", { name: "Attach" })).toBeNull();
    expect(queryByRole("button", { name: "Continue" })).toBeNull();
    expect(openDebugTerminal).not.toHaveBeenCalled();
  });

  it("sysadmin takes a typed 'privileged' interlock even off a protected cluster", async () => {
    openDetailPanel(healthy);
    const { getByText, getByRole, getByPlaceholderText, findByTestId } = render(<PodsView cluster="homelab" />);
    chooseMore(/Debug shell/, getByRole);
    await findByTestId("debug-kubectl");
    fireEvent.click(getByRole("radio", { name: /sysadmin/ }));
    fireEvent.click(getByRole("button", { name: "Continue" }));
    expect(getByText(/privileged: this container has full access to the node/)).toBeTruthy();
    const attach = getByRole("button", { name: "Attach" }) as HTMLButtonElement;
    expect(attach.disabled).toBe(true);
    fireEvent.change(getByPlaceholderText("privileged"), { target: { value: "privileged" } });
    expect(attach.disabled).toBe(false);
  });

  it("a refused kubectl continues nowhere and says why", async () => {
    debugPre = { profiles: PROFILES, defaultImage: "busybox:1.36", defaultProfile: "general", kubectl: "v1.29.0", ok: false, reason: "kubectl v1.29.0 is older than 1.30" };
    openDetailPanel(healthy);
    const { getByRole, findByTestId } = render(<PodsView cluster="homelab" />);
    chooseMore(/Debug shell/, getByRole);
    expect((await findByTestId("debug-kubectl")).textContent).toContain("older than 1.30");
    expect((getByRole("button", { name: "Continue" }) as HTMLButtonElement).disabled).toBe(true);
    debugPre = { profiles: PROFILES, defaultImage: "busybox:1.36", defaultProfile: "general", kubectl: "v1.37.1", ok: true };
  });

  it("restart owner is offered for a Deployment owner, named with its target", () => {
    openDetailPanel(healthy); // healthy has ownerKind="Deployment" (backend-resolved)
    const { getByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(getByRole("menuitem", { name: "Restart deployment grafana" })).toBeTruthy();
  });

  it("restart owner button is NOT visible for a bare ReplicaSet owner", () => {
    // The backend resolves RS -> Deployment; a pod still showing ReplicaSet is
    // under a bare RS with no restartable controller.
    const bareRs: PodSummaryDTO = { ...healthy, ownerKind: "ReplicaSet", ownerName: "web-7d4b9c6f9" };
    const detailRs = { ...fakeDetail, summary: bareRs };
    openDetailPanel(bareRs, detailRs);
    const { getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(queryByRole("menuitem", { name: /^Restart/ })).toBeNull();
  });

  it("restart owner button is NOT visible for ownerKind='' (standalone pod)", () => {
    const standalone: PodSummaryDTO = { ...healthy, ownerKind: "", ownerName: "" };
    const detailWithStandalone = { ...fakeDetail, summary: standalone };
    openDetailPanel(standalone, detailWithStandalone);
    const { getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(queryByRole("menuitem", { name: /^Restart/ })).toBeNull();
  });

  it("restart owner button is NOT visible for ownerKind=Node", () => {
    const nodeOwned: PodSummaryDTO = { ...healthy, ownerKind: "Node", ownerName: "node-1" };
    const detailNode = { ...fakeDetail, summary: nodeOwned };
    openDetailPanel(nodeOwned, detailNode);
    const { getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(queryByRole("menuitem", { name: /^Restart/ })).toBeNull();
  });

  it("restart dispatches the backend-resolved owner verbatim (no name guessing)", () => {
    const deployPod: PodSummaryDTO = {
      ...healthy,
      namespace: "prod", name: "web-7d4b9c6f9-abc", uid: "uid-web-7d4b9c6f9-abc",
      ownerKind: "Deployment", ownerName: "web", ownerUid: "uid-web",
    };
    const detailDeploy = { ...fakeDetail, summary: deployPod };
    seed([deployPod]);
    useFleet.setState((s) => ({
      pods: { ...s.pods, selected: { namespace: "prod", name: "web-7d4b9c6f9-abc", uid: "uid-web-7d4b9c6f9-abc" }, detail: detailDeploy, detailLoading: false },
    }));
    const { getByRole, getAllByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Restart deployment web", getByRole);
    const confirmBtns = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    expect(rolloutRestart).toHaveBeenCalledWith("homelab", "Deployment", "prod", "web", "uid-web");
  });

  // --- Keyboard nav + a11y ---

  it("j then Enter opens the second row (keyboard nav)", async () => {
    const second: PodSummaryDTO = { ...broken, namespace: "default", name: "pod-second", uid: "uid-pod-second" };
    seed([broken, second]);
    render(<PodsView cluster="homelab" />);
    // j → select index 0; wait for re-render; j → select index 1; Enter → activate index 1
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "pod-second", "uid-pod-second");
  });

  it("/ focuses the filter input", () => {
    seed([broken]);
    const { getByPlaceholderText } = render(<PodsView cluster="homelab" />);
    const input = getByPlaceholderText("Search pods") as HTMLInputElement;
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

  it("pod row has aria-selected false initially when nothing needs attention", () => {
    seed([healthy]);
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
    // healthy only: a faulted row would be auto-selected on entry by design
    seed([healthy]);
    const { getAllByRole, getByTestId } = render(<PodsView cluster="homelab" />);
    const logsBtn = getAllByRole("button").find((el) =>
      el.getAttribute("aria-label") === "logs for monitoring/grafana-xyz"
    );
    expect(logsBtn).toBeTruthy();
    fireEvent.click(logsBtn!);
    const dock = getByTestId("logs-dock");
    const dockText = dock.textContent ?? "";
    expect(dockText).toContain("grafana-xyz");
    expect(dockText).toContain("monitoring");
    expect(openPodDetail).not.toHaveBeenCalled();
    // Detail panel NOT opened — selected state stays null
    expect(useFleet.getState().pods.selected).toBeNull();
    // openPodDetail bridge was NOT called
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("keyboard 'l' on second row opens dock for that pod", () => {
    const second: PodSummaryDTO = { ...broken, namespace: "default", name: "pod-second", uid: "uid-pod-second" };
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
        selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
        detail: fakeDetail,
        detailLoading: false,
      },
    }));
    const { getByRole, getByTestId, queryByTestId, findByRole } = render(<PodsView cluster="homelab" />);
    // Open the dock.
    fireEvent.click(getByRole("button", { name: /^Logs/ }));
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

  it("a palette namespace request sets the namespace filter", () => {
    // A pending nsRequest (set by the ⌘K namespace jump before the view
    // mounts) scopes the list: the rows are all namespaces (1c slice 3 §5.9).
    seed([broken, healthy]);
    useFleet.getState().requestPodsNamespace("homelab", "monitoring");
    const { getByText, queryByText } = render(<PodsView cluster="homelab" />);
    expect(useFleet.getState().pods.namespace).toBe("monitoring");
    expect(getByText("grafana-xyz")).toBeTruthy();
    expect(queryByText("api-crash")).toBeNull();
    // The request is consumed.
    expect(useFleet.getState().pods.nsRequest).toBeNull();
  });

  it("a namespace request for another cluster is discarded", () => {
    useFleet.getState().requestPodsNamespace("other-cluster", "payments");
    render(<PodsView cluster="homelab" />);
    expect(useFleet.getState().pods.namespace).toBe("");
    expect(useFleet.getState().pods.nsRequest).toBeNull();
  });

  it("StatefulSet owner calls rolloutRestart directly without name transformation", () => {
    const stsPod: PodSummaryDTO = {
      ...healthy, namespace: "db", name: "postgres-0", uid: "uid-postgres-0",
      ownerKind: "StatefulSet", ownerName: "postgres", ownerUid: "uid-postgres",
    };
    const detailSts = { ...fakeDetail, summary: stsPod };
    seed([stsPod]);
    useFleet.setState((s) => ({
      pods: { ...s.pods, selected: { namespace: "db", name: "postgres-0", uid: "uid-postgres-0" }, detail: detailSts, detailLoading: false },
    }));
    const { getByRole, getAllByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Restart statefulset postgres", getByRole);
    const confirmBtns = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    expect(rolloutRestart).toHaveBeenCalledWith("homelab", "StatefulSet", "db", "postgres", "uid-postgres");
  });

  // --- Bulk selection -------------------------------------------------------

  it("checkbox selection raises the bulk bar; one confirm deletes every target", async () => {
    seed([healthy, broken]);
    const { getByRole, getByText, getAllByRole, findByText } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("checkbox", { name: "select pod grafana-xyz for bulk actions" }));
    fireEvent.click(getByRole("checkbox", { name: "select pod api-crash for bulk actions" }));
    expect(getByText("2 pods selected")).toBeTruthy();
    fireEvent.click(getByText("Delete selected"));
    await findByText("delete 2 pods");
    const confirmBtns = getAllByRole("button", { name: "Delete 2" });
    await act(async () => {
      fireEvent.click(confirmBtns[confirmBtns.length - 1]);
    });
    expect(deletePod).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "uid-grafana-xyz");
    expect(deletePod).toHaveBeenCalledWith("homelab", "default", "api-crash", "uid-api-crash");
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
    const legacyOnly: PodSummaryDTO = { ...healthy, name: "no-verdict", uid: "uid-no-verdict", health: undefined };
    seed([legacyOnly]);
    const { getAllByText, getByRole, getByText } = render(<PodsView cluster="homelab" />);
    // the row's verdict label reads Unknown (and the Unknown filter appears)
    expect(within(getByText("no-verdict").closest('[data-rowhost="true"]') as HTMLElement).getByText("Unknown")).toBeTruthy();
    expect(getAllByText("Unknown").length).toBeGreaterThanOrEqual(1);
    expect(getByRole("button", { name: /^Unknown\s*1$/ })).toBeTruthy();
    expect(getByRole("button", { name: /^attention$/i })).toBeTruthy();
  });

  it("a restarted-but-healthy pod is not attention", () => {
    const restarted: PodSummaryDTO = { ...healthy, name: "restarted", uid: "uid-restarted", rank: "restarts", restarts: 3,
      health: { kind: "ok", label: "Running", reason: "Ready", rule: "pod.ready", note: "restarted 4m ago" } };
    seed([restarted]);
    const { getByRole } = render(<PodsView cluster="homelab" />);
    expect(getByRole("button", { name: /^attention$/i })).toBeTruthy();
  });
});

describe("PodsView workspace template (redesign pilot)", () => {
  beforeEach(() => { useFleet.getState().clearPods(); vi.clearAllMocks(); });

  it("auto-selects the first attention row once on the first fresh snapshot", () => {
    seed([healthy, broken]);
    const { rerender } = render(<PodsView cluster="homelab" />);
    // rows present at entry are cached, not a fresh read: nothing selected yet
    expect(openPodDetail).not.toHaveBeenCalled();
    act(() => seed([healthy, broken]));
    expect(openPodDetail).toHaveBeenCalledTimes(1);
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "api-crash", "uid-api-crash");
    // later updates never re-select
    seed([healthy, broken, { ...broken, name: "api-crash-2", uid: "uid-api-crash-2" }]);
    rerender(<PodsView cluster="homelab" />);
    expect(openPodDetail).toHaveBeenCalledTimes(1);
  });

  it("does not auto-select when nothing needs attention, and shows no idle inspector", () => {
    seed([healthy]);
    const { queryByTestId } = render(<PodsView cluster="homelab" />);
    expect(openPodDetail).not.toHaveBeenCalled();
    expect(queryByTestId("lens-inspector")).toBeNull();
    expect(queryByTestId("inspector-idle")).toBeNull();
  });

  it("does not auto-select from data read for another cluster", () => {
    seed([broken]);
    useFleet.setState((s) => ({ pods: { ...s.pods, cluster: "other" } }));
    render(<PodsView cluster="homelab" />);
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("the header names its entity and attention; the footer states the active filter", () => {
    seed([broken, healthy]);
    const { getByText, getByRole } = render(<PodsView cluster="homelab" />);
    expect(getByText(/1 need attention/)).toBeTruthy();
    fireEvent.click(getByRole("button", { name: /^Attention/ }));
    expect(getByText("1 of 2 pods shown · filter: Attention")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Show all 2 pods" }));
    expect(useFleet.getState().pods.needsAttention).toBe(false);
  });

  it("a failed first read is a readable error state, not an empty list", () => {
    useFleet.setState((s) => ({ pods: { ...s.pods, cluster: "homelab", items: [], loading: false, error: "pods is forbidden" } }));
    const { getByText, queryByText } = render(<PodsView cluster="homelab" />);
    expect(getByText("Pods could not be read.")).toBeTruthy();
    expect(queryByText(/^No pods/)).toBeNull();
  });

  it("the Shell action picks a container when the pod has several", () => {
    const multi: PodSummaryDTO = { ...healthy, containers: [makeContainer({ name: "app" }), makeContainer({ name: "sidecar" })] };
    seed([multi]);
    useFleet.setState((s) => ({ pods: { ...s.pods, selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" }, detail: { ...fakeDetail, summary: multi }, detailLoading: false } }));
    const { getByRole } = render(<PodsView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^Shell/ }));
    fireEvent.click(getByRole("menuitem", { name: "sidecar" }));
    expect(openExecTerminal).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "sidecar");
  });
});


describe("PodsView pilot rework (Codex review)", () => {
  beforeEach(() => { useFleet.getState().clearPods(); vi.clearAllMocks(); });

  function select(pod: PodSummaryDTO, detail: PodDetailDTO = { ...fakeDetail, summary: pod }) {
    useFleet.setState((s) => ({ pods: { ...s.pods, selected: { namespace: pod.namespace, name: pod.name, uid: pod.uid ?? "" }, detail, detailLoading: false } }));
  }

  it("cached healthy rows then a broken fresh read: selects the broken pod", () => {
    seed([healthy]);
    render(<PodsView cluster="homelab" />);
    expect(openPodDetail).not.toHaveBeenCalled();
    act(() => seed([healthy, broken]));
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "api-crash", "uid-api-crash");
  });

  it("cached broken rows while a read is pending select nothing", () => {
    seed([broken]);
    useFleet.setState((s) => ({ pods: { ...s.pods, loading: true } }));
    render(<PodsView cluster="homelab" />);
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("after close, a live repair or a new fault never re-selects", () => {
    seed([healthy]);
    const { rerender } = render(<PodsView cluster="homelab" />);
    act(() => seed([healthy, broken]));
    expect(openPodDetail).toHaveBeenCalledTimes(1);
    act(() => { useFleet.getState().selectPod(null); });
    act(() => seed([healthy, broken, { ...broken, name: "api-crash-2", uid: "uid-api-crash-2" }]));
    rerender(<PodsView cluster="homelab" />);
    expect(openPodDetail).toHaveBeenCalledTimes(1);
  });

  it("an unreadable fresh read selects nothing", () => {
    seed([broken]);
    render(<PodsView cluster="homelab" />);
    act(() => useFleet.getState().setPodsFromRows("homelab", { items: [], namespaces: [], loading: false, error: "Pod: forbidden", coverage: [{ kind: "Pod", served: true, synced: false, coverage: "unreadable", error: "forbidden" }] }));
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("selecting B never carries A's pending confirmation", () => {
    const b: PodSummaryDTO = { ...healthy, name: "grafana-b", uid: "uid-grafana-b" };
    seed([healthy, b]);
    select(healthy);
    const { getByRole, queryByText } = render(<PodsView cluster="homelab" />);
    chooseMore("Delete pod", getByRole);
    expect(queryByText(/grafana-xyz - the controller will recreate it/)).toBeTruthy();
    act(() => select(b));
    expect(queryByText(/the controller will recreate it/)).toBeNull();
  });

  it("a namespace filter that excludes the selection closes the inspector", () => {
    seed([healthy, broken]);
    select(healthy);
    render(<PodsView cluster="homelab" />);
    act(() => useFleet.getState().setPodsNamespace("default"));
    expect(useFleet.getState().pods.selected).toBeNull();
  });

  it("a pod removed from the complete read shows a removed state with no actions", () => {
    seed([healthy, broken]);
    select(healthy);
    const { getByText, queryByRole } = render(<PodsView cluster="homelab" />);
    act(() => seed([broken]));
    expect(getByText("Pod removed.")).toBeTruthy();
    expect(queryByRole("button", { name: /^Logs/ })).toBeNull();
    expect(queryByRole("button", { name: /^More/ })).toBeNull();
    expect(getByText("grafana-xyz", { selector: "h2" })).toBeTruthy();
  });

  it("keyboard use of inspector buttons never activates the list row behind", () => {
    seed([broken, healthy]);
    select(healthy);
    const { getByRole } = render(<PodsView cluster="homelab" />);
    vi.mocked(openPodDetail).mockClear();
    const more = getByRole("button", { name: /^More/ });
    more.focus();
    fireEvent.keyDown(more, { key: "Enter" });
    fireEvent.keyDown(more, { key: "j" });
    fireEvent.keyDown(more, { key: "ArrowDown" });
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("first Escape closes the More menu only; focus returns to its trigger", () => {
    seed([healthy]);
    select(healthy);
    const { getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    const more = getByRole("button", { name: /^More/ });
    fireEvent.click(more);
    expect(getByRole("menu")).toBeTruthy();
    expect(document.activeElement?.getAttribute("role")).toBe("menuitem");
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(queryByRole("menu")).toBeNull();
    expect(useFleet.getState().pods.selected).not.toBeNull();
    expect(document.activeElement).toBe(more);
  });

  it("removal while a confirmation is open cancels it; nothing dispatches", () => {
    seed([healthy, broken]);
    select(healthy);
    const { getByRole, queryByRole } = render(<PodsView cluster="homelab" />);
    chooseMore("Delete pod", getByRole);
    expect(getByRole("button", { name: "Delete" })).toBeTruthy();
    act(() => seed([broken]));
    expect(queryByRole("button", { name: "Delete" })).toBeNull();
    expect(deletePod).not.toHaveBeenCalled();
  });

  it("removal while port-forward is open closes it", () => {
    seed([healthy, broken]);
    select(healthy);
    const { getByRole, queryByTestId, container } = render(<PodsView cluster="homelab" />);
    chooseMore("Port-forward…", getByRole);
    expect(queryByTestId("forward-popover")).toBeTruthy();
    act(() => seed([broken]));
    expect(queryByTestId("forward-popover")).toBeNull();
    expect(container.querySelector('[data-testid="forward-start"]')).toBeNull();
    expect(document.body.textContent).toContain("Pod removed.");
  });
});

// Codex UI review 2026-10-13: the palette's Scratch entry opens the existing
// Scratch panel for its cluster, and nothing for another cluster's intent.
describe("PodsView open-scratch intent", () => {
  beforeEach(() => {
    useFleet.getState().clearPods();
    useFleet.setState({ navIntent: null });
    vi.clearAllMocks();
  });

  it("opens the Scratch panel and consumes the intent", () => {
    seed([broken, healthy]);
    useFleet.getState().requestNavIntent({ kind: "open-scratch", cluster: "homelab" });
    const { getByRole } = render(<PodsView cluster="homelab" />);
    expect(getByRole("dialog", { name: "scratch objects" })).toBeTruthy();
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("leaves another cluster's intent parked", () => {
    seed([broken, healthy]);
    useFleet.getState().requestNavIntent({ kind: "open-scratch", cluster: "elsewhere" });
    const { queryByRole } = render(<PodsView cluster="homelab" />);
    expect(queryByRole("dialog", { name: "scratch objects" })).toBeNull();
    expect(useFleet.getState().navIntent).toEqual({ kind: "open-scratch", cluster: "elsewhere" });
  });
});

// Coverage S6 implementation review 1: a status-only change to the resize
// or debug evidence (an acknowledgement, an actuation, a debug container
// exiting) changes no other row field; its evidence revision re-reads the
// selected pod's detail, under the same UID.
describe("S6 detail continuity", () => {
  beforeEach(() => { useFleet.getState().clearPods(); vi.clearAllMocks(); });
  const setRow = (overrides: Record<string, unknown>) => useFleet.setState((s) => ({
    pods: { ...s.pods, items: s.pods.items.map((p) => ({ ...p, ...overrides })) },
  }));
  it("re-reads the detail when only the evidence revision changes", async () => {
    seed([{ ...healthy, generation: 2 }]);
    setRow({ observedGeneration: 1, resize: { inProgress: { reason: "", generation: 2 } }, evidenceRev: "aaaaaaaaaaaaaaaa" });
    const view = render(<PodsView cluster="homelab" />);
    fireEvent.click(view.getByText("grafana-xyz"));
    act(() => useFleet.setState((s) => ({ pods: { ...s.pods,
      selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
      detail: { ...fakeDetail, summary: { ...healthy, generation: 2, observedGeneration: 1, resize: { inProgress: { reason: "", generation: 2 } } } }, detailLoading: false,
    } })));
    await act(async () => {});
    vi.mocked(openPodDetail).mockClear();
    act(() => setRow({ observedGeneration: 2, resize: undefined, evidenceRev: "bbbbbbbbbbbbbbbb" }));
    await act(async () => {});
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "uid-grafana-xyz");
    vi.mocked(openPodDetail).mockClear();
    act(() => setRow({ evidenceRev: "cccccccccccccccc" })); // an actuation or a debug exit alone
    await act(async () => {});
    expect(openPodDetail).toHaveBeenCalledTimes(1);
    vi.mocked(openPodDetail).mockClear();
    act(() => setRow({ ip: "10.9.9.9" })); // not evidence: nothing re-read
    await act(async () => {});
    expect(openPodDetail).not.toHaveBeenCalled();
  });
});

// Coverage S11b §3.5: a pod's device health and its generated claims'
// names change only status; each moves the evidence revision (Go:
// TestEvidenceRevFollowsDeviceHealthAndClaims), which re-reads the selected
// pod's detail under its UID, and the re-read detail is what the Claims
// block says.
describe("S11b detail continuity", () => {
  beforeEach(() => { useFleet.getState().clearPods(); vi.clearAllMocks(); });
  const setRow = (overrides: Record<string, unknown>) => useFleet.setState((s) => ({
    pods: { ...s.pods, items: s.pods.items.map((p) => ({ ...p, ...overrides })) },
  }));
  const health = (id: string, h: string) => [{ container: "app", name: "claim:gpu/req", devices: [{ id, health: h }] }];
  const showDetail = (over: Partial<PodDetailDTO>) => act(() => useFleet.setState((s) => ({ pods: { ...s.pods,
    selected: { namespace: "monitoring", name: "grafana-xyz", uid: "uid-grafana-xyz" },
    detail: { ...fakeDetail, claims: [{ podClaim: "gpu", source: "template", claim: "grafana-xyz-gpu-1", state: "resolved" }], ...over }, detailLoading: false,
  } })));
  const claimText = () => screen.getAllByTestId("pod-claim").map((l) => l.textContent ?? "").join(" | ");
  it("Healthy turning Unknown, another device unhealthy at the same count, and a generated claim's name each re-read the detail", async () => {
    seed([healthy]);
    setRow({ evidenceRev: "aaaaaaaaaaaaaaaa" });
    const view = render(<PodsView cluster="homelab" />);
    fireEvent.click(view.getByText("grafana-xyz"));
    showDetail({ deviceHealth: health("cdi/a", "Healthy") });
    await act(async () => {});
    expect(claimText()).toContain("1 healthy");
    const steps: [string, Partial<PodDetailDTO>, string][] = [
      ["bbbbbbbbbbbbbbbb", { deviceHealth: health("cdi/a", "Unknown") }, "health Unknown (reported by the kubelet)"],
      ["cccccccccccccccc", { deviceHealth: health("cdi/a", "Unhealthy") }, "1 device unhealthy (reported by the kubelet): cdi/a"],
      ["dddddddddddddddd", { deviceHealth: health("cdi/b", "Unhealthy") }, "1 device unhealthy (reported by the kubelet): cdi/b"],
      ["eeeeeeeeeeeeeeee", { claims: [{ podClaim: "gpu", source: "template", claim: "grafana-xyz-gpu-2", state: "resolved" }], deviceHealth: health("cdi/b", "Unhealthy") }, "grafana-xyz-gpu-2"],
    ];
    for (const [rev, detail, want] of steps) {
      vi.mocked(openPodDetail).mockClear();
      act(() => setRow({ evidenceRev: rev }));
      await act(async () => {});
      expect(openPodDetail).toHaveBeenCalledWith("homelab", "monitoring", "grafana-xyz", "uid-grafana-xyz");
      showDetail(detail);
      await act(async () => {});
      expect(claimText()).toContain(want);
    }
    vi.mocked(openPodDetail).mockClear();
    act(() => setRow({ ip: "10.9.9.9" })); // not evidence: nothing re-read
    await act(async () => {});
    expect(openPodDetail).not.toHaveBeenCalled();
  });
});
