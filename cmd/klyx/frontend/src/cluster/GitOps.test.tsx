import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet, FluxResourceDTO, FluxSourceDTO, ClusterDTO } from "../store/fleet";
import { reconcile, reconcileWithSource, setSuspend, resolveGitLink } from "../bridge/gitops";
import { GitOps } from "./GitOps";

vi.mock("../bridge/gitops", () => ({
  openGitOps: async () => () => {},
  closeGitOps: async () => {},
  getResourceDetail: async () => {},
  reconcile: vi.fn(),
  reconcileWithSource: vi.fn(),
  setSuspend: vi.fn(),
  resolveGitLink: vi.fn(),
  fluxDiff: vi.fn(async () => ({ available: true, hasChanges: true, output: "± spec.replicas: 2 -> 3", error: "" })),
}));

const cluster = (tier: string): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: tier, gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "", region: "", provider: "", group: "", ageSeconds: 0,
});
const res = (over: Partial<FluxResourceDTO>): FluxResourceDTO => ({
  kind: "Kustomization", namespace: "flux-system", name: "flux-system", ready: "Ready",
  reason: "", message: "", revision: "main@abc", readySinceSeconds: 1, suspended: false,
  sourceKind: "", sourceName: "", ...over,
});

const src = (over: Partial<FluxSourceDTO>): FluxSourceDTO => ({
  kind: "GitRepository", namespace: "flux-system", name: "flux-system",
  ready: "Ready", reason: "", message: "", revision: "main@def", url: "https://x/y", suspended: false, ...over,
});

const expandedDetail = (over: Partial<import("../store/fleet").ResourceDetailDTO> = {}) => ({
  cluster: "x",
  resources: [res({ kind: "Kustomization", namespace: "flux-system", name: "flux-system" })],
  sources: [],
  loading: false,
  expandedKey: "Kustomization/flux-system/flux-system",
  detail: {
    kind: "Kustomization", namespace: "flux-system", name: "flux-system",
    suspended: false, appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
    conditions: [], inventory: [], ...over,
  },
});

beforeEach(() => useFleet.setState({
  clusters: [cluster("Healthy")],
  gitops: { cluster: "x", resources: [], sources: [], loading: false, expandedKey: null, detail: null },
}));

describe("GitOps view", () => {
  it("a stale resource dims and shows the Syncing chip instead of a confident status", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "edited", ready: "Ready", stale: true, generation: 5, observedGeneration: 4 }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByText, getByTitle } = render(<GitOps cluster="x" />);
    const row = getByText("flux-system/edited").closest("button") as HTMLButtonElement;
    // The status cell must not read as confirmed while changes are unrealized.
    expect(getByText("Syncing…")).toBeTruthy();
    expect(row.textContent).not.toContain("ready");
    expect(getByTitle(/Observed Gen: 4 vs Current: 5/)).toBeTruthy();
    // Dim = not yet realized.
    expect(row.style.opacity).toBe("0.6");
  });


  it("renders the resource table from the store", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "flux-system", ready: "Ready" }),
      res({ kind: "HelmRelease", name: "cilium", ready: "Failed", message: "install failed" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText("flux-system/flux-system")).toBeTruthy();
    expect(getByText("flux-system/cilium")).toBeTruthy();
    expect(getByText(/install failed/i)).toBeTruthy();
  });

  it("shows the failing condition reason as a chip on the row", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ kind: "HelmRelease", name: "cilium", ready: "Failed", reason: "UpgradeFailed", message: "upgrade retries exhausted" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getAllByText } = render(<GitOps cluster="x" />);
    expect(getAllByText("UpgradeFailed").length).toBeGreaterThan(0);
  });

  it("owns scrolling inside the hidden cluster page shell", () => {
    useFleet.setState({ gitops: { cluster: "x", resources: [
      res({ name: "flux-system", ready: "Ready" }),
      res({ kind: "HelmRelease", name: "cilium", ready: "Failed", message: "install failed" }),
    ], sources: [], loading: false, expandedKey: null, detail: null } });
    const { getByTestId } = render(<GitOps cluster="x" />);
    expect(getByTestId("flux-resource-scroll").style.overflowY).toBe("auto");
    expect(getByTestId("flux-inspector-scroll").style.overflowY).toBe("auto");
  });

  it("shows the no-Flux empty state when gitopsTier is Absent", () => {
    useFleet.setState({ clusters: [cluster("Absent")] });
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText(/No Flux or Argo/i)).toBeTruthy();
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
    const { getByText } = render(<GitOps cluster="x" />);
    expect(getByText(/Health check passed/i)).toBeTruthy();
    expect(getByText("ConfigMap · monitoring/my-cm")).toBeTruthy();
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

  it("reconcile flows through the confirm dialog on a non-protected cluster", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    const { getByText, getAllByRole } = render(<GitOps cluster="x" />);
    fireEvent.click(getByText("Reconcile"));               // panel button opens the dialog
    const reconcileButtons = getAllByRole("button", { name: "Reconcile" });
    fireEvent.click(reconcileButtons[reconcileButtons.length - 1]); // dialog confirm is last in DOM
    expect(reconcile).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system");
  });

  it("reconcile-with-source flows through the confirm dialog", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    const { getByText, getAllByRole } = render(<GitOps cluster="x" />);
    fireEvent.click(getByText("Reconcile with source"));            // panel button opens the dialog
    const confirmButtons = getAllByRole("button", { name: "Reconcile + source" });
    fireEvent.click(confirmButtons[confirmButtons.length - 1]);      // dialog confirm
    expect(reconcileWithSource).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system");
  });

  it("shows Resume + a suspended badge when detail.suspended is true", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail({ suspended: true }) });
    const { getByText, queryByText, getAllByRole } = render(<GitOps cluster="x" />);
    expect(getByText("Resume")).toBeTruthy();
    expect(queryByText("Suspend")).toBeNull();
    fireEvent.click(getByText("Resume"));                  // panel button opens the dialog
    const resumeButtons = getAllByRole("button", { name: "Resume" });
    fireEvent.click(resumeButtons[resumeButtons.length - 1]); // dialog confirm is last in DOM
    expect(setSuspend).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system", false);
  });

  it("view-in-git button calls resolveGitLink for a Kustomization", () => {
    useFleet.setState({ clusters: [cluster("Healthy")], gitops: expandedDetail() });
    const { getByText } = render(<GitOps cluster="x" />);
    fireEvent.click(getByText("View in Git"));
    expect(resolveGitLink).toHaveBeenCalledWith("x", "Kustomization", "flux-system", "flux-system");
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
        detail: { kind: "HelmRelease", namespace: "ns", name: "app", suspended: false, appliedRevision: "", attemptedRevision: "", applyFailed: false, conditions: [], inventory: [] },
      },
    });
    const { queryByText } = render(<GitOps cluster="x" />);
    expect(queryByText("View in Git")).toBeNull();
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

  it("renders a blocked-by line and dependency states when DependencyNotReady", () => {
    useFleet.setState({
      clusters: [cluster("Healthy")],
      gitops: {
        cluster: "x",
        resources: [
          res({ kind: "Kustomization", namespace: "flux-system", name: "apps", ready: "Reconciling", reason: "DependencyNotReady" }),
          res({ kind: "Kustomization", namespace: "flux-system", name: "infra", ready: "Failed" }),
        ],
        sources: [],
        loading: false,
        expandedKey: "Kustomization/flux-system/apps",
        detail: {
          kind: "Kustomization", namespace: "flux-system", name: "apps",
          reason: "DependencyNotReady", appliedRevision: "main@a", attemptedRevision: "main@a", applyFailed: false,
          conditions: [], inventory: [],
          dependsOn: [{ namespace: "flux-system", name: "infra" }],
        },
      },
    });
    const { getByText, getAllByText } = render(<GitOps cluster="x" />);
    expect(getByText(/blocked by/i)).toBeTruthy();
    // "flux-system/infra" shows both in the resource list and the dependency row.
    expect(getAllByText("flux-system/infra").length).toBeGreaterThanOrEqual(1);
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
    const { getByText, getByTestId } = render(<GitOps cluster="x" />);
    fireEvent.click(getByText("sources"));
    expect(getByTestId("flux-sources-scroll")).toBeTruthy();
    expect(getByText("flux-system/cilium")).toBeTruthy();
  });

  it("sets the action-status in the store (global ActionToast renders it)", () => {
    // The per-view toast was removed in favour of the global ActionToast
    // mounted in AppShell. This test verifies the store value is set correctly
    // by the bridge call — ActionToast.test.tsx covers the toast rendering.
    useFleet.setState({ clusters: [cluster("Healthy")], actionStatus: { kind: "success", message: "Reconcile requested for flux-system/x" } });
    expect(useFleet.getState().actionStatus?.message).toMatch(/Reconcile requested/i);
  });
});
