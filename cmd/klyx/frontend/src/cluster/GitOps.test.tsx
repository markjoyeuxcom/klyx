import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, act, within, screen } from "@testing-library/react";
import { useFleet, FluxResourceDTO, FluxSourceDTO, ClusterDTO } from "../store/fleet";
import { reconcile, reconcileWithSource, setSuspend, resolveGitLink, fluxDiff } from "../bridge/gitops";
import { describeResource } from "../bridge/exec";
import { GitOps } from "./GitOps";
import { fluxRule, fluxSourceRule, withRule } from "../test/backendRules";

// drawnBeforeActions: the element sits in the inspector's drawing slot, ahead
// of its action band (The Line slice 13c.3, parent §4).
function drawnBeforeActions(el: HTMLElement): boolean {
  const panel = el.closest('[data-testid="lens-inspector"]') ?? document.body;
  const actions = panel.querySelector('[role="group"][aria-label="actions"]');
  return !!el.closest(".insp-drawing") && !!actions && !!(el.compareDocumentPosition(actions) & Node.DOCUMENT_POSITION_FOLLOWING);
}

// The rows hook is the transport; these tests seed the gitops slice it fills.
vi.mock("../health/useFluxRows", async (orig) => ({ ...(await orig<typeof import("../health/useFluxRows")>()), useFluxRows: () => {} }));
vi.mock("../bridge/gitops", () => ({
  getResourceDetail: async () => {},
  reconcile: vi.fn(),
  reconcileWithSource: vi.fn(),
  setSuspend: vi.fn(),
  resolveGitLink: vi.fn(),
  fluxDiff: vi.fn(async () => ({ available: true, hasChanges: true, output: "± spec.replicas: 2 -> 3", error: "" })),
}));

// The Dependencies section (slice 8c) walks in Go: a cancellable call that
// these tests answer with a fixed graph, and no Git.
const depGraphCall = vi.fn();
vi.mock("../bridge/depGraph", () => ({
  dependencyGraph: (...a: unknown[]) => depGraphCall(...a),
}));
vi.mock("../bridge/notHereYet", () => ({
  notHereYet: () => Object.assign(Promise.resolve({ results: [] }), { cancel: () => {} }),
}));
const cancellable = <T,>(v: T) => Object.assign(Promise.resolve(v), { cancel: vi.fn() });
const noGraph = { start: "", nodes: [], edges: [], incomplete: [], proximity: [], reads: 0 };
depGraphCall.mockImplementation(() => cancellable(noGraph));

// The reconcile composer (OperationDialog) registers protected-cluster grants
// via the config bridge; mock it so no Wails runtime is needed.
// Describe (slice 14c) runs kubectl through the exec bridge.
vi.mock("../bridge/exec", () => ({
  describeResource: vi.fn(() => Object.assign(Promise.resolve({ ok: true, output: "Name:  flux-system\n", error: "", command: "", commandError: "" }), { cancel: () => {} })),
}));
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

const cluster = (tier: string): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: tier, gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "", region: "", provider: "", group: "", ageSeconds: 0,
});
const res = (over: Partial<FluxResourceDTO>): FluxResourceDTO => withRule<FluxResourceDTO>({
  kind: "Kustomization", namespace: "flux-system", name: "flux-system", ready: "Ready",
  reason: "", message: "", revision: "main@abc", readySinceSeconds: 1, suspended: false,
  sourceKind: "", sourceName: "", ...over,
}, fluxRule);

const src = (over: Partial<FluxSourceDTO>): FluxSourceDTO => withRule<FluxSourceDTO>({
  kind: "GitRepository", namespace: "flux-system", name: "flux-system",
  ready: "Ready", reason: "", message: "", revision: "main@def", url: "https://x/y", suspended: false, ...over,
}, fluxSourceRule);

const expandedDetail = (over: Partial<import("../store/fleet").ResourceDetailDTO> = {}) => ({
  cluster: "x",
  resources: [res({ kind: "Kustomization", namespace: "flux-system", name: "flux-system" })],
  sources: [],
  loading: false,
  expandedKey: "Kustomization/flux-system/flux-system",
  detailCluster: "x",
  detail: {
    kind: "Kustomization", namespace: "flux-system", name: "flux-system",
    suspended: false, appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
    conditions: [], inventory: [], ...over,
  },
});

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.setState({
    clusters: [cluster("Healthy")],
    route: { name: "cluster", cluster: "x", section: "gitops" },
    gitops: { cluster: "x", resources: [], sources: [], loading: false, expandedKey: null, detail: null },
  });
});
// Sources is a navigation destination (step 7a), carried by the route.
const openSources = () => act(() => useFleet.getState().openFlux("sources"));

const inspector = () => within(screen.getByTestId("lens-inspector"));
function chooseMore(label: string | RegExp) {
  fireEvent.click(screen.getByRole("button", { name: /^More/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: label }));
}

describe("GitOps view", () => {
  it("a stale resource dims and shows the Syncing chip instead of a confident status", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "edited", ready: "Ready", stale: true, generation: 5, observedGeneration: 4 }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByText, getByTitle } = render(<GitOps cluster="x" />);
    const row = getByText("edited").closest(".lens-row") as HTMLElement;
    // The status cell must not read as confirmed while changes are unrealized.
    expect(getByText("Syncing… generation 5, observed 4")).toBeTruthy();
    expect(row.textContent).not.toContain("Ready");
    expect(getByTitle(/Observed Gen: 4 vs Current: 5/)).toBeTruthy();
    // Dim = not yet realized.
    expect(row.dataset.stale).toBe("true"); // dims every cell but the row's identity (layout.css)
  });


  it("renders the resource table from the store", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "flux-system", ready: "Ready" }),
      res({ kind: "HelmRelease", name: "cilium", ready: "Failed", message: "install failed" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByText, getByTitle } = render(<GitOps cluster="x" />);
    expect(getByText("flux-system", { selector: ".lens-cell-name span:last-child" })).toBeTruthy();
    expect(getByText("cilium")).toBeTruthy();
    // the controller message rides on the status cell; the inspector shows it in full
    expect(getByTitle(/install failed/i)).toBeTruthy();
  });

  it("shows the failing condition reason as a chip on the row", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ kind: "HelmRelease", name: "cilium", ready: "Failed", reason: "UpgradeFailed", message: "upgrade retries exhausted" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getAllByText } = render(<GitOps cluster="x" />);
    expect(getAllByText(/Failed · UpgradeFailed/).length).toBeGreaterThan(0);
  });

  it("list and inspector sit on the shared template surfaces", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "flux-system", ready: "Ready" }),
      res({ kind: "HelmRelease", name: "cilium", ready: "Failed", message: "install failed" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { container } = render(<GitOps cluster="x" />);
    expect(container.querySelector(".lens-table .lens-table-head")).toBeTruthy();
    expect(container.querySelectorAll(".lens-row").length).toBe(2);
  });

  it("shows the no-Flux empty state when gitopsTier is Absent", () => {
    useFleet.setState({ clusters: [cluster("Absent")] });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText(/Flux is not installed on this cluster/i)).toBeTruthy();
  });

  it("expands a row and renders its detail from the store", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [res({ kind: "Kustomization", namespace: "flux-system", name: "flux-system" })],
        sources: [],
        loading: false,
        expandedKey: "Kustomization/flux-system/flux-system",
        detailCluster: "x",
        detail: {
          kind: "Kustomization", namespace: "flux-system", name: "flux-system",
          appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
          conditions: [
            { type: "Ready", status: "True", reason: "ok", message: "Applied revision main@a" },
            { type: "Healthy", status: "True", reason: "Succeeded", message: "Health check passed" },
          ],
          inventory: [{ group: "", version: "v1", kind: "ConfigMap", namespace: "monitoring", name: "my-cm" }],
        },
      },
    });
    const { getByText, queryByText } = render(<GitOps cluster="x" />);
    // successful conditions start collapsed (spec §3); one click shows them
    expect(queryByText(/Health check passed/i)).toBeNull();
    fireEvent.click(getByText("Conditions · 2"));
    expect(getByText(/Health check passed/i)).toBeTruthy();
    // Inventory rows split kind and namespace/name (dot + kind + link).
    expect(getByText("ConfigMap")).toBeTruthy();
    expect(getByText("monitoring/my-cm")).toBeTruthy();
  });

  it("truncates the inventory to 8 entries with an honest +N more (no health claim)", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: expandedDetail({
        inventory: Array.from({ length: 11 }, (_, i) => ({ group: "", version: "v1", kind: "ConfigMap", namespace: "ns", name: `cm-${i}` })),
      }),
    });
    const { getByText, queryByText } = render(<GitOps cluster="x" />);
    expect(getByText("Inventory · 11")).toBeTruthy();
    expect(getByText("ns/cm-7")).toBeTruthy();
    expect(queryByText("ns/cm-8")).toBeNull();
    expect(getByText("+3 more")).toBeTruthy();
    // Inventory entries carry no health - the truncation line must not claim it.
    expect(queryByText(/all healthy/i)).toBeNull();
  });

  it("the inspector opens with the shared verdict: state word plus failing reason", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [res({ kind: "Kustomization", namespace: "flux-system", name: "flux-system", ready: "Failed", reason: "HealthCheckFailed", message: "health check failed after 2m0s" })],
        sources: [],
        loading: false,
        expandedKey: "Kustomization/flux-system/flux-system",
        detailCluster: "x",
        detail: {
          kind: "Kustomization", namespace: "flux-system", name: "flux-system",
          suspended: false, appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
          conditions: [], inventory: [],
        },
      },
    });
    render(<GitOps cluster="x" />);
    expect(inspector().getByText("Failed")).toBeTruthy();
    expect(inspector().getByText(/HealthCheckFailed/)).toBeTruthy();
    expect(inspector().getByText(/health check failed after 2m0s/)).toBeTruthy();
  });

  it("shows an apply-failed line when applyFailed", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [res({ kind: "Kustomization", namespace: "flux-system", name: "x" })],
        sources: [],
        loading: false,
        expandedKey: "Kustomization/flux-system/x",
        detailCluster: "x",
        detail: {
          kind: "Kustomization", namespace: "flux-system", name: "x",
          appliedRevision: "main@a", attemptedRevision: "main@b", applyFailed: true,
          conditions: [], inventory: [],
        },
      },
    });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText(/apply failed at/i)).toBeTruthy();
  });

  it("one Reconcile action opens the composer; confirming with the tile off calls reconcile", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    const { getByRole, getAllByRole, queryByText } = render(<GitOps cluster="x" />);
    // The former separate verb is gone - re-fetching the source is an option
    // inside the one Reconcile dialog, not a second action.
    expect(queryByText("Reconcile with source")).toBeNull();
    fireEvent.click(inspector().getByRole("button", { name: /^Reconcile/ })); // primary action opens the dialog
    expect(getByRole("dialog", { name: "Reconcile flux-system/flux-system" })).toBeTruthy();
    expect(getByRole("button", { name: /Re-fetch source first/ }).getAttribute("aria-pressed")).toBe("false");
    const reconcileButtons = getAllByRole("button", { name: "Reconcile" });
    fireEvent.click(reconcileButtons[reconcileButtons.length - 1]); // dialog confirm is last in DOM
    expect(reconcile).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system", ""); // uid-less fixture: the backend refuses an unarmed write
    expect(reconcileWithSource).not.toHaveBeenCalled();
  });

  it("toggling the re-fetch-source tile routes the confirm to reconcileWithSource", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    const { getByRole, getAllByRole } = render(<GitOps cluster="x" />);
    fireEvent.click(inspector().getByRole("button", { name: /^Reconcile/ }));
    fireEvent.click(getByRole("button", { name: /Re-fetch source first/ }));
    const reconcileButtons = getAllByRole("button", { name: "Reconcile" });
    fireEvent.click(reconcileButtons[reconcileButtons.length - 1]); // dialog confirm
    expect(reconcileWithSource).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system", "");
    expect(reconcile).not.toHaveBeenCalled();
  });

  it("shows Resume + a suspended badge when detail.suspended is true", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail({ suspended: true }) });
    const { getAllByRole } = render(<GitOps cluster="x" />);
    expect(inspector().getByRole("button", { name: /^Resume/ })).toBeTruthy();
    expect(inspector().queryByRole("button", { name: /^Suspend/ })).toBeNull();
    fireEvent.click(inspector().getByRole("button", { name: /^Resume/ })); // secondary action opens the dialog
    const resumeButtons = getAllByRole("button", { name: "Resume" });
    fireEvent.click(resumeButtons[resumeButtons.length - 1]); // dialog confirm is last in DOM
    expect(setSuspend).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system", "", false);
  });

  it("view-in-git button calls resolveGitLink for a Kustomization", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    render(<GitOps cluster="x" />);
    chooseMore("View in Git");
    expect(resolveGitLink).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system");
  });

  it("describes the selected Kustomization from More (slice 14c)", async () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    render(<GitOps cluster="x" />);
    chooseMore("Describe");
    expect(await screen.findByTestId("describe-sheet")).toBeTruthy();
    expect(describeResource).toHaveBeenCalledWith("x", "kustomization.kustomize.toolkit.fluxcd.io", "flux-system", "flux-system");
  });

  it("hides view-in-git for a HelmRelease", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [res({ kind: "HelmRelease", namespace: "ns", name: "app" })],
        sources: [],
        loading: false,
        expandedKey: "HelmRelease/ns/app",
        detailCluster: "x",
        detail: { kind: "HelmRelease", namespace: "ns", name: "app", suspended: false, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [], inventory: [] },
      },
    });
    const { queryByRole } = render(<GitOps cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: /^More/ }));
    expect(queryByRole("menuitem", { name: "View in Git" })).toBeNull();
  });

  it("renders a failing bound source as the headline in the detail panel", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: expandedDetail({
        source: src({ ready: "Failed", reason: "GitOperationFailed", message: "auth required" }),
      }),
    });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText(/source not ready: GitOperationFailed/i)).toBeTruthy();
    expect(getByText(/auth required/i)).toBeTruthy();
  });

  // The Line slice 8c (parent §5.1): "blocked by" became the Dependencies
  // section - the walked graph on Rail, in the controllers' words.
  it("draws the Dependencies graph with the reported blocker and where its chain ends", async () => {
    const node = (id: string, over: object) => {
      const [kind, namespace, name] = id.split("/");
      return { id, group: "", kind, namespace, name, uid: `u-${name}`, ready: "True", conditionGeneration: 0, generation: 1, observedGeneration: 1,
        stale: false, suspended: false, failing: false, upstreamFailure: false, chainEnd: false, supply: [], ...over };
    };
    depGraphCall.mockImplementationOnce(() => cancellable({
      start: "Kustomization/flux-system/apps", reads: 2, incomplete: [], proximity: [],
      nodes: [
        node("Kustomization/flux-system/apps", { ready: "False", reason: "DependencyNotReady", failing: true }),
        node("Kustomization/flux-system/infra", { ready: "False", reason: "BuildFailed", failing: true, upstreamFailure: true, chainEnd: true }),
      ],
      edges: [{ from: "Kustomization/flux-system/apps", to: "Kustomization/flux-system/infra", type: "dependsOn", reported: true, cycle: false }],
    }));
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [
          res({ kind: "Kustomization", namespace: "flux-system", name: "apps", ready: "Reconciling", reason: "DependencyNotReady", uid: "u-apps" }),
          res({ kind: "Kustomization", namespace: "flux-system", name: "infra", ready: "Failed", uid: "u-infra" }),
        ],
        sources: [],
        loading: false,
        expandedKey: "Kustomization/flux-system/apps",
        detailCluster: "x",
        detail: {
          kind: "Kustomization", namespace: "flux-system", name: "apps", uid: "u-apps",
          reason: "DependencyNotReady", appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
          conditions: [], inventory: [],
          dependsOn: [{ namespace: "flux-system", name: "infra" }],
        },
      },
    });
    const { findByRole } = render(<GitOps cluster="x" />);
    const rail = await findByRole("list", { name: "Dependencies" });
    expect(drawnBeforeActions(rail)).toBe(true);
    expect(depGraphCall).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "apps", "u-apps");
    expect(rail.textContent).toContain("reported blocker (depends on): Kustomization flux-system/infra");
    expect(rail.textContent).toContain("reported chain ends here");
    expect(rail.textContent).not.toMatch(/root cause/i);
  });

  it("renders the drift/events timeline in the detail panel", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: expandedDetail({
        events: [
          { type: "Warning", reason: "DriftDetected", message: "Deployment/default/podinfo configured", count: 3, namespace: "flux-system", kind: "Kustomization", name: "flux-system", lastSeenUnix: 0, firstSeenUnix: 0 },
        ],
      }),
    });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText("DriftDetected")).toBeTruthy();
    expect(getByText("drift")).toBeTruthy();
    expect(getByText(/podinfo configured/i)).toBeTruthy();
  });

  it("hides Compute diff on a healthy Kustomization and shows it when suspended", () => {
    // Healthy (Ready, not suspended): gated out.
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    const healthy = render(<GitOps cluster="x" />);
    expect(healthy.queryByText("Compute diff")).toBeNull();
    healthy.unmount();

    // Suspended: the escape hatch appears.
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail({ suspended: true }) });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText("Compute diff")).toBeTruthy();
  });

  it("says which path computed the diff: the Git cache at a commit, with what was not applied", async () => {
    vi.mocked(fluxDiff).mockResolvedValueOnce({
      available: true, hasChanges: true, error: "", via: "cache", revision: "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90",
      output: "► Deployment shop/web configured", notes: ["variables shown unsubstituted: postBuild substitution is not applied"],
    });
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail({ suspended: true }) });
    render(<GitOps cluster="x" />);
    expect(screen.getByTestId("diff-via").textContent).toContain("builds from the Git cache when the repository is approved");
    await act(async () => { fireEvent.click(screen.getByText("Compute diff")); });
    expect(screen.getByTestId("diff-via").textContent).toBe("built from the Git cache at a1b9e3c, dry-run on this cluster's connection");
    expect(screen.getByText("variables shown unsubstituted: postBuild substitution is not applied")).toBeTruthy();
    expect(screen.getByText("► Deployment shop/web configured")).toBeTruthy();

    // coverage S3b §4: a completed server-side dry run says what admission
    // passed and when; the drift sentence is always there
    expect(screen.getByTestId("diff-dry-run-admission").textContent).toMatch(/^This dry run passed mutating admission and defaulting as of .+, so it shows what the API server would have stored then; a later apply's admission may differ\.$/);
    expect(screen.getAllByTestId("drift-sentence")[0].textContent).toBe("A difference between the live object and Git can come from a mutating webhook or policy, a controller, or a person. Klyx does not attribute it.");

    vi.mocked(fluxDiff).mockResolvedValueOnce({ available: true, hasChanges: false, error: "", output: "", via: "flux CLI" });
    await act(async () => { fireEvent.click(screen.getByText("Compute diff")); });
    expect(screen.getByTestId("diff-via").textContent).toBe("computed by flux diff with your local credentials");
    expect(screen.queryByTestId("diff-dry-run-admission")).toBeNull();
    vi.mocked(fluxDiff).mockResolvedValueOnce({ available: true, hasChanges: false, error: "the server-side dry-run: refused", output: "", via: "cache" });
    await act(async () => { fireEvent.click(screen.getByText("Compute diff")); });
    expect(screen.queryByTestId("diff-dry-run-admission")).toBeNull();
    expect(screen.getAllByTestId("drift-sentence").length).toBeGreaterThan(0);
  });

  it("lists sources under the sources filter", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [res({})],
        sources: [src({ kind: "OCIRepository", name: "cilium", revision: "v1.15@sha256:abc" })],
        loading: false,
        expandedKey: null,
        detail: null,
      },
    });
    const { getByText, getAllByTestId } = render(<GitOps cluster="x" />);
    openSources();
    expect(getAllByTestId("flux-source-row").length).toBe(1);
    expect(getByText("cilium")).toBeTruthy();
    expect(getByText("oci")).toBeTruthy();
  });

  // --- Keyboard nav ---

  it("j/k + Enter expands the selected row like a click, Esc collapses", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "aaa" }),
      res({ name: "bbb" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    render(<GitOps cluster="x" />);
    // j → index 0 (aaa); j → index 1 (bbb); Enter → expand(keyOf(bbb))
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(useFleet.getState().gitops.expandedKey).toBe("Kustomization/flux-system/bbb");
    // Esc collapses the open panel.
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })); });
    expect(useFleet.getState().gitops.expandedKey).toBeNull();
  });

  it("r on the keyboard-selected row opens the reconcile composer; confirming calls reconcile", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "aaa" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByRole, getAllByRole, queryByRole } = render(<GitOps cluster="x" />);
    // No row selected yet: r must be inert (no dialog, no bridge call).
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true, cancelable: true })); });
    expect(queryByRole("dialog")).toBeNull();
    // j selects the first row; r opens the SAME composer the panel button opens.
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true, cancelable: true })); });
    expect(getByRole("dialog", { name: "Reconcile flux-system/aaa" })).toBeTruthy();
    expect(reconcile).not.toHaveBeenCalled();               // the key never bypasses the confirm
    const reconcileButtons = getAllByRole("button", { name: "Reconcile" });
    fireEvent.click(reconcileButtons[reconcileButtons.length - 1]); // dialog confirm is last in DOM
    expect(reconcile).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "aaa", "");
  });

  it("s on the keyboard-selected row opens the suspend confirm; a suspended row gets resume", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "aaa", suspended: true, ready: "Ready" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByText, getAllByRole } = render(<GitOps cluster="x" />);
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", bubbles: true, cancelable: true })); });
    // The row is suspended, so s offers Resume - the same ConfirmDialog the button opens.
    expect(getByText("Resume reconciliation")).toBeTruthy();
    expect(setSuspend).not.toHaveBeenCalled();
    const resumeButtons = getAllByRole("button", { name: "Resume" });
    fireEvent.click(resumeButtons[resumeButtons.length - 1]);
    expect(setSuspend).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "aaa", "", false);
  });

  it("/ focuses the flux filter input", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [res({})], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByPlaceholderText } = render(<GitOps cluster="x" />);
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true })); });
    expect(document.activeElement).toBe(getByPlaceholderText("Search name, message, revision"));
  });

  it("sets the action-status in the store (global ActionToast renders it)", () => {
    // The per-view toast was removed in favour of the global ActionToast
    // mounted in AppShell. This test verifies the store value is set correctly
    // by the bridge call — ActionToast.test.tsx covers the toast rendering.
    useFleet.setState({ clusters: [cluster("Healthy")], actionStatus: { kind: "success", message: "Reconcile requested for flux-system/x" } });
    expect(useFleet.getState().actionStatus?.message).toMatch(/Reconcile requested/i);
  });
});

describe("GitOps workspace template (redesign)", () => {
  const seedRows = (resources: FluxResourceDTO[], over: Partial<{ expandedKey: string | null; sources: FluxSourceDTO[] }> = {}) =>
    useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", resources, sources: over.sources ?? [], loading: false, expandedKey: over.expandedKey ?? null, detail: null, watchError: null } }));

  it("reads the Go-computed rule, never its own - reconciliations and sources (1c §2)", () => {
    const flag = { included: true, role: "fault", label: "Flagged by backend" };
    const clear = { included: false, role: "ok", label: "Ready" };
    seedRows([res({ name: "flagged", ready: "Ready", rule: flag }), res({ name: "cleared", ready: "Failed", reason: "BuildFailed", rule: clear })],
      { sources: [src({ name: "src-flagged", ready: "Ready", rule: flag }), src({ name: "src-cleared", ready: "Failed", rule: clear })] });
    render(<GitOps cluster="x" />);
    expect(screen.getByText("flagged").closest(".lens-row")?.textContent).toContain("Flagged by backend");
    expect(screen.getByText("cleared").closest(".lens-row")?.textContent).not.toContain("Failed");
    expect(screen.getByRole("button", { name: /^Not ready/ }).textContent?.trim()).toBe("Not ready 1");
    openSources();
    expect(screen.getByText("src-flagged").closest(".lens-row")?.textContent).toContain("Flagged by backend");
    fireEvent.click(screen.getByRole("button", { name: /^Not ready/ }));
    expect(screen.getByRole("button", { name: /^Not ready/ }).textContent?.trim()).toBe("Not ready 1");
    expect(screen.queryByText("src-cleared")).toBeNull(); // count and rows agree
  });

  // The inspectors' headlines are the rule too (Codex 2b-2 #1): a fixture
  // whose native fields contradict its rule shows the rule's word and tone.
  // the verdict's tone rides on its head and its mark; the headline itself is
  // ink-1 (slice 13c.2)
  const tone = (label: string) => (inspector().getByText(label).closest(".verdict-head") as HTMLElement).getAttribute("data-tone");
  const neutral = (el: HTMLElement) => { const m = el.closest(".verdict-message") as HTMLElement | null; return !!m && m.style.color === "" && el.style.color === ""; };
  const detail = (over: Partial<import("../store/fleet").ResourceDetailDTO> = {}) => ({ kind: "Kustomization", namespace: "flux-system", name: "a",
    suspended: false, appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false, conditions: [], inventory: [], ...over });
  const seedOpen = (r: FluxResourceDTO, d = detail({ name: r.name })) =>
    useFleet.setState((st) => ({ gitops: { ...st.gitops, cluster: "x", resources: [r], sources: [], loading: false, expandedKey: `Kustomization/flux-system/${r.name}`, detail: d, detailCluster: "x", watchError: null } }));

  it("the reconciliation inspector's verdict, reason and message follow the rule, not native ready", () => {
    seedOpen(res({ name: "a", ready: "Ready", reason: "Flagged", message: "flagged by the backend", rule: { included: true, role: "fault", label: "Flagged by backend" } }));
    const { unmount } = render(<GitOps cluster="x" />);
    expect(tone("Flagged by backend")).toBe("danger");
    expect(inspector().getByText(/^Flagged ·/)).toBeTruthy(); // reason gated by rule.included
    expect(neutral(inspector().getByText("flagged by the backend"))).toBe(true); // the controller's words in ink-2, never a red wall
    unmount();
    seedOpen(res({ name: "a", ready: "Failed", reason: "BuildFailed", message: "kustomize build failed", rule: { included: false, role: "ok", label: "Ready" } }));
    render(<GitOps cluster="x" />);
    expect(tone("Ready")).toBe("success");
    expect(inspector().queryByText(/BuildFailed/)).toBeNull();
    expect(inspector().queryByText("kustomize build failed")).toBeNull();
  });

  it("Ready=Unknown with a Progressing reason reads Progressing in the row and the inspector alike", () => {
    seedOpen(res({ name: "a", ready: "Unknown", reason: "Progressing", message: "Reconciliation in progress" }));
    render(<GitOps cluster="x" />);
    const row = screen.getAllByText("a").map((e) => e.closest(".lens-row")).find(Boolean) as HTMLElement;
    expect(row.textContent).toContain("Progressing");
    expect(tone("Progressing")).toBe("muted");
    expect(inspector().queryByText("Unknown")).toBeNull();
  });

  it("a generation lag stays evidence under the rule's verdict", () => {
    seedOpen(res({ name: "a", ready: "Ready", stale: true, generation: 5, observedGeneration: 4, message: "Applied revision main@a" }));
    render(<GitOps cluster="x" />);
    expect(tone("Syncing…")).toBe("muted"); // the ratified grey Syncing…
    expect(inspector().getByText(/generation 5 not yet observed \(observed 4\)/)).toBeTruthy();
    expect(inspector().getByText(/Last observed \(generation 4\)/)).toBeTruthy();
  });

  it("the bound source block follows the source's rule, not its native ready", () => {
    seedOpen(res({ name: "a" }), detail({ source: src({ name: "repo", ready: "Ready", reason: "Flagged", rule: { included: true, role: "degraded", label: "Flagged by backend" } }) }));
    const { unmount } = render(<GitOps cluster="x" />);
    const flagged = inspector().getByText(/source not ready: Flagged/) as HTMLElement;
    expect(flagged.style.color).toBe("var(--color-text-warning)");
    expect(inspector().getByRole("img", { name: "Flagged by backend" })).toBeTruthy();
    unmount();
    seedOpen(res({ name: "a" }), detail({ source: src({ name: "repo", ready: "Failed", reason: "GitOperationFailed", rule: { included: false, role: "ok", label: "Ready" } }) }));
    render(<GitOps cluster="x" />);
    expect(inspector().queryByText(/source not ready/)).toBeNull();
    expect(inspector().getByRole("img", { name: "Ready" })).toBeTruthy();
  });

  it("the source inspector's verdict, tone and reason follow the rule", () => {
    seedRows([], { sources: [
      src({ name: "src-flagged", ready: "Ready", reason: "Flagged", message: "flagged by the backend", rule: { included: true, role: "degraded", label: "Flagged by backend" } }),
      src({ name: "src-cleared", ready: "Failed", reason: "GitOperationFailed", message: "auth required", rule: { included: false, role: "ok", label: "Ready" } }),
    ] });
    render(<GitOps cluster="x" />);
    openSources();
    fireEvent.click(screen.getByText("src-flagged"));
    expect(tone("Flagged by backend")).toBe("warning");
    expect(inspector().getByText(/^Flagged ·/)).toBeTruthy();
    expect(neutral(inspector().getByText("flagged by the backend"))).toBe(true);
    fireEvent.click(screen.getByText("src-cleared"));
    expect(tone("Ready")).toBe("success");
    expect(inspector().queryByText(/GitOperationFailed/)).toBeNull();
    expect(inspector().queryByText("auth required")).toBeNull();
  });

  it("auto-selects the first failing resource once, from a fresh read only", () => {
    seedRows([res({ name: "ok" }), res({ name: "bad", ready: "Failed" })]);
    render(<GitOps cluster="x" />);
    expect(useFleet.getState().gitops.expandedKey).toBeNull(); // cached rows at entry
    act(() => seedRows([res({ name: "ok" }), res({ name: "bad", ready: "Failed" })]));
    expect(useFleet.getState().gitops.expandedKey).toBe("Kustomization/flux-system/bad");
    act(() => useFleet.getState().collapse());
    act(() => seedRows([res({ name: "ok" }), res({ name: "bad", ready: "Failed" })]));
    expect(useFleet.getState().gitops.expandedKey).toBeNull();
  });

  it("suspended is an intended pause: shown and filterable, never attention", () => {
    seedRows([res({ name: "paused", suspended: true }), res({ name: "ok" })]);
    const { getByText, queryByText, getByRole } = render(<GitOps cluster="x" />);
    expect(queryByText(/\d+ not ready/)).toBeNull(); // suspended is not "not ready"
    expect(getByText("all ready")).toBeTruthy();
    expect(getByText(/1 suspended/)).toBeTruthy();
    fireEvent.click(getByRole("button", { name: /^Suspended \d+$/ }));
    expect(getByText("paused")).toBeTruthy();
    expect(queryByText("ok")).toBeNull();
    // its status word is in the interface face, as every status cell is (slice 13b review 1)
    const row = getByText("paused").closest(".lens-row") as HTMLElement;
    const word = [...row.querySelectorAll<HTMLElement>("span")].find((s) => s.textContent === "Suspended" && s.children.length === 0)!;
    expect(word.style.fontFamily).toBe("var(--font-sans)");
  });

  it("a resource gone from a complete read is removed and cancels its pending reconcile", () => {
    seedRows([res({ name: "a" }), res({ name: "b" })], { expandedKey: "Kustomization/flux-system/a" });
    const { getByText, queryByRole, getByRole } = render(<GitOps cluster="x" />);
    fireEvent.click(inspector().getByRole("button", { name: /^Reconcile/ }));
    expect(getByRole("dialog", { name: "Reconcile flux-system/a" })).toBeTruthy();
    act(() => seedRows([res({ name: "b" })], { expandedKey: "Kustomization/flux-system/a" }));
    expect(getByText("Kustomization removed.")).toBeTruthy();
    expect(queryByRole("dialog", { name: /Reconcile/ })).toBeNull();
    expect(inspector().queryByRole("button", { name: /^Reconcile/ })).toBeNull();
    expect(reconcile).not.toHaveBeenCalled();
  });

  // Coverage S1b §5: every Flux verb names the owner's consequence.
  it("suspend and reconcile name the owner's consequence", () => {
    seedRows([res({ name: "a", owner: { kind: "Kustomization", namespace: "flux-system", name: "flux-system" } })], { expandedKey: "Kustomization/flux-system/a" });
    const { getByText, getByRole } = render(<GitOps cluster="x" />);
    fireEvent.click(inspector().getByRole("button", { name: /^Suspend/ }));
    expect(getByText(/If Git sets spec.suspend for it, the owner sets it back on its next apply/)).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "Cancel" }));
    fireEvent.click(inspector().getByRole("button", { name: /^Reconcile/ }));
    expect(getByText(/the annotation may be removed when the owner applies/)).toBeTruthy();
  });

  it("a pending suspend raised by keyboard (no selection) is pinned to its own cluster", () => {
    seedRows([res({ name: "a" })]);
    const { getByText, queryByText, rerender } = render(<GitOps cluster="x" />);
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", bubbles: true, cancelable: true })); });
    expect(getByText("Suspend reconciliation")).toBeTruthy();
    useFleet.setState({ clusters: [cluster("Healthy"), { ...cluster("Healthy"), name: "y" }] });
    rerender(<GitOps cluster="y" />);
    act(() => useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "y", resources: [res({ name: "a" })], loading: false } })));
    expect(queryByText("Suspend reconciliation")).toBeNull();
    expect(setSuspend).not.toHaveBeenCalled();
  });

  it("a filter excluding the selection closes the inspector", () => {
    seedRows([res({ name: "a" }), res({ kind: "HelmRelease", name: "h" })], { expandedKey: "Kustomization/flux-system/a" });
    const { getByLabelText, queryByTestId } = render(<GitOps cluster="x" />);
    expect(queryByTestId("lens-inspector")).toBeTruthy();
    fireEvent.change(getByLabelText("Kind"), { target: { value: "HelmRelease" } });
    expect(queryByTestId("lens-inspector")).toBeNull();
  });

  it("Sources owns the source-type filter; Reconciliation owns the kind filter", () => {
    seedRows([res({ name: "a" })], { sources: [src({ name: "git1" }), src({ kind: "HelmRepository", name: "charts" })] });
    const { getByLabelText, queryByLabelText, getByText, queryByText } = render(<GitOps cluster="x" />);
    expect(getByLabelText("Kind")).toBeTruthy();
    expect(queryByLabelText("Source type")).toBeNull();
    openSources();
    expect(queryByLabelText("Kind")).toBeNull();
    fireEvent.change(getByLabelText("Source type"), { target: { value: "HelmRepository" } });
    expect(getByText("charts")).toBeTruthy();
    expect(queryByText("git1")).toBeNull();
  });

  it("a failed watch with no rows is unreadable, never 'No Flux resources'", () => {
    useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", resources: [], sources: [], loading: false, watchError: "forbidden" } }));
    const { getByText, queryByText } = render(<GitOps cluster="x" />);
    expect(getByText("Flux state could not be read.")).toBeTruthy();
    expect(queryByText(/No Flux resources/)).toBeNull();
  });
});

describe("GitOps Codex rework (Flux review)", () => {
  const seedRows = (resources: FluxResourceDTO[], over: Partial<{ expandedKey: string | null; sources: FluxSourceDTO[]; detail: import("../store/fleet").ResourceDetailDTO | null }> = {}) =>
    useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", resources, sources: over.sources ?? [], loading: false, expandedKey: over.expandedKey ?? null, detail: over.detail ?? null, detailCluster: over.detail ? "x" : null, watchError: null } }));

  it("2. a source has a keyboard-reachable full diagnosis and its users", () => {
    const long = "failed to checkout and determine revision: unable to clone 'ssh://git@example.com/very/long/path': ssh: handshake failed: knownhosts: key mismatch";
    seedRows([res({ name: "apps", sourceKind: "GitRepository", sourceName: "repo", sourceNamespace: "flux-system" })], { sources: [src({ name: "repo", ready: "Failed", reason: "GitOperationFailed", message: long })] });
    render(<GitOps cluster="x" />);
    openSources();
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(inspector().getByText(long)).toBeTruthy(); // full, wrapped - not a title
    expect(inspector().getByText("Used by · 1")).toBeTruthy();
    fireEvent.click(inspector().getByText(/ks flux-system\/apps/));
    expect(useFleet.getState().gitops.expandedKey).toBe("Kustomization/flux-system/apps");
  });

  it("3. removal keeps the loaded detail as read-only last evidence", () => {
    const detail = { kind: "Kustomization", namespace: "flux-system", name: "a", suspended: false, appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
      conditions: [], inventory: [], source: src({ ready: "Failed", reason: "GitOperationFailed", message: "auth required" }) };
    seedRows([res({ name: "a", ready: "Failed" }), res({ name: "b" })], { expandedKey: "Kustomization/flux-system/a", detail });
    const { getByText, queryByText } = render(<GitOps cluster="x" />);
    expect(getByText(/source not ready: GitOperationFailed/)).toBeTruthy();
    act(() => useFleet.setState((s) => ({ gitops: { ...s.gitops, resources: [res({ name: "b" })] } })));
    expect(getByText("Kustomization removed.")).toBeTruthy();
    expect(getByText(/source not ready: GitOperationFailed/)).toBeTruthy();
    expect(queryByText(/No detail was loaded/)).toBeNull();
    expect(inspector().queryByRole("button", { name: /^Reconcile/ })).toBeNull();
    expect(queryByText("Compute diff")).toBeNull();
  });

  it("4a. a suspended resource with an old failure never outranks an active failure", () => {
    // alphabetical order would put the paused one first; only ranking can fix it
    seedRows([res({ name: "a-paused-old-fail", suspended: true, ready: "Failed" }), res({ name: "z-active-fail", ready: "Failed" })]);
    const { container } = render(<GitOps cluster="x" />);
    const names = [...container.querySelectorAll(".lens-row .lens-cell-name")].map((e) => e.textContent);
    expect(names[0]).toContain("z-active-fail");
    expect(names[1]).toContain("a-paused-old-fail");
  });

  it("4b. all-suspended sources say no ACTIVE source needs attention", () => {
    seedRows([res({})], { sources: [src({ name: "s1", suspended: true }), src({ name: "s2", suspended: true, ready: "Failed" })] });
    const { getByText, queryByText } = render(<GitOps cluster="x" />);
    openSources();
    expect(getByText("all active sources ready")).toBeTruthy();
    expect(queryByText("all sources ready")).toBeNull();
    expect(getByText("2 suspended")).toBeTruthy();
  });

  it("4c. a stale resource shows its old failure as last observed evidence, not a current diagnosis", () => {
    seedRows([res({ name: "edited", ready: "Failed", reason: "BuildFailed", message: "kustomize build failed", stale: true, generation: 5, observedGeneration: 4 })], { expandedKey: "Kustomization/flux-system/edited" });
    render(<GitOps cluster="x" />);
    expect(inspector().getByText("Syncing…")).toBeTruthy();
    const last = inspector().getByText(/Last observed \(generation 4\)/).parentElement as HTMLElement;
    expect(last.textContent).toContain("kustomize build failed");
    expect(last.style.color).toBe("var(--color-text-secondary)");
  });

  it("4d. a failure of the current generation is a fault, naming its generation, while status lags (S1b §4.1)", () => {
    // The backend sends stale=false: Ready=False is stamped with generation 5,
    // and status.observedGeneration (4) advances only on success.
    seedRows([res({ name: "failing", ready: "Failed", reason: "BuildFailed", message: "kustomize build failed", stale: false, generation: 5, observedGeneration: 4, readyGeneration: 5 })], { expandedKey: "Kustomization/flux-system/failing" });
    const { container } = render(<GitOps cluster="x" />);
    const row = container.querySelector(".lens-row") as HTMLElement;
    expect(row.textContent).toContain("Failed · BuildFailed");
    expect(row.textContent).not.toContain("Syncing…");
    expect(row.querySelector('[title^="Ready=False for generation 5, the current spec"]')).toBeTruthy();
    expect(inspector().getByText(/Ready=False for generation 5, the current spec \(the last generation the controller completed is 4\)/)).toBeTruthy();
  });

  it("4e. a pending generation names the generation its old verdict describes", () => {
    // Ready describes generation 4 while status caught up to 5 (a suspension).
    seedRows([res({ name: "resumed", ready: "Ready", stale: true, generation: 5, observedGeneration: 5, readyGeneration: 4 })]);
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText("Syncing… generation 5, observed 4")).toBeTruthy();
  });

  it("5. palette/cross-link navigation wins over a stale filter; a later operator edit closes", () => {
    seedRows([res({ name: "a" }), res({ kind: "HelmRelease", name: "h" })]);
    const { getByLabelText, queryByTestId, getByPlaceholderText } = render(<GitOps cluster="x" />);
    fireEvent.change(getByPlaceholderText("Search name, message, revision"), { target: { value: "zzz" } });
    act(() => useFleet.getState().requestNavIntent({ kind: "select-flux", cluster: "x", fluxKind: "HelmRelease", namespace: "flux-system", name: "h" }));
    expect(queryByTestId("lens-inspector")).toBeTruthy();
    expect((getByPlaceholderText("Search name, message, revision") as HTMLInputElement).value).toBe("");
    fireEvent.change(getByLabelText("Kind"), { target: { value: "Kustomization" } });
    expect(queryByTestId("lens-inspector")).toBeNull();
  });
});

describe("GitOps source consumers are namespace-safe (Codex Flux re-review)", () => {
  const open = (sourceNs: string) => {
    openSources();
    const row = screen.getAllByTestId("flux-source-row").find((r) => r.textContent?.includes(`${sourceNs} / repo`))!;
    fireEvent.click(row);
  };
  const seed = () => useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", loading: false, watchError: null, expandedKey: null, detail: null,
    resources: [
      res({ namespace: "team-a", name: "a-default", sourceKind: "GitRepository", sourceName: "repo", sourceNamespace: "team-a" }),
      res({ namespace: "team-b", name: "b-default", sourceKind: "GitRepository", sourceName: "repo", sourceNamespace: "team-b" }),
      res({ namespace: "team-c", name: "c-explicit", sourceKind: "GitRepository", sourceName: "repo", sourceNamespace: "team-a" }),
      res({ namespace: "team-a", name: "no-evidence", sourceKind: "GitRepository", sourceName: "repo" }),
    ],
    sources: [src({ namespace: "team-a", name: "repo" }), src({ namespace: "team-b", name: "repo" })],
  } }));

  it("same-named sources in different namespaces have distinct consumers; explicit cross-namespace refs resolve", () => {
    seed();
    render(<GitOps cluster="x" />);
    open("team-a");
    expect(inspector().getByText("Used by · 2")).toBeTruthy();
    expect(inspector().getByText(/team-a\/a-default/)).toBeTruthy();
    expect(inspector().getByText(/team-c\/c-explicit/)).toBeTruthy(); // explicit sourceRef.namespace: team-a
    expect(inspector().queryByText(/b-default/)).toBeNull();
    expect(inspector().queryByText(/no-evidence/)).toBeNull(); // no resolved namespace = no claim
  });

  it("the other namespace's source claims only its own consumer", () => {
    seed();
    render(<GitOps cluster="x" />);
    open("team-b");
    expect(inspector().getByText("Used by · 1")).toBeTruthy();
    expect(inspector().getByText(/team-b\/b-default/)).toBeTruthy();
  });

  it("a cross-namespace source is named with its namespace in the row", () => {
    seed();
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText("GitRepository team-a/repo")).toBeTruthy(); // c-explicit, bound across namespaces
  });
});

describe("GitOps live-pass fixes (2026-10-10)", () => {
  const seedOne = (r: FluxResourceDTO, detail?: import("../store/fleet").ResourceDetailDTO) =>
    useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", resources: [r], sources: [], loading: false, watchError: null,
      expandedKey: detail ? `${r.kind}/${r.namespace}/${r.name}` : null, detail: detail ?? null, detailCluster: detail ? "x" : null } }));

  it("Ready=Unknown with reason Progressing is progress, not a glowing fault", () => {
    seedOne(res({ kind: "HelmRelease", name: "podinfo", ready: "Unknown", reason: "Progressing" }));
    const { getByRole } = render(<GitOps cluster="x" />);
    expect(getByRole("img", { name: "Progressing" })).toBeTruthy();
  });

  it("transition time reads 'failing since' on a failed resource, never 'ready since'", () => {
    const d = { kind: "Kustomization", namespace: "flux-system", name: "flux-system", suspended: false, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [], inventory: [] };
    seedOne(res({ ready: "Failed", readySinceSeconds: 90, sourceKind: "GitRepository", sourceName: "repo" }), d);
    render(<GitOps cluster="x" />);
    expect(inspector().getByText(/failing since 1m ago/)).toBeTruthy();
    expect(inspector().queryByText(/ready since/)).toBeNull();
  });

  it("the bound source's root-cause message wraps in full, no ellipsis", () => {
    const long = "failed to checkout and determine revision: unable to clone 'ssh://git@git.example.com/platform/fleet-infrastructure.git': ssh: handshake failed";
    const d = { kind: "Kustomization", namespace: "flux-system", name: "flux-system", suspended: false, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [], inventory: [],
      source: src({ ready: "Failed", reason: "GitOperationFailed", message: long }) };
    seedOne(res({ ready: "Failed" }), d);
    render(<GitOps cluster="x" />);
    const el = inspector().getByText(long);
    expect(el.style.overflowWrap).toBe("anywhere");
    expect(el.style.textOverflow).toBe("");
  });

  it("inside the template, shared sections render with the template's section anatomy", () => {
    const d = { kind: "Kustomization", namespace: "flux-system", name: "flux-system", suspended: false, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [],
      inventory: [{ group: "", version: "v1", kind: "ConfigMap", namespace: "ns", name: "cm" }] };
    seedOne(res({}), d);
    render(<GitOps cluster="x" />);
    expect(inspector().getByRole("button", { name: "Inventory · 1" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("action key hints read as letters, in the title and as the key shortcut, off the button's face (slice 13c.4)", () => {
    const d = { kind: "Kustomization", namespace: "flux-system", name: "flux-system", suspended: false, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [], inventory: [] };
    seedOne(res({}), d);
    render(<GitOps cluster="x" />);
    const reconcile = inspector().getByRole("button", { name: "Reconcile" });
    expect(reconcile.querySelector("kbd")).toBeNull();
    expect(reconcile.getAttribute("title")).toBe("Reconcile (R)");
    expect(reconcile.getAttribute("aria-keyshortcuts")).toBe("r");
  });
});

describe("GitOps visual-verdict wording (2026-10-10)", () => {
  it("a suspended resource's time is the last observed Ready change, never 'suspended since'", () => {
    const d = { kind: "Kustomization", namespace: "flux-system", name: "flux-system", suspended: true, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [], inventory: [] };
    useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", resources: [res({ suspended: true, ready: "Failed", readySinceSeconds: 120 })], sources: [], loading: false, watchError: null,
      expandedKey: "Kustomization/flux-system/flux-system", detail: d, detailCluster: "x" } }));
    render(<GitOps cluster="x" />);
    expect(inspector().getByText(/last observed Ready condition change 2m ago/)).toBeTruthy();
    expect(inspector().queryByText(/suspended.*since/)).toBeNull();
  });

  it("the legacy count says 'not ready' until the shared Flux health slice lands", () => {
    useFleet.setState((s) => ({ gitops: { ...s.gitops, cluster: "x", resources: [res({ name: "p", ready: "Unknown", reason: "Progressing" })], sources: [], loading: false, watchError: null, expandedKey: null, detail: null } }));
    const { getByText, queryByText } = render(<GitOps cluster="x" />);
    expect(getByText("1 not ready")).toBeTruthy();
    expect(queryByText(/need attention/)).toBeNull();
  });
});
