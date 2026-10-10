import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";
import { WorkloadsView } from "./WorkloadsView";
import { useFleet } from "../store/fleet";
import type { WorkloadDTO, ClusterDTO } from "../store/fleet";

vi.mock("../bridge/workloads", () => ({
  listWorkloads: vi.fn().mockResolvedValue(undefined),
  openLiveWorkloads: vi.fn().mockReturnValue(() => {}),
  rolloutRestart: vi.fn().mockResolvedValue(undefined),
  rolloutHistory: vi.fn(async () => ({ revisions: [] })),
  rolloutUndo: vi.fn().mockResolvedValue(undefined),
  rolloutPause: vi.fn().mockResolvedValue(undefined),
  scaleWorkload: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("../bridge/workload-metrics", () => ({ getWorkloadMetrics: vi.fn().mockResolvedValue(undefined) }));
vi.mock("../bridge/windows", () => ({ openWorkloadLogsWindow: vi.fn().mockResolvedValue(true) }));
vi.mock("../bridge/pods", () => ({
  deletePod: vi.fn().mockResolvedValue(undefined),
  openPodDetail: vi.fn().mockResolvedValue(undefined),
}));
import type { SparklinesDTO } from "../bridge/metrics";
const mockGetWorkloadSparklines = vi.fn<(c: string, ns: string, kind: string, name: string) => Promise<SparklinesDTO>>(() =>
  Promise.resolve({ available: true, cpu: [{ t: 0, v: 0.1 }, { t: 60, v: 0.2 }], mem: [{ t: 0, v: 100 }, { t: 60, v: 120 }] }),
);
vi.mock("../bridge/metrics", () => ({
  getWorkloadSparklines: (c: string, ns: string, kind: string, name: string) => mockGetWorkloadSparklines(c, ns, kind, name),
  getClusterSparklines: vi.fn(),
  getClusterMetrics: vi.fn(),
}));
// Stub LogsPane — its real implementation drags in the Wails runtime and the
// stream lifecycle, both covered by LogsPane.test.tsx. Here we only assert the
// dock plumbing: what target it gets and in which mode.
vi.mock("./LogsPane", () => ({
  LogsPane: ({ pod, workload }: { pod: { namespace: string; name: string }; workload?: { kind: string; name: string } }) => (
    <div data-testid="logs-pane-stub">{workload ? `${workload.kind}:${pod.namespace}/${workload.name}` : `pod:${pod.namespace}/${pod.name}`}</div>
  ),
}));
import { rolloutRestart, rolloutHistory, rolloutUndo, rolloutPause, scaleWorkload, openLiveWorkloads } from "../bridge/workloads";
import { openWorkloadLogsWindow } from "../bridge/windows";

const noResources = { cpu: { usage: null, request: null, limit: null }, mem: { usage: null, request: null, limit: null } };
const broken: WorkloadDTO = { kind: "Deployment", namespace: "ollama-prod", name: "ollama", desired: 1, ready: 0, available: 0, updated: 1, restarts: 7, reason: "CrashLoopBackOff", rank: "unhealthy", gitops: { kind: "Kustomization", namespace: "flux-system", name: "ollama" }, pods: [{ name: "ollama-x", ready: false, restarts: 7, reason: "CrashLoopBackOff", node: "node-3", ageSeconds: 720 }], resources: noResources, health: { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", rule: "workload.pod-failure", rank: 70 } };
const healthy: WorkloadDTO = { kind: "Deployment", namespace: "monitoring", name: "grafana", desired: 1, ready: 1, available: 1, updated: 1, restarts: 0, reason: "Available", rank: "healthy", gitops: null, pods: [], resources: noResources, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" } };

function seed(items: WorkloadDTO[]) {
  useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: "homelab-nelli", items, namespaces: ["monitoring", "ollama-prod"], loading: false } }));
}

describe("WorkloadsView", () => {
  beforeEach(() => useFleet.getState().clearWorkloads());

  it("renders triage rows with reason, restarts, and gitops owner", () => {
    seed([broken, healthy]);
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText(/CrashLoopBackOff/)).toBeTruthy();
    expect(getByText("flux ks/ollama")).toBeTruthy();
    expect(getByText("0 / 1")).toBeTruthy();
  });

  it("expands a row to show its pods", () => {
    seed([broken]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText("ollama-x")).toBeNull();
    fireEvent.click(getByText("ollama"));
    expect(getByText("ollama-x")).toBeTruthy();
    expect(getByText("node-3")).toBeTruthy();
  });

  it("rollout history lists revisions; undo opens the danger confirm with the GitOps warning", async () => {
    vi.mocked(rolloutHistory).mockResolvedValue({
      revisions: [
        { revision: 3, ageSeconds: 600, images: ["shop/cart:v3"], current: true },
        { revision: 2, ageSeconds: 7200, images: ["shop/cart:v2"], current: false, changeCause: "hotfix" },
      ],
    });
    seed([broken]);
    const { getByText, findByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama"));
    expect(await findByText("#3")).toBeTruthy();
    expect(getByText("current")).toBeTruthy();
    // The current revision gets no undo; revision 2 does.
    const undoButtons = getByText("undo →");
    fireEvent.click(undoButtons);
    // Danger confirm names the target AND the GitOps-owned consequence.
    expect(await findByText(/revision 2/)).toBeTruthy();
    expect(getByText(/Flux will revert this on its next reconcile/)).toBeTruthy();
    expect(getByText("Roll back")).toBeTruthy();
    expect(vi.mocked(rolloutUndo)).not.toHaveBeenCalled();
    fireEvent.click(getByText("Roll back"));
    expect(vi.mocked(rolloutUndo)).toHaveBeenCalledWith("homelab-nelli", "Deployment", "ollama-prod", "ollama", 2);
    expect(queryByText("Roll back")).toBeNull();
  });

  it("a failed history read renders its error, never a silently missing section", async () => {
    vi.mocked(rolloutHistory).mockResolvedValue({ revisions: [], error: "list replicasets in shop: forbidden" });
    seed([broken]);
    const { getByText, findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama"));
    expect(await findByText(/list replicasets in shop: forbidden/)).toBeTruthy();
  });

  it("pause/resume: button flips with paused state and the confirm fires the verb", async () => {
    seed([{ ...healthy, paused: true }]);
    const { getByText, findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("grafana"));
    // Paused state: the resume affordance + the status fact.
    const resume = await findByText("resume rollouts");
    expect(getByText("paused")).toBeTruthy();
    fireEvent.click(resume);
    expect(await findByText("Resume")).toBeTruthy();
    fireEvent.click(getByText("Resume"));
    expect(vi.mocked(rolloutPause)).toHaveBeenCalledWith("homelab-nelli", "monitoring", "grafana", false);
  });

  it("pause is Deployment-only: no button on other kinds", async () => {
    seed([{ ...broken, kind: "DaemonSet" }]);
    const { getByText, queryByText, findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama"));
    await findByTestId("rollout-section");
    expect(queryByText("pause rollouts")).toBeNull();
    expect(queryByText("resume rollouts")).toBeNull();
  });

  it("needs-attention filter hides healthy rows", () => {
    seed([broken, healthy]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText(/needs attention/i));
    expect(getByText("ollama")).toBeTruthy();
    expect(queryByText("grafana")).toBeNull();
  });

  it("filter input narrows workload rows", () => {
    seed([broken, healthy]);
    const { getByPlaceholderText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.change(getByPlaceholderText("filter workloads"), { target: { value: "grafana" } });
    expect(queryByText("grafana")).toBeTruthy();
    expect(queryByText("ollama")).toBeNull();
  });

  it("hides cpu/mem columns and near-limit control when metrics unavailable", () => {
    seed([broken, healthy]);
    // metricsAvailable stays false (default store state)
    const { queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText("near limit")).toBeNull();
    expect(queryByText("cpu")).toBeNull();
    expect(queryByText("mem")).toBeNull();
  });

  it("shows cpu/mem columns and near-limit control when metrics available", () => {
    seed([broken, healthy]);
    // Call setWorkloadUsage with an available result so metricsAvailable becomes true
    useFleet.getState().setWorkloadUsage("homelab-nelli", "", {
      status: { available: true, message: "", updatedAt: "2026-06-09T00:00:00Z" },
      usage: {},
    });
    const { getByText, getAllByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText("near limit")).toBeTruthy();
    expect(getAllByText("cpu").length).toBeGreaterThan(0);
    expect(getAllByText("mem").length).toBeGreaterThan(0);
  });

  it("restart button is visible after expanding a Deployment row", () => {
    seed([broken]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText("restart")).toBeNull();
    fireEvent.click(getByText("ollama")); // expand the row
    expect(getByText("restart")).toBeTruthy();
  });

  it("confirming restart dispatches rolloutRestart for the correct workload", () => {
    seed([broken]);
    const { getByText, getAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // expand
    fireEvent.click(getByText("restart")); // open confirm dialog
    const confirmBtns = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]); // confirm
    expect(rolloutRestart).toHaveBeenCalledWith("homelab-nelli", "Deployment", "ollama-prod", "ollama");
  });

  // --- Scale button ---

  it("scale button is visible after expanding a Deployment row", () => {
    seed([broken]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText("scale")).toBeNull();
    fireEvent.click(getByText("ollama")); // expand
    expect(getByText("scale")).toBeTruthy();
  });

  it("scale button is hidden for DaemonSets", () => {
    const daemonSet: WorkloadDTO = {
      kind: "DaemonSet", namespace: "kube-system", name: "cilium",
      desired: 3, ready: 3, available: 3, updated: 3, restarts: 0,
      reason: "Available", rank: "healthy", gitops: null, pods: [], resources: noResources, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" },
    };
    seed([daemonSet]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("cilium")); // expand
    expect(queryByText("scale")).toBeNull();
    expect(getByText("restart")).toBeTruthy(); // restart still shown
  });

  it("scale button is visible for StatefulSets", () => {
    const sts: WorkloadDTO = {
      kind: "StatefulSet", namespace: "db", name: "postgres",
      desired: 1, ready: 1, available: 1, updated: 1, restarts: 0,
      reason: "Available", rank: "healthy", gitops: null, pods: [], resources: noResources, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" },
    };
    seed([sts]);
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("postgres")); // expand
    expect(getByText("scale")).toBeTruthy();
  });

  it("clicking scale shows the replica input popover prefilled with desired count", () => {
    seed([broken]); // broken.desired = 1
    const { getByText, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // expand
    fireEvent.click(getByText("scale")); // open popover
    const input = getByRole("spinbutton", { name: /replica count/i });
    expect((input as HTMLInputElement).value).toBe("1");
  });

  it("confirming scale dispatches scaleWorkload with the entered replica count", () => {
    seed([broken]);
    const { getByText, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // expand
    fireEvent.click(getByText("scale")); // open popover
    const input = getByRole("spinbutton", { name: /replica count/i });
    fireEvent.change(input, { target: { value: "3" } });
    fireEvent.click(getByText("apply")); // confirm in popover
    expect(scaleWorkload).toHaveBeenCalledWith("homelab-nelli", "Deployment", "ollama-prod", "ollama", 3);
  });

  it("shows a loud truncation banner only when the list was capped", () => {
    seed([healthy]);
    const { queryByText, rerender } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText(/broken-first triage may miss a fault/)).toBeNull();
    act(() => { useFleet.setState((s) => ({ workloads: { ...s.workloads, truncated: true } })); });
    rerender(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText(/broken-first triage may miss a fault/)).toBeTruthy();
  });

  // --- Reloader forward "reloads on" view ---

  it("shows the reloads-on well with an auto chip and the reload-target chips", () => {
    const w: WorkloadDTO = {
      ...healthy, name: "api", namespace: "app",
      reloader: { auto: true, configMaps: ["app-config"], secrets: ["app-secret"] },
    };
    seed([w]);
    const { getByText, getByTitle } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("api")); // select -> panel
    expect(getByText("reloads on")).toBeTruthy();
    expect(getByText("auto")).toBeTruthy();
    // Chips are cross-links into Resources (title carries the target identity).
    expect(getByTitle("open ConfigMap app/app-config in Resources")).toBeTruthy();
    expect(getByTitle("open Secret app/app-secret in Resources")).toBeTruthy();
  });

  it("warns when reload annotations exist but the Reloader operator is not detected", () => {
    const w: WorkloadDTO = { ...healthy, name: "api", namespace: "app", reloader: { auto: true, configMaps: [], secrets: [] } };
    seed([w]); // no cluster entry -> reloaderPresent defaults false
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("api"));
    expect(getByText(/annotations may be inert/)).toBeTruthy();
  });

  it("does not warn when the Reloader operator is present AND healthy", () => {
    const c: ClusterDTO = { name: "homelab-nelli", state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.30", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 1, reloaderPresent: true, reloaderHealthy: true };
    useFleet.setState({ clusters: [c] });
    const w: WorkloadDTO = { ...healthy, name: "api", namespace: "app", reloader: { auto: false, configMaps: ["only-this"], secrets: [] } };
    seed([w]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("api"));
    expect(getByText("reloads on")).toBeTruthy();
    expect(queryByText(/annotations may be inert/)).toBeNull();
    expect(queryByText(/reloads may not fire/)).toBeNull();
  });

  it("warns 'present but not ready' when the operator is detected but Degraded", () => {
    // present but unhealthy (crashlooping controller) -> annotations are still inert.
    const c: ClusterDTO = { name: "homelab-nelli", state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.30", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 1, reloaderPresent: true, reloaderHealthy: false };
    useFleet.setState({ clusters: [c] });
    const w: WorkloadDTO = { ...healthy, name: "api", namespace: "app", reloader: { auto: true, configMaps: ["cfg"], secrets: [] } };
    seed([w]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("api"));
    expect(getByText(/reloads may not fire/)).toBeTruthy();
    expect(queryByText(/annotations may be inert/)).toBeNull();
  });

  it("renders no reloads-on well for a workload without Reloader annotations", () => {
    seed([{ ...healthy, name: "plain", namespace: "app" }]); // reloader undefined
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("plain"));
    expect(queryByText("reloads on")).toBeNull();
  });

  // --- Keyboard nav + a11y ---

  it("j then Enter expands the second row", () => {
    const second: WorkloadDTO = {
      kind: "Deployment", namespace: "default", name: "second-workload",
      desired: 1, ready: 1, available: 1, updated: 1, restarts: 0,
      reason: "Available", rank: "healthy", gitops: null, pods: [], resources: noResources, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" },
    };
    seed([broken, second]);
    const { queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText("second-workload")).toBeTruthy();
    // j → index 0; wait for re-render; j → index 1; Enter → expand second row
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    // The "restart" button appears in the expanded section
    expect(queryByText("restart")).toBeTruthy();
  });

  it("/ focuses the filter input", () => {
    seed([broken]);
    const { getByPlaceholderText } = render(<WorkloadsView cluster="homelab-nelli" />);
    const input = getByPlaceholderText("filter workloads") as HTMLInputElement;
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }));
    expect(input).toBeTruthy();
  });

  it("workload row has role=button and aria-expanded", () => {
    seed([broken]);
    const { getAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    const rows = getAllByRole("button").filter((el) => el.hasAttribute("aria-expanded"));
    expect(rows.length).toBeGreaterThan(0);
    expect(rows[0].getAttribute("aria-expanded")).toBe("false");
  });

  it("workload row aria-expanded toggles on click", () => {
    seed([broken]);
    const { getAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    const rows = getAllByRole("button").filter((el) => el.hasAttribute("aria-expanded"));
    const row = rows[0];
    expect(row.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(row);
    expect(row.getAttribute("aria-expanded")).toBe("true");
  });

  it("cancelling scale popover hides the input", () => {
    seed([broken]);
    const { getByText, getByRole, queryByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // expand
    fireEvent.click(getByText("scale")); // open popover
    fireEvent.click(getByRole("button", { name: "cancel scale" })); // cancel
    expect(queryByRole("spinbutton", { name: /replica count/i })).toBeNull();
    expect(getByText("scale")).toBeTruthy(); // button back
  });

  it("openLiveWorkloads is called on mount (replaces listWorkloads)", () => {
    render(<WorkloadsView cluster="homelab-nelli" />);
    expect(openLiveWorkloads).toHaveBeenCalledWith("homelab-nelli", "");
  });

  it("live=true renders green live indicator", () => {
    seed([broken]);
    useFleet.setState((s) => ({ workloads: { ...s.workloads, live: true } }));
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText("live")).toBeTruthy();
  });

  it("live=false renders manual fallback indicator", () => {
    seed([broken]);
    useFleet.setState((s) => ({ workloads: { ...s.workloads, live: false } }));
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText("○ manual")).toBeTruthy();
  });

  // --- Aggregate logs dock ---

  it("row terminal icon opens the aggregate-logs dock in workload mode", () => {
    seed([broken]);
    const { getByRole, getByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /aggregate logs for ollama-prod\/ollama/i }));
    expect(getByTestId("workload-logs-dock")).toBeTruthy();
    expect(getByTestId("logs-pane-stub").textContent).toBe("Deployment:ollama-prod/ollama");
  });

  it("clicking the logs icon does not expand the row", () => {
    seed([broken]);
    const { getByRole, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /aggregate logs for ollama-prod\/ollama/i }));
    expect(queryByText("ollama-x")).toBeNull(); // expanded pod table absent
  });

  it("l key opens the dock for the keyboard-selected row", () => {
    seed([broken, healthy]);
    const { getByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "l", bubbles: true, cancelable: true })); });
    expect(getByTestId("logs-pane-stub").textContent).toBe("Deployment:ollama-prod/ollama");
  });

  it("✕ closes the dock", () => {
    seed([broken]);
    const { getByRole, queryByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /aggregate logs for ollama-prod\/ollama/i }));
    expect(queryByTestId("workload-logs-dock")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: /close logs dock/i }));
    expect(queryByTestId("workload-logs-dock")).toBeNull();
  });

  // --- Sparklines ---

  it("expanding a row with metrics shows 30m cpu/mem sparklines", async () => {
    seed([broken]);
    useFleet.getState().setWorkloadUsage("homelab-nelli", "", {
      status: { available: true, message: "", updatedAt: "2026-06-10T00:00:00Z" },
      usage: {},
    });
    const { getByText, findByText, findAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // expand
    await findByText("cpu 30m");
    expect(mockGetWorkloadSparklines).toHaveBeenCalledWith("homelab-nelli", "ollama-prod", "Deployment", "ollama");
    const sparks = await findAllByRole("img", { name: /metric sparkline/i });
    expect(sparks.length).toBe(2);
  });

  it("sparkline row shows the reason when unavailable", async () => {
    mockGetWorkloadSparklines.mockResolvedValueOnce({ available: false, message: "metrics unavailable: no source", cpu: [], mem: [] });
    seed([broken]);
    useFleet.getState().setWorkloadUsage("homelab-nelli", "", {
      status: { available: true, message: "", updatedAt: "2026-06-10T00:00:00Z" },
      usage: {},
    });
    const { getByText, findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama"));
    expect(await findByText(/sparklines unavailable: metrics unavailable: no source/)).toBeTruthy();
  });

  // --- Live-update stability (selection + pending action by stable key) ---

  it("selection follows the workload when a live emit re-orders the rows", () => {
    seed([broken, healthy]);
    const { getByText, getAllByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    // Select grafana (second row) via click.
    fireEvent.click(getByText("grafana"));
    // Context panel shows grafana (name appears in row + panel).
    expect(getAllByText("grafana").length).toBeGreaterThan(1);
    // Live emit re-ranks: grafana now first, ollama second.
    act(() => { seed([healthy, broken]); });
    // The panel must still show grafana, not silently retarget to index 1 (ollama).
    expect(getAllByText("grafana").length).toBeGreaterThan(1);
    expect(getAllByText("ollama").length).toBe(1); // row only, not the panel
  });

  it("selection clears when the selected workload leaves the visible set", () => {
    seed([broken, healthy]);
    const { getByText, getByPlaceholderText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("grafana")); // select
    fireEvent.change(getByPlaceholderText("filter workloads"), { target: { value: "ollama" } });
    // grafana filtered out — no selection, so no action buttons for a wrong row.
    expect(queryByText("restart")).toBeNull();
    expect(getByText(/select a workload/i)).toBeTruthy();
  });

  it("open scale popover survives a live emit that replaces the DTOs", () => {
    seed([broken]);
    const { getByText, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // select + expand
    fireEvent.click(getByText("scale")); // open popover
    expect(getByRole("spinbutton", { name: /replica count/i })).toBeTruthy();
    // Live emit replaces the workload DTO (same identity key, new object).
    act(() => { seed([{ ...broken, restarts: 8 }]); });
    // Popover must still be open — the pending match is by key, not object identity.
    expect(getByRole("spinbutton", { name: /replica count/i })).toBeTruthy();
  });

  it("status trail reads shown/total, not shown/shown", () => {
    seed([broken, healthy]);
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText(/needs attention/i)); // hides healthy → 1 of 2
    expect(getByText("1/2")).toBeTruthy();
  });

  it("context-panel pod rows open the pod detail in the Pods section", async () => {
    const { openPodDetail } = await import("../bridge/pods");
    useFleet.getState().openCluster("homelab-nelli");
    seed([broken]);
    const { getByText, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama")); // select -> context panel
    fireEvent.click(getByRole("button", { name: "open pod ollama-x" }));
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "pods" });
    expect(openPodDetail).toHaveBeenCalledWith("homelab-nelli", "ollama-prod", "ollama-x");
  });

  it("context-panel flux owner jumps to the GitOps lens with a select intent", () => {
    useFleet.getState().openCluster("homelab-nelli");
    seed([broken]);
    const { getByText, getByTitle } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByText("ollama"));
    fireEvent.click(getByTitle(/open Kustomization flux-system\/ollama in the Flux lens/));
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "gitops" });
    expect(useFleet.getState().navIntent).toEqual({
      kind: "select-flux", cluster: "homelab-nelli", fluxKind: "Kustomization", namespace: "flux-system", name: "ollama",
    });
  });

  it("pop-out calls openWorkloadLogsWindow and closes the dock on success", async () => {
    seed([broken]);
    const { getByRole, queryByTestId, findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /aggregate logs for ollama-prod\/ollama/i }));
    await findByTestId("workload-logs-dock");
    fireEvent.click(getByRole("button", { name: /open logs in window/i }));
    expect(openWorkloadLogsWindow).toHaveBeenCalledWith("homelab-nelli", "ollama-prod", "Deployment", "ollama", "");
    await act(async () => {}); // flush the resolved promise
    expect(queryByTestId("workload-logs-dock")).toBeNull();
  });
});
