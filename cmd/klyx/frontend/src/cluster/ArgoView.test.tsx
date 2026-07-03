import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";
import { ArgoView, resolveArgoResourceRef } from "./ArgoView";
import { useFleet } from "../store/fleet";
import type { ArgoAppDTO, ArgoResourceDTO } from "../store/fleet";

vi.mock("../bridge/argo", () => ({
  listArgoApps: vi.fn().mockResolvedValue(undefined),
  listArgoProjects: vi.fn().mockResolvedValue(undefined),
  listArgoApplicationSets: vi.fn().mockResolvedValue(undefined),
  refreshArgoApp: vi.fn().mockResolvedValue(undefined),
  syncArgoApp: vi.fn().mockResolvedValue(undefined),
}));
import { listArgoApps, refreshArgoApp, syncArgoApp } from "../bridge/argo";

const healthy: ArgoAppDTO = {
  namespace: "argocd", name: "console-dev", project: "default",
  syncStatus: "Synced", healthStatus: "Healthy", broken: false,
  revision: "abc1234def5678", repoURL: "https://gitlab.com/x/workloads.git",
  path: "apps/console/overlays/dev", chart: "", targetRevision: "main",
  extraSources: 0, destNamespace: "console-dev", autoSync: true,
  opPhase: "Succeeded", opMessage: "", conditions: [],
  resources: [
    { group: "", kind: "Service", namespace: "console-dev", name: "console", status: "Synced", health: "Healthy" },
    { group: "apps", kind: "Deployment", namespace: "console-dev", name: "console", status: "OutOfSync", health: "Progressing" },
  ],
  reconciledUnix: Math.floor(Date.now() / 1000) - 120,
};
const broken: ArgoAppDTO = {
  ...healthy, name: "ollama-prod", syncStatus: "OutOfSync", healthStatus: "Degraded",
  broken: true, destNamespace: "ollama-prod", autoSync: false,
  conditions: [{ type: "ComparisonError", message: "repo unreachable" }],
};

function seed(apps: ArgoAppDTO[], extra: Partial<{ available: boolean; message: string }> = {}) {
  useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", apps, available: extra.available ?? true, message: extra.message ?? "", loading: false } }));
}

describe("ArgoView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearArgo();
    useFleet.getState().clearCRDs();
  });

  it("lists apps in Argo vocabulary with sync + health columns", () => {
    seed([broken, healthy]);
    const { getByText, getAllByText } = render(<ArgoView cluster="homelab-nelli" />);
    expect(getAllByText("OutOfSync").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("Degraded").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("Synced").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("Healthy").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText(/abc1234d/).length).toBeGreaterThanOrEqual(1); // shortened revision
    expect(getByText(/1 need attention/)).toBeTruthy();
  });

  it("unavailable renders the reason, never an empty healthy list", () => {
    seed([], { available: false, message: "Argo CD not detected (no applications.argoproj.io resource)" });
    const { getByText } = render(<ArgoView cluster="homelab-nelli" />);
    expect(getByText(/Argo CD not detected/)).toBeTruthy();
  });

  it("selecting a row shows source, conditions, and the action buttons in the inspector", () => {
    seed([broken]);
    const { getByText, getAllByRole } = render(<ArgoView cluster="homelab-nelli" />);
    expect(getByText(/repo unreachable/)).toBeTruthy();
    expect(getByText(/apps\/console\/overlays\/dev/)).toBeTruthy();
    expect(getByText("refresh app")).toBeTruthy();
    expect(getAllByRole("button", { name: "sync" }).length).toBeGreaterThan(0);
  });

  it("sync opens the Argo operation dialog and dispatches selected options", () => {
    seed([broken]);
    const { getAllByRole, getByLabelText, getByRole, getByText } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getAllByRole("button", { name: "sync" })[0]);
    expect(getByRole("dialog", { name: /Sync Application ollama-prod/ })).toBeTruthy();
    expect(getByText("Prune")).toBeTruthy();
    fireEvent.change(getByLabelText("revision git ref"), { target: { value: "feature/test" } });
    fireEvent.click(getByText("Dry-run"));
    // Prune deletes live resources: it must default OFF and require an
    // explicit opt-in click per sync.
    fireEvent.click(getByText("Prune"));
    fireEvent.click(getByLabelText("select Deployment console-dev/console"));
    fireEvent.click(getByRole("button", { name: "Synchronize" }));
    expect(syncArgoApp).toHaveBeenCalledWith("homelab-nelli", "argocd", "ollama-prod", expect.objectContaining({
      revision: "feature/test",
      strategy: "hook",
      prune: true,
      dryRun: true,
      force: false,
      replace: false,
      serverSideApply: false,
      resources: [expect.objectContaining({ kind: "Deployment", namespace: "console-dev", name: "console" })],
    }));
  });

  it("sync defaults to prune off", () => {
    seed([broken]);
    const { getAllByRole, getByRole } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getAllByRole("button", { name: "sync" })[0]);
    fireEvent.click(getByRole("button", { name: "Synchronize" }));
    expect(syncArgoApp).toHaveBeenCalledWith("homelab-nelli", "argocd", "ollama-prod", expect.objectContaining({
      prune: false,
    }));
  });

  it("refresh flows through ConfirmDialog", () => {
    seed([healthy]);
    const { getByText, getAllByText, getAllByRole } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getAllByText("console-dev")[0]); // name cell (dest ns matches too)
    fireEvent.click(getByText("refresh app"));
    const confirm = getAllByRole("button", { name: "Refresh" });
    fireEvent.click(confirm[confirm.length - 1]);
    expect(refreshArgoApp).toHaveBeenCalledWith("homelab-nelli", "argocd", "console-dev");
  });

  it("fetches on mount", () => {
    render(<ArgoView cluster="homelab-nelli" />);
    expect(listArgoApps).toHaveBeenCalledWith("homelab-nelli");
  });

  // --- Keyboard nav (applications mode only) ---

  it("j/k + Enter selects an app like a click, Esc returns to the broken-first default", () => {
    seed([broken, healthy]);
    const { getByText, queryByText } = render(<ArgoView cluster="homelab-nelli" />);
    // Default inspector selection is the first (broken) app.
    expect(getByText(/repo unreachable/)).toBeTruthy();
    // j → index 0 (ollama-prod); j → index 1 (console-dev); Enter → select it
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    // The inspector now shows console-dev - the same action as clicking its row.
    expect(queryByText(/repo unreachable/)).toBeNull();
    // Esc collapses the explicit selection back to the broken-first default.
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })); });
    expect(getByText(/repo unreachable/)).toBeTruthy();
  });

  it("renders projects as an Argo-native view", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        projects: [{ namespace: "argocd", name: "platform", description: "platform apps", sourceRepos: ["https://gitlab.com/org/repo.git"], destinations: ["in-cluster/apps"], roles: 2, orphanWarn: true }],
        projectsAvailable: true,
        projectsLoading: false,
      },
    }));
    const { getAllByText, getByText } = render(<ArgoView cluster="homelab-nelli" mode="projects" />);
    expect(getAllByText("platform").length).toBeGreaterThanOrEqual(2);
    expect(getAllByText("unused").length).toBeGreaterThanOrEqual(2);
    expect(getByText("no linked Applications loaded")).toBeTruthy();
  });

  it("renders ApplicationSets with generator and generated app context", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        applicationSets: [{
          namespace: "argocd",
          name: "platform-set",
          generators: ["clusters"],
          templateName: "{{name}}-platform",
          project: "platform",
          repoURL: "https://gitlab.com/org/repo.git",
          path: "apps/platform",
          targetRevision: "main",
          destNamespace: "apps",
          generatedApps: 3,
          healthy: false,
          conditions: [{ type: "ErrorOccurred", status: "False", reason: "RenderError", message: "bad template" }],
        }],
        applicationSetsAvailable: true,
        applicationSetsLoading: false,
      },
    }));
    const { getAllByText, getByText } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    expect(getAllByText("platform-set").length).toBeGreaterThanOrEqual(2);
    expect(getByText("clusters")).toBeTruthy();
    expect(getAllByText("3").length).toBeGreaterThanOrEqual(2);
    expect(getAllByText(/RenderError/).length).toBeGreaterThanOrEqual(2);
  });

  it("shows ApplicationSet expected versus actual generated app drift", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        apps: [{ ...healthy, name: "dev-platform", project: "platform", destNamespace: "apps" }],
        applicationSets: [{
          namespace: "argocd",
          name: "platform-set",
          generators: ["clusters"],
          templateName: "{{name}}-platform",
          project: "platform",
          repoURL: "https://gitlab.com/org/repo.git",
          path: "apps/platform",
          targetRevision: "main",
          destNamespace: "apps",
          generatedApps: 2,
          generatedNames: ["dev-platform", "prd-platform"],
          healthy: true,
          conditions: [],
        }],
        applicationSetsAvailable: true,
        applicationSetsLoading: false,
      },
    }));
    const { getByText, getAllByText } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    expect(getByText("expected")).toBeTruthy();
    expect(getAllByText("2").length).toBeGreaterThanOrEqual(2);
    expect(getByText("actual")).toBeTruthy();
    expect(getAllByText("1").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("prd-platform").length).toBeGreaterThanOrEqual(2);
    expect(getByText(/1 missing generated/)).toBeTruthy();
  });

  it("filters applications by attention and query", () => {
    seed([broken, healthy]);
    const { getByRole, getByText, getAllByText, queryByText } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: /needs attention 1/i }));
    expect(getAllByText("ollama-prod").length).toBeGreaterThanOrEqual(1);
    expect(queryByText("console-dev")).toBeNull();
    fireEvent.change(getByRole("textbox", { name: "filter applications" }), { target: { value: "nope" } });
    expect(getByText(/No applications match/)).toBeTruthy();
  });

  it("links ApplicationSets to Applications by owner reference fallback", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        apps: [{ ...healthy, name: "dev-platform", project: "platform", applicationSet: "platform-set" }],
        applicationSets: [{
          namespace: "argocd",
          name: "platform-set",
          generators: ["clusters"],
          templateName: "{{name}}-platform",
          project: "platform",
          repoURL: "https://gitlab.com/org/repo.git",
          path: "apps/platform",
          targetRevision: "main",
          destNamespace: "apps",
          generatedApps: 1,
          healthy: true,
          conditions: [],
        }],
        applicationSetsAvailable: true,
        applicationSetsLoading: false,
      },
    }));
    const { getAllByText, getByText } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    expect(getAllByText("dev-platform").length).toBeGreaterThanOrEqual(1);
    expect(getByText("actual")).toBeTruthy();
  });

  it("resolves common Argo resource rows to generic resource refs", () => {
    const refs: Array<[ArgoResourceDTO, string]> = [
      [{ group: "apps", kind: "Deployment", namespace: "apps", name: "api", status: "Synced", health: "Healthy" }, "deployments"],
      [{ group: "", kind: "Service", namespace: "apps", name: "api", status: "Synced", health: "Healthy" }, "services"],
      [{ group: "gateway.networking.k8s.io", kind: "HTTPRoute", namespace: "apps", name: "api", status: "Synced", health: "Healthy" }, "httproutes"],
      [{ group: "cert-manager.io", kind: "Certificate", namespace: "apps", name: "api-tls", status: "Synced", health: "Healthy" }, "certificates"],
    ];
    for (const [resource, plural] of refs) {
      expect(resolveArgoResourceRef(resource)?.plural).toBe(plural);
    }
    expect(resolveArgoResourceRef({ group: "example.com", kind: "Widget", namespace: "apps", name: "w1", status: "Synced", health: "Healthy" })).toBeNull();
  });

  it("resolves Argo resource rows from the loaded CRD registry before built-in fallback", () => {
    const groups = [{
      group: "example.com",
      category: "Custom",
      kinds: [{ kind: "Widget", plural: "widgets", scope: "Namespaced", version: "v1beta2", operator: "example", shortNames: [] }],
    }];
    expect(resolveArgoResourceRef({ group: "example.com", kind: "Widget", namespace: "apps", name: "w1", status: "Synced", health: "Healthy" }, groups)).toEqual({
      group: "example.com",
      version: "v1beta2",
      plural: "widgets",
      kind: "Widget",
      scope: "Namespaced",
    });
  });

  it("clicking a resolvable Application resource opens generic resource detail", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "homelab-nelli", section: "argo" } });
    seed([healthy]);
    const { getByRole } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: "open Deployment console-dev/console" }));
    const route = useFleet.getState().route;
    expect(route.name === "cluster" && route.section).toBe("resources");
    expect(route.name === "cluster" && route.resource?.kind).toBe("Deployment");
    expect(route.name === "cluster" && route.resource?.plural).toBe("deployments");
    expect(route.name === "cluster" && route.instance).toEqual({ namespace: "console-dev", name: "console" });
  });

  it("leaves unknown Application resources as non-clickable rows", () => {
    seed([{ ...healthy, resources: [{ group: "example.com", kind: "Widget", namespace: "apps", name: "w1", status: "Synced", health: "Healthy" }] }]);
    const { getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" />);
    expect(getByText("Widget")).toBeTruthy();
    expect(getByText("apps/w1")).toBeTruthy();
    expect(queryByRole("button", { name: "open Widget apps/w1" })).toBeNull();
  });

  it("clicking a CRD-backed Application resource opens generic resource detail", () => {
    useFleet.setState((s) => ({
      ...s,
      route: { name: "cluster", cluster: "homelab-nelli", section: "argo" },
      crd: {
        ...s.crd,
        cluster: "homelab-nelli",
        groups: [{
          group: "example.com",
          category: "Custom",
          kinds: [{ kind: "Widget", plural: "widgets", scope: "Namespaced", version: "v1", operator: "example", shortNames: [] }],
        }],
      },
    }));
    seed([{ ...healthy, resources: [{ group: "example.com", kind: "Widget", namespace: "apps", name: "w1", status: "Synced", health: "Healthy" }] }]);
    const { getByRole } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getByRole("button", { name: "open Widget apps/w1" }));
    const route = useFleet.getState().route;
    expect(route.name === "cluster" && route.resource).toEqual({ group: "example.com", version: "v1", plural: "widgets", kind: "Widget", scope: "Namespaced" });
    expect(route.name === "cluster" && route.instance).toEqual({ namespace: "apps", name: "w1" });
  });
});
