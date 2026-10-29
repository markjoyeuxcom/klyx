import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, screen } from "@testing-library/react";
import { WorkloadsView } from "./WorkloadsView";
import { useFleet } from "../store/fleet";
import type { WorkloadDTO, ClusterDTO } from "../store/fleet";

// The rows adapter is mocked: these tests seed the workloads slice directly
// (WorkloadRows.test.tsx mounts the real adapter over the health store).
vi.mock("../health/useWorkloadRows", () => ({ useWorkloadRows: () => {} }));

vi.mock("../bridge/workloads", () => ({
  rolloutRestart: vi.fn().mockResolvedValue(undefined),
  rolloutHistory: vi.fn(async () => ({ revisions: [] })),
  rolloutUndo: vi.fn().mockResolvedValue(undefined),
  rolloutPause: vi.fn().mockResolvedValue(undefined),
  scaleWorkload: vi.fn().mockResolvedValue(undefined),
  workloadCompare: vi.fn(() => new Promise(() => {})), // never settles: only the dialog's presence matters
}));
vi.mock("../bridge/workload-metrics", () => ({ getWorkloadMetrics: vi.fn().mockResolvedValue(undefined) }));
// coverage S9 §7: who owns the replica count, per test
const owner = vi.hoisted(() => ({ value: { autoscaler: "unowned", gitops: "unowned", owners: [] as string[] } as Record<string, unknown> }));
vi.mock("../bridge/keda", () => ({ replicaOwner: vi.fn(async () => owner.value) }));
// the undo dialog's authoritative ownership read (A3 §9 stage 1)
const ownership = vi.hoisted(() => ({ value: null as unknown }));
vi.mock("../bridge/crd", () => ({ gitOpsOwnership: vi.fn(async () => ownership.value) }));
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
import { rolloutRestart, rolloutHistory, rolloutUndo, rolloutPause, scaleWorkload } from "../bridge/workloads";
import { openWorkloadLogsWindow } from "../bridge/windows";
import { deletePod } from "../bridge/pods";

const noResources = { cpu: { usage: null, request: null, limit: null }, mem: { usage: null, request: null, limit: null } };
const broken: WorkloadDTO = { kind: "Deployment", namespace: "ollama-prod", name: "ollama", uid: "uid-ollama", desired: 1, ready: 0, available: 0, updated: 1, restarts: 7, reason: "CrashLoopBackOff", rank: "unhealthy", gitops: { kind: "Kustomization", namespace: "flux-system", name: "ollama" }, pods: [{ name: "ollama-x", uid: "uid-ollama-x", ready: false, restarts: 7, reason: "CrashLoopBackOff", node: "node-3", ageSeconds: 720 }], resources: noResources, health: { kind: "fault", label: "CrashLoopBackOff", reason: "CrashLoopBackOff", rule: "workload.pod-failure", rank: 70 } };
const healthy: WorkloadDTO = { kind: "Deployment", namespace: "monitoring", name: "grafana", uid: "uid-grafana", desired: 1, ready: 1, available: 1, updated: 1, restarts: 0, reason: "Available", rank: "healthy", gitops: null, pods: [], resources: noResources, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" } };

// Every test starts from the store as the app boots it. Per-describe resets
// cleared only the workloads slice, so clusters, route and NavIntents leaked
// between tests and the file failed under a shuffled order.
const initialStore = useFleet.getState();
beforeEach(() => { useFleet.setState(initialStore, true); });

function seed(items: WorkloadDTO[]) {
  // the pods attribution read is live (the dependency the rows were evaluated with)
  useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: "homelab-nelli", items, namespaces: ["monitoring", "ollama-prod"], loading: false,
    dependencies: [{ kind: "Pod", served: true, synced: true, coverage: "live" }, { kind: "ReplicaSet", served: true, synced: true, coverage: "live" }] } }));
}

// The row is the first match: once selected, the inspector repeats the name.
function select(name: string) { fireEvent.click(screen.getAllByText(name)[0]); }
function chooseMore(label: string | RegExp) {
  fireEvent.click(screen.getByRole("button", { name: /^More/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: label }));
}

describe("WorkloadsView", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); });

  it("saved kinds fill only an unchosen default: an explicit All workloads stays all (step 7b review)", () => {
    localStorage.setItem("klyx-view-prefs", JSON.stringify({ "homelab-nelli": { workloads: { kinds: ["StatefulSet"] } } }));
    useFleet.setState({ route: { name: "cluster", cluster: "homelab-nelli", section: "overview" } });
    useFleet.getState().openWorkloads(["Deployment", "StatefulSet", "DaemonSet"]);
    const { unmount } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(useFleet.getState().workloads.kindFilter).toEqual({ Deployment: true, StatefulSet: true, DaemonSet: true });
    // Leaving the view resets it; entering again without a kind choice (a
    // section link) still restores the saved kinds.
    useFleet.getState().setSection("pods");
    unmount();
    useFleet.getState().clearWorkloads(); // the rows adapter's slice owner (mocked here) clears on the last unmount
    useFleet.getState().setSection("workloads");
    render(<WorkloadsView cluster="homelab-nelli" />);
    expect(useFleet.getState().workloads.kindFilter).toEqual({ Deployment: false, StatefulSet: true, DaemonSet: false });
    expect(useFleet.getState().workloads.kindExplicit).toBe(false);
  });

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
    select("ollama");
    expect(getByText("ollama-x")).toBeTruthy();
    expect(getByText("on node-3")).toBeTruthy();
  });

  it("rollout history lists revisions; undo opens the danger confirm with the authoritative GitOps consequence", async () => {
    ownership.value = {
      ownerKind: "Kustomization", ownerNamespace: "flux-system", ownerName: "ollama", ownerPrunes: true, ownerSuspended: false, basis: "flux-inventory",
      namespaceOwnerKind: "", namespaceOwnerNamespace: "", namespaceOwnerName: "", namespacePrunes: false, namespaceManaged: false, namespaceBasis: "",
      argoApps: [], warnings: [],
    };
    vi.mocked(rolloutHistory).mockResolvedValue({
      uid: "uid-ollama",
      revisions: [
        { revision: 3, ageSeconds: 600, images: ["shop/cart:v3"], current: true, objectUID: "rs3", templateDigest: "d3" },
        { revision: 2, ageSeconds: 7200, images: ["shop/cart:v2"], current: false, changeCause: "hotfix", objectUID: "rs2", templateDigest: "d2" },
      ],
    });
    seed([broken]);
    const { getByText, findByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect(await findByText("#3")).toBeTruthy();
    expect(getByText("current")).toBeTruthy();
    // The current revision gets no undo; revision 2 does.
    const undoButtons = getByText("undo →");
    fireEvent.click(undoButtons);
    // Danger confirm names the target AND the GitOps-owned consequence.
    expect(await findByText(/revision 2/)).toBeTruthy();
    expect(await findByText(/Kustomization flux-system\/ollama manages it \(its Flux inventory\)\. While it is not suspended/)).toBeTruthy();
    expect(getByText("Roll back")).toBeTruthy();
    expect(vi.mocked(rolloutUndo)).not.toHaveBeenCalled();
    fireEvent.click(getByText("Roll back"));
    // the revision the history showed travels with the undo (A3 §4.5)
    expect(vi.mocked(rolloutUndo)).toHaveBeenCalledWith("homelab-nelli", "Deployment", "ollama-prod", "ollama", "uid-ollama", 2, "rs2", "d2");
    expect(queryByText("Roll back")).toBeNull();
  });

  // A3 stage 1 review, finding 2: Confirm waits for the consequence, and a
  // reply to a cancelled opening never lands on a later one.
  it("undo offers no confirmation until its ownership read lands, and ignores a cancelled opening's reply", async () => {
    const crd = await import("../bridge/crd");
    const replies: Array<(o: unknown) => void> = [];
    vi.mocked(crd.gitOpsOwnership).mockImplementation(() => new Promise((r) => { replies.push(r as (o: unknown) => void); }) as never);
    vi.mocked(rolloutHistory).mockResolvedValue({ uid: "uid-ollama", revisions: [
      { revision: 3, ageSeconds: 600, images: ["v3"], current: true, objectUID: "rs3", templateDigest: "d3" },
      { revision: 2, ageSeconds: 7200, images: ["v2"], current: false, objectUID: "rs2", templateDigest: "d2" },
    ] });
    seed([broken]);
    const { getByText, findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect(await findByText("#3")).toBeTruthy();
    fireEvent.click(getByText("undo →"));
    expect(getByText(/checking which GitOps unit manages it/)).toBeTruthy();
    expect((getByText("Roll back") as HTMLButtonElement).disabled).toBe(true);
    const before = vi.mocked(rolloutUndo).mock.calls.length;
    fireEvent.click(getByText("Roll back"));
    expect(vi.mocked(rolloutUndo).mock.calls.length).toBe(before);
    fireEvent.click(getByText("Cancel"));
    fireEvent.click(getByText("undo →"));
    const suspended = { ownerKind: "Kustomization", ownerNamespace: "flux-system", ownerName: "ollama", ownerPrunes: true, ownerSuspended: true, basis: "flux-inventory",
      namespaceOwnerKind: "", namespaceOwnerNamespace: "", namespaceOwnerName: "", namespacePrunes: false, namespaceManaged: false, namespaceBasis: "", argoApps: [], warnings: [] };
    await act(async () => { replies[0](suspended); });
    expect(screen.queryByText(/It is suspended/)).toBeNull();
    await act(async () => { replies[1]({ ...suspended, ownerSuspended: false }); });
    expect(getByText(/While it is not suspended/)).toBeTruthy();
    expect((getByText("Roll back") as HTMLButtonElement).disabled).toBe(false);
  });

  it("a revision whose identity was not read offers no undo", async () => {
    vi.mocked(rolloutHistory).mockResolvedValue({ uid: "uid-ollama", revisions: [
      { revision: 3, ageSeconds: 600, images: ["v3"], current: true },
      { revision: 2, ageSeconds: 7200, images: ["v2"], current: false },
    ] });
    seed([broken]);
    const { findByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect(await findByText("no undo")).toBeTruthy();
    expect(queryByText("undo →")).toBeNull();
  });

  it("a failed history read renders its error, never a silently missing section", async () => {
    vi.mocked(rolloutHistory).mockResolvedValue({ revisions: [], error: "list replicasets in shop: forbidden" });
    seed([broken]);
    const { findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect(await findByText(/list replicasets in shop: forbidden/)).toBeTruthy();
  });

  it("pause/resume: button flips with paused state and the confirm fires the verb", async () => {
    seed([{ ...healthy, paused: true }]);
    const { getByText, findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("grafana");
    // Paused state: the status fact in the identity + the resume verb in More.
    expect(getByText(/rollouts paused/)).toBeTruthy();
    chooseMore("Resume rollouts");
    expect(await findByText("Resume")).toBeTruthy();
    fireEvent.click(getByText("Resume"));
    expect(vi.mocked(rolloutPause)).toHaveBeenCalledWith("homelab-nelli", "monitoring", "grafana", "uid-grafana", false);
  });

  it("pause is Deployment-only: no button on other kinds", async () => {
    seed([{ ...broken, kind: "DaemonSet" }]);
    const { queryByText, findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    await findByTestId("rollout-section");
    fireEvent.click(screen.getByRole("button", { name: /^More/ }));
    expect(queryByText("Pause rollouts")).toBeNull();
    expect(queryByText("Resume rollouts")).toBeNull();
  });

  it("needs-attention filter hides healthy rows", () => {
    seed([broken, healthy]);
    const { getByText, getByRole, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /^Attention/ }));
    expect(getByText("ollama")).toBeTruthy();
    expect(queryByText("grafana")).toBeNull();
  });

  it("filter input narrows workload rows", () => {
    seed([broken, healthy]);
    const { getByPlaceholderText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.change(getByPlaceholderText("Search workloads"), { target: { value: "grafana" } });
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
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText(/Sort: faults first/)).toBeTruthy();
    expect(getByText("CPU")).toBeTruthy();
    expect(getByText("Memory")).toBeTruthy();
  });

  it("Restart is the secondary action once a Deployment is selected", () => {
    seed([broken]);
    const { queryByRole, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByRole("button", { name: "Restart" })).toBeNull();
    select("ollama");
    expect(getByRole("button", { name: "Restart" })).toBeTruthy();
  });

  it("confirming restart dispatches rolloutRestart for the correct workload", () => {
    seed([broken]);
    const { getAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    fireEvent.click(getAllByRole("button", { name: "Restart" })[0]); // open confirm
    const confirmBtns = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirmBtns[confirmBtns.length - 1]); // confirm
    expect(rolloutRestart).toHaveBeenCalledWith("homelab-nelli", "Deployment", "ollama-prod", "ollama", "uid-ollama");
  });

  // --- Scale (More menu) ---

  it("Scale is offered for Deployments and StatefulSets, not DaemonSets", () => {
    const daemonSet: WorkloadDTO = {
      kind: "DaemonSet", namespace: "kube-system", name: "cilium",
      desired: 3, ready: 3, available: 3, updated: 3, restarts: 0,
      reason: "Available", rank: "healthy", gitops: null, pods: [], resources: noResources, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" },
    };
    const sts: WorkloadDTO = { ...daemonSet, kind: "StatefulSet", namespace: "db", name: "postgres", desired: 1, ready: 1, available: 1, updated: 1 };
    seed([broken, daemonSet, sts]);
    const { queryByRole, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    for (const [name, scalable] of [["ollama", true], ["cilium", false], ["postgres", true]] as const) {
      select(name);
      fireEvent.click(getByRole("button", { name: /^More/ }));
      expect(!!queryByRole("menuitem", { name: /^Scale/ })).toBe(scalable);
      fireEvent.click(getByRole("button", { name: /^More/ })); // close
      expect(getByRole("button", { name: "Restart" })).toBeTruthy();
    }
  });

  it("Scale opens the replica input prefilled with the desired count", () => {
    seed([broken]); // broken.desired = 1
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    const input = getByRole("spinbutton", { name: /replica count/i });
    expect((input as HTMLInputElement).value).toBe("1");
  });

  it("confirming scale dispatches scaleWorkload with the entered replica count (a count proven unowned)", async () => {
    owner.value = { autoscaler: "unowned", gitops: "unowned", owners: [] };
    seed([broken]);
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    await act(async () => {});
    fireEvent.change(getByRole("spinbutton", { name: /replica count/i }), { target: { value: "3" } });
    fireEvent.click(getByRole("button", { name: "apply scale" }));
    expect(scaleWorkload).toHaveBeenCalledWith("homelab-nelli", "Deployment", "ollama-prod", "ollama", "uid-ollama", 3);
  });

  // coverage S9 §7: an owned or unverified count takes the full
  // confirmation, naming the owner and its consequence
  it("an owned replica count says so and scales only through the confirmation", async () => {
    vi.mocked(scaleWorkload).mockClear();
    owner.value = { autoscaler: "owned", gitops: "owned", owners: ["KEDA ScaledObject ollama, through HPA keda-hpa-ollama"], gitopsOwner: "Kustomization flux-system/apps" };
    seed([broken]);
    const { getByRole, getByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    await act(async () => {});
    expect(getByTestId("scale-owner").textContent).toContain("pause the ScaledObject instead");
    expect(getByRole("button", { name: "apply scale" }).textContent).toBe("scale anyway");
    fireEvent.change(getByRole("spinbutton", { name: /replica count/i }), { target: { value: "3" } });
    fireEvent.click(getByRole("button", { name: "apply scale" }));
    expect(scaleWorkload).not.toHaveBeenCalled();
    expect(screen.getByText(/Kustomization flux-system\/apps sets it back/)).toBeTruthy();
  });

  it("no autoscaler but Git owning the count still takes the confirmation", async () => {
    vi.mocked(scaleWorkload).mockClear();
    owner.value = { autoscaler: "unowned", gitops: "owned", owners: [], gitopsOwner: "Kustomization flux-system/apps" };
    seed([broken]);
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    await act(async () => {});
    expect(getByRole("button", { name: "apply scale" }).textContent).toBe("scale anyway");
    fireEvent.click(getByRole("button", { name: "apply scale" }));
    expect(scaleWorkload).not.toHaveBeenCalled();
    expect(screen.getByText(/Kustomization flux-system\/apps sets it back/)).toBeTruthy();
  });

  it("an unverified owner never takes the popover-only path", async () => {
    vi.mocked(scaleWorkload).mockClear();
    owner.value = { autoscaler: "unverified", autoscalerWhy: "the HPA or ScaledObject read is incomplete", gitops: "unowned", owners: [] };
    seed([broken]);
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    await act(async () => {});
    fireEvent.click(getByRole("button", { name: "apply scale" }));
    expect(scaleWorkload).not.toHaveBeenCalled();
    expect(screen.getByText(/could not prove no autoscaler sets this count/)).toBeTruthy();
  });

  // --- Reloader forward "reloads on" view ---

  it("shows the reloads-on well with an auto chip and the reload-target chips", () => {
    const w: WorkloadDTO = {
      ...healthy, name: "api", namespace: "app",
      reloader: { auto: true, configMaps: ["app-config"], secrets: ["app-secret"] },
    };
    seed([w]);
    const { getByText, getByTitle } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("api");
    expect(getByText("Reloads on")).toBeTruthy();
    expect(getByText("auto")).toBeTruthy();
    // Chips are cross-links into Resources (title carries the target identity).
    expect(getByTitle("open ConfigMap app/app-config in Resources")).toBeTruthy();
    expect(getByTitle("open Secret app/app-secret in Resources")).toBeTruthy();
  });

  it("warns when reload annotations exist but the Reloader operator is not detected", () => {
    const w: WorkloadDTO = { ...healthy, name: "api", namespace: "app", reloader: { auto: true, configMaps: [], secrets: [] } };
    seed([w]); // no cluster entry -> reloaderPresent defaults false
    const { getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("api");
    expect(getByText(/annotations may be inert/)).toBeTruthy();
  });

  it("does not warn when the Reloader operator is present AND healthy", () => {
    const c: ClusterDTO = { name: "homelab-nelli", state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.30", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 1, reloaderPresent: true, reloaderHealthy: true };
    useFleet.setState({ clusters: [c] });
    const w: WorkloadDTO = { ...healthy, name: "api", namespace: "app", reloader: { auto: false, configMaps: ["only-this"], secrets: [] } };
    seed([w]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("api");
    expect(getByText("Reloads on")).toBeTruthy();
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
    select("api");
    expect(getByText(/reloads may not fire/)).toBeTruthy();
    expect(queryByText(/annotations may be inert/)).toBeNull();
  });

  it("renders no reloads-on well for a workload without Reloader annotations", () => {
    seed([{ ...healthy, name: "plain", namespace: "app" }]); // reloader undefined
    const { queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("plain");
    expect(queryByText("Reloads on")).toBeNull();
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
    // Enter opens the inspector on the second row
    expect(screen.getByRole("heading", { name: "second-workload" })).toBeTruthy();
  });

  it("/ focuses the filter input", () => {
    seed([broken]);
    const { getByPlaceholderText } = render(<WorkloadsView cluster="homelab-nelli" />);
    const input = getByPlaceholderText("Search workloads") as HTMLInputElement;
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }));
    expect(input).toBeTruthy();
  });

  it("clicking a row selects it and opens the inspector", () => {
    seed([broken]);
    const { getAllByRole, getByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    const row = getAllByRole("button").find((el) => el.classList.contains("lens-row"))!;
    expect(row.getAttribute("data-selected")).toBeNull();
    fireEvent.click(row);
    expect(row.getAttribute("data-selected")).toBe("true");
    expect(getByTestId("lens-inspector")).toBeTruthy();
  });

  it("cancelling the scale input hides it", () => {
    seed([broken]);
    const { getByRole, queryByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    fireEvent.click(getByRole("button", { name: "cancel scale" }));
    expect(queryByRole("spinbutton", { name: /replica count/i })).toBeNull();
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
    const { findByText, findAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama"); // expand
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
    const { findByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect(await findByText(/sparklines unavailable: metrics unavailable: no source/)).toBeTruthy();
  });

  it("sparkline row says no data under the cluster's scope (Settings S3b §3.3)", async () => {
    mockGetWorkloadSparklines.mockResolvedValueOnce({ available: true, cpu: [], mem: [], noData: "no data for homelab-nelli under cluster=nelli" });
    seed([broken]);
    useFleet.getState().setWorkloadUsage("homelab-nelli", "", {
      status: { available: true, message: "", updatedAt: "2026-06-10T00:00:00Z" },
      usage: {},
    });
    const { findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect((await findByTestId("spark-no-data")).textContent).toBe("no data for homelab-nelli under cluster=nelli");
  });

  it("sparkline row says its own source where the usage readout reads another (Codex's S3b-2c review, R3)", async () => {
    mockGetWorkloadSparklines.mockResolvedValueOnce({ available: true, cpu: [{ t: 1, v: 0.1 }, { t: 61, v: 0.2 }], mem: [{ t: 1, v: 0.3 }, { t: 61, v: 0.4 }], sentence: "from source-A", lineage: "A" });
    seed([broken]);
    useFleet.getState().setWorkloadUsage("homelab-nelli", "", {
      status: { available: true, message: "", updatedAt: "2026-06-10T00:00:00Z", sentence: "from source-B", lineage: "B" },
      usage: {},
    });
    const { findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect((await findByTestId("spark-source")).textContent).toBe("Earlier read from source-A, not current");
  });

  it("says where usage is read, and keeps an earlier read's sentence when the latest fails (Settings S3b §3.2, §3.4)", async () => {
    seed([broken]);
    useFleet.getState().setWorkloadUsage("homelab-nelli", "", {
      status: { available: true, message: "", updatedAt: "2026-06-10T00:00:00Z", sentence: "from lgtm-we-metrics, hosted by prd, filtered by cluster=nelli", lineage: "k1" },
      usage: {},
    });
    const { findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect((await findByTestId("workloads-source")).textContent).toBe("Read from lgtm-we-metrics, hosted by prd, filtered by cluster=nelli");
    act(() => {
      useFleet.getState().setWorkloadUsage("homelab-nelli", "", { status: { available: false, message: "metrics unavailable", updatedAt: "" }, usage: {} });
    });
    expect((await findByTestId("workloads-source")).textContent).toBe("Earlier read from lgtm-we-metrics, hosted by prd, filtered by cluster=nelli, not current");
  });

  // --- Live-update stability (selection + pending action by stable key) ---

  it("selection follows the workload when a live emit re-orders the rows", () => {
    seed([broken, healthy]);
    const { getAllByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    // Select grafana (second row) via click.
    select("grafana");
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
    const { getByPlaceholderText, queryByText, queryByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("grafana");
    fireEvent.change(getByPlaceholderText("Search workloads"), { target: { value: "ollama" } });
    // grafana filtered out - the inspector closes rather than retarget a wrong row.
    expect(queryByTestId("lens-inspector")).toBeNull();
    expect(queryByText("Restart")).toBeNull();
  });

  it("open scale popover survives a live emit that replaces the DTOs", () => {
    seed([broken]);
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore(/^Scale/);
    expect(getByRole("spinbutton", { name: /replica count/i })).toBeTruthy();
    // Live emit replaces the workload DTO (same identity key, new object).
    act(() => { seed([{ ...broken, restarts: 8 }]); });
    // Popover must still be open — the pending match is by key, not object identity.
    expect(getByRole("spinbutton", { name: /replica count/i })).toBeTruthy();
  });

  it("status trail reads shown/total, not shown/shown", () => {
    seed([broken, healthy]);
    const { getByText, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /^Attention/ })); // hides healthy → 1 of 2
    expect(getByText(/^1 of 2 workloads/)).toBeTruthy();
  });

  it("context-panel pod rows open the pod in the Pods section", async () => {
    useFleet.getState().openCluster("homelab-nelli");
    seed([broken]);
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama"); // select -> context panel
    fireEvent.click(getByRole("button", { name: "open pod ollama-x" }));
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", section: "pods" });
    // the Pods lens resolves the name to its row's object (design §5b)
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", cluster: "homelab-nelli", namespace: "ollama-prod", name: "ollama-x" });
  });

  it("context-panel flux owner jumps to the GitOps lens with a select intent", () => {
    useFleet.getState().openCluster("homelab-nelli");
    seed([broken]);
    const { getByTitle } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
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

describe("WorkloadsView health model (1b rework)", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); });

  it("offers an unknown filter while a verdict is unknown, and it isolates those rows", () => {
    const mystery: WorkloadDTO = { ...healthy, name: "mystery", health: { kind: "unknown", label: "Unverified", reason: "PodsIncomplete", rule: "workload.pods-incomplete" } };
    seed([broken, healthy, mystery]);
    const { getByRole, queryByText, getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /^Unknown 1/ }));
    expect(getByText("mystery")).toBeTruthy();
    expect(queryByText("grafana")).toBeNull();
    expect(queryByText("ollama")).toBeNull();
  });

  it("the inspector never derives its own pod health: a recovered restart asserts nothing", () => {
    const recovered: WorkloadDTO = { ...healthy, name: "api", pods: [{ name: "api-1", ready: true, restarts: 4, reason: "", node: "n1", ageSeconds: 99 }] };
    seed([recovered]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("api");
    expect(getByText("Pods · selector match · 1")).toBeTruthy();
    expect(queryByText(/affected/)).toBeNull();
    expect(queryByText(/\d+ healthy/)).toBeNull();
  });

  it("rows read for another cluster are neither shown nor counted", () => {
    useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: "other", items: [broken], loading: false } }));
    const { queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByText("ollama")).toBeNull();
  });
});

describe("WorkloadsView empty states never claim a quiet they did not see", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); });

  it("a failed first read never says 'No workloads'", () => {
    useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: "homelab-nelli", items: [], loading: false, error: "forbidden" } }));
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText(/Workloads could not be read/)).toBeTruthy();
    expect(queryByText(/^No workloads\.$/)).toBeNull();
  });

  it("Clear filters also clears the unknown filter after its last row resolves", () => {
    const mystery: WorkloadDTO = { ...healthy, name: "mystery", health: { kind: "unknown", label: "Unverified", reason: "PodsIncomplete", rule: "workload.pods-incomplete" } };
    seed([healthy, mystery]);
    const { getByRole, getByText, rerender } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /^Unknown 1/ }));
    // the unknown workload resolves to ok
    seed([healthy, { ...mystery, health: { kind: "ok", label: "Available", reason: "Ready", rule: "workload.ready" } }]);
    rerender(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: "Clear filters" }));
    expect(getByText("grafana")).toBeTruthy();
    expect(getByText("mystery")).toBeTruthy();
  });
});

describe("WorkloadsView workspace template (redesign)", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); vi.clearAllMocks(); });

  it("auto-selects the first attention workload once, from a fresh read only", () => {
    seed([healthy, broken]);
    const { queryByTestId, getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByTestId("lens-inspector")).toBeNull(); // cached rows at entry
    act(() => seed([healthy, broken]));
    expect(getByRole("heading", { name: "ollama" })).toBeTruthy();
    // closing it is final: a later read never reopens
    fireEvent.click(getByRole("button", { name: "close details" }));
    act(() => seed([healthy, broken, { ...broken, name: "other" }]));
    expect(queryByTestId("lens-inspector")).toBeNull();
  });

  it("no idle inspector when nothing is selected", () => {
    seed([healthy]);
    const { queryByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByTestId("lens-inspector")).toBeNull();
  });

  it("a workload gone from a complete read shows 'Workload removed', cancels a pending restart, and drops actions", () => {
    seed([broken, healthy]);
    const { getByText, queryByRole, getAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    fireEvent.click(getAllByRole("button", { name: "Restart" })[0]);
    expect(getAllByRole("button", { name: "Restart" }).length).toBe(2); // confirm open
    act(() => seed([healthy]));
    expect(getByText("Workload removed.")).toBeTruthy();
    expect(queryByRole("button", { name: "Restart" })).toBeNull();
    expect(queryByRole("menuitem", { name: /Scale/ })).toBeNull();
    expect(rolloutRestart).not.toHaveBeenCalled();
  });

  it("the kind filter lives in the view and narrows rows", () => {
    const ds: WorkloadDTO = { ...healthy, kind: "DaemonSet", name: "cilium", namespace: "kube-system" };
    seed([broken, ds]);
    const { getByLabelText, queryByText, getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.change(getByLabelText("Kind"), { target: { value: "DaemonSet" } });
    expect(getByText("cilium")).toBeTruthy();
    expect(queryByText("ollama")).toBeNull();
  });
});

describe("WorkloadsView pending verbs are pinned to their own target", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); useFleet.setState({ navIntent: null }); vi.clearAllMocks(); });

  it("a restart raised from the palette (no selection) is cancelled when its workload is removed", () => {
    seed([broken, healthy]);
    useFleet.getState().requestNavIntent({ kind: "restart-workload", cluster: "homelab-nelli", workloadKind: "Deployment", namespace: "ollama-prod", name: "ollama" });
    const { getAllByRole, getByRole, queryAllByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getAllByRole("button", { name: "Restart" }).length).toBe(2);
    fireEvent.click(getByRole("button", { name: "close details" }));
    expect(getAllByRole("button", { name: "Restart" }).length).toBe(1); // confirm stays: selection is not its owner
    act(() => seed([healthy]));
    expect(queryAllByRole("button", { name: "Restart" }).length).toBe(0);
    expect(rolloutRestart).not.toHaveBeenCalled();
  });

  it("a cluster switch drops a pending restart instead of retargeting it", () => {
    seed([broken]);
    const { getAllByRole, queryAllByRole, rerender } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    fireEvent.click(getAllByRole("button", { name: "Restart" })[0]);
    rerender(<WorkloadsView cluster="other" />);
    expect(rolloutRestart).not.toHaveBeenCalled();
    act(() => useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: "other", items: [broken], loading: false } })));
    expect(queryAllByRole("button", { name: "Cancel" }).length).toBe(0); // the confirm is gone
    fireEvent.click(getAllByRole("button", { name: "Restart" })[0]); // a NEW restart on "other" is fine
    const confirm = getAllByRole("button", { name: "Restart" });
    fireEvent.click(confirm[confirm.length - 1]);
    expect(rolloutRestart).toHaveBeenCalledTimes(1);
    expect(rolloutRestart).toHaveBeenCalledWith("other", "Deployment", "ollama-prod", "ollama", "uid-ollama");
  });
});

describe("WorkloadsView Codex review corrections", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); useFleet.setState({ navIntent: null, clusters: [] }); vi.clearAllMocks(); });
  const switchTo = (rerender: (ui: React.ReactElement) => void, c: string, items: WorkloadDTO[]) => {
    rerender(<WorkloadsView cluster={c} />);
    act(() => useFleet.setState((s) => ({ workloads: { ...s.workloads, cluster: c, items, loading: false } })));
  };

  it("the logs dock belongs to its origin cluster: a switch to a same-name workload closes it", () => {
    seed([broken]);
    const { getByRole, queryByTestId, rerender } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /aggregate logs for ollama-prod\/ollama/i }));
    expect(queryByTestId("workload-logs-dock")).toBeTruthy();
    switchTo(rerender, "other", [broken]);
    expect(queryByTestId("workload-logs-dock")).toBeNull();
    switchTo(rerender, "homelab-nelli", [broken]); // and never resurrects
    expect(queryByTestId("workload-logs-dock")).toBeNull();
  });

  it("pop-out targets the dock's own cluster", async () => {
    seed([broken]);
    const { getByRole, findByTestId } = render(<WorkloadsView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /aggregate logs for ollama-prod\/ollama/i }));
    await findByTestId("workload-logs-dock");
    fireEvent.click(getByRole("button", { name: /open logs in window/i }));
    expect(openWorkloadLogsWindow).toHaveBeenCalledWith("homelab-nelli", "ollama-prod", "Deployment", "ollama", "");
  });

  it("fleet compare belongs to its origin cluster: a switch closes it", () => {
    const c = (name: string): ClusterDTO => ({ name, state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.30", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 1 });
    useFleet.setState({ clusters: [c("homelab-nelli"), c("other")] });
    seed([broken]);
    const { getByRole, queryByRole, rerender } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    chooseMore("Compare across the fleet");
    expect(getByRole("dialog", { name: "Fleet compare" })).toBeTruthy();
    switchTo(rerender, "other", [broken]);
    expect(queryByRole("dialog", { name: "Fleet compare" })).toBeNull();
  });

  it("pod deletion is a per-pod action and is cancelled when that pod leaves the evidence", () => {
    const two: WorkloadDTO = { ...broken, pods: [broken.pods[0], { name: "ollama-y", uid: "uid-ollama-y", ready: true, restarts: 0, reason: "", node: "node-1", ageSeconds: 60 }] };
    seed([two]);
    const { getByRole, queryByText, queryByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(queryByRole("menuitem", { name: /Delete pod/ })).toBeNull(); // no controller-level guess
    fireEvent.click(getByRole("button", { name: /^More/ }));
    fireEvent.click(getByRole("button", { name: "delete pod ollama-x" }));
    expect(queryByText(/ollama-x - matched by deployment ollama's selector/)).toBeTruthy();
    // the chosen pod is replaced; the workload remains
    act(() => seed([{ ...two, pods: [{ ...two.pods[1] }, { ...broken.pods[0], name: "ollama-z", uid: "uid-ollama-z" }] }]));
    expect(queryByText(/ollama-x - matched by/)).toBeNull();
    expect(queryByRole("button", { name: "Delete" })).toBeNull();
    expect(deletePod).not.toHaveBeenCalled();
  });

  it("a paused Deployment with running replicas is 'paused', never 'scaled to 0'", () => {
    const paused: WorkloadDTO = { ...healthy, name: "held", desired: 2, ready: 2, available: 2, paused: true, health: { kind: "paused", label: "Paused", reason: "RolloutsPaused", rule: "workload.paused" } };
    seed([paused]);
    const { getByText, queryByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByText(/1 paused/)).toBeTruthy();
    expect(queryByText(/scaled to 0/)).toBeNull();
  });

  it("no Manifest tab that navigates away; View manifest stays available on removed evidence", () => {
    seed([broken, healthy]);
    const { queryByRole, getByRole, getByText } = render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    expect(queryByRole("tab", { name: "Manifest" })).toBeNull();
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(getByRole("menuitem", { name: "View manifest in Resources" })).toBeTruthy();
    expect(queryByRole("menuitem", { name: /Edit manifest/ })).toBeNull();
    fireEvent.click(getByRole("button", { name: /^More/ }));
    act(() => seed([healthy]));
    expect(getByText("Workload removed.")).toBeTruthy();
    fireEvent.click(getByRole("button", { name: /^More/ }));
    expect(getByRole("menuitem", { name: "View manifest in Resources" })).toBeTruthy();
    expect(queryByRole("menuitem", { name: /Scale|Pause|Compare/ })).toBeNull();
  });
});

describe("WorkloadsView verdict prints counts once (visual verdict)", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); });
  it("with a native line, the detail adds only what it lacks", () => {
    seed([{ ...broken, health: { ...broken.health!, native: "0/1 ready · 0 available · 1 updated · generation 1/1" } }]);
    render(<WorkloadsView cluster="homelab-nelli" />);
    select("ollama");
    const v = screen.getByTestId("verdict-status").textContent ?? "";
    expect(v.match(/ready/g)?.length).toBe(1);
    expect(v).toContain("7 restarts");
  });
});

describe("WorkloadsView history across clusters (Codex 7b re-review)", () => {
  // The reused view: one WorkloadsView whose cluster prop changes, as in
  // ClusterDetail when history or the palette switches cluster on Workloads.
  function Host() {
    const r = useFleet((s) => s.route);
    return r.name === "cluster" && r.section === "workloads" ? <WorkloadsView cluster={r.cluster} /> : null;
  }
  const kinds = () => useFleet.getState().workloads.kindFilter;
  const tick = () => act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });
  let off = () => {};

  beforeEach(async () => {
    off();
    const h = await import("../nav/history");
    h._resetHistoryForTest();
    localStorage.clear();
    // Restrictive saved kinds on both clusters: they must never override a place.
    localStorage.setItem("klyx-view-prefs", JSON.stringify({ a: { workloads: { kinds: ["DaemonSet"] } }, b: { workloads: { kinds: ["DaemonSet"] } } }));
    useFleet.setState({ route: { name: "fleet" }, clusters: [{ name: "a", state: "Synced" } as ClusterDTO, { name: "b", state: "Synced" } as ClusterDTO] });
    useFleet.getState().clearWorkloads();
    off = h.initHistory();
  });

  for (const [label, aKinds] of [["a single kind", ["StatefulSet"]], ["explicit All workloads", ["Deployment", "StatefulSet", "DaemonSet"]]] as const) {
    it(`${label} on A survives a switch to B and comes back with Back; Forward returns B's choice`, async () => {
      const { back, forward } = await import("../nav/history");
      const want = { Deployment: aKinds.includes("Deployment" as never), StatefulSet: aKinds.includes("StatefulSet" as never), DaemonSet: aKinds.includes("DaemonSet" as never) };
      act(() => { useFleet.getState().openCluster("a"); useFleet.getState().openWorkloads(aKinds); });
      render(<Host />);
      await tick();
      expect(kinds()).toEqual(want);
      act(() => { useFleet.getState().openCluster("b"); useFleet.getState().openWorkloads(["Deployment"]); });
      await tick();
      expect(kinds()).toEqual({ Deployment: true, StatefulSet: false, DaemonSet: false });
      act(() => { back(); });
      await tick();
      expect(useFleet.getState().route).toMatchObject({ cluster: "a", section: "workloads" });
      expect(kinds()).toEqual(want);
      act(() => { forward(); });
      await tick();
      expect(useFleet.getState().route).toMatchObject({ cluster: "b", section: "workloads" });
      expect(kinds()).toEqual({ Deployment: true, StatefulSet: false, DaemonSet: false });
    });
  }
});

// Codex UI review 2026-10-13: the palette's compare verb opens the existing
// fleet compare for its workload; with one cluster it opens nothing.
describe("WorkloadsView compare-workload intent", () => {
  beforeEach(() => { localStorage.clear(); useFleet.getState().clearWorkloads(); useFleet.setState({ navIntent: null, clusters: [] }); vi.clearAllMocks(); });
  const c = (name: string): ClusterDTO => ({ name, state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1.30", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 1 });

  it("opens fleet compare for the named workload when the fleet has two clusters", () => {
    useFleet.setState({ clusters: [c("homelab-nelli"), c("other")] });
    seed([broken, healthy]);
    useFleet.getState().requestNavIntent({ kind: "compare-workload", cluster: "homelab-nelli", workloadKind: "Deployment", namespace: "ollama-prod", name: "ollama" });
    const { getByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(getByRole("dialog", { name: "Fleet compare" })).toBeTruthy();
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("with one cluster it selects the workload and opens nothing", () => {
    useFleet.setState({ clusters: [c("homelab-nelli")] });
    seed([broken, healthy]);
    useFleet.getState().requestNavIntent({ kind: "compare-workload", cluster: "homelab-nelli", workloadKind: "Deployment", namespace: "ollama-prod", name: "ollama" });
    const { queryByRole } = render(<WorkloadsView cluster="homelab-nelli" />);
    expect(queryByRole("dialog", { name: "Fleet compare" })).toBeNull();
    expect(useFleet.getState().navIntent).toBeNull();
  });
});
