import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, screen, within } from "@testing-library/react";
import { ArgoView, resolveArgoResourceRef } from "./ArgoView";
import { useFleet } from "../store/fleet";
import type { ArgoAppDTO, ArgoApplicationSetDTO, ArgoResourceDTO } from "../store/fleet";

// The rows hook is the transport for Applications and ApplicationSets; these
// tests seed the argo slice it fills. Projects stay a fetch.
vi.mock("../health/useArgoRows", () => ({ useArgoRows: () => {} }));
vi.mock("../bridge/argo", () => ({
  listArgoProjects: vi.fn().mockResolvedValue(undefined),
  refreshArgoApp: vi.fn().mockResolvedValue(undefined),
  syncArgoApp: vi.fn().mockResolvedValue(undefined),
}));
import { listArgoProjects, refreshArgoApp, syncArgoApp } from "../bridge/argo";
import { argoAppRule, argoAppSetRule } from "../test/backendRules";

// Fixtures enter the store with the rule the backend computes
// (test/backendRules.ts, held to the Go golden fixture). A fixture that sets
// its own rule keeps it (contradiction tests).
type NoRule<T> = Omit<T, "rule"> & { rule?: ArgoAppDTO["rule"] };
const ruledApps = (xs: NoRule<ArgoAppDTO>[]): ArgoAppDTO[] => xs.map((a) => ({ ...a, rule: a.rule ?? argoAppRule(a) }));
const ruledSets = (xs: NoRule<ArgoApplicationSetDTO>[]): ArgoApplicationSetDTO[] => xs.map((a) => ({ ...a, rule: a.rule ?? argoAppSetRule(a) }));

const healthy: NoRule<ArgoAppDTO> = {
  namespace: "argocd", name: "console-dev", project: "default",
  syncStatus: "Synced", healthStatus: "Healthy", broken: false,
  revision: "abc1234def5678", repoURL: "https://gitlab.com/x/workloads.git",
  path: "apps/console/overlays/dev", chart: "", targetRevision: "main",
  // affirmative in-cluster evidence: the inventory is on this cluster
  extraSources: 0, destNamespace: "console-dev", destServer: "https://kubernetes.default.svc", autoSync: true,
  opPhase: "Succeeded", opMessage: "", conditions: [],
  resources: [
    { group: "", kind: "Service", namespace: "console-dev", name: "console", status: "Synced", health: "Healthy" },
    { group: "apps", kind: "Deployment", namespace: "console-dev", name: "console", status: "OutOfSync", health: "Progressing" },
  ],
  reconciledUnix: Math.floor(Date.now() / 1000) - 120,
};
const broken: NoRule<ArgoAppDTO> = {
  ...healthy, name: "ollama-prod", syncStatus: "OutOfSync", healthStatus: "Degraded",
  broken: true, destNamespace: "ollama-prod", autoSync: false,
  conditions: [{ type: "ComparisonError", message: "repo unreachable" }],
};

function seed(apps: NoRule<ArgoAppDTO>[], extra: Partial<{ available: boolean; message: string }> = {}) {
  useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", apps: ruledApps(apps), available: extra.available ?? true, message: extra.message ?? "", loading: false } }));
}

// The row is the first match: once selected, the inspector repeats the name.
function select(name: string) { fireEvent.click(screen.getAllByText(name)[0]); }
const inspector = () => within(screen.getByTestId("lens-inspector"));

describe("ArgoView", () => {
  it("reads the Go-computed rule, never its own - applications and ApplicationSets (1c §2)", () => {
    const flag = { included: true, role: "fault", label: "Flagged by backend" };
    const clear = { included: false, role: "ok", label: "Healthy" };
    seed([{ ...healthy, name: "flagged", rule: flag }, { ...broken, name: "cleared", rule: clear }]);
    const { unmount } = render(<ArgoView cluster="homelab-nelli" />);
    // Sync and health columns are Argo's native evidence; the verdict is the lamp.
    expect(within(screen.getByText("flagged").closest(".lens-row") as HTMLElement).getByRole("img", { name: "Flagged by backend" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Attention/ }));
    expect(screen.getByRole("button", { name: /^Attention/ }).textContent?.trim()).toBe("Attention 1");
    expect(screen.queryByText("cleared")).toBeNull();
    unmount();
    const set = { namespace: "argocd", generators: ["list"], templateName: "", project: "default", repoURL: "", path: "", targetRevision: "", destNamespace: "", generatedApps: 0, generatedNames: [], conditions: [] };
    useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", applicationSetsAvailable: true, applicationSetsLoading: false,
      applicationSets: ruledSets([{ ...set, name: "set-flagged", healthy: true, rule: { included: true, role: "degraded", label: "Flagged by backend" } },
        { ...set, name: "set-cleared", healthy: false, rule: { included: false, role: "ok", label: "healthy" } }]) } }));
    render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    fireEvent.click(screen.getByRole("button", { name: /^Attention/ }));
    expect(screen.getByRole("button", { name: /^Attention/ }).textContent?.trim()).toBe("Attention 1");
    expect(screen.getByText("set-flagged")).toBeTruthy();
    expect(screen.queryByText("set-cleared")).toBeNull();
  });

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
    // the row's mark carries the shape (The Line slice 11a): no ▲ in the cell
    const degraded = [...document.querySelectorAll(".lens-row")].find((r) => r.textContent?.includes("Degraded")) as HTMLElement;
    expect(degraded.querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(degraded.textContent).not.toContain("▲");
    expect(getAllByText("Synced").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("Healthy").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText(/abc1234d/).length).toBeGreaterThanOrEqual(1); // shortened revision
    expect(getByText(/1 out of sync or unhealthy/)).toBeTruthy(); // backend broken(), worded precisely
  });

  it("unavailable renders the reason, never an empty healthy list", () => {
    seed([], { available: false, message: "Argo CD not detected (no applications.argoproj.io resource)" });
    const { getByText } = render(<ArgoView cluster="homelab-nelli" />);
    expect(getByText(/Argo CD not detected/)).toBeTruthy();
  });

  it("selecting a row shows source, conditions, and the action band in the inspector", () => {
    seed([broken]);
    render(<ArgoView cluster="homelab-nelli" />);
    select("ollama-prod");
    expect(inspector().getByText(/repo unreachable/)).toBeTruthy();
    expect(inspector().getByText(/apps\/console\/overlays\/dev/)).toBeTruthy();
    expect(inspector().getByRole("button", { name: /^Refresh/ })).toBeTruthy(); // primary (spec §3)
    expect(inspector().getByRole("button", { name: /^Sync/ })).toBeTruthy();    // safeguarded secondary
  });

  it("sync opens the Argo operation dialog and dispatches selected options", () => {
    seed([broken]);
    const { getByLabelText, getByRole, getByText } = render(<ArgoView cluster="homelab-nelli" />);
    select("ollama-prod");
    fireEvent.click(inspector().getByRole("button", { name: /^Sync/ }));
    expect(getByRole("dialog", { name: /Sync Application ollama-prod/ })).toBeTruthy();
    expect(getByText("Prune")).toBeTruthy();
    fireEvent.change(getByLabelText("revision git ref"), { target: { value: "feature/test" } });
    fireEvent.click(getByText("Dry-run"));
    // Prune deletes live resources: it must default OFF and require an
    // explicit opt-in click per sync.
    fireEvent.click(getByText("Prune"));
    fireEvent.click(getByLabelText("select Deployment console-dev/console"));
    fireEvent.click(getByRole("button", { name: "Synchronize" }));
    expect(syncArgoApp).toHaveBeenCalledWith("homelab-nelli", "argocd", "ollama-prod", "", expect.objectContaining({
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
    const { getByRole } = render(<ArgoView cluster="homelab-nelli" />);
    select("ollama-prod");
    fireEvent.click(inspector().getByRole("button", { name: /^Sync/ }));
    fireEvent.click(getByRole("button", { name: "Synchronize" }));
    expect(syncArgoApp).toHaveBeenCalledWith("homelab-nelli", "argocd", "ollama-prod", "", expect.objectContaining({
      prune: false,
    }));
  });

  it("refresh flows through ConfirmDialog", () => {
    seed([healthy]);
    const { getAllByText, getAllByRole } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.click(getAllByText("console-dev")[0]); // name cell (dest ns matches too)
    fireEvent.click(inspector().getByRole("button", { name: /^Refresh/ }));
    const confirm = getAllByRole("button", { name: "Refresh" });
    fireEvent.click(confirm[confirm.length - 1]);
    expect(refreshArgoApp).toHaveBeenCalledWith("homelab-nelli", "argocd", "console-dev", ""); // uid-less fixture
  });

  it("fetches the projects on mount (Applications and ApplicationSets are rows)", () => {
    render(<ArgoView cluster="homelab-nelli" />);
    expect(listArgoProjects).toHaveBeenCalledWith("homelab-nelli");
  });

  // --- Keyboard nav (applications mode only) ---

  it("j/k + Enter selects an app like a click, Esc closes the inspector", () => {
    seed([broken, healthy]);
    const { queryByTestId } = render(<ArgoView cluster="homelab-nelli" />);
    // j → index 0 (ollama-prod); j → index 1 (console-dev); Enter → select it
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(inspector().getByRole("heading", { name: "console-dev" })).toBeTruthy();
    expect(inspector().queryByText(/repo unreachable/)).toBeNull();
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })); });
    expect(queryByTestId("lens-inspector")).toBeNull();
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
    select("platform");
    expect(getAllByText("platform").length).toBeGreaterThanOrEqual(2);
    expect(getAllByText("unused").length).toBeGreaterThanOrEqual(2);
    expect(getByText("no linked Applications loaded")).toBeTruthy();
  });

  it("renders ApplicationSets with generator and generated app context", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        applicationSets: ruledSets([{
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
          // ErrorOccurred is adverse when True (Argo's polarity)
          conditions: [{ type: "ErrorOccurred", status: "True", reason: "RenderError", message: "bad template" }],
        }]),
        applicationSetsAvailable: true,
        applicationSetsLoading: false,
      },
    }));
    const { getAllByText } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    select("platform-set");
    expect(getAllByText("platform-set").length).toBeGreaterThanOrEqual(2);
    expect(getAllByText("clusters").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("3").length).toBeGreaterThanOrEqual(2);
    // rows are fixed-height; the failing condition reads in full in the inspector
    expect(inspector().getByText(/RenderError/)).toBeTruthy();
    expect(inspector().getByText(/ErrorOccurred: bad template/)).toBeTruthy();
  });

  it("shows ApplicationSet expected versus actual generated app drift", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        apps: ruledApps([{ ...healthy, name: "dev-platform", project: "platform", destNamespace: "apps" }]),
        applicationSets: ruledSets([{
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
        }]),
        applicationSetsAvailable: true,
        applicationSetsLoading: false,
      },
    }));
    const { getByText, getAllByText } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    select("platform-set");
    fireEvent.click(getByText("Generated applications"));
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
    fireEvent.click(getByRole("button", { name: /^Attention\s*1/i }));
    expect(getAllByText("ollama-prod").length).toBeGreaterThanOrEqual(1);
    expect(queryByText("console-dev")).toBeNull();
    fireEvent.change(getByRole("searchbox", { name: "Search Argo CD" }), { target: { value: "nope" } });
    expect(getByText(/No applications match/)).toBeTruthy();
  });

  it("links ApplicationSets to Applications by owner reference fallback", () => {
    useFleet.setState((s) => ({
      argo: {
        ...s.argo,
        cluster: "homelab-nelli",
        apps: ruledApps([{ ...healthy, name: "dev-platform", project: "platform", applicationSet: "platform-set" }]),
        applicationSets: ruledSets([{
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
        }]),
        applicationSetsAvailable: true,
        applicationSetsLoading: false,
      },
    }));
    const { getAllByText, getByText } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    select("platform-set");
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
    select("console-dev");
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
    select("console-dev");
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
    select("console-dev");
    fireEvent.click(getByRole("button", { name: "open Widget apps/w1" }));
    const route = useFleet.getState().route;
    expect(route.name === "cluster" && route.resource).toEqual({ group: "example.com", version: "v1", plural: "widgets", kind: "Widget", scope: "Namespaced" });
    expect(route.name === "cluster" && route.instance).toEqual({ namespace: "apps", name: "w1" });
  });
});

describe("ArgoView workspace template (redesign step 4b)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearArgo();
    useFleet.setState({ navIntent: null, clusters: [] });
  });

  it("auto-selects the first broken application once, from a fresh read only", () => {
    seed([healthy, broken]);
    const { queryByTestId } = render(<ArgoView cluster="homelab-nelli" />);
    expect(queryByTestId("lens-inspector")).toBeNull(); // cached rows at entry
    act(() => seed([healthy, broken]));
    expect(inspector().getByRole("heading", { name: "ollama-prod" })).toBeTruthy();
    fireEvent.click(inspector().getByRole("button", { name: "close details" }));
    act(() => seed([healthy, broken]));
    expect(queryByTestId("lens-inspector")).toBeNull(); // once only
  });

  it("Suspended is an intended pause: never attention, paused lamp", () => {
    const paused: NoRule<ArgoAppDTO> = { ...healthy, name: "paused-app", healthStatus: "Suspended", broken: false };
    seed([paused]);
    const { getByText, getByRole } = render(<ArgoView cluster="homelab-nelli" />);
    expect(getByText("none out of sync or unhealthy reported")).toBeTruthy(); // no Healthy claim
    expect(getByText(/1 suspended/)).toBeTruthy();
    expect(getByRole("img", { name: "Suspended" })).toBeTruthy();
  });

  it("an application gone from a complete read is removed and cancels its pending sync", () => {
    seed([broken, healthy]);
    const { getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" />);
    select("ollama-prod");
    fireEvent.click(inspector().getByRole("button", { name: /^Sync/ }));
    expect(screen.getByRole("dialog", { name: /Sync Application ollama-prod/ })).toBeTruthy();
    act(() => seed([healthy]));
    expect(getByText("Application removed.")).toBeTruthy();
    expect(queryByRole("dialog", { name: /Sync Application/ })).toBeNull();
    expect(inspector().queryByRole("button", { name: /^Refresh/ })).toBeNull();
    expect(syncArgoApp).not.toHaveBeenCalled();
  });

  it("a pending refresh raised by keyboard is pinned to its own cluster", () => {
    seed([broken]);
    const { getByText, queryByText, rerender } = render(<ArgoView cluster="homelab-nelli" />);
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true, cancelable: true })); });
    expect(getByText("refresh application")).toBeTruthy();
    rerender(<ArgoView cluster="other" />);
    act(() => useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "other", apps: ruledApps([broken]), loading: false, error: null } })));
    expect(queryByText("refresh application")).toBeNull();
    expect(refreshArgoApp).not.toHaveBeenCalled();
  });

  it("a failed re-read keeps the last good list under a notice; with no rows it is unreadable, not 'not installed'", () => {
    seed([broken]);
    useFleet.setState((s) => ({ argo: { ...s.argo, error: "forbidden" } }));
    const first = render(<ArgoView cluster="homelab-nelli" />);
    expect(first.getByText(/showing the last good read/)).toBeTruthy();
    expect(first.getAllByText("ollama-prod").length).toBeGreaterThan(0);
    first.unmount();
    useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", apps: ruledApps([]), available: false, message: "forbidden", error: "forbidden", loading: false } }));
    const second = render(<ArgoView cluster="homelab-nelli" />);
    expect(second.getByText("Argo CD applications could not be read.")).toBeTruthy();
    expect(second.queryByText(/not served/)).toBeNull();
  });

  it("a palette sync for an app hidden by a stale filter reveals it and opens the composer", () => {
    seed([broken, healthy]);
    const { getByRole } = render(<ArgoView cluster="homelab-nelli" />);
    fireEvent.change(getByRole("searchbox", { name: "Search Argo CD" }), { target: { value: "zzz" } });
    act(() => useFleet.getState().requestNavIntent({ kind: "sync-argo", cluster: "homelab-nelli", namespace: "argocd", name: "console-dev" }));
    expect(screen.getByRole("dialog", { name: /Sync Application console-dev/ })).toBeTruthy();
    expect((getByRole("searchbox", { name: "Search Argo CD" }) as HTMLInputElement).value).toBe("");
    expect(inspector().getByRole("heading", { name: "console-dev" })).toBeTruthy();
  });
});

describe("ArgoView ApplicationSet condition polarity (live pass)", () => {
  it("ErrorOccurred=True is the headline error; ResourcesUpToDate=False follows; unknown types make no claim", () => {
    useFleet.getState().clearArgo();
    useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", applicationSetsAvailable: true, applicationSetsLoading: false,
      applicationSets: ruledSets([{ namespace: "argocd", name: "tenant-apps", generators: ["list"], templateName: "", project: "platform", repoURL: "", path: "", targetRevision: "main", destNamespace: "", generatedApps: 0, healthy: false,
        conditions: [
          { type: "ResourcesUpToDate", status: "False", reason: "ApplicationGenerationFromParamsError", message: "generation failed" },
          { type: "ErrorOccurred", status: "True", reason: "ApplicationGenerationFromParamsError", message: "function tenant not defined" },
          { type: "SomethingNew", status: "False", reason: "X", message: "unknown polarity" },
        ] }]) } }));
    render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    select("tenant-apps");
    const lines = inspector().getAllByText(/^(ErrorOccurred|ResourcesUpToDate|SomethingNew):/).map((e) => e.textContent);
    expect(lines[0]).toBe("ErrorOccurred: function tenant not defined");
    expect(lines).toContain("ResourcesUpToDate: generation failed");
    expect(lines.some((l) => l?.startsWith("SomethingNew"))).toBe(false);
  });
});

describe("ArgoView Codex rework (Argo review)", () => {
  const project = { namespace: "argocd", name: "platform", description: "", sourceRepos: ["https://x/y.git"], destinations: ["in-cluster/apps"], roles: 0, orphanWarn: true };
  const appSet = { namespace: "argocd", name: "tenant-apps", generators: ["list"], templateName: "", project: "platform", repoURL: "", path: "", targetRevision: "main", destNamespace: "", generatedApps: 0, generatedNames: ["team-a-apps"], healthy: true, conditions: [] };
  beforeEach(() => { vi.clearAllMocks(); useFleet.getState().clearArgo(); });

  it("linked apps light by the evaluated role, never by inclusion - Project and ApplicationSet inspectors (Codex 2b-2 #2)", () => {
    const linked = ruledApps([
      { ...healthy, name: "rolling", project: "platform", applicationSet: "tenant-apps", syncStatus: "Synced", healthStatus: "Progressing" }, // excluded, progressing
      { ...healthy, name: "parked", project: "platform", applicationSet: "tenant-apps", syncStatus: "Synced", healthStatus: "Suspended" }, // excluded, paused
      { ...healthy, name: "drifting", project: "platform", applicationSet: "tenant-apps", syncStatus: "OutOfSync", healthStatus: "Healthy" }, // included, degraded
      { ...healthy, name: "steady", project: "platform", applicationSet: "tenant-apps" },
    ]);
    expect(linked.map((a) => [a.rule.included, a.rule.role])).toEqual([[false, "progressing"], [false, "paused"], [true, "degraded"], [false, "ok"]]);
    const lampOf = (name: string) => {
      const row = inspector().getAllByTestId("linked-app").find((r) => r.textContent?.includes(name)) as HTMLElement;
      return (row.querySelector(".row-mark") as HTMLElement).outerHTML;
    };
    const check = () => {
      // The Line aspects (spec 2026-10-14 §2.4): shape carries the role.
      expect(lampOf("rolling")).toContain('data-aspect="syncing"');
      expect(lampOf("parked")).toContain('data-aspect="paused"'); // intentional, never healthy
      expect(lampOf("drifting")).toContain('data-aspect="attention"'); // degraded, never fault
      expect(lampOf("drifting")).not.toContain("color-text-danger");
      expect(lampOf("steady")).not.toContain("data-aspect"); // healthy carries no mark
      expect(lampOf("steady")).toContain('aria-hidden="true"'); // and is not announced
    };
    const seedLinked = () => useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", projects: [project], projectsAvailable: true, projectsLoading: false,
      apps: linked, loading: false, error: null, applicationSets: ruledSets([appSet]), applicationSetsAvailable: true, applicationSetsLoading: false } }));
    seedLinked();
    const { unmount } = render(<ArgoView cluster="homelab-nelli" mode="projects" />);
    select("platform");
    check();
    unmount();
    seedLinked(); // leaving a mode resets the reads it owned
    render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    select("tenant-apps");
    check();
  });

  it("3a. a failed Application read never makes a project 'unused' or claim zero apps", () => {
    useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", projects: [project], projectsAvailable: true, projectsLoading: false,
      apps: ruledApps([]), loading: false, available: false, message: "forbidden", error: "forbidden" } }));
    const { queryByText, getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" mode="projects" />);
    expect(queryByText("unused")).toBeNull();
    expect(getByText(/app usage unverified/)).toBeTruthy();
    expect(queryByRole("button", { name: /^Unused/ })).toBeNull();
    select("platform");
    expect(getByText(/links unverified, not zero/)).toBeTruthy();
  });

  it("3b. a still-loading Application read never claims missing generated apps", () => {
    useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", applicationSets: ruledSets([appSet]), applicationSetsAvailable: true, applicationSetsLoading: false,
      apps: ruledApps([]), loading: true, error: null } }));
    const { getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    expect(getByText(/generated apps unverified/)).toBeTruthy();
    expect(queryByRole("button", { name: /^Missing apps/ })).toBeNull();
    select("tenant-apps");
    expect(getByText(/actual and missing are unverified, not zero/)).toBeTruthy();
  });

  it("3c. stale retained apps are labelled as the last good read", () => {
    useFleet.setState((s) => ({ argo: { ...s.argo, cluster: "homelab-nelli", projects: [project], projectsAvailable: true, projectsLoading: false,
      apps: ruledApps([{ ...healthy, project: "platform" }]), loading: false, error: "timeout" } }));
    render(<ArgoView cluster="homelab-nelli" mode="projects" />);
    select("platform");
    expect(screen.getByText(/From the last good Application read/)).toBeTruthy();
  });

  it("4. condition severity follows the native type: Error red, Warning amber, other neutral", () => {
    seed([{ ...broken, conditions: [
      { type: "ComparisonError", message: "repo unreachable" },
      { type: "SharedResourceWarning", message: "shared with app x" },
      { type: "SomethingElse", message: "native evidence" },
    ] }]);
    render(<ArgoView cluster="homelab-nelli" />);
    select("ollama-prod");
    const color = (re: RegExp) => (inspector().getByText(re) as HTMLElement).style.color;
    expect(color(/^ComparisonError:/)).toBe("var(--color-text-danger)");
    expect(color(/^SharedResourceWarning:/)).toBe("var(--color-text-warning)");
    expect(color(/^SomethingElse:/)).toBe("var(--color-text-secondary)");
  });

  it("5. a remote destination's inventory is labelled and never opens a same-named local object", () => {
    seed([{ ...healthy, destServer: "https://prd-weu.example.com:6443" }]);
    const { getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" />);
    select("console-dev");
    expect(getByText(/Managed on https:\/\/prd-weu\.example\.com:6443/)).toBeTruthy();
    expect(queryByRole("button", { name: "open Deployment console-dev/console" })).toBeNull();
  });
});

describe("ArgoView ApplicationSet polarity, full matrix (Codex rework #1)", () => {
  const base = { namespace: "argocd", name: "s", generators: ["list"], templateName: "", project: "", repoURL: "", path: "", targetRevision: "", destNamespace: "", generatedApps: 0 };
  const show = (healthy: boolean, conditions: { type: string; status: string; reason: string; message: string }[]) => {
    useFleet.getState().clearArgo();
    useFleet.setState((st) => ({ argo: { ...st.argo, cluster: "homelab-nelli", applicationSetsAvailable: true, applicationSetsLoading: false, applicationSets: ruledSets([{ ...base, healthy, conditions }]) } }));
    const r = render(<ArgoView cluster="homelab-nelli" mode="applicationsets" />);
    select("s");
    return r;
  };
  it("healthy ErrorOccurred=False with positive conditions shows no diagnosis", () => {
    const { unmount } = show(true, [
      { type: "ErrorOccurred", status: "False", reason: "ApplicationSetUpToDate", message: "All applications have been generated successfully" },
      { type: "ParametersGenerated", status: "True", reason: "ParametersGenerated", message: "ok" },
      { type: "ResourcesUpToDate", status: "True", reason: "ApplicationSetUpToDate", message: "ok" },
    ]);
    expect(inspector().getByText("Healthy")).toBeTruthy();
    expect(inspector().queryByText(/^(ErrorOccurred|ParametersGenerated|ResourcesUpToDate):/)).toBeNull();
    unmount();
  });
  it("ErrorOccurred=True leads the diagnosis", () => {
    const { unmount } = show(false, [{ type: "ErrorOccurred", status: "True", reason: "RenderError", message: "bad template" }]);
    expect(inspector().getByText("ErrorOccurred: bad template")).toBeTruthy();
    unmount();
  });
  it("an Unknown status is unverified evidence, not a diagnosis", () => {
    const { unmount } = show(false, [{ type: "ResourcesUpToDate", status: "Unknown", reason: "", message: "controller restarting" }]);
    expect(inspector().getByText("Unverified")).toBeTruthy();
    const line = inspector().getByText(/ResourcesUpToDate is Unknown \(unverified\)/) as HTMLElement;
    expect(line.style.color).toBe("var(--color-text-secondary)");
    unmount();
  });
});

describe("ArgoView Codex re-review (destination + join coverage + zero wording)", () => {
  const project = { namespace: "argocd", name: "platform", description: "", sourceRepos: ["https://x/y.git"], destinations: ["in-cluster/apps"], roles: 0, orphanWarn: true };
  beforeEach(() => { vi.clearAllMocks(); useFleet.getState().clearArgo(); });

  it("1a. no destination evidence is unverified - rows are not linked", () => {
    seed([{ ...healthy, destServer: undefined, destName: undefined }]);
    const { getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" />);
    select("console-dev");
    expect(getByText(/Destination unverified \(no destination server or name reported\)/)).toBeTruthy();
    expect(queryByRole("button", { name: "open Deployment console-dev/console" })).toBeNull();
  });

  it("1b. conflicting server and name evidence is unverified", () => {
    seed([{ ...healthy, destServer: "https://kubernetes.default.svc", destName: "prd-weu" }]);
    const { getByText, queryByRole } = render(<ArgoView cluster="homelab-nelli" />);
    select("console-dev");
    expect(getByText(/Destination unverified \(prd-weu \/ https:\/\/kubernetes\.default\.svc - conflicting/)).toBeTruthy();
    expect(queryByRole("button", { name: "open Deployment console-dev/console" })).toBeNull();
  });

  it("2a. Projects succeed, ApplicationSets fail: appset counts are '?' and the inspector says unverified", () => {
    useFleet.setState((st) => ({ argo: { ...st.argo, cluster: "homelab-nelli", projects: [project], projectsAvailable: true, projectsLoading: false,
      apps: ruledApps([{ ...healthy, project: "platform" }]), loading: false,
      applicationSets: ruledSets([]), applicationSetsLoading: false, applicationSetsAvailable: false, applicationSetsError: "forbidden" } }));
    const { container, getByText } = render(<ArgoView cluster="homelab-nelli" mode="projects" />);
    const row = container.querySelector(".lens-row") as HTMLElement;
    expect(row.textContent).toContain("?");
    select("platform");
    expect(getByText(/ApplicationSets unverified/)).toBeTruthy();
  });

  it("2b. Applications succeed, Projects and ApplicationSets fail: attribution says unverified", () => {
    useFleet.setState((st) => ({ argo: { ...st.argo, cluster: "homelab-nelli", apps: ruledApps([healthy]), loading: false, error: null, available: true,
      projects: [], projectsLoading: false, projectsError: "forbidden",
      applicationSets: ruledSets([]), applicationSetsLoading: false, applicationSetsError: "forbidden" } }));
    render(<ArgoView cluster="homelab-nelli" />);
    select("console-dev");
    expect(inspector().getByText(/project read unavailable - unverified/)).toBeTruthy();
    expect(inspector().getByText(/unverified \(ApplicationSet read unavailable\)/)).toBeTruthy();
  });

  it("2c. stale-derived headline counts and filter chips are marked last-good", () => {
    useFleet.setState((st) => ({ argo: { ...st.argo, cluster: "homelab-nelli", projects: [project], projectsAvailable: true, projectsLoading: false,
      apps: ruledApps([{ ...healthy, project: "other" }]), loading: false, error: "timeout" } }));
    const { getByText, getByRole } = render(<ArgoView cluster="homelab-nelli" mode="projects" />);
    expect(getByText(/1 unused \(last good Application read\)/)).toBeTruthy();
    expect(getByRole("button", { name: /^Unused 1\*/ })).toBeTruthy();
  });

  it("3. an empty Application catalogue gets a neutral zero, never a health claim", () => {
    seed([]);
    const { queryByText } = render(<ArgoView cluster="homelab-nelli" />);
    expect(queryByText(/none out of sync/)).toBeNull();
    expect(queryByText(/healthy/i)).toBeNull();
  });
});
